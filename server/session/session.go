// Package session keeps browser sessions in the database and checks the
// password a sign-in is made with (design 9.3).
//
// A session row is bookkeeping, not a fact anybody audits: it is written
// through ent directly, outside the trail, the way payday's own stores are.
// The trail still sees who did what, because every call a session makes is
// recorded with its holder.
package session

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/credential"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/party"
	"github.com/lesomnus/rove/internal/ent/predicate"
	"github.com/lesomnus/rove/internal/ent/session"
	"github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/server/password"
	"github.com/lesomnus/rove/server/pd"
)

// Store is an [authsession.Store] over the Session table. The key is never
// written down: the row holds its SHA-256, which is what a cookie is looked up
// by, so a copy of the table signs nobody in.
type Store struct {
	Db *ent.Client
}

var _ authsession.Store = Store{}

func digest(key string) []byte {
	v := sha256.Sum256([]byte(key))
	return v[:]
}

// whole is the one grant a password gives: whatever the person may do.
var whole = []byte("whole")

func (s Store) Put(ctx context.Context, v authsession.Session) error {
	if len(v.Held) > 0 {
		return authsession.ErrCannotHold
	}
	actor, err := pdid.Parse(v.Id)
	if err != nil {
		return err
	}
	tn, err := pdid.Parse(v.TenantId)
	if err != nil {
		return err
	}

	// Put is also how the idle clock moves: the same key, again.
	now := time.Now()
	n, err := s.Db.Session.Update().
		Where(session.Secret(digest(v.Key)), session.DateErasedIsNil()).
		SetNillableDateIdle(nonzero(v.Idle)).
		SetNillableDateExpires(nonzero(v.Expires)).
		SetDateUpdated(now).
		Save(ctx)
	if err != nil || n > 0 {
		return err
	}

	return s.Db.Session.Create().
		SetId(pdid.New(pd.SessionDomain).Uuid()).
		SetTenantId(tn.Uuid()).
		SetHolderId(actor.Uuid()).
		SetSecret(digest(v.Key)).
		SetGrant(whole).
		SetNillableDateIdle(nonzero(v.Idle)).
		SetNillableDateExpires(nonzero(v.Expires)).
		SetDateCreated(now).
		SetDateUpdated(now).
		Exec(ctx)
}

func (s Store) Get(ctx context.Context, key string) (authsession.Session, error) {
	v, err := s.Db.Session.Query().Where(session.Secret(digest(key)), session.DateErasedIsNil()).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return authsession.Session{}, authsession.ErrNoSession
		}
		return authsession.Session{}, err
	}
	out := authsession.Session{
		Key:      key,
		Id:       pdid.Id(v.HolderId).String(),
		TenantId: pdid.Id(v.TenantId).String(),
		Grant:    frame.Whole(),
	}
	if v.DateExpires != nil {
		out.Expires = *v.DateExpires
	}
	if v.DateIdle != nil {
		out.Idle = *v.DateIdle
	}
	return out, nil
}

func (s Store) Del(ctx context.Context, key string) error {
	_, err := s.Db.Session.Delete().Where(session.Secret(digest(key))).Exec(ctx)
	return err
}

// Sweep deletes sessions that are over, for the table not to keep every
// sign-in there ever was.
func (s Store) Sweep(ctx context.Context) error {
	now := time.Now()
	_, err := s.Db.Session.Delete().Where(session.Or(
		session.DateExpiresLT(now),
		session.DateIdleLT(now),
	)).Exec(ctx)
	return err
}

