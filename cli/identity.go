package cli

import (
	"context"
	"encoding/json"
	"strings"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"

	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/session"
	enttenant "github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/internal/identity"
)

// NewCmdIdentity is `rove identity`: who people are, which is roster's
// (design 9.10).
func NewCmdIdentity(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "identity",
		Brief: "who people are: roster, in this process or of its own",

		Commands: xli.Commands{
			{
				Name:  "migrate",
				Brief: "bring the tenants and people of a deployment from before roster into it, with their identifiers",
				Flags: flg.Flags{
					&flg.String{Name: "password", Brief: "give everybody this one, for a deployment to try things on; empty makes one up for each"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					return migrateIdentity(ctx, self, c)
				}),
			},
		},
	}
}

// migrateIdentity is `rove identity migrate`, the upgrade of a deployment
// whose tenants and logins were made here before roster held them. Nobody of
// it is at roster, so nobody of it can sign in.
//
// On the roster in this process it writes every tenant and every login that
// has not ended into roster with the identifiers they have here, gives each a
// new password and prints it once, and ends the sessions from before. Run
// again it makes nothing. A roster of its own is its operator's to write to,
// so for one this prints what to make there, with the same identifiers.
//
// Either way it then drops the table the passwords from before were kept in,
// which nothing reads any more.
func migrateIdentity(ctx context.Context, self *xli.Command, c *cmd.Config) error {
	s, err := cmd.Build(ctx, *c)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := Migrate(ctx, s); err != nil {
		return err
	}

	tenants, err := s.Ent.Tenant.Query().Order(ent.Asc(enttenant.FieldAlias)).All(ctx)
	if err != nil {
		return err
	}
	if s.Identity.Embedded() {
		err = adoptAll(ctx, self, s, tenants, flag(self, "password"))
	} else {
		err = describeAll(ctx, self, s, tenants)
	}
	if err != nil {
		return err
	}

	dropped, err := dropCredentials(ctx, s)
	if err != nil {
		return err
	}
	if dropped {
		self.Printf("dropped `credential`, the passwords from before roster\n")
	}
	return nil
}

// loginsOf is everybody who signs in to a tenant here, in the order they were
// made.
func loginsOf(ctx context.Context, s *cmd.Server, t *ent.Tenant) ([]*ent.Holder, error) {
	return s.Ent.Holder.Query().
		Where(holder.TenantId(t.Id), holder.DateErasedIsNil()).
		Order(ent.Asc(holder.FieldDateCreated), ent.Asc(holder.FieldId)).
		All(ctx)
}

func adoptAll(ctx context.Context, self *xli.Command, s *cmd.Server, tenants []*ent.Tenant, password string) error {
	made := 0
	for _, t := range tenants {
		hs, err := loginsOf(ctx, s, t)
		if err != nil {
			return err
		}
		people := make([]identity.Adoptee, 0, len(hs))
		for _, h := range hs {
			people = append(people, identity.Adoptee{Id: pdid.Id(h.Id), Alias: h.Alias, Name: h.Name})
		}
		passwords, err := s.Identity.Adopt(ctx, pdid.Id(t.Id), t.Alias, t.Name, people, password)
		if err != nil {
			return err
		}

		renewed := []uuid.UUID{}
		for _, h := range hs {
			pw, ok := passwords[h.Alias]
			if !ok {
				continue
			}
			self.Printf("@%s/%s  password %s\n", t.Alias, h.Alias, pw)
			renewed = append(renewed, h.Id)
			made++
		}
		// Signed in with a password that is not theirs any more.
		if _, err := s.Ent.Session.Delete().Where(session.HolderIdIn(renewed...)).Exec(ctx); err != nil {
			return err
		}
	}

	switch {
	case made == 0:
		self.Printf("nobody to bring in: roster has everybody here already\n")
	case password != "":
		self.Printf("\n%d login(s), every one with the password given; the ones from before roster are not brought along\n", made)
	default:
		self.Printf("\n%d password(s), shown once; the ones from before roster are not brought along\n", made)
	}
	return nil
}

func describeAll(ctx context.Context, self *xli.Command, s *cmd.Server, tenants []*ent.Tenant) error {
	self.Printf("# roster is not in this process: its operator makes these there, with the identifiers they have here\n")
	for _, t := range tenants {
		hs, err := loginsOf(ctx, s, t)
		if err != nil {
			return err
		}
		self.Printf("\nroster tenant add @%s %s\n", t.Alias, quoted(map[string]string{"id": pdid.Id(t.Id).String(), "name": t.Name}))
		for _, h := range hs {
			if h.Alias == "admin" {
				self.Printf("# roster made @%s/admin with the tenant: erase it first, and bind this one to `everything` in its place\n", t.Alias)
			}
			self.Printf("roster holder add @%s/%s %s\n", t.Alias, h.Alias, quoted(map[string]string{"id": pdid.Id(h.Id).String(), "name": h.Name}))
		}
		self.Printf("roster app install --tenant %s --role %s rove\n", t.Alias, strings.Join(identity.AgentMethods, ","))
	}
	self.Printf("\n# and gives each of them a way in there, such as `roster vouch reset @<tenant>/<login>`\n")
	return nil
}

// quoted is a request body for a shell.
func quoted(v map[string]string) string {
	b, _ := json.Marshal(v)
	return "'" + strings.ReplaceAll(string(b), "'", `'\''`) + "'"
}

// dropCredentials takes out the table a deployment from before roster kept its
// password verifiers in. Nothing reads it, its rows point at holders a purge
// takes, and a verifier nobody reads is only something to lose.
//
// The trail has nothing of them to forget: payday kept a declared secret out
// of it before Rove had passwords.
func dropCredentials(ctx context.Context, s *cmd.Server) (bool, error) {
	if left, err := fromBeforeRoster(ctx, s); err != nil || !left {
		return false, err
	}
	if _, err := s.Db.ExecContext(ctx, "DROP TABLE credential"); err != nil {
		return false, err
	}
	return true, nil
}

// fromBeforeRoster says whether this deployment still has the table it kept
// passwords in before roster held them: one `rove identity migrate` has not
// brought in, whose people cannot sign in.
func fromBeforeRoster(ctx context.Context, s *cmd.Server) (bool, error) {
	q := "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'credential'"
	if s.Dialect == "postgres" {
		q = "SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'credential'"
	}
	n := 0
	if err := s.Db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}
