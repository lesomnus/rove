package domain

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/slug"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent/audit"
	"github.com/lesomnus/rove/internal/ent/credential"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/party"
	"github.com/lesomnus/rove/server/password"
)

// The kinds of party and the roles a holder acts with.
var (
	PartyKinds = []string{"person", "team", "org", "vendor"}
	Roles      = []string{"owner", "admin", "manager", "member", "auditor"}
)

type domainParty struct {
	Domain
	app.PartyServiceServer
}

func (s Domain) Party() app.PartyServiceServer { return domainParty{s, s.Next().Party()} }

// Add checks what a party is before it is kept.
func (s domainParty) Add(ctx context.Context, req *app.PartyAddRequest) (*app.Party, error) {
	var out *app.Party
	err := s.tx(ctx, func(t *Tx) error {
		row := proto.Clone(req).(*app.PartyAddRequest)
		row.SetTenant(t.tenantRef())
		if row.GetKind() == "" {
			row.SetKind("person")
		}
		if !slices.Contains(PartyKinds, row.GetKind()) {
			return invalid("kind", "is one of %s", strings.Join(PartyKinds, ", "))
		}
		row.SetName(strings.TrimSpace(row.GetName()))
		if row.GetName() == "" {
			return invalid("name", "must not be empty")
		}
		if req.HasHolder() {
			return invalid("holder", "a login is given by Invite")
		}
		if p := idOf(req.GetParentId()); !p.IsZero() {
			if err := t.parentParty(p); err != nil {
				return err
			}
		}
		if err := t.begin(nil, "party.add", pdid.Nil, t.now, "추가: "+row.GetName(), "", nil); err != nil {
			return err
		}
		v, err := t.next.Party().Add(t.ctx, row)
		out = v
		return err
	})
	return out, err
}

// parentParty refuses a parent that is not a team or an organization of this
// tenant.
func (t *Tx) parentParty(id pdid.Id) error {
	p, err := t.db.Party.Query().Where(party.Id(id.Uuid()), party.TenantId(t.tenant.Uuid()), party.DateErasedIsNil()).Only(t.ctx)
	if err != nil {
		return status.Error(codes.NotFound, "parent_id: there is no such party")
	}
	if p.Kind != "team" && p.Kind != "org" {
		return invalid("parent_id", "a party is in a team or an organization")
	}
	return nil
}

// Me answers who is calling.
func (s domainParty) Me(ctx context.Context, _ *app.PartyMeRequest) (*app.PartyMeResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	h, err := t.next.Holder().Get(ctx, app.HolderGetRequest_builder{Ref: app.HolderRef_builder{Id: t.actor.Bytes()}.Build()}.Build())
	if err != nil {
		return nil, err
	}
	tn, err := t.next.Tenant().Get(ctx, app.TenantGetRequest_builder{Ref: app.TenantRef_builder{Id: t.tenant.Bytes()}.Build()}.Build())
	if err != nil {
		return nil, err
	}
	out := app.PartyMeResponse_builder{Holder: h, Tenant: tn, Role: h.GetRole()}.Build()
	if p, err := t.db.Party.Query().Where(party.TenantId(t.tenant.Uuid()), party.HolderId(t.actor.Uuid()), party.DateErasedIsNil()).First(ctx); err == nil {
		if v, err := t.next.Party().Get(ctx, app.PartyGetRequest_builder{Ref: app.PartyRef_builder{Id: pdid.Id(p.Id).Bytes()}.Build()}.Build()); err == nil {
			out.SetParty(v)
		}
	}
	return out, nil
}

