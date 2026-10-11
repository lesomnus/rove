// Package tenancy is how a tenant and the people in it arrive in Rove: from
// roster, the first time somebody it vouched for signs in (design 9.10), and
// with what a tenant starts with when it does.
//
// Rove's `Tenant` and `Holder` are payday's entities, the same ones roster
// has, and are made with roster's identifiers. So `Holder.id` is the `sub`
// every product knows the person by, and the trail names the same actor in
// both apps.
package tenancy

import (
	"context"
	"errors"
	"fmt"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/z"
	"github.com/protobuf-orm/ent/dialect"
	"github.com/protobuf-orm/protoc-gen-orm-ent/runtime/enttx"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/party"
	enttenant "github.com/lesomnus/rove/internal/ent/tenant"
	"github.com/lesomnus/rove/internal/identity"
)

var (
	// ErrEnded is somebody whose login was ended here: a login stopped in
	// Rove stays stopped, whatever roster says of them.
	ErrEnded = errors.New("this login was ended in Rove")

	// ErrClash is a tenant or a person already here under the name roster
	// gives, with another identifier: a deployment from before roster held
	// its people, which `rove identity migrate` brings in.
	ErrClash = errors.New("here already under another identifier; see `rove identity migrate`")
)

// Anchor makes Rove's rows for somebody roster vouched for.
type Anchor struct {
	// Base is the stack with no wall and no domain layer, and Drv the driver
	// it writes through: what puts up a tenant and the first person in it,
	// before there is anybody to act as.
	Base app.Server
	Drv  dialect.Driver

	// Ungated is the stack with the domain layer and no wall, which a new
	// tenant's setup is written through as its first person.
	Ungated app.Server

	// LabelSuffix is what a default label subdomain ends with; empty gives a
	// new tenant none.
	LabelSuffix string
}

// Of is Rove's holder for somebody roster vouched for, made the first time
// they arrive: their tenant if it is new here, with what a register starts
// with; their holder -- the owner when they are the tenant's first person
// here, a member otherwise -- and the person record beside it. One
// transaction, so a sign-in that fails half way leaves nothing to trip over.
func (a Anchor) Of(ctx context.Context, p identity.Person) (*ent.Holder, error) {
	db := ent.NewClient(ent.Driver(a.Drv))
	h, err := db.Holder.Get(ctx, p.Id.Uuid())
	switch {
	case err == nil && h.DateErased != nil:
		return nil, ErrEnded
	case err == nil:
		return h, nil
	case !ent.IsNotFound(err):
		return nil, err
	}

	if err := a.make(ctx, p); err != nil {
		// Two first sign-ins at once: the one that lost reads what the other
		// made.
		if h, e := db.Holder.Get(ctx, p.Id.Uuid()); e == nil {
			if h.DateErased != nil {
				return nil, ErrEnded
			}
			return h, nil
		}

		return nil, err
	}

	return db.Holder.Get(ctx, p.Id.Uuid())
}

func (a Anchor) make(ctx context.Context, p identity.Person) error {
	tdrv, tx, err := dialect.BeginTx(ctx, a.Drv)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	base, err := enttx.Rebind(a.Base, tdrv)
	if err != nil {
		return err
	}
	db := ent.NewClient(ent.Driver(tdrv))
	tref := app.TenantRef_builder{Id: p.Tenant.Bytes()}.Build()

	fresh := false
	if _, err := db.Tenant.Get(ctx, p.Tenant.Uuid()); ent.IsNotFound(err) {
		clash, err := db.Tenant.Query().Where(enttenant.Alias(p.TenantAlias)).Exist(ctx)
		if err != nil {
			return err
		}
		if clash {
			return fmt.Errorf("tenant @%s: %w", p.TenantAlias, ErrClash)
		}
		if _, err := base.Tenant().Add(ctx, app.TenantAddRequest_builder{
			Id:    p.Tenant.Bytes(),
			Alias: p.TenantAlias,
			Name:  or(p.TenantName, p.TenantAlias),
		}.Build()); err != nil {
			return fmt.Errorf("tenant @%s: %w", p.TenantAlias, err)
		}
		fresh = true
	} else if err != nil {
		return err
	}

	clash, err := db.Holder.Query().Where(holder.TenantId(p.Tenant.Uuid()), holder.Alias(p.Alias), holder.DateErasedIsNil()).Exist(ctx)
	if err != nil {
		return err
	}
	if clash {
		return fmt.Errorf("@%s/%s: %w", p.TenantAlias, p.Alias, ErrClash)
	}
	others, err := db.Holder.Query().Where(holder.TenantId(p.Tenant.Uuid()), holder.DateErasedIsNil()).Exist(ctx)
	if err != nil {
		return err
	}
	role := "member"
	if !others {
		// Somebody has to be able to manage a tenant, and the first person
		// of one is who roster made it for.
		role = "owner"
	}
	h, err := base.Holder().Add(ctx, app.HolderAddRequest_builder{
		Id:     p.Id.Bytes(),
		Tenant: tref,
		Alias:  p.Alias,
		Name:   or(p.Name, p.Alias),
		Role:   z.Ptr(role),
	}.Build())
	if err != nil {
		return fmt.Errorf("@%s/%s: %w", p.TenantAlias, p.Alias, err)
	}
	href := app.HolderRef_builder{Id: h.GetId()}.Build()
	linked, err := db.Party.Query().Where(party.HolderId(p.Id.Uuid())).Exist(ctx)
	if err != nil {
		return err
	}
	if !linked {
		if _, err := base.Party().Add(ctx, app.PartyAddRequest_builder{
			Tenant: tref,
			Name:   or(p.Name, p.Alias),
			Kind:   "person",
			Holder: href,
		}.Build()); err != nil {
			return err
		}
	}

	if fresh {
		ungated, err := enttx.Rebind(a.Ungated, tdrv)
		if err != nil {
			return err
		}
		// As its first person, so that what it starts with reads as theirs.
		as := frame.Into(ctx, frame.New(p.Id, p.Tenant, frame.Whole()).WithScope(frame.Only(p.Tenant)).WithRow(h))
		if err := SetUp(as, ungated, p.TenantAlias, a.LabelSuffix); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// SetUp is what a tenant starts with: the types an asset register of an office
// has, and a default label subdomain when the deployment offers one. It is
// written as whoever `ctx` is in the tenant, through the stack with the domain
// layer.
func SetUp(ctx context.Context, s app.Server, alias, labelSuffix string) error {
	if err := seedTypes(ctx, s); err != nil {
		return err
	}
	if labelSuffix != "" {
		if _, err := s.TenantDomain().Add(ctx, app.TenantDomainAddRequest_builder{Sub: z.Ptr(alias)}.Build()); err != nil {
			return fmt.Errorf("label domain: %w", err)
		}
	}

	return nil
}

func or(v, otherwise string) string {
	if v == "" {
		return otherwise
	}
	return v
}
