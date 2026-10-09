package domain

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/allocation"
	"github.com/lesomnus/rove/internal/ent/asset"
	"github.com/lesomnus/rove/internal/ent/bookable"
	"github.com/lesomnus/rove/internal/ent/holder"
	"github.com/lesomnus/rove/internal/ent/link"
	"github.com/lesomnus/rove/internal/ent/party"
	"github.com/lesomnus/rove/internal/ent/reservation"
	"github.com/lesomnus/rove/internal/ent/reservationitem"
	"github.com/lesomnus/rove/server/pd"
)

// The states a reservation is in (design 4). The first four take time.
const (
	resHeld      = "held"
	resRequested = "requested"
	resConfirmed = "confirmed"
	resInUse     = "in_use"
	resCompleted = "completed"
	resCancelled = "cancelled"
	resRejected  = "rejected"
	resExpired   = "expired"
	resNoShow    = "no_show"
)

// HoldFor is how long a hold keeps its time while the rest is decided.
const HoldFor = 10 * time.Minute

func blocks(state string) bool {
	switch state {
	case resHeld, resRequested, resConfirmed, resInUse:
		return true
	}
	return false
}

type domainBookable struct {
	Domain
	app.BookableServiceServer
}

func (s Domain) Bookable() app.BookableServiceServer { return domainBookable{s, s.Next().Bookable()} }

// Add makes an asset reservable, with defaults a person would pick.
func (s domainBookable) Add(ctx context.Context, req *app.BookableAddRequest) (*app.Bookable, error) {
	var out *app.Bookable
	err := s.tx(ctx, func(t *Tx) error {
		row := proto.Clone(req).(*app.BookableAddRequest)
		row.SetTenant(t.tenantRef())
		a, _, err := t.get(req.GetAsset(), "asset")
		if err != nil {
			return err
		}
		if row.GetTimezone() == "" {
			row.SetTimezone("Asia/Seoul")
		}
		if _, err := time.LoadLocation(row.GetTimezone()); err != nil {
			return invalid("timezone", "is an IANA zone such as Asia/Seoul")
		}
		if row.GetUnits() == 0 {
			row.SetUnits(1)
		}
		if a.GetKind() == "group" && row.GetUnits() == 1 {
			// A pool of interchangeable units: as many as are in it.
			n, err := t.db.Link.Query().Where(link.TenantId(t.tenant.Uuid()), link.TargetId(uuidOf(a.GetId())), link.Kind("member_of"), link.SupersededAtIsNil(), link.ValidToIsNil()).Count(t.ctx)
			if err != nil {
				return err
			}
			row.SetUnits(uint32(max(n, 1)))
		}
		row.SetEnabled(true)
		if err := t.begin(nil, "bookable.add", idOf(a.GetId()), t.now, a.GetTag()+" 예약 가능", "", nil); err != nil {
			return err
		}
		out, err = t.next.Bookable().Add(t.ctx, row)
		return err
	})
	return out, err
}