// Update changes a party's own fields. An empty string leaves a field alone.
func (s domainParty) Update(ctx context.Context, req *app.PartyUpdateRequest) (*app.Party, error) {
	var out *app.Party
	err := s.tx(ctx, func(t *Tx) error {
		p, err := t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		patch := app.PartyPatchRequest_builder{Ref: req.GetRef(), DateUpdatedForce: z.Ptr(true)}.Build()
		if v := strings.TrimSpace(req.GetName()); v != "" {
			patch.SetName(v)
		}
		if v := req.GetDesc(); v != "" {
			patch.SetDesc(v)
		}
		if v := req.GetKind(); v != "" {
			if !slices.Contains(PartyKinds, v) {
				return invalid("kind", "is one of %s", strings.Join(PartyKinds, ", "))
			}
			patch.SetKind(v)
		}
		if req.GetParentNull() {
			patch.SetParentIdNull(true)
		} else if v := idOf(req.GetParentId()); !v.IsZero() {
			if v == idOf(p.GetId()) {
				return invalid("parent_id", "a party is not inside itself")
			}
			if err := t.parentParty(v); err != nil {
				return err
			}
			patch.SetParentId(v.Bytes())
		}
		if v := req.GetEmail(); v != "" {
			patch.SetEmail(v)
		}
		if v := req.GetPhone(); v != "" {
			patch.SetPhone(v)
		}
		if v := req.GetCode(); v != "" {
			patch.SetCode(v)
		}
		if len(req.GetLabels()) > 0 {
			patch.SetLabels(req.GetLabels())
		}
		if err := t.begin(nil, "party.update", pdid.Nil, t.now, "수정: "+p.GetName(), "", nil); err != nil {
			return err
		}
		out, err = t.next.Party().Patch(t.ctx, patch)
		return err
	})
	return out, err
}

// Invite gives a person a login.
func (s domainParty) Invite(ctx context.Context, req *app.PartyInviteRequest) (*app.PartyInviteResponse, error) {
	var out *app.PartyInviteResponse
	err := s.tx(ctx, func(t *Tx) error {
		p, err := t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		if p.GetKind() != "person" {
			return invalid("ref", "only a person signs in")
		}
		if p.HasHolder() {
			return failed("%s already has a login", p.GetName())
		}
		alias, err := slug.ParseAlias(req.GetAlias())
		if err != nil {
			return invalid("alias", "%v", err)
		}
		role := or(req.GetRole(), "member")
		if !slices.Contains(Roles, role) {
			return invalid("role", "is one of %s", strings.Join(Roles, ", "))
		}
		pw := req.GetPassword()
		made := ""
		if pw == "" {
			pw = password.Make()
			made = pw
		}
		hash, err := password.Hash(pw)
		if err != nil {
			return invalid("password", "%v", err)
		}

		if err := t.begin(nil, "party.invite", pdid.Nil, t.now, fmt.Sprintf("로그인 발급: %s (@%s)", p.GetName(), alias), "", map[string]string{"role": role}); err != nil {
			return err
		}
		h, err := t.next.Holder().Add(t.ctx, app.HolderAddRequest_builder{
			Tenant: t.tenantRef(),
			Alias:  alias,
			Name:   p.GetName(),
			Role:   z.Ptr(role),
		}.Build())
		if err != nil {
			return err
		}
		if _, err := t.next.Credential().Add(t.ctx, app.CredentialAddRequest_builder{
			Tenant: t.tenantRef(),
			Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
			Secret: []byte(hash),
		}.Build()); err != nil {
			return err
		}
		v, err := t.next.Party().Patch(t.ctx, app.PartyPatchRequest_builder{
			Ref:              req.GetRef(),
			Holder:           app.HolderRef_builder{Id: h.GetId()}.Build(),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		if err != nil {
			return err
		}
		out = app.PartyInviteResponse_builder{Party: v, Holder: h, Password: made}.Build()
		return nil
	})
	return out, err
}

// SetRole changes what a person may do. The last owner of a tenant cannot be
// demoted, or nobody could manage it.
func (s domainParty) SetRole(ctx context.Context, req *app.PartySetRoleRequest) (*app.Party, error) {
	var out *app.Party
	err := s.tx(ctx, func(t *Tx) error {
		if !slices.Contains(Roles, req.GetRole()) {
			return invalid("role", "is one of %s", strings.Join(Roles, ", "))
		}
		p, err := t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		h := idOf(p.GetHolder().GetId())
		if h.IsZero() {
			return failed("%s has no login", p.GetName())
		}
		cur, err := t.db.Holder.Query().Where(holder.Id(h.Uuid())).Only(t.ctx)
		if err != nil {
			return err
		}
		if cur.Role == "owner" && req.GetRole() != "owner" {
			n, err := t.db.Holder.Query().Where(holder.TenantId(t.tenant.Uuid()), holder.Role("owner"), holder.DateErasedIsNil()).Count(t.ctx)
			if err != nil {
				return err
			}
			if n <= 1 {
				return failed("this is the last owner; make somebody else an owner first")
			}
		}
		if err := t.begin(nil, "party.role", pdid.Nil, t.now, fmt.Sprintf("역할 변경: %s → %s", p.GetName(), req.GetRole()), "", nil); err != nil {
			return err
		}
		if _, err := t.next.Holder().Patch(t.ctx, app.HolderPatchRequest_builder{
			Ref:              app.HolderRef_builder{Id: h.Bytes()}.Build(),
			Role:             z.Ptr(req.GetRole()),
			DateUpdatedForce: z.Ptr(true),
		}.Build()); err != nil {
			return err
		}
		out, err = t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: req.GetRef()}.Build())
		return err
	})
	return out, err
}

