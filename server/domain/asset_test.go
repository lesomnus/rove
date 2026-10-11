package domain_test

import (
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
)

const day = 24 * time.Hour

// where answers what an asset was in at a moment, as known at another (or
// now), read through QueryAt over `root`.
func (e *env) where(root, a *app.Asset, at time.Time, known *time.Time) []byte {
	req := app.AssetQueryAtRequest_builder{Root: assetRef(root), At: timestamppb.New(at)}.Build()
	if known != nil {
		req.SetKnown(timestamppb.New(*known))
	}
	v, err := e.app().Asset().QueryAt(e.owner, req)
	e.x.NoError(err)
	for _, s := range v.GetItems() {
		if sameId(s.GetId(), a.GetId()) && s.GetExisted() {
			return s.GetParentId()
		}
	}
	return nil
}

func TestTimeTravel(t *testing.T) {
	e := newEnv(t)
	x := e.x

	hq := e.space("본사", nil)
	a := e.space("A", hq)
	b := e.space("B", hq)
	laptop := e.item("노트북", "NB-1", a, 30*day)

	// Found out today that it went to B ten days ago.
	_, err := e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{
		Ref:    assetRef(laptop),
		To:     assetRef(b),
		At:     e.ago(10 * day),
		Reason: "늦게 알게 됨",
	}.Build())
	x.NoError(err)

	x.Equal(b.GetId(), e.get(laptop).GetParentId(), "the row says where it is now")
	x.Equal(a.GetId(), e.where(hq, laptop, e.now.Add(-20*day), nil), "it was in A twenty days ago")
	x.Equal(b.GetId(), e.where(hq, laptop, e.now.Add(-5*day), nil), "and in B five days ago")
	x.Nil(e.where(hq, laptop, e.now.Add(-40*day), nil), "and nowhere before it was registered")

	tl, err := e.app().Asset().Timeline(e.owner, app.AssetTimelineRequest_builder{Ref: assetRef(laptop)}.Build())
	x.NoError(err)
	var toB *app.TimelineEntry
	for _, en := range tl.GetEntries() {
		if en.GetKind() == "placement" && sameId(en.GetOtherId(), b.GetId()) {
			toB = en
		}
	}
	x.NotNil(toB)
	x.WithinDuration(e.now.Add(-10*day), toB.GetValidFrom().AsTime(), time.Second)

	// It turns out the move was fifteen days ago, not ten.
	before := e.now
	e.now = e.now.Add(time.Minute)
	_, err = e.app().Asset().Correct(e.owner, app.AssetCorrectRequest_builder{
		Ref:       assetRef(laptop),
		RowId:     toB.GetRowId(),
		ValidFrom: e.ago(15 * day),
		Reason:    "영수증 확인",
	}.Build())
	x.NoError(err)
	x.Equal(b.GetId(), e.where(hq, laptop, e.now.Add(-12*day), nil), "corrected: in B twelve days ago")
	x.Equal(a.GetId(), e.where(hq, laptop, e.now.Add(-12*day), &before), "as known before the correction: still in A")

	// And now that it never moved at all.
	tl, err = e.app().Asset().Timeline(e.owner, app.AssetTimelineRequest_builder{Ref: assetRef(laptop)}.Build())
	x.NoError(err)
	toB = nil
	for _, en := range tl.GetEntries() {
		if en.GetKind() == "placement" && sameId(en.GetOtherId(), b.GetId()) && !en.HasSupersededAt() {
			toB = en
		}
	}
	x.NotNil(toB)
	_, err = e.app().Asset().Correct(e.owner, app.AssetCorrectRequest_builder{
		Ref:     assetRef(laptop),
		RowId:   toB.GetRowId(),
		Retract: true,
		Reason:  "잘못 기록",
	}.Build())
	x.NoError(err)
	x.Equal(a.GetId(), e.get(laptop).GetParentId(), "retracted: back in A, as if the move never happened")
	x.Equal(a.GetId(), e.where(hq, laptop, e.now.Add(-5*day), nil))

	// The history keeps what was corrected, for whoever asks to see it.
	tl, err = e.app().Asset().Timeline(e.owner, app.AssetTimelineRequest_builder{Ref: assetRef(laptop), Superseded: true}.Build())
	x.NoError(err)
	gone := 0
	for _, en := range tl.GetEntries() {
		if en.HasSupersededAt() {
			gone++
		}
	}
	x.GreaterOrEqual(gone, 2)
}

