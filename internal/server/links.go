package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-alerts/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// Pocket Core app links (docs/links.md in pocket-homelab-core), Alerts as the
// target: another Pocket app the person allowed in Core may act here for
// them with a pcx_ token, only within these scopes. Every route not listed
// in linkRoutes stays closed to it (403).

var linkOffers = []core.Offer{
	{Scope: "alerts:read", Name: "See your alerts", Description: "Read your alerts, where they come from and what happened to them, for your journal, dashboards and recaps."},
	{Scope: "alerts:write", Name: "Send you alerts", Description: "Let another app put an alert in your inbox (it goes to your phone like any other, by your rules and quiet hours). It changes nothing else."},
}

// portable is what Pocket Move may copy out and bring in for a person
// (docs/move.md in pocket-homelab-core); Register adds move:export/import.
var portable = &core.Portable{
	Exports: []core.PortableExport{
		{Path: "/api/v1/export.json", File: "alerts.json", Kind: core.KindPocket, Title: "Your sources, channels, rules and every alert you got"},
		{Path: "/api/v1/export.csv", File: "alerts.csv", Kind: core.KindCSV, Title: "Your alerts as a table"},
	},
	Imports: []core.PortableImport{
		{Path: "/api/v1/import", Kind: core.KindPocket, ContentType: "application/json", Title: "Adds the sources, channels, rules and alerts from another box; what you already have is left alone"},
	},
}

// capabilities is what Pocket Search, Pocket Day and Pocket Hub may ask
// Alerts (docs/capabilities.md in pocket-homelab-core); Register adds the
// pocket:* offers. The routes are in capabilities.go; all three only read.
// No capture: an alert comes from a source, not from a line a person types.
var capabilities = core.Capabilities{
	Search: &core.Searchable{What: "Your alerts, by title, text and where they came from", Kinds: []string{"alert"}, Bangs: []string{"alerts", "alert"}},
	Day:    &core.DayFeed{Label: "Alerts", One: "alert", Many: "alerts", What: "The alerts that came in that day, the most important first"},
	Widget: &core.Widget{Title: "Alerts", What: "Your open alerts, the most urgent first"},
}

type linkRoute struct {
	methods string
	path    *regexp.Regexp
	scope   string // "" = any linked app
}

var linkRoutes = []linkRoute{
	{"GET", regexp.MustCompile(`^/api/v1/auth/me$`), ""},
	{"GET", regexp.MustCompile(`^/api/v1/(dashboard|alerts|sources)$`), "alerts:read"},
	{"GET", regexp.MustCompile(`^/api/v1/alerts/\d+$`), "alerts:read"},
	{"GET", regexp.MustCompile(`^/api/v1/(day|summary)/\d{4}-\d{2}-\d{2}$`), "alerts:read"},
	{"POST", regexp.MustCompile(`^/api/v1/alerts$`), "alerts:write"},
}

var errLinkScope = errors.New("outside the link's scope")

// linkAllows reports whether a link token with these scopes may make this request.
func linkAllows(scopes []string, r *http.Request) bool {
	if portable.LinkAllows(scopes, r) || capabilities.LinkAllows(scopes, r) {
		return true
	}
	for _, lr := range linkRoutes {
		if !strings.Contains(" "+lr.methods+" ", " "+r.Method+" ") || !lr.path.MatchString(r.URL.Path) {
			continue
		}
		if lr.scope == "" {
			return true
		}
		for _, sc := range scopes {
			if sc == lr.scope {
				return true
			}
		}
	}
	return false
}

// linkUser validates a pcx_ token, checks the route against its scopes and
// maps it to the person it acts for. The second value is the acting app's slug.
func (s *Server) linkUser(ctx context.Context, tok string, r *http.Request) (*store.User, string, error) {
	in, err := s.core.Introspect(ctx, tok)
	if err != nil {
		return nil, "", err
	}
	if !in.Active || !in.Delegated() || in.Audience != "alerts" || in.User == nil || in.User.Email == "" || in.App == nil || in.App.Slug == "" {
		return nil, "", http.ErrNoCookie
	}
	if !linkAllows(strings.Fields(in.Scope), r) {
		return nil, "", errLinkScope
	}
	u, err := s.coreIdentityUser(ctx, in)
	return u, in.App.Slug, err
}

const linkAppKey ctxKey = 2

// linkApp is the acting app's slug when the request came in on a link token.
func linkApp(ctx context.Context) string {
	a, _ := ctx.Value(linkAppKey).(string)
	return a
}
