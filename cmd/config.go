// Package cmd is this app's own wiring, and it is short on purpose.
//
// Everything that does not change from one app to the next is in payday. What
// is left is here, and it is deliberately **not** hidden behind a
// `payday.Serve(cfg)`: the stack, the order of the interceptors and which
// server the wall is on are the decisions a reader of an app most needs to be
// able to see, and a framework that hid them would be hiding the only part
// worth reading.
package cmd

import (
	"net/url"
	"time"

	"github.com/lesomnus/payday/config"
)

// Name is what this app is called, and it is the only place it is written.
// The environment prefix and the names of the configuration files are derived
// from it -- APPTEST_DB_DSN, apptest.yaml -- by the loader `cli` makes with it,
// so there is nothing to keep in step.
const Name = "rove"

// Config is what this app is configured with.
//
// The framework cannot own this struct, since what an app is configured with is
// the app's. What it owns is the pieces: each of these is a payday type, and
// what is written here is only which of them this app has.
type Config struct {
	Server config.ServerConfig `yaml:"server"`
	Db     config.DbConfig     `yaml:"db"`
	Otel   config.OtelConfig   `yaml:"otel"`
	Watch  config.WatchConfig  `yaml:"watch"`

	// How long the trail keeps a row, per kind of thing. Every write this app
	// makes is recorded, and nothing else applies a window, so a deployment
	// that names none keeps all of it forever -- which is this field left
	// empty, and is the only honest default: a version upgrade is not the right
	// thing to decide how long somebody's evidence lasts.
	//
	// What the values are is what this app is regulated as, which payday cannot
	// know. `trail.Profiles` carries the sentence each number comes from.
	Audit config.AuditConfig `yaml:"audit"`

	// App is what rove is configured with beyond payday's pieces.
	App AppConfig `yaml:"app"`
}

// AppConfig is rove's own part of the configuration.
type AppConfig struct {
	// PublicUrl is where a browser reaches this server's HTTP listener, such
	// as http://localhost:8080. Download links are made on it, and its scheme
	// says whether the session cookie needs HTTPS.
	PublicUrl string `yaml:"public_url"`
	// AppUrl is where the UI is: PublicUrl when this server serves the built
	// UI, and the dev server's address during `npm run dev`. A scanned label
	// is sent there.
	AppUrl string `yaml:"app_url"`
	// Web is a directory holding the built UI, served at `/`. Empty serves
	// none, which is right while the UI runs on its own dev server.
	Web string `yaml:"web"`

	// Files is the directory attachments are kept in.
	Files string `yaml:"files"`
	// SigningKey signs the short-lived download links. Empty makes one per
	// process, which means a link stops working when the process restarts.
	SigningKey string `yaml:"signing_key"`

	Labels  LabelConfig   `yaml:"labels"`
	Session SessionConfig `yaml:"session"`

	// NoShowAfter is how long after a room reservation begins it is released
	// when nobody checked in. Zero holds nobody to checking in.
	NoShowAfter time.Duration `yaml:"no_show_after"`
	// Sweep is the wait between passes of the background work.
	Sweep time.Duration `yaml:"sweep"`
}

// LabelConfig is how printed labels are addressed (design 9.9).
type LabelConfig struct {
	// Suffix is what a tenant's default label host ends with: a tenant that
	// asks for `acme` prints `acme.<suffix>`. Empty offers no default
	// subdomain, and a tenant has labels only with a domain of its own.
	Suffix string `yaml:"suffix"`
	// Target is what a custom label domain's CNAME points at.
	Target string `yaml:"target"`
}

// SessionConfig is how long a browser stays signed in.
type SessionConfig struct {
	// Idle ends a session nobody used for this long.
	Idle time.Duration `yaml:"idle"`
	// Lifetime ends any session this long after it began.
	Lifetime time.Duration `yaml:"lifetime"`
}

// Public is PublicUrl, parsed, with the default a checkout runs on.
func (c AppConfig) Public() *url.URL {
	u, err := url.Parse(c.PublicUrl)
	if err != nil || u.Host == "" {
		u, _ = url.Parse("http://localhost:8080")
	}
	return u
}

// App is AppUrl, or the public address when the UI is served from it.
func (c AppConfig) App() string {
	if c.AppUrl != "" {
		return c.AppUrl
	}
	return c.Public().String()
}
