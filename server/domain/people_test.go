package domain_test

import (
	"context"
	"testing"
	"time"

	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/identity"
)

// signsIn is whether `alias` gets in at the roster with `password`.
func (e *env) signsIn(alias, password string) bool {
	_, err := e.s.Identity.Verify(context.Background(), "acme", alias, password)
	if err != nil {
		e.x.ErrorIs(err, identity.ErrRefused, "not a refusal: %v", err)
	}
	return err == nil
}

// TestAPasswordIsTheRosters.
//
// What the people screen does to a password, it does at the roster in this
// process: one's own needs the current one, and a reset by somebody else ends
// where they were signed in.
func TestAPasswordIsTheRosters(t *testing.T) {
	e := newEnv(t)
	x := e.x
	kimCtx, kim := e.person("member")
	alias := "p1"
	x.True(e.signsIn(alias, "password1234"))

	_, err := e.app().Party().SetPassword(kimCtx, app.PartySetPasswordRequest_builder{Current: "wrong-password", Password: "another1234"}.Build())
	x.Error(err, "a password changed without the current one")
	_, err = e.app().Party().SetPassword(kimCtx, app.PartySetPasswordRequest_builder{Current: "password1234", Password: "another1234"}.Build())
	x.NoError(err)
	x.False(e.signsIn(alias, "password1234"))
	x.True(e.signsIn(alias, "another1234"))

	began := time.Now().Add(-time.Second)
	_, err = e.app().Party().SetPassword(e.owner, app.PartySetPasswordRequest_builder{Ref: ref[app.PartyRef](kim.GetId()), Password: "reset-by-owner"}.Build())
	x.NoError(err)
	x.True(e.signsIn(alias, "reset-by-owner"))
	h, err := pdid.From(kim.GetHolder().GetId())
	x.NoError(err)
	st, err := e.s.Identity.StandingOf(context.Background(), e.tenant, h)
	x.NoError(err)
	x.False(st.Good(began), "a reset that left them signed in at roster")
}

// TestALoginEndedHereIsGoneFromTheRosterInThisProcess.
//
// The roster in this process is Rove's: a login stopped or a person forgotten
// here leaves nothing there to sign in with, and their login is free for
// somebody else.
func TestALoginEndedHereIsGoneFromTheRosterInThisProcess(t *testing.T) {
	for _, end := range []string{"deactivate", "pseudonymize"} {
		t.Run(end, func(t *testing.T) {
			e := newEnv(t)
			x := e.x
			_, kim := e.person("member")
			x.True(e.signsIn("p1", "password1234"))

			r := ref[app.PartyRef](kim.GetId())
			var err error
			switch end {
			case "deactivate":
				_, err = e.app().Party().Deactivate(e.owner, app.PartyDeactivateRequest_builder{Ref: r}.Build())
			case "pseudonymize":
				_, err = e.app().Party().Pseudonymize(e.owner, app.PartyPseudonymizeRequest_builder{Ref: r}.Build())
			}
			x.NoError(err)
			x.False(e.signsIn("p1", "password1234"))

			lee, err := e.app().Party().Add(e.owner, app.PartyAddRequest_builder{Name: "Lee", Kind: "person"}.Build())
			x.NoError(err)
			_, err = e.app().Party().Invite(e.owner, app.PartyInviteRequest_builder{
				Ref:      ref[app.PartyRef](lee.GetId()),
				Alias:    "p1",
				Role:     "member",
				Password: "password5678",
			}.Build())
			x.NoError(err, "the login is not free for somebody else")
			x.True(e.signsIn("p1", "password5678"))
		})
	}
}
