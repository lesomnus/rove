package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/z"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/server/tenancy"
)

// NewCmdInit is `rove init`: the first tenant, the person who owns it, and
// the types an asset register starts with.
//
// The tenant and the person are made at the roster in this process first --
// that is where they sign in -- and here with the same identifiers (design
// 9.10). A deployment whose roster is one of its own has no first tenant to
// make: tenants and people are made there, and the first person of a tenant
// to sign in here owns it.
//
// It exists because there is nowhere else it could happen. A tenant is not put
// up from inside one, so the first row of a deployment cannot arrive over the
// API. What puts it there is [cmd.Server.Base], which is not a privilege
// anybody holds: it is a server instance this process was handed, reachable
// from this command and from nowhere a request can get to.
//
// Running it twice is an error rather than a no-op, because an alias is unique
// and the database says so. That is the right answer -- an `init` that quietly
// did nothing is one somebody runs against the wrong deployment and believes.
func NewCmdInit(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "init",
		Brief: "put up the first tenant and its owner",

		Flags: flg.Flags{
			&flg.String{Name: "tenant", Brief: "the tenant's alias, which a sign-in may name"},
			&flg.String{Name: "name", Brief: "the organization's name"},
			&flg.String{Name: "login", Brief: "the owner's login"},
			&flg.String{Name: "person", Brief: "the owner's name"},
			&flg.String{Name: "email", Brief: "the owner's e-mail, kept on their person record"},
			&flg.String{Name: "password", Brief: "the owner's password; empty makes one up"},
			&flg.Switch{Name: "demo", Brief: "also fill it with teams, people, spaces and assets to try things on"},
		},

		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			get := func(name, otherwise string) string {
				v, ok := flg.Find[string](self, name)
				if !ok || strings.TrimSpace(v) == "" {
					return otherwise
				}
				return strings.TrimSpace(v)
			}
			alias := get("tenant", "rove")
			name := get("name", alias)
			login := get("login", "admin")
			person := get("person", "관리자")
			email := get("email", "")
			pw := get("password", "")
			demo, _ := flg.Find[bool](self, "demo")

			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()
			if !s.Identity.Embedded() {
				return errors.New("auth.roster names a roster of its own, and tenants and people are made there: " +
					"`roster tenant add`, then `roster app install --tenant <alias> rove`; the first person of a tenant to sign in here owns it")
			}

			// The schema, so that a fresh database is one this can run against.
			if err := Migrate(ctx, s); err != nil {
				return err
			}

			// At roster, which is where they sign in.
			p, made, err := s.Identity.Seed(ctx, alias, login, pdid.Nil)
			if err != nil {
				return fmt.Errorf("roster: %w", err)
			}
			if pw != "" {
				if err := s.Identity.SetPassword(ctx, p.Id, pw); err != nil {
					return fmt.Errorf("roster: %w", err)
				}
				made = ""
			}

			// And here, with roster's identifiers.
			t, err := s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Id: p.Tenant.Bytes(), Alias: alias, Name: name}.Build())
			if err != nil {
				return fmt.Errorf("tenant %q: %w", alias, err)
			}
			tenant := app.TenantRef_builder{Id: t.GetId()}.Build()
			h, err := s.Base.Holder().Add(ctx, app.HolderAddRequest_builder{
				Id:     p.Id.Bytes(),
				Tenant: tenant,
				Alias:  login,
				Name:   person,
				Role:   z.Ptr("owner"),
			}.Build())
			if err != nil {
				return fmt.Errorf("holder %q: %w", login, err)
			}
			if _, err := s.Base.Party().Add(ctx, app.PartyAddRequest_builder{
				Tenant: tenant,
				Name:   person,
				Kind:   "person",
				Email:  email,
				Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
			}.Build()); err != nil {
				return err
			}

			// From here on it is the owner doing it, through the domain layer,
			// so that what it writes reads as theirs.
			tid, _ := pdid.From(t.GetId())
			hid, _ := pdid.From(h.GetId())
			ctx = frame.Into(ctx, frame.New(hid, tid, frame.Whole()).WithScope(frame.Only(tid)).WithRow(h))

			if err := tenancy.SetUp(ctx, s.Ungated, alias, c.App.Labels.Suffix); err != nil {
				return err
			}
			if demo {
				if err := seedDemo(ctx, s.Ungated, self); err != nil {
					return fmt.Errorf("demo: %w", err)
				}
			}

			self.Printf("tenant   %s (%s)\n", alias, tid)
			self.Printf("owner    %s (%s)\n", login, hid)
			if made != "" {
				self.Printf("password %s\n", made)
			}
			self.Printf("\nsign in at %s with %s\n", c.App.App(), login)
			return nil
		}),
	}
}
