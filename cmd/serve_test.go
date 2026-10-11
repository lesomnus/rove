package cmd_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lesomnus/payday/auth/authsession"
	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/payday/pdpb"
	"github.com/lesomnus/payday/pdtest"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cli"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/party"
	"github.com/lesomnus/rove/internal/identity"
	"github.com/lesomnus/rove/server/session"
)

type world struct {
	x      *require.Assertions
	s      *cmd.Server
	c      cmd.Config
	conn   *grpc.ClientConn
	tenant *app.Tenant
	owner  context.Context
}

// signIn mints a session for a holder and answers a context that carries its
// cookie, the way a browser's call arrives.
func (w *world) signIn(h *app.Holder) context.Context {
	hid, _ := pdid.From(h.GetId())
	tid, _ := pdid.From(w.tenant.GetId())
	_, c, err := w.s.Sessions.Mint(context.Background(), authsession.Session{Id: hid.String(), TenantId: tid.String(), Grant: frame.Whole()})
	w.x.NoError(err)
	return metadata.AppendToOutgoingContext(context.Background(), "cookie", c.Name+"="+c.Value)
}

// holder is somebody with a role here, made at roster first -- where they
// sign in and what a session is held to -- and here with its identifier.
func (w *world) holder(alias, role string) *app.Holder {
	ctx := context.Background()
	tid, _ := pdid.From(w.tenant.GetId())
	p, err := w.s.Identity.AddPerson(ctx, tid, alias, alias, pdid.Nil)
	w.x.NoError(err)
	return w.here(p, role)
}

func (w *world) here(p identity.Person, role string) *app.Holder {
	h, err := w.s.Base.Holder().Add(context.Background(), app.HolderAddRequest_builder{
		Id:     p.Id.Bytes(),
		Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Alias:  p.Alias,
		Role:   z.Ptr(role),
	}.Build())
	w.x.NoError(err)
	return h
}

func newWorld(t *testing.T) *world {
	x := require.New(t)
	ctx := context.Background()
	driver, dsn := pdtest.DB(t)
	c := cmd.Config{}
	c.Db.Driver = driver
	c.Db.Dsn = dsn
	c.Watch.Broker = "memory"
	c.App.Files = t.TempDir()
	c.Auth.Roster.Db.Driver, c.Auth.Roster.Db.Dsn = pdtest.DB(t)

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(cli.Migrate(ctx, s))

	// The tenant and its owner at roster, then here, as `rove init` does.
	owner, _, err := s.Identity.Seed(ctx, "acme", "owner", pdid.Nil)
	x.NoError(err)
	tn, err := s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Id: owner.Tenant.Bytes(), Alias: "acme"}.Build())
	x.NoError(err)

	g, err := s.Grpc(ctx, c)
	x.NoError(err)
	w := &world{x: x, s: s, c: c, conn: pdtest.Serve(t, g), tenant: tn}
	w.owner = w.signIn(w.here(owner, "owner"))
	return w
}

