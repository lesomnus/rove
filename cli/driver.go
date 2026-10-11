package cli

// The drivers this app runs on in a process. They are blank-imported by the app
// rather than by payday so that an app does not carry an engine it never opens.
//
// They are in this package and not in `cmd` because a blank import is a
// property of the package that writes it, and `cmd` is what an app's sandbox
// imports for `Build`. A driver named there is linked into the page as well.
//
// SQLite is what a checkout runs on; PostgreSQL is what a deployment does
// (design 9.4), and the extras that only it has are in `migrations/pg`.
import (
	_ "github.com/lesomnus/payday/config/dbpgx"
	_ "github.com/lesomnus/payday/config/dbsqlite3"
)