// SetPassword changes a password: the caller's own after checking the current
// one, or anybody's for an admin, which the policy decides.
func (s domainParty) SetPassword(ctx context.Context, req *app.PartySetPasswordRequest) (*app.PartySetPasswordResponse, error) {
	err := s.tx(ctx, func(t *Tx) error {
		target := t.actor
		own := true
		if req.HasRef() {
			p, err := t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: req.GetRef()}.Build())
			if err != nil {
				return err
			}
			if h := idOf(p.GetHolder().GetId()); !h.IsZero() && h != t.actor {
				target, own = h, false
			}
		}
		if !own {
			me, err := t.db.Holder.Query().Where(holder.Id(t.actor.Uuid())).Only(t.ctx)
			if err != nil {
				return err
			}
			if me.Role != "owner" && me.Role != "admin" {
				return status.Error(codes.PermissionDenied, "only an admin sets another person's password")
			}
		}

		c, err := t.db.Credential.Query().Where(credential.TenantId(t.tenant.Uuid()), credential.HolderId(target.Uuid())).Only(t.ctx)
		if err != nil {
			return failed("this person has no password to change")
		}
		if own {
			ok, err := password.Check(string(c.Secret), req.GetCurrent())
			if err != nil || !ok {
				return invalid("current", "is not the current password")
			}
		}
		hash, err := password.Hash(req.GetPassword())
		if err != nil {
			return invalid("password", "%v", err)
		}
		if err := t.begin(nil, "party.password", pdid.Nil, t.now, "비밀번호 변경", "", nil); err != nil {
			return err
		}
		_, err = t.next.Credential().Patch(t.ctx, app.CredentialPatchRequest_builder{
			Ref:              app.CredentialRef_builder{Id: pdid.Id(c.Id).Bytes()}.Build(),
			Secret:           []byte(hash),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		return err
	})
	if err != nil {
		return nil, err
	}
	return &app.PartySetPasswordResponse{}, nil
}

