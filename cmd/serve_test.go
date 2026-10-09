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
	"github.com/lesomnus/rove/cmd"
	entmigrate "github.com/lesomnus/rove/internal/ent/migrate"
	"github.com/lesomnus/rove/server/password"
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

func (w *world) holder(alias, role string) *app.Holder {
	h, err := w.s.Base.Holder().Add(context.Background(), app.HolderAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Alias:  alias,
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

	s, err := cmd.Build(ctx, c)
	x.NoError(err)
	t.Cleanup(func() { s.Close() })
	x.NoError(entmigrate.NewSchema(s.Drv).Create(ctx))

	tn, err := s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: "acme"}.Build())
	x.NoError(err)

	g, err := s.Grpc(ctx, c)
	x.NoError(err)
	w := &world{x: x, s: s, c: c, conn: pdtest.Serve(t, g), tenant: tn}
	w.owner = w.signIn(w.holder("owner", "owner"))
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
	_, err = app.NewCredentialServiceClient(w.conn).Get(w.owner, app.CredentialGetRequest_builder{}.Build())
	x.Equal(codes.PermissionDenied, code(err), "nobody reads a password hash")

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

func TestSignIn(t *testing.T) {
	w := newWorld(t)
	x := w.x
	ctx := context.Background()
	h := w.holder("park", "member")
	hash, err := password.Hash("correct horse")
	x.NoError(err)
	_, err = w.s.Base.Credential().Add(ctx, app.CredentialAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
		Secret: []byte(hash),
	}.Build())
	x.NoError(err)
	_, err = w.s.Base.Party().Add(ctx, app.PartyAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: w.tenant.GetId()}.Build(),
		Name:   "박",
		Email:  "Park@Example.com",
		Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
	}.Build())
	x.NoError(err)

	login := w.s.Sessions.Serve(session.Verify(w.s.Ent))
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/session", bytes.NewBufferString(body))
		r.RemoteAddr = "10.0.0.1:1234"
		rec := httptest.NewRecorder()
		login.ServeHTTP(rec, r)
		return rec
	}

	x.Equal(http.StatusNoContent, post(`{"login":"park","password":"correct horse"}`).Code)
	x.Equal(http.StatusNoContent, post(`{"login":"park@example.com","password":"correct horse"}`).Code, "by e-mail, any case")
	x.Equal(http.StatusNoContent, post(`{"tenant":"acme","login":"park","password":"correct horse"}`).Code)
	x.Equal(http.StatusUnauthorized, post(`{"tenant":"other","login":"park","password":"correct horse"}`).Code)
	x.Equal(http.StatusUnauthorized, post(`{"login":"park","password":"wrong"}`).Code)
	x.Equal(http.StatusUnauthorized, post(`{"login":"nobody","password":"correct horse"}`).Code)

	res := post(`{"login":"park","password":"correct horse"}`)
	cookie := res.Result().Cookies()[0]
	md := metadata.AppendToOutgoingContext(ctx, "cookie", cookie.Name+"="+cookie.Value)
	me, err := app.NewPartyServiceClient(w.conn).Me(md, &app.PartyMeRequest{})
	x.NoError(err)
	x.Equal("park", me.GetHolder().GetAlias())

	// Ten wrong guesses from one address and even the right one is refused
	// for a while.
	for range 10 {
		post(`{"login":"park","password":"guess"}`)
	}
	x.Equal(http.StatusUnauthorized, post(`{"login":"park","password":"correct horse"}`).Code)
}