func (s domainBookable) Update(ctx context.Context, req *app.BookableUpdateRequest) (*app.Bookable, error) {
	var out *app.Bookable
	err := s.tx(ctx, func(t *Tx) error {
		v, err := t.next.Bookable().Get(t.ctx, app.BookableGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		patch := app.BookablePatchRequest_builder{
			Ref:              req.GetRef(),
			BufferBefore:     z.Ptr(req.GetBufferBefore()),
			BufferAfter:      z.Ptr(req.GetBufferAfter()),
			Approval:         z.Ptr(req.GetApproval()),
			MinMinutes:       z.Ptr(req.GetMinMinutes()),
			MaxMinutes:       z.Ptr(req.GetMaxMinutes()),
			HorizonDays:      z.Ptr(req.GetHorizonDays()),
			ExclusiveGroup:   z.Ptr(req.GetExclusiveGroup()),
			Enabled:          z.Ptr(req.GetEnabled()),
			ScheduleVersion:  z.Ptr(v.GetScheduleVersion() + 1),
			DateUpdatedForce: z.Ptr(true),
		}.Build()
		if tz := req.GetTimezone(); tz != "" {
			if _, err := time.LoadLocation(tz); err != nil {
				return invalid("timezone", "is an IANA zone such as Asia/Seoul")
			}
			patch.SetTimezone(tz)
		}
		if req.GetHoursNull() {
			patch.SetHoursNull(true)
		} else if req.HasHours() {
			for i, r := range req.GetHours().GetRanges() {
				if r.GetWeekday() < 0 || r.GetWeekday() > 6 || r.GetFromMinute() < 0 || r.GetToMinute() > 24*60 || r.GetFromMinute() >= r.GetToMinute() {
					return invalid(fmt.Sprintf("hours.ranges[%d]", i), "is a weekday 0-6 and minutes from < to within a day")
				}
			}
			patch.SetHours(req.GetHours())
		}
		if u := req.GetUnits(); u > 0 {
			patch.SetUnits(u)
		}
		if err := t.begin(nil, "bookable.update", idOf(v.GetAsset().GetId()), t.now, "예약 정책 변경", "", nil); err != nil {
			return err
		}
		out, err = t.next.Bookable().Patch(t.ctx, patch)
		return err
	})
	return out, err
}

// resource is a bookable asset as a reservation sees it.
type resource struct {
	asset *ent.Asset
	book  *ent.Bookable
}

func (t *Tx) resourceOf(id uuid.UUID) (*resource, error) {
	a, err := t.db.Asset.Query().Where(asset.Id(id), asset.TenantId(t.tenant.Uuid()), asset.DateErasedIsNil()).Only(t.ctx)
	if err != nil {
		return nil, status.Error(codes.NotFound, "there is no such resource")
	}
	b, err := t.db.Bookable.Query().Where(bookable.TenantId(t.tenant.Uuid()), bookable.AssetId(id), bookable.DateErasedIsNil()).Only(t.ctx)
	if err != nil {
		return nil, failed("%s %s is not reservable", a.Tag, a.Name)
	}
	if !b.Enabled {
		return nil, failed("%s %s is not taking reservations", a.Tag, a.Name)
	}
	return &resource{a, b}, nil
}

// lockBookables bumps each resource's schedule version, in identifier order,
// which is both the lock a reservation write takes and the signal a calendar
// watching the resource hears (design 4, 9.6).
func (t *Tx) lockBookables(rs []*resource) error {
	sorted := slices.Clone(rs)
	sort.Slice(sorted, func(i, j int) bool { return pdid.Id(sorted[i].book.Id).String() < pdid.Id(sorted[j].book.Id).String() })
	seen := map[uuid.UUID]bool{}
	for _, r := range sorted {
		if seen[r.book.Id] {
			continue
		}
		seen[r.book.Id] = true
		v, err := t.next.Bookable().Patch(t.ctx, app.BookablePatchRequest_builder{
			Ref:              app.BookableRef_builder{Id: pdid.Id(r.book.Id).Bytes()}.Build(),
			ScheduleVersion:  z.Ptr(r.book.ScheduleVersion + 1),
			DateUpdatedForce: z.Ptr(true),
		}.Build())
		if err != nil {
			return err
		}
		r.book.ScheduleVersion = v.GetScheduleVersion()
	}
	return nil
}

// window is the time an allocation takes for a reservation: the reservation
// and its buffers either side.
func window(b *ent.Bookable, begins, ends time.Time) (time.Time, time.Time) {
	return begins.Add(-time.Duration(b.BufferBefore) * time.Minute), ends.Add(time.Duration(b.BufferAfter) * time.Minute)
}

// conflicts answers the blocking allocations that keep `r` from being taken
// for [from, to) by `units` more: on the resource itself, or on a space of
// the same exclusive group above or below it.
func (t *Tx) conflicts(r *resource, from, to time.Time, units uint32, except []uuid.UUID) ([]*ent.Allocation, error) {
	ids := []uuid.UUID{r.asset.Id}
	if g := r.book.ExclusiveGroup; g != "" {
		group, err := t.groupMates(r)
		if err != nil {
			return nil, err
		}
		ids = append(ids, group...)
	}

	q := t.db.Allocation.Query().Where(
		allocation.TenantId(t.tenant.Uuid()),
		allocation.ResourceIdIn(ids...),
		allocation.Blocking(true),
		allocation.BeginsAtLT(to),
		allocation.EndsAtGT(from),
	)
	if len(except) > 0 {
		q = q.Where(allocation.Or(allocation.RefIdIsNil(), allocation.RefIdNotIn(except...)))
	}
	as, err := q.All(t.ctx)
	if err != nil {
		return nil, err
	}

	if r.book.Units > 1 {
		// A pool: what matters is how many are taken at the busiest moment.
		own := []*ent.Allocation{}
		for _, a := range as {
			if a.ResourceId == r.asset.Id {
				own = append(own, a)
			}
		}
		if peak(own, from, to)+units > r.book.Units {
			return own, nil
		}
		others := []*ent.Allocation{}
		for _, a := range as {
			if a.ResourceId != r.asset.Id {
				others = append(others, a)
			}
		}
		return others, nil
	}
	return as, nil
}

// peak answers the most units taken at any one moment of [from, to).
func peak(as []*ent.Allocation, from, to time.Time) uint32 {
	type edge struct {
		at time.Time
		d  int
	}
	es := []edge{}
	for _, a := range as {
		b, e := a.BeginsAt, a.EndsAt
		if b.Before(from) {
			b = from
		}
		if e.After(to) {
			e = to
		}
		es = append(es, edge{b, int(a.Units)}, edge{e, -int(a.Units)})
	}
	sort.Slice(es, func(i, j int) bool {
		if es[i].at.Equal(es[j].at) {
			return es[i].d < es[j].d
		}
		return es[i].at.Before(es[j].at)
	})
	cur, top := 0, 0
	for _, e := range es {
		cur += e.d
		top = max(top, cur)
	}
	return uint32(top)
}

// groupMates answers the spaces above and below `r` that share its exclusive
// group: booking the hall takes the rooms in it, and a room taken keeps the
// hall from being booked whole.
func (t *Tx) groupMates(r *resource) ([]uuid.UUID, error) {
	g := r.book.ExclusiveGroup
	inGroup := func(ids []uuid.UUID) ([]uuid.UUID, error) {
		if len(ids) == 0 {
			return nil, nil
		}
		bs, err := t.db.Bookable.Query().Where(bookable.TenantId(t.tenant.Uuid()), bookable.AssetIdIn(ids...), bookable.ExclusiveGroup(g)).All(t.ctx)
		if err != nil {
			return nil, err
		}
		out := []uuid.UUID{}
		for _, b := range bs {
			out = append(out, b.AssetId)
		}
		return out, nil
	}

	down, err := t.descendants(r.asset.Id)
	if err != nil {
		return nil, err
	}
	up := []uuid.UUID{}
	cur := r.asset
	for range 32 {
		if cur.ParentId == nil {
			break
		}
		p, err := t.db.Asset.Query().Where(asset.Id(*cur.ParentId), asset.TenantId(t.tenant.Uuid())).Only(t.ctx)
		if err != nil {
			break
		}
		up = append(up, p.Id)
		cur = p
	}
	return inGroup(append(down, up...))
}

// open says whether [begins, ends) is inside a resource's opening hours.
func open(b *ent.Bookable, begins, ends time.Time) bool {
	if b.Hours == nil || len(b.Hours.GetRanges()) == 0 {
		return true
	}
	loc, err := time.LoadLocation(b.Timezone)
	if err != nil {
		loc = time.UTC
	}
	cur := begins.In(loc)
	end := ends.In(loc)
	for cur.Before(end) {
		dayEnd := time.Date(cur.Year(), cur.Month(), cur.Day()+1, 0, 0, 0, 0, loc)
		segEnd := end
		if dayEnd.Before(segEnd) {
			segEnd = dayEnd
		}
		from := cur.Hour()*60 + cur.Minute()
		to := segEnd.Hour()*60 + segEnd.Minute()
		if segEnd.Equal(dayEnd) {
			to = 24 * 60
		}
		ok := false
		for _, r := range b.Hours.GetRanges() {
			if int(r.GetWeekday()) == int(cur.Weekday()) && int(r.GetFromMinute()) <= from && to <= int(r.GetToMinute()) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
		cur = segEnd
	}
	return true
}

type domainReservation struct {
	Domain
	app.ReservationServiceServer
}

func (s Domain) Reservation() app.ReservationServiceServer {
	return domainReservation{s, s.Next().Reservation()}
}

// partyOf answers the party the caller is.
func (t *Tx) partyOf() (*ent.Party, error) {
	p, err := t.db.Party.Query().Where(party.TenantId(t.tenant.Uuid()), party.HolderId(t.actor.Uuid()), party.DateErasedIsNil()).First(t.ctx)
	if err != nil {
		return nil, failed("your login is not a person in this organization yet; ask an admin")
	}
	return p, nil
}

// roleOf answers the caller's role.
func (t *Tx) roleOf() string {
	h, err := t.db.Holder.Query().Where(holder.Id(t.actor.Uuid())).Only(t.ctx)
	if err != nil {
		return ""
	}
	return h.Role
}

func manages(role string) bool { return role == "owner" || role == "admin" || role == "manager" }

// Add reserves resources: a hold, a request waiting for approval, or a
// confirmed reservation, depending on what the resources ask. A series is all
// of its occurrences or none of them.
func (s domainReservation) Add(ctx context.Context, req *app.ReservationAddRequest) (*app.Reservation, error) {
	var out *app.Reservation
	err := s.tx(ctx, func(t *Tx) error {
		if !req.HasBeginsAt() || !req.HasEndsAt() {
			return invalid("begins_at", "a reservation is from a moment to a moment")
		}
		begins, ends := req.GetBeginsAt().AsTime(), req.GetEndsAt().AsTime()
		if !ends.After(begins) {
			return invalid("ends_at", "is after begins_at")
		}
		if ends.Before(t.now) {
			return invalid("ends_at", "is in the past")
		}
		if len(req.GetItems()) == 0 {
			return invalid("items", "say what to reserve")
		}
		override := req.GetOverride()
		if override {
			if !manages(t.roleOf()) {
				return status.Error(codes.PermissionDenied, "only a manager books over a conflict")
			}
			if strings.TrimSpace(req.GetReason()) == "" {
				return invalid("reason", "booking over a conflict says why")
			}
		}

		// Who it is for: the caller, unless a manager says otherwise.
		var who *ent.Party
		var err error
		if req.HasParty() {
			p, err := t.next.Party().Get(t.ctx, app.PartyGetRequest_builder{Ref: req.GetParty()}.Build())
			if err != nil {
				return err
			}
			if me, _ := t.partyOf(); (me == nil || pdid.Id(me.Id) != idOf(p.GetId())) && !manages(t.roleOf()) {
				return status.Error(codes.PermissionDenied, "only a manager reserves for somebody else")
			}
			who, err = t.db.Party.Query().Where(party.Id(uuidOf(p.GetId()))).Only(t.ctx)
			if err != nil {
				return err
			}
		} else if who, err = t.partyOf(); err != nil {
			return err
		}

		// What it takes: each resource, and a kit's required members with it.
		type want struct {
			r     *resource
			units uint32
			item  bool
		}
		wants := []want{}
		for i, it := range req.GetItems() {
			_, id, err := t.get(it.GetResource(), fmt.Sprintf("items[%d].resource", i))
			if err != nil {
				return err
			}
			r, err := t.resourceOf(id.Uuid())
			if err != nil {
				return err
			}
			units := max(it.GetUnits(), 1)
			if units > r.book.Units {
				return invalid(fmt.Sprintf("items[%d].units", i), "%s has %d", r.asset.Name, r.book.Units)
			}
			wants = append(wants, want{r, units, true})
			if r.asset.Kind == "kit" {
				ms, err := t.db.Link.Query().Where(link.TenantId(t.tenant.Uuid()), link.TargetId(r.asset.Id), link.Kind("member_of"), link.Required(true), link.SupersededAtIsNil(), link.ValidToIsNil()).All(t.ctx)
				if err != nil {
					return err
				}
				for _, m := range ms {
					mr, err := t.resourceOf(m.SourceId)
					if err != nil {
						// A member nobody made reservable is still taken with
						// the kit, by an allocation of its own.
						a, aerr := t.db.Asset.Query().Where(asset.Id(m.SourceId)).Only(t.ctx)
						if aerr != nil {
							return err
						}
						mr = &resource{asset: a, book: &ent.Bookable{AssetId: a.Id, Units: 1, Timezone: r.book.Timezone}}
					}
					wants = append(wants, want{mr, 1, false})
				}
			}
		}

		rs := []*resource{}
		for _, w := range wants {
			if w.r.book.Id != (uuid.UUID{}) {
				rs = append(rs, w.r)
			}
		}
		if err := t.lockBookables(rs); err != nil {
			return err
		}

		// The occurrences: one, or as many as the rule says.
		occs, err := occurrences(begins, ends, req.GetRepeat())
		if err != nil {
			return err
		}
		approval := false
		for _, w := range wants {
			approval = approval || w.r.book.Approval
			d := int32(ends.Sub(begins).Minutes())
			if w.item && w.r.book.MinMinutes > 0 && d < w.r.book.MinMinutes {
				return invalid("ends_at", "%s is reserved for at least %d minutes", w.r.asset.Name, w.r.book.MinMinutes)
			}
			if w.item && w.r.book.MaxMinutes > 0 && d > w.r.book.MaxMinutes {
				return invalid("ends_at", "%s is reserved for at most %d minutes", w.r.asset.Name, w.r.book.MaxMinutes)
			}
			if h := w.r.book.HorizonDays; w.item && h > 0 && begins.After(t.now.AddDate(0, 0, int(h))) {
				return invalid("begins_at", "%s is reserved at most %d days ahead", w.r.asset.Name, h)
			}
			for _, o := range occs {
				if w.item && !open(w.r.book, o[0], o[1]) && !override {
					return failed("%s is closed then (%s)", w.r.asset.Name, o[0].Format("2006-01-02 15:04"))
				}
				from, to := window(w.r.book, o[0], o[1])
				cs, err := t.conflicts(w.r, from, to, w.units, nil)
				if err != nil {
					return err
				}
				if len(cs) > 0 && !override {
					return status.Errorf(codes.Aborted, "%s is taken then (%s); pick another time",
						w.r.asset.Name, o[0].Format("2006-01-02 15:04"))
				}
			}
		}

		state := resConfirmed
		var expires *time.Time
		switch {
		case req.GetHold():
			state = resHeld
			e := t.now.Add(HoldFor)
			expires = &e
		case approval && !override:
			state = resRequested
		}

		if err := t.begin(req.GetOp(), "reservation.add", pdid.Id(wants[0].r.asset.Id), t.now,
			fmt.Sprintf("예약: %s %s", wants[0].r.asset.Name, begins.Format("01-02 15:04")), req.GetReason(),
			map[string]string{"state": state, "for": who.Name, "occurrences": strconv.Itoa(len(occs))}); err != nil {
			return err
		}

		var series []byte
		for i, o := range occs {
			row := app.ReservationAddRequest_builder{
				Tenant:      t.tenantRef(),
				Name:        or(req.GetName(), wants[0].r.asset.Name),
				Desc:        req.GetDesc(),
				Party:       app.PartyRef_builder{Id: pdid.Id(who.Id).Bytes()}.Build(),
				RequestedBy: t.actor.Bytes(),
				Status:      state,
				BeginsAt:    ts(o[0]),
				EndsAt:      ts(o[1]),
			}.Build()
			if expires != nil {
				row.SetExpiresAt(ts(*expires))
			}
			if i == 0 {
				row.SetRrule(req.GetRepeat())
			}
			if series != nil {
				row.SetSeriesId(series)
			}
			v, err := t.next.Reservation().Add(t.ctx, row)
			if err != nil {
				return err
			}
			if i == 0 {
				out = v
				if len(occs) > 1 {
					series = v.GetId()
					if _, err := t.next.Reservation().Patch(t.ctx, app.ReservationPatchRequest_builder{
						Ref:              app.ReservationRef_builder{Id: v.GetId()}.Build(),
						SeriesId:         v.GetId(),
						DateUpdatedForce: z.Ptr(true),
					}.Build()); err != nil {
						return err
					}
				}
			}

			for _, w := range wants {
				if w.item {
					if _, err := t.next.ReservationItem().Add(t.ctx, app.ReservationItemAddRequest_builder{
						Tenant:      t.tenantRef(),
						Reservation: app.ReservationRef_builder{Id: v.GetId()}.Build(),
						Resource:    assetRef(w.r.asset.Id),
						Units:       w.units,
					}.Build()); err != nil {
						return err
					}
				}
				from, to := window(w.r.book, o[0], o[1])
				kind := "reservation"
				if override {
					kind = "override"
				}
				if _, err := t.next.Allocation().Add(t.ctx, app.AllocationAddRequest_builder{
					Tenant:    t.tenantRef(),
					Resource:  assetRef(w.r.asset.Id),
					Kind:      kind,
					RefId:     v.GetId(),
					BeginsAt:  ts(from),
					EndsAt:    ts(to),
					Blocking:  !override,
					Exclusive: w.r.book.Units <= 1,
					Units:     w.units,
				}.Build()); err != nil {
					return err
				}
			}
		}

		if state == resRequested {
			if err := t.notifyManagers("reservation.requested", fmt.Sprintf("승인 요청: %s", out.GetName()),
				fmt.Sprintf("%s님이 %s %s 예약을 요청했습니다", who.Name, out.GetName(), begins.Format("01-02 15:04")),
				idOf(out.GetId()), "/reservations"); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// occurrences reads the small part of RFC 5545 a person books with: DAILY or
// WEEKLY, every INTERVAL, COUNT times or UNTIL a date. At most 52.
func occurrences(begins, ends time.Time, rule string) ([][2]time.Time, error) {
	out := [][2]time.Time{{begins, ends}}
	rule = strings.TrimSpace(strings.TrimPrefix(strings.ToUpper(rule), "RRULE:"))
	if rule == "" {
		return out, nil
	}
	freq, interval, count := "", 1, 0
	var until *time.Time
	for _, part := range strings.Split(rule, ";") {
		k, v, _ := strings.Cut(part, "=")
		switch k {
		case "FREQ":
			freq = v
		case "INTERVAL":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return nil, invalid("repeat", "INTERVAL is a positive number")
			}
			interval = n
		case "COUNT":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 {
				return nil, invalid("repeat", "COUNT is a positive number")
			}
			count = n
		case "UNTIL":
			u, err := time.Parse("20060102", v[:min(8, len(v))])
			if err != nil {
				return nil, invalid("repeat", "UNTIL is a date, YYYYMMDD")
			}
			u = u.Add(24 * time.Hour)
			until = &u
		}
	}
	step := map[string]int{"DAILY": 1, "WEEKLY": 7}[freq]
	if step == 0 {
		return nil, invalid("repeat", "FREQ is DAILY or WEEKLY")
	}
	if count == 0 && until == nil {
		return nil, invalid("repeat", "say COUNT or UNTIL")
	}
	for i := 1; len(out) < 52; i++ {
		b := begins.AddDate(0, 0, step*interval*i)
		if count > 0 && len(out) >= count {
			break
		}
		if until != nil && !b.Before(*until) {
			break
		}
		out = append(out, [2]time.Time{b, ends.AddDate(0, 0, step*interval*i)})
	}
	return out, nil
}

// decide moves a reservation from one state to another and frees its time
// when the new state takes none.
func (s domainReservation) decide(ctx context.Context, req *app.ReservationDecideRequest, kind string, from []string, to string, check func(t *Tx, r *app.Reservation) error) (*app.Reservation, error) {
	var out *app.Reservation
	err := s.tx(ctx, func(t *Tx) error {
		r, err := t.next.Reservation().Get(t.ctx, app.ReservationGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		targets := []*app.Reservation{r}
		if req.GetSeries() && len(r.GetSeriesId()) > 0 {
			rs, err := t.db.Reservation.Query().Where(
				reservation.TenantId(t.tenant.Uuid()),
				reservation.SeriesId(uuidOf(r.GetSeriesId())),
				reservation.BeginsAtGTE(r.GetBeginsAt().AsTime()),
			).Ids(t.ctx)
			if err != nil {
				return err
			}
			targets = nil
			for _, k := range rs {
				v, err := t.next.Reservation().Get(t.ctx, app.ReservationGetRequest_builder{Ref: app.ReservationRef_builder{Id: pdid.Id(k).Bytes()}.Build()}.Build())
				if err != nil {
					return err
				}
				targets = append(targets, v)
			}
		}

		for _, r := range targets {
			if !slices.Contains(from, r.GetStatus()) {
				if len(targets) == 1 {
					return failed("this reservation is %s", r.GetStatus())
				}
				continue
			}
			if check != nil {
				if err := check(t, r); err != nil {
					return err
				}
			}
			if t.ev.IsZero() {
				if err := t.begin(nil, kind, idOf(r.GetId()), t.now, fmt.Sprintf("예약 %s: %s", to, r.GetName()), req.GetReason(), nil); err != nil {
					return err
				}
			}
			if err := t.lockAllocations(idOf(r.GetId())); err != nil {
				return err
			}
			patch := app.ReservationPatchRequest_builder{
				Ref:              app.ReservationRef_builder{Id: r.GetId()}.Build(),
				Status:           z.Ptr(to),
				DateUpdatedForce: z.Ptr(true),
			}.Build()
			switch to {
			case resConfirmed, resRejected:
				patch.SetDecidedBy(t.actor.Bytes())
				patch.SetExpiresAtNull(true)
			case resInUse:
				patch.SetCheckedInAt(ts(t.now))
			}
			v, err := t.next.Reservation().Patch(t.ctx, patch)
			if err != nil {
				return err
			}
			if err := t.settleAllocations(idOf(r.GetId()), to); err != nil {
				return err
			}
			if r == targets[0] || out == nil {
				out = v
			}
			if to == resConfirmed || to == resRejected {
				if err := t.notifyParty(uuidOf(r.GetParty().GetId()), "reservation.decided",
					fmt.Sprintf("예약 %s: %s", map[string]string{resConfirmed: "승인", resRejected: "거절"}[to], r.GetName()),
					req.GetReason(), idOf(r.GetId()), "/reservations"); err != nil {
					return err
				}
			}
		}
		if out == nil {
			return failed("nothing in the series was %s", strings.Join(from, " or "))
		}
		return nil
	})
	return out, err
}

// lockAllocations takes the locks of the resources a reservation holds.
func (t *Tx) lockAllocations(rid pdid.Id) error {
	as, err := t.db.Allocation.Query().Where(allocation.TenantId(t.tenant.Uuid()), allocation.RefId(rid.Uuid())).All(t.ctx)
	if err != nil {
		return err
	}
	rs := []*resource{}
	for _, a := range as {
		b, err := t.db.Bookable.Query().Where(bookable.TenantId(t.tenant.Uuid()), bookable.AssetId(a.ResourceId)).Only(t.ctx)
		if err != nil {
			continue
		}
		rs = append(rs, &resource{book: b})
	}
	return t.lockBookables(rs)
}

// settleAllocations makes a reservation's allocations agree with its state:
// a state that takes no time frees it, and finishing early frees the rest.
func (t *Tx) settleAllocations(rid pdid.Id, state string) error {
	as, err := t.db.Allocation.Query().Where(allocation.TenantId(t.tenant.Uuid()), allocation.RefId(rid.Uuid())).All(t.ctx)
	if err != nil {
		return err
	}
	for _, a := range as {
		patch := app.AllocationPatchRequest_builder{
			Ref:              app.AllocationRef_builder{Id: pdid.Id(a.Id).Bytes()}.Build(),
			DateUpdatedForce: z.Ptr(true),
		}.Build()
		changed := false
		if !blocks(state) && a.Blocking {
			patch.SetBlocking(false)
			changed = true
		}
		if state == resCompleted && a.EndsAt.After(t.now) && a.BeginsAt.Before(t.now) {
			patch.SetEndsAt(ts(t.now))
			changed = true
		}
		if changed {
			if _, err := t.next.Allocation().Patch(t.ctx, patch); err != nil {
				return err
			}
		}
	}
	return nil
}

// Confirm turns a hold into what it was held for, before it expires.
func (s domainReservation) Confirm(ctx context.Context, req *app.ReservationDecideRequest) (*app.Reservation, error) {
	var to string
	return s.decideTo(ctx, req, "reservation.confirm", []string{resHeld}, &to, func(t *Tx, r *app.Reservation) error {
		if r.HasExpiresAt() && r.GetExpiresAt().AsTime().Before(t.now) {
			return failed("the hold expired; reserve again")
		}
		to = resConfirmed
		needs, err := t.needsApproval(idOf(r.GetId()))
		if err != nil {
			return err
		}
		if needs {
			to = resRequested
		}
		return nil
	})
}

func (s domainReservation) decideTo(ctx context.Context, req *app.ReservationDecideRequest, kind string, from []string, to *string, check func(t *Tx, r *app.Reservation) error) (*app.Reservation, error) {
	var out *app.Reservation
	err := s.tx(ctx, func(t *Tx) error {
		r, err := t.next.Reservation().Get(t.ctx, app.ReservationGetRequest_builder{Ref: req.GetRef()}.Build())
		if err != nil {
			return err
		}
		if !slices.Contains(from, r.GetStatus()) {
			return failed("this reservation is %s", r.GetStatus())
		}
		if err := check(t, r); err != nil {
			return err
		}
		if err := t.begin(nil, kind, idOf(r.GetId()), t.now, "예약 확정: "+r.GetName(), req.GetReason(), nil); err != nil {
			return err
		}
		patch := app.ReservationPatchRequest_builder{
			Ref:              req.GetRef(),
			Status:           z.Ptr(*to),
			ExpiresAtNull:    z.Ptr(true),
			DateUpdatedForce: z.Ptr(true),
		}.Build()
		out, err = t.next.Reservation().Patch(t.ctx, patch)
		if err != nil {
			return err
		}
		if *to == resRequested {
			return t.notifyManagers("reservation.requested", "승인 요청: "+r.GetName(), "확정된 홀드가 승인을 기다립니다", idOf(r.GetId()), "/reservations")
		}
		return nil
	})
	return out, err
}

func (t *Tx) needsApproval(rid pdid.Id) (bool, error) {
	items, err := t.db.ReservationItem.Query().Where(reservationitem.TenantId(t.tenant.Uuid()), reservationitem.ReservationId(rid.Uuid())).All(t.ctx)
	if err != nil {
		return false, err
	}
	for _, it := range items {
		b, err := t.db.Bookable.Query().Where(bookable.TenantId(t.tenant.Uuid()), bookable.AssetId(it.ResourceId)).Only(t.ctx)
		if err == nil && b.Approval {
			return true, nil
		}
	}
	return false, nil
}

func (s domainReservation) Approve(ctx context.Context, req *app.ReservationDecideRequest) (*app.Reservation, error) {
	return s.decide(ctx, req, "reservation.approve", []string{resRequested}, resConfirmed, nil)
}

func (s domainReservation) Reject(ctx context.Context, req *app.ReservationDecideRequest) (*app.Reservation, error) {
	return s.decide(ctx, req, "reservation.reject", []string{resRequested, resHeld}, resRejected, nil)
}

func (s domainReservation) Cancel(ctx context.Context, req *app.ReservationDecideRequest) (*app.Reservation, error) {
	return s.decide(ctx, req, "reservation.cancel", []string{resHeld, resRequested, resConfirmed}, resCancelled, func(t *Tx, r *app.Reservation) error {
		return t.mineOrManager(r)
	})
}

func (s domainReservation) CheckIn(ctx context.Context, req *app.ReservationDecideRequest) (*app.Reservation, error) {
	return s.decide(ctx, req, "reservation.checkin", []string{resConfirmed}, resInUse, func(t *Tx, r *app.Reservation) error {
		if err := t.mineOrManager(r); err != nil {
			return err
		}
		if t.now.Before(r.GetBeginsAt().AsTime().Add(-30 * time.Minute)) {
			return failed("check-in opens 30 minutes before it begins")
		}
		return nil
	})
}

func (s domainReservation) Complete(ctx context.Context, req *app.ReservationDecideRequest) (*app.Reservation, error) {
	return s.decide(ctx, req, "reservation.complete", []string{resConfirmed, resInUse}, resCompleted, func(t *Tx, r *app.Reservation) error {
		return t.mineOrManager(r)
	})
}

// mineOrManager lets a person act on their own reservation, and a manager on
// anybody's.
func (t *Tx) mineOrManager(r *app.Reservation) error {
	if manages(t.roleOf()) {
		return nil
	}
	me, err := t.partyOf()
	if err != nil {
		return err
	}
	if pdid.Id(me.Id) != idOf(r.GetParty().GetId()) && idOf(r.GetRequestedBy()) != t.actor {
		return status.Error(codes.PermissionDenied, "this is somebody else's reservation")
	}
	return nil
}

// Calendar answers the reservations touching a span.
func (s domainReservation) Calendar(ctx context.Context, req *app.ReservationCalendarRequest) (*app.ReservationCalendarResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	from, to := t.now.AddDate(0, 0, -7), t.now.AddDate(0, 0, 30)
	if req.HasFrom() {
		from = req.GetFrom().AsTime()
	}
	if req.HasTo() {
		to = req.GetTo().AsTime()
	}
	q := t.db.Reservation.Query().Where(
		reservation.TenantId(t.tenant.Uuid()),
		reservation.BeginsAtLT(to),
		reservation.EndsAtGT(from),
		reservation.DateErasedIsNil(),
	)
	if len(req.GetResources()) > 0 {
		ids := []uuid.UUID{}
		for i, r := range req.GetResources() {
			_, id, err := t.get(r, fmt.Sprintf("resources[%d]", i))
			if err != nil {
				return nil, err
			}
			ids = append(ids, id.Uuid())
		}
		rids, err := t.db.ReservationItem.Query().Where(reservationitem.TenantId(t.tenant.Uuid()), reservationitem.ResourceIdIn(ids...)).All(ctx)
		if err != nil {
			return nil, err
		}
		in := []uuid.UUID{}
		for _, v := range rids {
			in = append(in, v.ReservationId)
		}
		q = q.Where(reservation.IdIn(in...))
	}
	if req.GetMine() {
		me, err := t.partyOf()
		if err != nil {
			return nil, err
		}
		q = q.Where(reservation.Or(reservation.PartyId(me.Id), reservation.RequestedBy(t.actor.Uuid())))
	}
	rs, err := q.Order(reservation.ByBeginsAt()).Limit(1000).All(ctx)
	if err != nil {
		return nil, err
	}

	names := namer{t: t}
	entries := []*app.CalendarEntry{}
	for _, r := range rs {
		v, err := t.next.Reservation().Get(ctx, app.ReservationGetRequest_builder{Ref: app.ReservationRef_builder{Id: pdid.Id(r.Id).Bytes()}.Build()}.Build())
		if err != nil {
			continue
		}
		items, err := t.db.ReservationItem.Query().Where(reservationitem.TenantId(t.tenant.Uuid()), reservationitem.ReservationId(r.Id)).All(ctx)
		if err != nil {
			return nil, err
		}
		ids := [][]byte{}
		for _, it := range items {
			ids = append(ids, pdid.Id(it.ResourceId).Bytes())
		}
		entries = append(entries, app.CalendarEntry_builder{Reservation: v, ResourceIds: ids, PartyName: names.party(r.PartyId)}.Build())
	}
	return app.ReservationCalendarResponse_builder{Entries: entries}.Build(), nil
}

// Availability answers when resources are taken, and when they are closed.
func (s domainBookable) Availability(ctx context.Context, req *app.BookableAvailabilityRequest) (*app.BookableAvailabilityResponse, error) {
	t, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	from, to := t.now, t.now.AddDate(0, 0, 7)
	if req.HasFrom() {
		from = req.GetFrom().AsTime()
	}
	if req.HasTo() {
		to = req.GetTo().AsTime()
	}
	if to.Sub(from) > 92*24*time.Hour {
		return nil, invalid("to", "a span of at most three months")
	}

	busy := []*app.BusySpan{}
	for i, ref := range req.GetResources() {
		_, id, err := t.get(ref, fmt.Sprintf("resources[%d]", i))
		if err != nil {
			return nil, err
		}
		as, err := t.db.Allocation.Query().Where(
			allocation.TenantId(t.tenant.Uuid()),
			allocation.ResourceId(id.Uuid()),
			allocation.BeginsAtLT(to),
			allocation.EndsAtGT(from),
		).Order(allocation.ByBeginsAt()).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, a := range as {
			label := a.Kind
			if a.RefId != nil {
				if r, err := t.db.Reservation.Query().Where(reservation.Id(*a.RefId)).Only(ctx); err == nil {
					label = r.Name
				}
			}
			v := app.BusySpan_builder{
				ResourceId: id.Bytes(),
				BeginsAt:   ts(a.BeginsAt),
				EndsAt:     ts(a.EndsAt),
				Kind:       a.Kind,
				Units:      a.Units,
				Blocking:   a.Blocking,
				Label:      label,
			}.Build()
			if a.RefId != nil {
				v.SetRefId(pdid.Id(*a.RefId).Bytes())
			}
			busy = append(busy, v)
		}

		// Closed hours, a day at a time.
		b, err := t.db.Bookable.Query().Where(bookable.TenantId(t.tenant.Uuid()), bookable.AssetId(id.Uuid())).Only(ctx)
		if err != nil || b.Hours == nil || len(b.Hours.GetRanges()) == 0 {
			continue
		}
		loc, err := time.LoadLocation(b.Timezone)
		if err != nil {
			loc = time.UTC
		}
		for d := from.In(loc); d.Before(to); d = d.AddDate(0, 0, 1) {
			day := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
			ranges := []*app.OpeningRange{}
			for _, r := range b.Hours.GetRanges() {
				if int(r.GetWeekday()) == int(day.Weekday()) {
					ranges = append(ranges, r)
				}
			}
			sort.Slice(ranges, func(i, j int) bool { return ranges[i].GetFromMinute() < ranges[j].GetFromMinute() })
			cur := 0
			closed := func(a, b int) {
				if b > a {
					busy = append(busy, app.BusySpan_builder{
						ResourceId: id.Bytes(),
						BeginsAt:   ts(day.Add(time.Duration(a) * time.Minute)),
						EndsAt:     ts(day.Add(time.Duration(b) * time.Minute)),
						Kind:       "closed",
						Blocking:   true,
						Label:      "운영 시간 외",
					}.Build())
				}
			}
			for _, r := range ranges {
				closed(cur, int(r.GetFromMinute()))
				cur = max(cur, int(r.GetToMinute()))
			}
			closed(cur, 24*60)
		}
	}
	return app.BookableAvailabilityResponse_builder{Busy: busy}.Build(), nil
}

var _ = pd.ReservationDomain
