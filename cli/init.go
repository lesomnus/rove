package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/z"

	"github.com/lesomnus/payday/frame"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/cmd"
	"github.com/lesomnus/rove/server/password"
)

// NewCmdInit is `rove init`: the first tenant, the person who owns it, and
// the types an asset register starts with.
//
// It exists because there is nowhere else it could happen. A tenant is not put
// up from inside one, so the first row of a deployment cannot arrive over the
// API. What puts it there is [cmd.Server.Base], which is not a privilege
// anybody holds: it is a server instance this process was handed, reachable
// from this command and from nowhere a request can get to.
//
// Running it twice is an error rather than a no-op, because an alias is unique
// and the database says so. That is the right answer -- an `init` that quietly
// did nothing is one somebody runs against the wrong deployment and believes.
func NewCmdInit(c *cmd.Config) *xli.Command {
	return &xli.Command{
		Name:  "init",
		Brief: "put up the first tenant and its owner",

		Flags: flg.Flags{
			&flg.String{Name: "tenant", Brief: "the tenant's alias, which a sign-in may name"},
			&flg.String{Name: "name", Brief: "the organization's name"},
			&flg.String{Name: "login", Brief: "the owner's login"},
			&flg.String{Name: "person", Brief: "the owner's name"},
			&flg.String{Name: "email", Brief: "the owner's e-mail, which also signs in"},
			&flg.String{Name: "password", Brief: "the owner's password; empty makes one up"},
			&flg.Switch{Name: "demo", Brief: "also fill it with teams, people, spaces and assets to try things on"},
		},

		Handler: xli.OnRun(func(ctx context.Context, self *xli.Command, next xli.Next) error {
			get := func(name, otherwise string) string {
				v, ok := flg.Find[string](self, name)
				if !ok || strings.TrimSpace(v) == "" {
					return otherwise
				}
				return strings.TrimSpace(v)
			}
			alias := get("tenant", "rove")
			name := get("name", alias)
			login := get("login", "admin")
			person := get("person", "관리자")
			email := get("email", "")
			pw := get("password", "")
			demo, _ := flg.Find[bool](self, "demo")

			made := ""
			if pw == "" {
				pw = password.Make()
				made = pw
			}
			hash, err := password.Hash(pw)
			if err != nil {
				return err
			}

			s, err := cmd.Build(ctx, *c)
			if err != nil {
				return err
			}
			defer s.Close()

			// The schema, so that a fresh database is one this can run against.
			if err := Migrate(ctx, s); err != nil {
				return err
			}

			t, err := s.Base.Tenant().Add(ctx, app.TenantAddRequest_builder{Alias: alias, Name: name}.Build())
			if err != nil {
				return fmt.Errorf("tenant %q: %w", alias, err)
			}
			tenant := app.TenantRef_builder{Id: t.GetId()}.Build()
			h, err := s.Base.Holder().Add(ctx, app.HolderAddRequest_builder{
				Tenant: tenant,
				Alias:  login,
				Name:   person,
				Role:   z.Ptr("owner"),
			}.Build())
			if err != nil {
				return fmt.Errorf("holder %q: %w", login, err)
			}
			if _, err := s.Base.Credential().Add(ctx, app.CredentialAddRequest_builder{
				Tenant: tenant,
				Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
				Secret: []byte(hash),
			}.Build()); err != nil {
				return err
			}
			if _, err := s.Base.Party().Add(ctx, app.PartyAddRequest_builder{
				Tenant: tenant,
				Name:   person,
				Kind:   "person",
				Email:  email,
				Holder: app.HolderRef_builder{Id: h.GetId()}.Build(),
			}.Build()); err != nil {
				return err
			}

			// From here on it is the owner doing it, through the domain layer,
			// so that what it writes reads as theirs.
			tid, _ := pdid.From(t.GetId())
			hid, _ := pdid.From(h.GetId())
			ctx = frame.Into(ctx, frame.New(hid, tid, frame.Whole()).WithScope(frame.Only(tid)).WithRow(h))

			if err := seedTypes(ctx, s.Ungated); err != nil {
				return err
			}
			if c.App.Labels.Suffix != "" {
				if _, err := s.Ungated.TenantDomain().Add(ctx, app.TenantDomainAddRequest_builder{Sub: z.Ptr(alias)}.Build()); err != nil {
					return fmt.Errorf("label domain: %w", err)
				}
			}
			if demo {
				if err := seedDemo(ctx, s.Ungated, self); err != nil {
					return fmt.Errorf("demo: %w", err)
				}
			}

			self.Printf("tenant   %s (%s)\n", alias, tid)
			self.Printf("owner    %s (%s)\n", login, hid)
			if made != "" {
				self.Printf("password %s\n", made)
			}
			self.Printf("\nsign in at %s with %s\n", c.App.App(), login)
			return nil
		}),
	}
}

