// Package session keeps browser sessions in the database, and signs a person in
// once roster has checked their password (design 9.10).
//
// A session row is bookkeeping, not a fact anybody audits: it is written
// through ent directly, outside the trail, the way payday's own stores are.
// The trail still sees who did what, because every call a session makes is
// recorded with its holder.
//
// The session is Rove's, so that it can be ended at once (report 3); the
// person is roster's, so every call a session makes is held to what roster
// says of them now: suspended, erased or signed out everywhere since it began
// is a session that is over.
package session

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/payday/auth"
	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/session"
	"github.com/lesomnus/rove/internal/identity"
	"github.com/lesomnus/rove/server/pd"
	"github.com/lesomnus/rove/server/tenancy"
)

// Store is an [authsession.Store] over the Session table. The key is never
// written down: the row holds its SHA-256, which is what a cookie is looked up
// by, so a copy of the table signs nobody in.
type Store struct {
	Db *ent.Client

	// People is what a session's person is held to on every call; nil holds
	// them to nothing, which only a test of the table itself wants.
	People Held
}

// Held is roster's word on whether a session that began at `began` is still
// good: [identity.Store.Held].
type Held interface {
	Held(ctx context.Context, tenant, holder pdid.Id, began time.Time) error
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
	if s.People != nil {
		switch err := s.People.Held(ctx, pdid.Id(v.TenantId), pdid.Id(v.HolderId), v.DateCreated); {
		case errors.Is(err, identity.ErrRefused):
			// Over: what roster says of them since it began ends it here too.
			if _, err := s.Db.Session.Delete().Where(session.Id(v.Id)).Exec(ctx); err != nil {
				log.From(ctx).WarnContext(ctx, "session: ending one roster refused", "err", err.Error())
			}
			return authsession.Session{}, authsession.ErrNoSession
		case err != nil:
			// Not known, which is not the same as over: the cookie stays,
			// and the call is refused until roster can be asked.
			return authsession.Session{}, fmt.Errorf("%w: %w", auth.ErrUnavailable, err)
		}
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
// `login` is a person's alias, or the address they are reached at. `tenant`
// may be left out where this deployment serves one tenant only.
type Login struct {
	Tenant   string `json:"tenant"`
	Login    string `json:"login"`
	Password string `json:"password"`
}

// People is what a sign-in is checked with: roster.
type People interface {
	Verify(ctx context.Context, tenant, login, password string) (identity.Person, error)
	Only(ctx context.Context) (string, bool, error)
}

// SignIn is `POST /session` and `DELETE /session`: a password roster checked,
// then this deployment's own rows for the person if they are new here, then
// Rove's session cookie.
//
// A refusal is one answer whatever was wrong -- nobody by that name, the wrong
// password, a login ended here, an address that keeps getting it wrong -- and
// a roster that cannot be asked is another, since telling somebody their
// password is wrong when nobody could check it is the answer that sends them
// to reset it.
func SignIn(sessions *authsession.Sessions, people People, anchor tenancy.Anchor) http.Handler {
	l := &limiter{seen: map[string][]time.Time{}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
		case http.MethodDelete:
			http.SetCookie(w, sessions.End(r.Context(), sessions.KeyOf(r.Header.Values("Cookie"))))
			w.WriteHeader(http.StatusNoContent)
			return
		default:
			w.Header().Set("Allow", "POST, DELETE")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		ctx := r.Context()
		var in Login
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<10)).Decode(&in); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		in.Login = strings.TrimSpace(in.Login)
		in.Tenant = strings.TrimSpace(in.Tenant)
		who := addr(r) + "\x00" + strings.ToLower(in.Login)
		if in.Login == "" || in.Password == "" || !l.ok(who) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		v, err := verify(ctx, people, anchor, in)
		switch {
		case errors.Is(err, errNo):
			l.fail(who)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		case errors.Is(err, tenancy.ErrClash):
			// Roster vouched for them, and this deployment has somebody of
			// that name from before roster did: the operator's to bring in,
			// and nothing waiting would fix.
			log.From(ctx).ErrorContext(ctx, "session: a sign-in roster vouched for is somebody here already", "err", err.Error())
			http.Error(w, "not brought in yet", http.StatusConflict)
			return
		case err != nil:
			log.From(ctx).WarnContext(ctx, "session: a sign-in nobody could check", "err", err.Error())
			http.Error(w, "cannot sign in just now", http.StatusServiceUnavailable)
			return
		}
		l.forget(who)

		_, c, err := sessions.Mint(ctx, v)
		if err != nil {
			http.Error(w, "cannot sign in just now", http.StatusServiceUnavailable)
			return
		}
		http.SetCookie(w, c)
		w.WriteHeader(http.StatusNoContent)
	})
}

// errNo is a refusal: everything a sign-in can be told no for.
var errNo = errors.New("session: no")

func verify(ctx context.Context, people People, anchor tenancy.Anchor, in Login) (authsession.Session, error) {
	tenant := in.Tenant
	if tenant == "" {
		only, ok, err := people.Only(ctx)
		if err != nil {
			return authsession.Session{}, err
		}
		if !ok {
			// More than one tenant here, and the form said none.
			return authsession.Session{}, errNo
		}
		tenant = only
	}

	p, err := people.Verify(ctx, tenant, in.Login, in.Password)
	switch {
	case errors.Is(err, identity.ErrRefused), errors.Is(err, identity.ErrSecondFactor),
		errors.Is(err, identity.ErrNoTenant), errors.Is(err, identity.ErrNoPerson):
		return authsession.Session{}, errNo
	case err != nil:
		return authsession.Session{}, err
	}

	h, err := anchor.Of(ctx, p)
	switch {
	case errors.Is(err, tenancy.ErrEnded):
		return authsession.Session{}, errNo
	case err != nil:
		return authsession.Session{}, err
	}

	return authsession.Session{
		Id:       pdid.Id(h.Id).String(),
		TenantId: pdid.Id(h.TenantId).String(),
		Grant:    frame.Whole(),
	}, nil
}

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
