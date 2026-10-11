package cli_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/session"
	rovesession "github.com/lesomnus/rove/server/session"
)

// before is a deployment from before roster held its people: a tenant and its
// logins of this deployment's own minting, a session one of them is signed in
// with, and the table their passwords were kept in.
type before struct {
	tenant           pdid.Id
	admin, kim, gone pdid.Id
}

func deploymentFromBefore(t *testing.T, c cmd.Config) before {
	t.Helper()
	x := require.New(t)
	ctx := context.Background()

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	x.NoError(cli.Migrate(ctx, s))

	v, err := s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "acme", Name: "Acme"}.Build())
	x.NoError(err)
	out := before{}
	out.tenant, _ = pdid.From(v.GetId())
	tref := app.TenantRef_builder{Id: v.GetId()}.Build()
	login := func(alias, role string) pdid.Id {
		h, err := s.Base.Holder().Add(ctx, app.HolderAddRequest_builder{Tenant: tref, Alias: alias, Name: alias, Role: z.Ptr(role)}.Build())
		x.NoError(err)
		_, err = s.Base.Party().Add(ctx, app.PartyAddRequest_builder{Tenant: tref, Name: alias, Kind: "person", Holder: app.HolderRef_builder{Id: h.GetId()}.Build()}.Build())
		x.NoError(err)
		id, _ := pdid.From(h.GetId())
		return id
	}
	// `admin` is what `rove init` called its owner, and the name roster gives
	// the first person of every tenant it makes.
	out.admin = login("admin", "owner")
	out.kim = login("kim", "member")
	out.gone = login("gone", "member")
	_, err = s.Base.Holder().Erase(ctx, app.HolderRef_builder{Id: out.gone.Bytes()}.Build())
	x.NoError(err)

	_, _, err = s.Sessions.Mint(ctx, authsession.Session{Id: out.kim.String(), TenantId: out.tenant.String()})
	x.NoError(err)

	_, err = s.Db.ExecContext(ctx, "CREATE TABLE credential (id uuid PRIMARY KEY, secret text NOT NULL)")
	x.NoError(err)
	_, err = s.Db.ExecContext(ctx, "INSERT INTO credential (id, secret) VALUES ($1, $2)", pdid.New(37).String(), "$argon2id$v=19$m=65536,t=3,p=4$...")
	x.NoError(err)

	return out
}

var passwordLine = regexp.MustCompile(`(?m)^@acme/(\S+)  password (\S+)$`)

// TestADeploymentFromBeforeRosterIsBroughtIntoIt.
//
// Its people are made at the roster in this process with the identifiers
// they have here, so that what they did is still theirs; each is given a new
// password, shown once, and signs in with it as who they were.
func TestADeploymentFromBeforeRosterIsBroughtIntoIt(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	was := deploymentFromBefore(t, c)

	// What somebody tries first, and is told no before roster is written to:
	// a tenant made there for it would have the alias this one needs.
	err := cli.NewCmdInit(&c).Run(ctx, []string{"--tenant", "acme", "--login", "admin", "--password", "admin1234"})
	x.ErrorContains(err, "here already")

	out := run(t, cli.NewCmdIdentity(&c), "migrate")
	passwords := map[string]string{}
	for _, m := range passwordLine.FindAllStringSubmatch(out, -1) {
		passwords[m[1]] = m[2]
	}
	x.Len(passwords, 2, out)
	x.Contains(passwords, "admin")
	x.Contains(passwords, "kim")
	x.Contains(out, "dropped `credential`")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()

	for alias, id := range map[string]pdid.Id{"admin": was.admin, "kim": was.kim} {
		p, err := s.Identity.Verify(ctx, "acme", alias, passwords[alias])
		x.NoError(err, alias)
		x.Equal(id, p.Id, "%s is somebody new at roster", alias)
		x.Equal(was.tenant, p.Tenant)
	}

	login := rovesession.SignIn(s.Sessions, s.Identity, s.Anchor(c))
	r := httptest.NewRequest(http.MethodPost, "/session", bytes.NewBufferString(`{"login":"admin","password":"`+passwords["admin"]+`"}`))
	rec := httptest.NewRecorder()
	login.ServeHTTP(rec, r)
	x.Equal(http.StatusNoContent, rec.Code)
	h, err := s.Ent.Holder.Get(ctx, was.admin.Uuid())
	x.NoError(err)
	x.Equal("owner", h.Role, "the owner came back as somebody else")
	n, err := s.Ent.Holder.Query().Where(holder.TenantId(was.tenant.Uuid())).Count(ctx)
	x.NoError(err)
	x.Equal(3, n, "a sign-in made a second login beside the one from before")

	left, err := s.Ent.Session.Query().Where(session.HolderId(was.kim.Uuid())).Count(ctx)
	x.NoError(err)
	x.Zero(left, "a session signed in with a password that is not theirs any more")

	again := run(t, cli.NewCmdIdentity(&c), "migrate")
	x.Contains(again, "nobody to bring in")
	x.NotContains(again, "password ")
	x.NotContains(again, "dropped")
}

// TestADeploymentToTryThingsOnIsBroughtInWithOnePassword.
func TestADeploymentToTryThingsOnIsBroughtInWithOnePassword(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	deploymentFromBefore(t, c)

	// One roster would not take leaves nobody half brought in.
	err := cli.NewCmdIdentity(&c).Run(ctx, []string{"migrate", "--password", "short"})
	x.ErrorContains(err, "@acme/admin")

	out := run(t, cli.NewCmdIdentity(&c), "migrate", "--password", "demo1234")
	x.Contains(out, "2 login(s), every one with the password given")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	for _, alias := range []string{"admin", "kim"} {
		_, err := s.Identity.Verify(ctx, "acme", alias, "demo1234")
		x.NoError(err, alias)
	}
}

// TestARosterOfItsOwnIsToldWhatToMake: one this process cannot write to is
// its operator's, and is told the identifiers.
func TestARosterOfItsOwnIsToldWhatToMake(t *testing.T) {
	x := require.New(t)

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)
	was := deploymentFromBefore(t, c)

	c.Auth.Roster = cmd.Config{}.Auth.Roster
	c.Auth.Roster.Addr = "127.0.0.1:1"
	c.Auth.Roster.Insecure = true
	c.Auth.Roster.Key = "rk_nothing-is-asked-with-it"

	out := run(t, cli.NewCmdIdentity(&c), "migrate")
	x.Contains(out, `roster tenant add @acme '{"id":"`+was.tenant.String()+`","name":"Acme"}'`)
	x.Contains(out, `roster holder add @acme/kim '{"id":"`+was.kim.String()+`","name":"kim"}'`)
	x.Contains(out, "# roster made @acme/admin with the tenant")
	x.NotContains(out, "@acme/gone", "a login that ended")
	x.Contains(out, "roster app install --tenant acme --role /roster.VouchService/Verify,")
	x.Contains(out, "dropped `credential`")
}
