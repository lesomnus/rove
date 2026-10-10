package domain_test

import (
	"context"
	"strings"
	"testing"
	"uuid"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent/audit"
	"github.com/lesomnus/rove/internal/ent/event"
)

// copies is how many of the trail's rows about these objects still hold what
// they said, and how many there are.
func (e *env) copies(ids ...[]byte) (full, all int) {
	us := []uuid.UUID{}
	for _, v := range ids {
		us = append(us, idUUID(v))
	}
	vs, err := e.s.Ent.Audit.Query().Where(audit.ObjectIdIn(us...)).All(context.Background())
	e.x.NoError(err)
	for _, v := range vs {
		if len(v.Value) > 0 || len(v.Patch) > 0 {
			full++
		}
	}
	return full, len(vs)
}

// TestAPersonForgottenIsForgottenInTheTrail.
//
// The trail kept the person's row, and their login's, after every write. They
// go with the person; that the writes happened stays.
func TestAPersonForgottenIsForgottenInTheTrail(t *testing.T) {
	e := newEnv(t)
	x := e.x
	_, kim := e.person("member")
	h := kim.GetHolder().GetId()

	full, all := e.copies(kim.GetId(), h)
	x.Positive(full, "the trail kept nothing of the person to begin with")

	v, err := e.app().Party().Pseudonymize(e.owner, app.PartyPseudonymizeRequest_builder{Ref: ref[app.PartyRef](kim.GetId())}.Build())
	x.NoError(err)
	x.True(strings.HasPrefix(v.GetName(), "익명 "))

	full, after := e.copies(kim.GetId(), h)
	x.Zero(full, "a copy of the person in the trail")
	x.GreaterOrEqual(after, all, "the record that the writes happened went with them")
}

// TestAHoldKeepsWhatTheTrailSaidOfAPerson.
//
// A legal claim outweighs an erasure request: the person's row is forgotten,
// the trail's copies a hold is on are not, and the event says so.
func TestAHoldKeepsWhatTheTrailSaidOfAPerson(t *testing.T) {
	e := newEnv(t)
	x := e.x
	_, kim := e.person("member")

	_, err := e.s.Base.LegalHold().Add(e.owner, app.LegalHoldAddRequest_builder{
		Tenant: app.TenantRef_builder{Id: e.tenant.Bytes()}.Build(),
		Name:   "사건 1",
	}.Build())
	x.NoError(err)

	before, _ := e.copies(kim.GetId())
	_, err = e.app().Party().Pseudonymize(e.owner, app.PartyPseudonymizeRequest_builder{Ref: ref[app.PartyRef](kim.GetId())}.Build())
	x.NoError(err)

	full, _ := e.copies(kim.GetId())
	x.GreaterOrEqual(full, before, "a hold is on what the trail said")
	ev, err := e.s.Ent.Event.Query().Where(event.Kind("party.pseudonymize")).Only(context.Background())
	x.NoError(err)
	x.Contains(ev.Payload, "held")
}
