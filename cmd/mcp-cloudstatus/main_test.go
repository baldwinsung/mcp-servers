package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"unicode/utf16"

	"mcpservers/internal/egress"
)

func be16(s string) []byte {
	out := []byte{0xfe, 0xff}
	for _, u := range utf16.Encode([]rune(s)) {
		out = append(out, byte(u>>8), byte(u))
	}
	return out
}

var fixtures = map[string][]byte{
	"/sp": []byte(`{"status":{"indicator":"major","description":"Partial System Outage"},
		"components":[{"name":"API","status":"major_outage"},{"name":"Group","status":"major_outage","group":true},{"name":"Web","status":"operational"}],
		"incidents":[{"name":"API errors","status":"investigating","impact":"major","incident_updates":[{"body":"<p>Looking &amp; fixing</p>"}],"components":[{"name":"API"}]}],
		"scheduled_maintenances":[{"name":"DB upgrade","status":"in_progress"},{"name":"later","status":"scheduled"}]}`),
	"/slack":   []byte(`{"status":"active","active_incidents":[{"title":"Messages delayed","type":"incident","status":"active","services":["Messaging"],"notes":[{"body":"first"},{"body":"latest"}]}]}`),
	"/m365":    []byte(`[{"ServiceDisplayName":"Outlook.com","Status":"Service degradation","Title":"Can't send mail","Message":"<div>We're on it</div>"},{"ServiceDisplayName":"OneDrive","Status":"Operational"}]`),
	"/google":  []byte(`[{"begin":"2026-11-17T10:00:00Z","end":"","external_desc":"**Summary:**\nGmail delays","severity":"medium","service_name":"Gmail","uri":"incidents/abc","affected_products":[{"title":"Gmail"}],"most_recent_update":{"text":"Mitigating","status":"SERVICE_DISRUPTION"}},{"begin":"2026-10-01T00:00:00Z","end":"2026-10-01T01:00:00Z","severity":"high","service_name":"Meet"}]`),
	"/aws":     be16(`[{"date":"1772369485","region_name":"UAE","status":"3","service_name":"Multiple services","summary":"Region Availability","event_log":"[{'summary': 'x', 'message': 'first', 'status': 1}, {'summary': 'y', 'message': 'It\\'s improving', 'status': 1}]"},{"date":"1","status":"0","service_name":"EC2","summary":"resolved"}]`),
	"/azure":   []byte(`<?xml version="1.0"?><rss><channel><title>Azure Status</title></channel></rss>`),
	"/azure2":  []byte(`<?xml version="1.0"?><rss><channel><item><title>Storage - East US</title><description>&lt;p&gt;Impact&lt;/p&gt;</description></item></channel></rss>`),
	"/m365adm": []byte(`{"Status":"Available","Title":"","Message":"<div>banner</div>"}`),
}

func env(t *testing.T) (*fetcher, string) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(fixtures[r.URL.Path]) }))
	t.Cleanup(ts.Close)
	return &fetcher{c: egress.New(false), now: func() time.Time { return time.Unix(1_800_000_000, 0) }}, ts.URL
}

func TestFeeds(t *testing.T) {
	f, base := env(t)
	cases := []struct {
		s       Service
		filter  string
		status  string
		nInc    int
		latest  string
		compLen int
	}{
		{Service{Key: "x", Kind: "statuspage", URL: base + "/sp"}, "", PartialOutage, 1, "Looking & fixing", 1},
		{Service{Key: "slack", Kind: "slack", URL: base + "/slack"}, "", PartialOutage, 1, "latest", 0},
		{Service{Key: "microsoft365", Kind: "m365", URL: base + "/m365"}, "outlook", Degraded, 1, "We're on it", 0},
		{Service{Key: "microsoft365", Kind: "m365", URL: base + "/m365"}, "onedrive", Operational, 0, "", 0},
		{Service{Key: "google-workspace", Kind: "google", URL: base + "/google", Page: "https://g/"}, "gmail", PartialOutage, 1, "Mitigating", 0},
		{Service{Key: "google-workspace", Kind: "google", URL: base + "/google"}, "meet", Operational, 0, "", 0},
		{Service{Key: "aws", Kind: "aws", URL: base + "/aws"}, "", MajorOutage, 1, "It's improving", 0},
		{Service{Key: "azure", Kind: "azure", URL: base + "/azure"}, "", Operational, 0, "", 0},
		{Service{Key: "azure", Kind: "azure", URL: base + "/azure2"}, "", PartialOutage, 1, "Impact", 0},
		{Service{Key: "m", Kind: "m365admin", URL: base + "/m365adm"}, "", Operational, 0, "", 0},
	}
	for _, c := range cases {
		r, err := f.check(context.Background(), &c.s, c.filter)
		if err != nil {
			t.Errorf("%s/%s: %v", c.s.Kind, c.filter, err)
			continue
		}
		if r.Status != c.status || len(r.Incidents) != c.nInc || len(r.Components) != c.compLen {
			t.Errorf("%s/%s: status %s incidents %d comps %v", c.s.Kind, c.filter, r.Status, len(r.Incidents), r.Components)
			continue
		}
		if c.nInc > 0 && r.Incidents[0].LatestUpdate != c.latest {
			t.Errorf("%s: latest %q, want %q", c.s.Kind, r.Incidents[0].LatestUpdate, c.latest)
		}
	}
	r, _ := f.check(context.Background(), &cases[0].s, "")
	if len(r.Maintenance) != 1 {
		t.Errorf("maintenance in progress: %v", r.Maintenance)
	}
	g, _ := f.check(context.Background(), &cases[4].s, "gmail")
	if g.Incidents[0].Title != "Gmail delays" || g.Incidents[0].URL != "https://g/incidents/abc" {
		t.Errorf("google incident: %+v", g.Incidents[0])
	}
}

func TestLookup(t *testing.T) {
	svcs := registry()
	for q, want := range map[string][2]string{
		"Outlook": {"microsoft365", "outlook"}, "outlook.com": {"microsoft365", "outlook"}, "teams": {"microsoft365", "teams"},
		"gmail": {"google-workspace", "gmail"}, "Google Meet": {"google-workspace", "meet"}, "google": {"google-workspace", ""},
		"GitHub": {"github", ""}, "github status": {"github", ""}, "chatgpt": {"openai", ""}, "AWS": {"aws", ""},
		"exchange online": {"microsoft365-admin", ""}, "zoom": {"zoom", ""},
	} {
		s, f := lookup(svcs, q)
		if s == nil || s.Key != want[0] || f != want[1] {
			t.Errorf("lookup(%q) = %v %q, want %v", q, s, f, want)
		}
	}
	if s, _ := lookup(svcs, "myspace"); s != nil {
		t.Errorf("unknown matched %s", s.Key)
	}
	keys := map[string]bool{}
	for _, s := range svcs {
		if keys[s.Key] {
			t.Errorf("duplicate key %s", s.Key)
		}
		keys[s.Key] = true
	}
}