func TestTheRoleTable(t *testing.T) {
	w := newWorld(t)
	x := w.x
	assets := app.NewAssetServiceClient(w.conn)
	member := w.signIn(w.holder("kim", "member"))
	auditor := w.signIn(w.holder("lee", "auditor"))

	room, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "회의실", Kind: "space"}.Build())
	x.NoError(err)
	laptop, err := assets.Add(w.owner, app.AssetAddRequest_builder{Name: "노트북"}.Build())
	x.NoError(err)

	code := func(err error) codes.Code { return status.Code(err) }

	_, err = assets.Search(context.Background(), app.AssetSearchRequest_builder{}.Build())
	x.Equal(codes.Unauthenticated, code(err), "nobody is signed in")

	_, err = assets.Search(member, app.AssetSearchRequest_builder{}.Build())
	x.NoError(err, "a member reads the register")
	_, err = assets.Move(member, app.AssetMoveRequest_builder{Ref: app.AssetRef_builder{Id: laptop.GetId()}.Build(), To: app.AssetRef_builder{Id: room.GetId()}.Build()}.Build())
	x.Equal(codes.PermissionDenied, code(err), "and does not move things")
	_, err = assets.Search(auditor, app.AssetSearchRequest_builder{}.Build())
	x.NoError(err)
	_, err = assets.Add(auditor, app.AssetAddRequest_builder{Name: "x"}.Build())
	x.Equal(codes.PermissionDenied, code(err), "an auditor changes nothing")

	// What only the domain layer writes is sealed for everybody, the owner
	// included, and so is a generated write that would skip the history.
	_, err = app.NewPlacementServiceClient(w.conn).Add(w.owner, app.PlacementAddRequest_builder{}.Build())
	x.Equal(codes.PermissionDenied, code(err))
	_, err = assets.Patch(w.owner, app.AssetPatchRequest_builder{Ref: app.AssetRef_builder{Id: laptop.GetId()}.Build(), Name: z.Ptr("x")}.Build())
	x.Equal(codes.PermissionDenied, code(err))
	_, err = app.NewEventServiceClient(w.conn).Add(w.owner, app.EventAddRequest_builder{Kind: "forged"}.Build())
	x.Equal(codes.PermissionDenied, code(err))
	_, err = app.NewPlacementServiceClient(w.conn).List(w.owner, app.PlacementListRequest_builder{}.Build())
	x.Equal(codes.PermissionDenied, code(err), "the time rows are read through the history, which keeps to the view window")

	_, err = app.NewAuditServiceClient(w.conn).Recent(auditor, app.AuditRecentRequest_builder{}.Build())
	x.NoError(err, "the trail is what an auditor is for")
	_, err = app.NewAuditServiceClient(w.conn).Recent(member, app.AuditRecentRequest_builder{}.Build())
	x.Equal(codes.PermissionDenied, code(err))

	// A batch is checked per operation, not as the batch.
	any, err := anypb.New(app.PlacementAddRequest_builder{}.Build())
	x.NoError(err)
	_, err = pdpb.NewBatchServiceClient(w.conn).Do(w.owner, pdpb.BatchRequest_builder{Ops: []*pdpb.Op{
		pdpb.Op_builder{Method: "/rove.PlacementService/Add", Request: any}.Build(),
	}}.Build())
	x.Equal(codes.PermissionDenied, code(err))

	move, err := anypb.New(app.AssetMoveRequest_builder{Ref: app.AssetRef_builder{Id: laptop.GetId()}.Build(), To: app.AssetRef_builder{Id: room.GetId()}.Build()}.Build())
	x.NoError(err)
	_, err = pdpb.NewBatchServiceClient(w.conn).Do(member, pdpb.BatchRequest_builder{Ops: []*pdpb.Op{
		pdpb.Op_builder{Method: "/rove.AssetService/Move", Request: move}.Build(),
	}}.Build())
	x.Equal(codes.PermissionDenied, code(err))
	_, err = pdpb.NewBatchServiceClient(w.conn).Do(w.owner, pdpb.BatchRequest_builder{Ops: []*pdpb.Op{
		pdpb.Op_builder{Method: "/rove.AssetService/Move", Request: move}.Build(),
	}}.Build())
	x.NoError(err, "the owner may, and the domain layer runs inside the batch's transaction")
	got, err := assets.Get(w.owner, app.AssetGetRequest_builder{Ref: app.AssetRef_builder{Id: laptop.GetId()}.Build()}.Build())
	x.NoError(err)
	x.Equal(room.GetId(), got.GetParentId())
}