func TestNoCycles(t *testing.T) {
	e := newEnv(t)
	hq := e.space("본사", nil)
	floor := e.space("3층", hq)
	room := e.space("회의실", floor)

	_, err := e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{Ref: assetRef(hq), To: assetRef(room)}.Build())
	e.x.Error(err, "a building cannot go inside its own room")

	// Nor at a moment in the past, which a later fact would make a loop of.
	_, err = e.app().Asset().Move(e.owner, app.AssetMoveRequest_builder{Ref: assetRef(floor), To: assetRef(room), At: e.ago(day)}.Build())
	e.x.Error(err)
}

func TestAttributesAndHistory(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("창고", nil)
	a := e.item("모니터", "MN-1", room, 10*day)

	_, err := e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{
		Ref: assetRef(a),
		Set: map[string]string{"status": "in_repair", "condition": "broken", "attr.size": "27"},
		At:  e.ago(2 * day),
	}.Build())
	x.NoError(err)
	v := e.get(a)
	x.Equal("in_repair", v.GetStatus())
	x.Equal("broken", v.GetCondition())
	x.Equal("27", v.GetAttributes()["size"])

	_, err = e.app().Asset().SetAttributes(e.owner, app.AssetSetAttributesRequest_builder{
		Ref: assetRef(a),
		Set: map[string]string{"status": "nonsense"},
	}.Build())
	x.Equal(codes.InvalidArgument, codeOf(err))

	q, err := e.app().Asset().QueryAt(e.owner, app.AssetQueryAtRequest_builder{Root: assetRef(room), At: e.ago(5 * day)}.Build())
	x.NoError(err)
	for _, s := range q.GetItems() {
		if sameId(s.GetId(), a.GetId()) {
			x.Equal("active", s.GetFacts()["status"], "five days ago it was still working")
		}
	}

	// Erase keeps the history and refuses a space with something in it.
	_, err = e.app().Asset().Erase(e.owner, assetRef(room))
	x.Error(err)
	_, err = e.app().Asset().Erase(e.owner, assetRef(a))
	x.NoError(err)
	_, err = e.app().Asset().Get(e.owner, app.AssetGetRequest_builder{Ref: assetRef(a)}.Build())
	x.Equal(codes.NotFound, codeOf(err))
	// The tag of what was voided is free again: the unique index covers the
	// rows that are still here.
	e.item("모니터", "MN-1", room, 0)
}

func TestTheWall(t *testing.T) {
	e := newEnv(t)
	x := e.x
	room := e.space("회의실", nil)
	mine := e.item("노트북", "NB-1", room, day)

	_, other := e.newTenant("other")
	theirs, err := e.app().Asset().Add(other, app.AssetAddRequest_builder{Name: "남의 것", Kind: "item"}.Build())
	x.NoError(err)

	_, err = e.app().Asset().Get(other, app.AssetGetRequest_builder{Ref: assetRef(mine)}.Build())
	x.Equal(codes.NotFound, codeOf(err), "another tenant's asset is not there")

	found, err := e.app().Asset().Search(other, app.AssetSearchRequest_builder{Q: "노트북"}.Build())
	x.NoError(err)
	x.Zero(found.GetTotal())

	_, err = e.app().Asset().Move(other, app.AssetMoveRequest_builder{Ref: assetRef(theirs), To: assetRef(room)}.Build())
	x.Error(err, "nor can anything be put into it")

	_, err = e.app().Asset().Timeline(other, app.AssetTimelineRequest_builder{Ref: assetRef(mine)}.Build())
	x.Equal(codes.NotFound, codeOf(err))

	_, err = e.app().Label().Resolve(other, app.LabelResolveRequest_builder{Code: "NB-1"}.Build())
	x.Equal(codes.NotFound, codeOf(err), "a tag is read in the caller's tenant only")
}