type attr struct {
	key, label, kind, unit string
	options                []string
}

func spec(as ...attr) *app.TypeSpec {
	out := &app.TypeSpec{}
	for _, a := range as {
		out.SetAttributes(append(out.GetAttributes(), app.AttributeDef_builder{
			Key:     a.key,
			Label:   a.label,
			Type:    a.kind,
			Unit:    a.unit,
			Options: a.options,
		}.Build()))
	}
	return out
}

// rack is a type whose assets hold others at rack positions, which is what
// the rack view plugin draws.
func rack(as ...attr) *app.TypeSpec {
	v := spec(as...)
	v.SetCapabilities([]string{"rack"})
	return v
}

// seedTypes is what a register of an office starts with; a tenant changes
// them as it likes.
func seedTypes(ctx context.Context, s app.Server) error {
	warranty := attr{"warranty_until", "보증 만료", "date", "", nil}
	computer := []attr{
		{"cpu", "CPU", "text", "", nil},
		{"ram", "메모리", "number", "GB", nil},
		{"disk", "저장장치", "number", "GB", nil},
		{"os", "운영체제", "enum", "", []string{"Windows", "macOS", "Linux"}},
		warranty,
	}
	types := []struct {
		name, kind string
		spec       *app.TypeSpec
	}{
		{"노트북", "item", spec(computer...)},
		{"데스크톱", "item", spec(computer...)},
		{"모니터", "item", spec(attr{"size", "크기", "number", "inch", nil}, attr{"resolution", "해상도", "text", "", nil}, warranty)},
		{"휴대폰", "item", spec(attr{"os", "운영체제", "enum", "", []string{"iOS", "Android"}}, attr{"number", "전화번호", "text", "", nil})},
		{"태블릿", "item", nil},
		{"주변기기", "item", nil},
		{"네트워크 장비", "item", spec(attr{"ip", "IP", "text", "", nil}, attr{"mac", "MAC", "text", "", nil})},
		{"서버", "item", spec(attr{"ip", "IP", "text", "", nil}, attr{"cpu", "CPU", "text", "", nil}, attr{"ram", "메모리", "number", "GB", nil}, warranty)},
		{"랙", "item", rack(attr{"units", "높이", "number", "U", nil})},
		{"촬영 장비", "item", nil},
		{"가구", "item", nil},
		{"소모품", "item", nil},
		{"키트", "kit", nil},
		{"건물", "space", nil},
		{"층", "space", nil},
		{"사무실", "space", spec(attr{"seats", "좌석", "number", "석", nil})},
		{"회의실", "space", spec(attr{"capacity", "정원", "number", "명", nil}, attr{"display", "디스플레이", "bool", "", nil})},
		{"창고", "space", nil},
		{"서버실", "space", nil},
	}
	for _, ty := range types {
		req := app.AssetTypeAddRequest_builder{Name: ty.name, Kind: ty.kind, Spec: ty.spec}.Build()
		if _, err := s.AssetType().Add(ctx, req); err != nil {
			return fmt.Errorf("type %s: %w", ty.name, err)
		}
	}
	return nil
}