func nonzero(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// Login is what a sign-in form posts:
//
//	{"tenant": "acme", "login": "kim", "password": "..."}
//
// `login` is a holder's alias or the e-mail of the person who holds it, and
// `tenant` may be left out where the login names only one person anywhere --
// which a deployment with one organization always is.
type Login struct {
	Tenant   string `json:"tenant"`
	Login    string `json:"login"`
	Password string `json:"password"`
}

// Verify checks a sign-in against the credentials in the database, and refuses
// an address that keeps getting it wrong.
func Verify(db *ent.Client) authsession.Verify {
	l := &limiter{seen: map[string][]time.Time{}}
	return func(ctx context.Context, r *http.Request) (authsession.Session, error) {
		var in Login
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<10)).Decode(&in); err != nil {
			return authsession.Session{}, err
		}
		in.Login = strings.TrimSpace(in.Login)
		in.Tenant = strings.TrimSpace(in.Tenant)
		if in.Login == "" || in.Password == "" {
			return authsession.Session{}, errNo
		}

		who := addr(r) + "\x00" + strings.ToLower(in.Login)
		if !l.ok(who) {
			return authsession.Session{}, errNo
		}
		v, err := check(ctx, db, in)
		if err != nil {
			l.fail(who)
			return authsession.Session{}, err
		}
		l.forget(who)
		return v, nil
	}
}

var errNo = errors.New("session: no")

func check(ctx context.Context, db *ent.Client, in Login) (authsession.Session, error) {
	in_ := []predicate.Holder{holder.DateErasedIsNil()}
	if in.Tenant != "" {
		in_ = append(in_, holder.HasTenantWith(tenant.Alias(in.Tenant)))
	}

	// By alias, and by the e-mail of the person who holds the login.
	ids, err := db.Holder.Query().Where(append(in_, holder.Alias(in.Login))...).Ids(ctx)
	if err != nil {
		return authsession.Session{}, err
	}
	byMail, err := db.Party.Query().Where(
		party.EmailEqualFold(in.Login),
		party.DateErasedIsNil(),
		party.HolderIdNotNil(),
		party.HasHolderWith(in_...),
	).All(ctx)
	if err != nil {
		return authsession.Session{}, err
	}
	for _, p := range byMail {
		if !slices.Contains(ids, p.HolderId) {
			ids = append(ids, p.HolderId)
		}
	}
	if len(ids) != 1 {
		// Nobody, or somebody in more than one tenant who has to say which.
		// The hash is checked anyway, so that the answer takes as long.
		password.Check(dummy, in.Password)
		return authsession.Session{}, errNo
	}

	h, err := db.Holder.Query().Where(holder.Id(ids[0])).Only(ctx)
	if err != nil {
		return authsession.Session{}, err
	}
	c, err := db.Credential.Query().Where(credential.HolderId(h.Id)).Only(ctx)
	if err != nil {
		password.Check(dummy, in.Password)
		return authsession.Session{}, errNo
	}
	ok, err := password.Check(string(c.Secret), in.Password)
	if err != nil || !ok {
		return authsession.Session{}, errNo
	}
	return authsession.Session{
		Id:       pdid.Id(h.Id).String(),
		TenantId: pdid.Id(h.TenantId).String(),
		Grant:    frame.Whole(),
	}, nil
}

// dummy is a hash nobody's password matches, checked when there is nobody to
// check, so that a name that does not exist is not answered faster.
var dummy, _ = password.Hash("there is nobody here to sign in")

func addr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limiter refuses a login from an address after ten failures in fifteen
// minutes. It is per process, which is what one replica needs.
type limiter struct {
	mu   sync.Mutex
	seen map[string][]time.Time
}

const (
	maxFailures = 10
	window      = 15 * time.Minute
)

func (l *limiter) ok(k string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := time.Now().Add(-window)
	vs := l.seen[k]
	for len(vs) > 0 && vs[0].Before(cut) {
		vs = vs[1:]
	}
	l.seen[k] = vs
	if len(vs) == 0 {
		delete(l.seen, k)
	}
	return len(vs) < maxFailures
}

func (l *limiter) fail(k string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen[k] = append(l.seen[k], time.Now())
}

func (l *limiter) forget(k string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.seen, k)
}
