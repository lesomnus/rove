package cli_test

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/internal/identity"
)

// run is a command's output, run the way a shell runs it.
func run(t *testing.T, k *xli.Command, args ...string) string {
	t.Helper()
	x := require.New(t)

	r, w, err := os.Pipe()
	x.NoError(err)
	was := os.Stdout
	os.Stdout = w
	err = k.Run(t.Context(), args)
	os.Stdout = was
	x.NoError(w.Close())

	b, e := io.ReadAll(r)
	x.NoError(e)
	x.NoError(err, "%s: %s", strings.Join(args, " "), b)

	return string(b)
}

// TestAPurgeTakesTheTenantsPeopleOutOfTheRosterInThisProcess.
//
// The roster in this process holds nothing but Rove's logins, and nothing but
// Rove can reach it: what a tenant leaving takes, it takes from there too --
// and from that tenant alone.
func TestAPurgeTakesTheTenantsPeopleOutOfTheRosterInThisProcess(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)

	run(t, cli.NewCmdInit(&c), "--tenant", "acme", "--login", "owner", "--password", "owner1234")
	run(t, cli.NewCmdHolder(&c), "add", "--tenant", "acme", "--login", "kim", "--password", "kim-password")
	run(t, cli.NewCmdInit(&c), "--tenant", "beta", "--login", "owner", "--password", "owner5678")

	signsIn := func(tenant, login, password string) error {
		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		defer s.Close()
		_, err = s.Identity.Verify(ctx, tenant, login, password)
		return err
	}
	x.NoError(signsIn("acme", "kim", "kim-password"))

	out := run(t, cli.NewCmdTenant(&c), "purge", "--tenant", "acme")
	x.Contains(out, "roster in this process: would forget")
	x.NoError(signsIn("acme", "kim", "kim-password"), "a purge that was only asked about")

	out = run(t, cli.NewCmdTenant(&c), "purge", "--tenant", "acme", "--yes")
	x.Contains(out, "roster in this process: forgot")
	x.ErrorIs(signsIn("acme", "kim", "kim-password"), identity.ErrRefused)
	x.ErrorIs(signsIn("acme", "owner", "owner1234"), identity.ErrRefused)
	x.NoError(signsIn("beta", "owner", "owner5678"), "another tenant's people went with it")

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	defer s.Close()
	gone, err := s.Ent.Tenant.Query().Where(tenant.Alias("acme")).Exist(ctx)
	x.NoError(err)
	x.False(gone, "the tenant is still here")
}
