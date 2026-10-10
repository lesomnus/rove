package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/z"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/session"
	"github.com/lesomnus/rove/server/domain"
)

// NewCmdHolder is `rove holder`: a person's login, from a shell, on the roster
// in this process (design 9.10).
//
// What the people screen does, for the operator who has no screen to do it
// from -- the owner who lost their password above all. A deployment whose
// roster is one of its own refuses both: its people are made and given
// passwords there, by whoever administers the tenant.
func NewCmdHolder(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "holder",
		Brief: "a person's login, on the roster in this process",

		Commands: xli.Commands{
			{
				Name:  "add",
				Brief: "make somebody who signs in, with a password shown once",
				Flags: flg.Flags{
					&flg.String{Name: "tenant", Brief: "the tenant, by alias"},
					&flg.String{Name: "login", Brief: "their login"},
					&flg.String{Name: "name", Brief: "their name; the login by default"},
					&flg.String{Name: "email", Brief: "their e-mail, kept on the person record"},
					&flg.String{Name: "role", Brief: "what they may do: " + strings.Join(domain.Roles, ", ") + "; member by default"},
					&flg.String{Name: "password", Brief: "their password; empty makes one up"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					return addHolder(ctx, self, c)
				}),
			},
			{
				Name:  "password",
				Brief: "give somebody a new password, shown once, and end where they are signed in",
				Flags: flg.Flags{
					&flg.String{Name: "tenant", Brief: "the tenant, by alias"},
					&flg.String{Name: "login", Brief: "their login"},
					&flg.String{Name: "password", Brief: "the new password; empty makes one up"},
				},
				Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
					return resetHolder(ctx, self, c)
				}),
			},
		},
	}
}

func flag(self *xli.Command, name string) string {
	v, _ := flg.Find[string](self, name)
	return strings.TrimSpace(v)
}

// embedded is the server, refused when its roster is not in this process.
func embedded(ctx context.Context, c *cmd.Config) (*cmd.Server, error) {
	s, err := cmd.Build(ctx, *c)
	if err != nil {
		return nil, err
	}
	if !s.Identity.Embedded() {
		s.Close()
		return nil, errors.New("auth.roster names a roster of its own: people are made and given passwords there, by whoever administers the tenant")
	}
	return s, nil
}

func addHolder(ctx context.Context, self *xli.Command, c *cmd.Config) error {
	login := flag(self, "login")
	if login == "" {
		return errors.New("--login: what they sign in with")
	}
	name := flag(self, "name")
	if name == "" {
		name = login
	}
	role := flag(self, "role")
	if role == "" {
		role = "member"
	}
	if !slices.Contains(domain.Roles, role) {
		return fmt.Errorf("--role: one of %s", strings.Join(domain.Roles, ", "))
	}

	s, err := embedded(ctx, c)
	if err != nil {
		return err
	}
	defer s.Close()

	t, err := tenantNamed(ctx, s.Ent, self)
	if err != nil {
		return err
	}
	if taken, err := s.Ent.Holder.Query().Where(holder.TenantId(t.Id), holder.Alias(login), holder.DateErasedIsNil()).Exist(ctx); err != nil {
		return err
	} else if taken {
		return fmt.Errorf("--login: @%s/%s is somebody already", t.Alias, login)
	}

	// At roster, which is where they sign in, and then here with its
	// identifier.
	p, err := s.Identity.AddPerson(ctx, pdid.Id(t.Id), login, name, pdid.Nil)
	if err != nil {
		return fmt.Errorf("roster: %w", err)
	}
	made := ""
	if pw := flag(self, "password"); pw != "" {
		err = s.Identity.SetPassword(ctx, p.Id, pw)
	} else {
		made, err = s.Identity.IssuePassword(ctx, p.Id)
	}
	if err != nil {
		s.Identity.ForgetPerson(ctx, p.Id)
		return fmt.Errorf("roster: %w", err)
	}

	tref := app.TenantRef_builder{Id: pdid.Id(t.Id).Bytes()}.Build()
	h, err := s.Base.Holder().Add(ctx, app.HolderAddRequest_builder{
		Id:     p.Id.Bytes(),
		Tenant: tref,
		Alias:  login,
		Name:   name,
		Role:   z.Ptr(role),
	}.Build())
	if err == nil {
		_, err = s.Base.Party().Add(ctx, app.PartyAddRequest_builder{
			Tenant: tref,
			Name:   name,
			Kind:   "person",
			Email:  flag(self, "email"),
			Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
		}.Build())
	}
	if err != nil {
		s.Identity.ForgetPerson(ctx, p.Id)
		return err
	}

	self.Printf("holder   @%s/%s (%s), %s\n", t.Alias, login, p.Id, role)
	if made != "" {
		self.Printf("password %s\n", made)
	}
	return nil
}

func resetHolder(ctx context.Context, self *xli.Command, c *cmd.Config) error {
	login := flag(self, "login")
	if login == "" {
		return errors.New("--login: whose password")
	}

	s, err := embedded(ctx, c)
	if err != nil {
		return err
	}
	defer s.Close()

	t, err := tenantNamed(ctx, s.Ent, self)
	if err != nil {
		return err
	}
	h, err := s.Ent.Holder.Query().Where(holder.TenantId(t.Id), holder.Alias(login), holder.DateErasedIsNil()).Only(ctx)
	if ent.IsNotFound(err) {
		return fmt.Errorf("--login: nobody here is @%s/%s", t.Alias, login)
	}
	if err != nil {
		return err
	}

	id := pdid.Id(h.Id)
	made := ""
	if pw := flag(self, "password"); pw != "" {
		err = s.Identity.SetPassword(ctx, id, pw)
	} else {
		made, err = s.Identity.IssuePassword(ctx, id)
	}
	if err != nil {
		return fmt.Errorf("roster: %w", err)
	}
	// Wherever they were signed in -- here, and at roster -- ends with it.
	if err := s.Identity.Invalidate(ctx, id); err != nil {
		return fmt.Errorf("roster: %w", err)
	}
	if _, err := s.Ent.Session.Delete().Where(session.HolderId(h.Id)).Exec(ctx); err != nil {
		return err
	}

	self.Printf("holder   @%s/%s\n", t.Alias, login)
	if made != "" {
		self.Printf("password %s\n", made)
	}
	return nil
}