// TestSignIn is a password roster checked, and somebody it vouched for made
// here the first time they arrive.
func TestSignIn(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()
	tid, _ := pdid.From(w.tenant.GetId())

	// Somebody at roster who has not been here yet.
	park, err := w.s.Identity.AddPerson(ctx, tid, "park", "박", pdid.Nil)
	x.NoError(err)
	x.NoError(w.s.Identity.SetPassword(ctx, park.Id, "correct horse"))

	login := session.SignIn(w.s.Sessions, w.s.Identity, w.s.Anchor(w.c))
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/session", bytes.NewBufferString(body))
		r.RemoteAddr = "10.0.0.1:1234"
		rec := httptest.NewRecorder()
		login.ServeHTTP(rec, r)
		return rec
	}

	x.Equal(http.StatusNoContent, post(`{"login":"park","password":"correct horse"}`).Code, "the one tenant here, when the form names none")
	x.Equal(http.StatusNoContent, post(`{"tenant":"acme","login":"park","password":"correct horse"}`).Code)
	x.Equal(http.StatusUnauthorized, post(`{"tenant":"other","login":"park","password":"correct horse"}`).Code)
	x.Equal(http.StatusUnauthorized, post(`{"login":"park","password":"wrong one"}`).Code)
	x.Equal(http.StatusUnauthorized, post(`{"login":"nobody","password":"correct horse"}`).Code)

	// They arrived: a holder with roster's identifier, a member, and the
	// person record beside it.
	h, err := w.s.Ent.Holder.Get(ctx, park.Id.Uuid())
	x.NoError(err)
	x.Equal("member", h.Role)
	x.Equal(tid.Uuid(), h.TenantId)
	n, err := w.s.Ent.Party.Query().Where(party.HolderId(park.Id.Uuid())).Count(ctx)
	x.NoError(err)
	x.Equal(1, n, "a second sign-in made them again")

	res := post(`{"login":"park","password":"correct horse"}`)
	cookie := res.Result().Cookies()[0]
	md := metadata.AppendToOutgoingContext(ctx, "cookie", cookie.Name+"="+cookie.Value)
	me, err := app.NewPartyServiceClient(w.conn).Me(md, &app.PartyMeRequest{})
	x.NoError(err)
	x.Equal("park", me.GetHolder().GetAlias())

	t.Run("by the address on their person record, on the roster in this process", func(t *testing.T) {
		x := require.New(t)

		p, err := w.s.Ent.Party.Query().Where(party.HolderId(park.Id.Uuid())).Only(ctx)
		x.NoError(err)
		_, err = w.s.Base.Party().Patch(ctx, app.PartyPatchRequest_builder{
			Ref:              app.PartyRef_builder{Id: pdid.Id(p.Id).Bytes()}.Build(),
			Email:            z.Ptr("park@example.com"),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		x.NoError(err)

		x.Equal(http.StatusNoContent, post(`{"login":"Park@Example.com","password":"correct horse"}`).Code)
		x.Equal(http.StatusUnauthorized, post(`{"login":"park@example.com","password":"wrong one"}`).Code)
		x.Equal(http.StatusUnauthorized, post(`{"login":"nobody@example.com","password":"correct horse"}`).Code)
	})

	t.Run("roster signing them out everywhere ends the session open here", func(t *testing.T) {
		x := require.New(t)

		x.NoError(w.s.Identity.Invalidate(ctx, park.Id))
		_, err := app.NewPartyServiceClient(w.conn).Me(md, &app.PartyMeRequest{})
		x.Equal(codes.Unauthenticated, status.Code(err))

		res := post(`{"login":"park","password":"correct horse"}`)
		x.Equal(http.StatusNoContent, res.Code, "and a sign-in after it is a session that is good")
	})

	t.Run("a login ended here stays ended, whatever roster says", func(t *testing.T) {
		x := require.New(t)

		_, err := w.s.Base.Holder().Erase(ctx, app.HolderRef_builder{Id: park.Id.Bytes()}.Build())
		x.NoError(err)
		x.Equal(http.StatusUnauthorized, post(`{"login":"park","password":"correct horse"}`).Code)
	})

	t.Run("somebody here from before roster is not somebody new", func(t *testing.T) {
		x := require.New(t)

		// A login this deployment made before roster held its people, and a
		// person roster has of the same name with an identifier of its own.
		_, err := w.s.Base.Holder().Add(ctx, app.HolderAddRequest_builder{
			Tenant: app.TenantRef_builder{Id: tid.Bytes()}.Build(),
			Alias:  "choi",
			Name:   "최",
			Role:   z.Ptr("member"),
		}.Build())
		x.NoError(err)
		choi, err := w.s.Identity.AddPerson(ctx, tid, "choi", "최", pdid.Nil)
		x.NoError(err)
		x.NoError(w.s.Identity.SetPassword(ctx, choi.Id, "correct horse"))

		x.Equal(http.StatusConflict, post(`{"login":"choi","password":"correct horse"}`).Code)
		_, err = w.s.Ent.Holder.Get(ctx, choi.Id.Uuid())
		x.True(ent.IsNotFound(err), "a second choi was made beside the first")
	})

	t.Run("and an address that keeps getting it wrong is refused for a while", func(t *testing.T) {
		x := require.New(t)

		lee, err := w.s.Identity.AddPerson(ctx, tid, "lee", "이", pdid.Nil)
		x.NoError(err)
		x.NoError(w.s.Identity.SetPassword(ctx, lee.Id, "correct horse"))
		for range 10 {
			post(`{"login":"lee","password":"a guess at it"}`)
		}
		x.Equal(http.StatusUnauthorized, post(`{"login":"lee","password":"correct horse"}`).Code)
	})
}

// TestARosterThatCannotBeAskedServesNoSession: not knowing what roster would
// say of somebody is not their session being over -- the cookie stays -- and
// it is not their session being good either.
func TestARosterThatCannotBeAskedServesNoSession(t *testing.T) {
	w := newWorld(t)
	x := w.x

	me := app.NewPartyServiceClient(w.conn)
	v, err := me.Me(w.owner, &app.PartyMeRequest{})
	x.NoError(err)
	owner, _ := pdid.From(v.GetHolder().GetId())

	x.NoError(w.s.Identity.Close())
	w.s.Identity.Recheck(owner)

	_, err = me.Me(w.owner, &app.PartyMeRequest{})
	x.Equal(codes.Unavailable, status.Code(err))

	login := session.SignIn(w.s.Sessions, w.s.Identity, w.s.Anchor(w.c))
	r := httptest.NewRequest(http.MethodPost, "/session", bytes.NewBufferString(`{"login":"owner","password":"anything at all"}`))
	rec := httptest.NewRecorder()
	login.ServeHTTP(rec, r)
	x.Equal(http.StatusServiceUnavailable, rec.Code, "a password nobody could check is not a wrong one")
}

// TestAContractIsTheOperators.
//
// What a tenant's history is kept for is its contract, and a tenant that could
// write its own would be extending its own plan. So everybody in the tenant
// reads it, nobody writes it, and the holds on it are for the people who run
// the tenant -- and the wall keeps one tenant's out of another's sight.
func TestAContractIsTheOperators(t *testing.T) {
	w := newWorld(t)
	x := w.x
	code := func(err error) codes.Code { return status.Code(err) }

	member := w.signIn(w.holder("kim", "member"))
	contracts := app.NewTenantContractServiceClient(w.conn)
	holds := app.NewLegalHoldServiceClient(w.conn)

	ctx := context.Background()
	other, err := w.s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "globex"}.Build())
	x.NoError(err)
	for _, tn := range []*app.Tenant{w.tenant, other} {
		_, err := w.s.Base.TenantContract().Add(ctx, app.TenantContractAddRequest_builder{
			Tenant:   app.TenantRef_builder{Id: tn.GetId()}.Build(),
			Name:     tn.GetAlias(),
			KeepDays: 730,
		}.Build())
		x.NoError(err)
	}

	vs, err := contracts.List(member, app.TenantContractListRequest_builder{}.Build())
	x.NoError(err, "a member cannot read how long their history is kept")
	x.Len(vs.GetItems(), 1, "a tenant read another's contract")
	x.Equal("acme", vs.GetItems()[0].GetName())

	_, err = contracts.Add(w.owner, app.TenantContractAddRequest_builder{KeepDays: 0}.Build())
	x.Equal(codes.PermissionDenied, code(err), "an owner wrote their own contract")

	_, err = holds.List(member, app.LegalHoldListRequest_builder{}.Build())
	x.Equal(codes.PermissionDenied, code(err), "a hold is for the people who run the tenant")
	_, err = holds.List(w.owner, app.LegalHoldListRequest_builder{}.Build())
	x.NoError(err)
	_, err = holds.Add(w.owner, app.LegalHoldAddRequest_builder{Name: "mine"}.Build())
	x.Equal(codes.PermissionDenied, code(err), "an owner placed a hold")
}