// Deactivate ends a login and keeps the person and their history.
func (s domainParty) Deactivate(ctx context.Context, req *app.PartyDeactivateRequest) (*app.Party, error) {
	var out *app.Party
	err := s.tx(ctx, func(t *Tx) error {
		p, err := t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		h := idOf(p.GetHolder().GetId())
		if h.IsZero() {
			out = p
			return nil
		}
		if h == t.actor {
			return failed("you cannot deactivate yourself")
		}
		if err := t.begin(nil, "party.deactivate", pdid.Nil, t.now, "로그인 비활성화: "+p.GetName(), "", nil); err != nil {
			return err
		}
		if _, err := t.next.Holder().Erase(t.ctx, app.HolderRef_builder{Id: h.Bytes()}.Build()); err != nil {
			return err
		}
		out, err = t.next.Party().Patch(t.ctx, app.PartyPatchRequest_builder{
			Ref:              req.GetRef(),
			HolderNull:       z.Ptr(true),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		return err
	})
	return out, err
}

// Pseudonymize forgets a person: the party keeps its identifier, so the
// history that names it still reads, and loses everything that said who it
// was -- on the row, and in the trail's copies of the row (design 8.2, D13).
func (s domainParty) Pseudonymize(ctx context.Context, req *app.PartyPseudonymizeRequest) (*app.Party, error) {
	var out *app.Party
	err := s.tx(ctx, func(t *Tx) error {
		p, err := t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		if p.GetKind() != "person" {
			return invalid("ref", "only a person is forgotten")
		}
		id := idOf(p.GetId())
		if err := t.begin(nil, "party.pseudonymize", pdid.Nil, t.now, "개인정보 삭제", "", nil); err != nil {
			return err
		}
		if h := idOf(p.GetHolder().GetId()); !h.IsZero() {
			if _, err := t.next.Holder().Erase(t.ctx, app.HolderRef_builder{Id: h.Bytes()}.Build()); err != nil {
				return err
			}
		}
		out, err = t.next.Party().Patch(t.ctx, app.PartyPatchRequest_builder{
			Ref:              req.GetRef(),
			Name:             z.Ptr("익명 " + id.String()[:8]),
			Desc:             z.Ptr(""),
			Email:            z.Ptr(""),
			Phone:            z.Ptr(""),
			Code:             z.Ptr(""),
			Labels:           map[string]string{},
			HolderNull:       z.Ptr(true),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		if err != nil {
			return err
		}

		// The trail kept the row as it was after every write; those copies
		// are the person too. The trail's RPCs refuse writes, rightly, so
		// this is the deployment going to the table it owns.
		_, err = t.db.Audit.Update().
			Where(audit.ObjectId(id.Uuid())).
			SetValue([]byte{}).
			SetPatch([]byte{}).
			Save(t.ctx)
		return err
	})
	return out, err
}

type domainAssetType struct {
	Domain
	app.AssetTypeServiceServer
}

func (s Domain) AssetType() app.AssetTypeServiceServer {
	return domainAssetType{s, s.Next().AssetType()}
}

func (s domainAssetType) Add(ctx context.Context, req *app.AssetTypeAddRequest) (*app.AssetType, error) {
	var out *app.AssetType
	err := s.tx(ctx, func(t *Tx) error {
		row := proto.Clone(req).(*app.AssetTypeAddRequest)
		row.SetTenant(t.tenantRef())
		if strings.TrimSpace(row.GetName()) == "" {
			return invalid("name", "must not be empty")
		}
		if row.GetKind() == "" {
			row.SetKind("item")
		}
		if !slices.Contains(Kinds, row.GetKind()) {
			return invalid("kind", "is one of %s", strings.Join(Kinds, ", "))
		}
		if err := checkSpec(row.GetSpec()); err != nil {
			return err
		}
		if p := idOf(req.GetParentId()); !p.IsZero() {
			if _, err := t.next.AssetType().Get(t.ctx, app.AssetTypeGetRequest_builder{Ref: app.AssetTypeRef_builder{Id: p.Bytes()}.Build()}.Build()); err != nil {
				return err
			}
		}
		row.SetSchemaVersion(1)
		if err := t.begin(nil, "type.add", pdid.Nil, t.now, "유형 추가: "+row.GetName(), "", nil); err != nil {
			return err
		}
		v, err := t.next.AssetType().Add(t.ctx, row)
		out = v
		return err
	})
	return out, err
}

func checkSpec(spec *app.TypeSpec) error {
	seen := map[string]bool{}
	for i, a := range spec.GetAttributes() {
		k := a.GetKey()
		if k == "" || strings.ContainsAny(k, " .") {
			return invalid(fmt.Sprintf("spec.attributes[%d].key", i), "is a word with no spaces or dots")
		}
		if seen[k] {
			return invalid(fmt.Sprintf("spec.attributes[%d].key", i), "%q is declared twice", k)
		}
		seen[k] = true
		switch a.GetType() {
		case "", "text", "number", "bool", "date":
		case "enum":
			if len(a.GetOptions()) == 0 {
				return invalid(fmt.Sprintf("spec.attributes[%d].options", i), "an enum offers options")
			}
		default:
			return invalid(fmt.Sprintf("spec.attributes[%d].type", i), "is text, number, bool, date or enum")
		}
	}
	return nil
}

// Update changes a type. A changed spec is a new schema version; the history
// keeps reading because facts are keyed by the attribute's key, not its label.
func (s domainAssetType) Update(ctx context.Context, req *app.AssetTypeUpdateRequest) (*app.AssetType, error) {
	var out *app.AssetType
	err := s.tx(ctx, func(t *Tx) error {
		v, err := t.next.AssetType().Get(t.ctx, app.AssetTypeGetRequest_builder{Ref: req.GetRef(), Select: app.AssetTypeSelect_builder{All: z.Ptr(true)}.Build()}.Build())
		if err != nil {
			return err
		}
		patch := app.AssetTypePatchRequest_builder{Ref: req.GetRef(), DateUpdatedForce: z.Ptr(true)}.Build()
		if n := strings.TrimSpace(req.GetName()); n != "" {
			patch.SetName(n)
		}
		if d := req.GetDesc(); d != "" {
			patch.SetDesc(d)
		}
		if k := req.GetKind(); k != "" {
			if !slices.Contains(Kinds, k) {
				return invalid("kind", "is one of %s", strings.Join(Kinds, ", "))
			}
			patch.SetKind(k)
		}
		if req.GetParentNull() {
			patch.SetParentIdNull(true)
		} else if p := idOf(req.GetParentId()); !p.IsZero() {
			// A type is not its own ancestor.
			cur := p
			for range 16 {
				if cur == idOf(v.GetId()) {
					return invalid("parent_id", "that would make the type its own ancestor")
				}
				pv, err := t.next.AssetType().Get(t.ctx, app.AssetTypeGetRequest_builder{Ref: app.AssetTypeRef_builder{Id: cur.Bytes()}.Build()}.Build())
				if err != nil {
					return err
				}
				cur = idOf(pv.GetParentId())
				if cur.IsZero() {
					break
				}
			}
			patch.SetParentId(p.Bytes())
		}
		if req.HasSpec() {
			if err := checkSpec(req.GetSpec()); err != nil {
				return err
			}
			if !proto.Equal(req.GetSpec(), v.GetSpec()) {
				patch.SetSpec(req.GetSpec())
				patch.SetSchemaVersion(v.GetSchemaVersion() + 1)
			}
		}
		if err := t.begin(nil, "type.update", pdid.Nil, t.now, "유형 수정: "+v.GetName(), "", nil); err != nil {
			return err
		}
		out, err = t.next.AssetType().Patch(t.ctx, patch)
		return err
	})
	return out, err
}

type domainItemModel struct {
	Domain
	app.ItemModelServiceServer
}

func (s Domain) ItemModel() app.ItemModelServiceServer {
	return domainItemModel{s, s.Next().ItemModel()}
}

func (s domainItemModel) Update(ctx context.Context, req *app.ItemModelUpdateRequest) (*app.ItemModel, error) {
	var out *app.ItemModel
	err := s.tx(ctx, func(t *Tx) error {
		v, err := t.next.ItemModel().Get(t.ctx, app.ItemModelGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		patch := app.ItemModelPatchRequest_builder{Ref: req.GetRef(), DateUpdatedForce: z.Ptr(true)}.Build()
		if n := strings.TrimSpace(req.GetName()); n != "" {
			patch.SetName(n)
		}
		if d := req.GetDesc(); d != "" {
			patch.SetDesc(d)
		}
		if m := req.GetMaker(); m != "" {
			patch.SetMaker(m)
		}
		if c := req.GetCode(); c != "" {
			patch.SetCode(c)
		}
		if req.GetTypeNull() {
			patch.SetTypeNull(true)
		} else if req.HasType() {
			patch.SetType(req.GetType())
		}
		if req.HasSpec() {
			patch.SetSpec(req.GetSpec())
		}
		if err := t.begin(nil, "model.update", pdid.Nil, t.now, "모델 수정: "+v.GetName(), "", nil); err != nil {
			return err
		}
		out, err = t.next.ItemModel().Patch(t.ctx, patch)
		return err
	})
	return out, err
}
