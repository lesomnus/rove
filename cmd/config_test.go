package cmd_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/rove/cmd"
)

func TestStateDirIsWhereTheSqliteFileIs(t *testing.T) {
	for dsn, want := range map[string]string{
		"file:data/rove.db?_pragma=foreign_keys(1)": "data",
		"file:/var/lib/rove/rove.db":                "/var/lib/rove",
		"data/rove.db":                              "data",
		"rove.db":                                   ".",
		"file::memory:?cache=shared":                "",
		":memory:":                                  "",
		"file:rove?mode=memory&cache=shared":        "",
		"":                                          "",
	} {
		c := cmd.Config{}
		c.Db.Driver, c.Db.Dsn = "sqlite3", dsn
		require.Equal(t, want, c.StateDir(), dsn)
	}

	c := cmd.Config{}
	c.Db.Driver, c.Db.Dsn = "pgx", "postgres://rove@localhost/rove"
	require.Empty(t, c.StateDir(), "a database that is not a file")
}
