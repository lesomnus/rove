package cli_test

import (
	"context"
	"testing"

	"github.com/lesomnus/payday/pdtest"
	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
)

// TestEverySchemaHasTheBackstop: what `pg.sql` adds is asked after in the
// schema it is added to, so a second one in the same database -- every test
// has its own -- has it as well as the first.
func TestEverySchemaHasTheBackstop(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	for range 2 {
		c := cmd.Config{}
		c.Db.Driver, c.Db.Dsn = pdtest.DB(t)
		if c.Db.Driver != "pgx" {
			t.Skip("PostgreSQL only; set PDTEST_POSTGRES")
		}
		c.Watch.Broker = "memory"
		c.App.Files = t.TempDir()
		c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)

		s, err := cmd.Build(ctx, c)
		x.NoError(err)
		x.NoError(cli.Migrate(ctx, s))
		for table, name := range map[string]string{
			"allocation":  "allocation_exclusive_no_overlap",
			"placement":   "placement_one_parent",
			"stewardship": "stewardship_one_per_role",
			"link":        "link_once",
		} {
			n := 0
			x.NoError(s.Db.QueryRowContext(ctx, "SELECT count(*) FROM pg_constraint WHERE conname = $1 AND conrelid = $2::regclass", name, table).Scan(&n))
			x.Equal(1, n, "%s on %s", name, table)
		}
		x.NoError(s.Close())
	}
}
