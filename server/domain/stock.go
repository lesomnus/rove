package domain

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/stock"
)

type domainStock struct {
	Domain
	app.StockServiceServer
}

func (s Domain) Stock() app.StockServiceServer { return domainStock{s, s.Next().Stock()} }

// Add starts keeping a model in a space, with what is there already.
func (s domainStock) Add(ctx context.Context, req *app.StockAddRequest) (*app.Stock, error) {
	var out *app.Stock
	err := s.tx(ctx, func(t *Tx) error {
		m, err := t.next.ItemModel().Get(t.ctx, app.ItemModelGetRequest_builder{Ref: req.GetModel()}.Build())
		if err != nil {
			return err
		}
		sp, spid, err := t.get(req.GetSpace(), "space")
		if err != nil {
			return err
		}
		if sp.GetKind() != "space" {
			return invalid("space", "stock is kept in a space")
		}
		dup, err := t.db.Stock.Query().Where(
			stock.TenantId(t.tenant.Uuid()),
			stock.ModelId(uuidOf(m.GetId())),
			stock.SpaceId(spid.Uuid()),
			stock.DateErasedIsNil(),
		).Exist(t.ctx)
		if err != nil {
			return err
		}
		if dup {
			return failed("%s is already kept in %s; change that stock instead", m.GetName(), sp.GetName())
		}
		if req.GetQuantity() < 0 || req.GetThreshold() < 0 {
			return invalid("quantity", "is not negative")
		}

		row := proto.Clone(req).(*app.StockAddRequest)
		row.SetTenant(t.tenantRef())
		row.SetName(or(strings.TrimSpace(row.GetName()), m.GetName()))
		row.SetQuantity(0)
		if row.GetUnit() == "" {
			row.SetUnit("개")
		}
		if err := t.begin(req.GetOp(), "stock.add", pdid.Nil, t.now, fmt.Sprintf("재고 시작: %s @ %s", row.GetName(), sp.GetName()), "", nil); err != nil {
			return err
		}
		v, err := t.next.Stock().Add(t.ctx, row)
		if err != nil {
			return err
		}
		if q := req.GetQuantity(); q > 0 {
			if err := t.moveStock(v, q, "receive", pdid.Nil, t.now); err != nil {
				return err
			}
		}
		out, err = t.next.Stock().Get(t.ctx, app.StockGetRequest_builder{Ref: app.StockRef_builder{Id: v.GetId()}.Build()}.Build())
		return err
	})
	return out, err
}

// moveStock changes a stock by `delta` and writes the movement, refusing to go
// below zero and telling the managers when it falls to its threshold.
func (t *Tx) moveStock(st *app.Stock, delta int64, reason string, ref pdid.Id, at time.Time) error {
	cur, err := t.db.Stock.Query().Where(stock.Id(uuidOf(st.GetId())), stock.TenantId(t.tenant.Uuid())).Only(t.ctx)
	if err != nil {
		return err
	}
	next := cur.Quantity + delta
	if next < 0 {
		return failed("%s has %d; %d cannot be taken", cur.Name, cur.Quantity, -delta)
	}
	if _, err := t.next.Stock().Patch(t.ctx, app.StockPatchRequest_builder{
		Ref:              app.StockRef_builder{Id: st.GetId()}.Build(),
		Quantity:         z.Ptr(next),
		DateUpdatedForce: z.Ptr(true),
	}.Build()); err != nil {
		return err
	}
	if t.ev.IsZero() {
		if err := t.begin(nil, "stock."+reason, pdid.Nil, at, fmt.Sprintf("재고 %s: %s %+d", reason, cur.Name, delta), "", nil); err != nil {
			return err
		}
	}
	req := app.StockMovementAddRequest_builder{
		Tenant:     t.tenantRef(),
		Stock:      app.StockRef_builder{Id: st.GetId()}.Build(),
		Delta:      delta,
		Reason:     reason,
		OccurredAt: ts(at),
		EventId:    t.ev.Bytes(),
		Balance:    next,
	}.Build()
	if !ref.IsZero() {
		req.SetRefId(ref.Bytes())
	}
	if _, err := t.next.StockMovement().Add(t.ctx, req); err != nil {
		return err
	}
	if delta < 0 && cur.Threshold > 0 && next <= cur.Threshold && cur.Quantity > cur.Threshold {
		return t.notifyManagers("stock.low", "재고 부족: "+cur.Name, fmt.Sprintf("%d%s 남았습니다 (기준 %d)", next, cur.Unit, cur.Threshold), pdid.Id(cur.Id), "/stock")
	}
	return nil
}

func (s domainStock) change(ctx context.Context, req *app.StockChangeRequest, reason string, sign int64) (*app.Stock, error) {
	var out *app.Stock
	err := s.tx(ctx, func(t *Tx) error {
		at, err := t.at(req.GetAt(), "at")
		if err != nil {
			return err
		}
		st, err := t.next.Stock().Get(t.ctx, app.StockGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		q := req.GetQuantity()
		if sign != 0 && q <= 0 {
			return invalid("quantity", "is at least 1")
		}
		if sign == 0 && q == 0 {
			return invalid("quantity", "an adjustment changes something")
		}
		if sign != 0 {
			q *= sign
		}
		if err := t.begin(req.GetOp(), "stock."+reason, pdid.Nil, at, fmt.Sprintf("재고 %s: %s %+d", reason, st.GetName(), q), req.GetReason(), nil); err != nil {
			return err
		}
		if err := t.moveStock(st, q, reason, pdid.Nil, at); err != nil {
			return err
		}
		out, err = t.next.Stock().Get(t.ctx, app.StockGetRequest_builder{Ref: req.GetRef()}.Build())
		return err
	})
	if err == errDone {
		return s.StockServiceServer.Get(ctx, app.StockGetRequest_builder{Ref: req.GetRef()}.Build())
	}
	return out, err
}

func (s domainStock) Receive(ctx context.Context, req *app.StockChangeRequest) (*app.Stock, error) {
	return s.change(ctx, req, "receive", 1)
}

func (s domainStock) Consume(ctx context.Context, req *app.StockChangeRequest) (*app.Stock, error) {
	return s.change(ctx, req, "consume", -1)
}

// Adjust corrects a stock to what a count found, by a signed quantity.
func (s domainStock) Adjust(ctx context.Context, req *app.StockChangeRequest) (*app.Stock, error) {
	return s.change(ctx, req, "adjust", 0)
}

// Transfer moves a quantity to the same model's stock in another space,
// starting that stock when there is none.
func (s domainStock) Transfer(ctx context.Context, req *app.StockTransferRequest) (*app.Stock, error) {
	var out *app.Stock
	err := s.tx(ctx, func(t *Tx) error {
		at, err := t.at(req.GetAt(), "at")
		if err != nil {
			return err
		}
		q := req.GetQuantity()
		if q <= 0 {
			return invalid("quantity", "is at least 1")
		}
		st, err := t.next.Stock().Get(t.ctx, app.StockGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		sp, spid, err := t.get(req.GetTo(), "to")
		if err != nil {
			return err
		}
		if sp.GetKind() != "space" {
			return invalid("to", "stock is kept in a space")
		}
		if err := t.begin(req.GetOp(), "stock.transfer", pdid.Nil, at, fmt.Sprintf("재고 이동: %s %d → %s", st.GetName(), q, sp.GetName()), req.GetReason(), nil); err != nil {
			return err
		}
		if err := t.moveStock(st, -q, "transfer_out", pdid.Nil, at); err != nil {
			return err
		}

		dst, err := t.db.Stock.Query().Where(
			stock.TenantId(t.tenant.Uuid()),
			stock.ModelId(uuidOf(st.GetModel().GetId())),
			stock.SpaceId(spid.Uuid()),
			stock.DateErasedIsNil(),
		).Only(t.ctx)
		var target *app.Stock
		switch {
		case err == nil:
			target, err = t.next.Stock().Get(t.ctx, app.StockGetRequest_builder{Ref: app.StockRef_builder{Id: pdid.Id(dst.Id).Bytes()}.Build()}.Build())
			if err != nil {
				return err
			}
		case ent.IsNotFound(err):
			target, err = t.next.Stock().Add(t.ctx, app.StockAddRequest_builder{
				Tenant:    t.tenantRef(),
				Name:      st.GetName(),
				Model:     app.ItemModelRef_builder{Id: st.GetModel().GetId()}.Build(),
				Space:     app.AssetRef_builder{Id: spid.Bytes()}.Build(),
				Threshold: st.GetThreshold(),
				Unit:      st.GetUnit(),
			}.Build())
			if err != nil {
				return err
			}
		default:
			return err
		}
		if err := t.moveStock(target, q, "transfer_in", pdid.Nil, at); err != nil {
			return err
		}
		out, err = t.next.Stock().Get(t.ctx, app.StockGetRequest_builder{Ref: req.GetRef()}.Build())
		return err
	})
	return out, err
}

// Convert takes units out of a stock and registers each as an asset of its
// own, in the stock's space: a monitor that turned out to be worth tracking.
func (s domainStock) Convert(ctx context.Context, req *app.StockConvertRequest) (*app.StockConvertResponse, error) {
	var out *app.StockConvertResponse
	err := s.tx(ctx, func(t *Tx) error {
		q := req.GetQuantity()
		if q <= 0 || q > 200 {
			return invalid("quantity", "is 1 to 200")
		}
		st, err := t.next.Stock().Get(t.ctx, app.StockGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		m, err := t.next.ItemModel().Get(t.ctx, app.ItemModelGetRequest_builder{Ref: app.ItemModelRef_builder{Id: st.GetModel().GetId()}.Build()}.Build())
		if err != nil {
			return err
		}
		if err := t.begin(req.GetOp(), "stock.convert", pdid.Nil, t.now, fmt.Sprintf("재고 → 자산: %s %d개", st.GetName(), q), "", nil); err != nil {
			return err
		}
		if err := t.moveStock(st, -q, "convert", pdid.Nil, t.now); err != nil {
			return err
		}

		out = app.StockConvertResponse_builder{}.Build()
		layer := t.layer()
		for i := range q {
			add := app.AssetAddRequest_builder{
				Name:  m.GetName(),
				Kind:  "item",
				Model: app.ItemModelRef_builder{Id: m.GetId()}.Build(),
				To:    app.AssetRef_builder{Id: st.GetSpace().GetId()}.Build(),
			}.Build()
			if ty := m.GetType().GetId(); len(ty) > 0 {
				add.SetType(app.AssetTypeRef_builder{Id: ty}.Build())
			}
			if p := strings.TrimSpace(req.GetTagPrefix()); p != "" {
				add.SetTag(fmt.Sprintf("%s-%03d", p, i+1))
			}
			a, err := layer.Asset().Add(t.ctx, add)
			if err != nil {
				return err
			}
			out.SetAssets(append(out.GetAssets(), a))
		}
		out.SetStock(st)
		return nil
	})
	return out, err
}
