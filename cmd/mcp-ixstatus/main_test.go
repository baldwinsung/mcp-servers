package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mcpservers/internal/egress"
	"mcpservers/internal/mcp"
)

func env(t *testing.T, listOK bool) (*app, *int) {
	var listCalls int
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/ix" && r.URL.Query().Get("name__contains") != "":
			if strings.Contains("NYIIX New York", r.URL.Query().Get("name__contains")) {
				w.Write([]byte(`{"data":[{"id":14,"name":"NYIIX New York","city":"New York","country":"US","net_count":186}]}`))
				return
			}
			w.Write([]byte(`{"data":[]}`))
		case r.URL.Path == "/api/ix" && r.URL.Query().Get("fields") != "" && len(r.URL.Query()) == 1:
			listCalls++
			if !listOK {
				w.WriteHeader(429)
				return
			}
			w.Write([]byte(`{"data":[
				{"id":14,"name":"NYIIX New York","name_long":"New York International Internet eXchange","city":"New York","country":"US","net_count":186},
				{"id":804,"name":"DE-CIX New York","city":"New York","country":"US","net_count":264},
				{"id":2,"name":"Equinix Chicago","city":"Chicago","country":"US","net_count":300},
				{"id":3607,"name":"SONIX Stockholm","city":"Stockholm","country":"SE","net_count":80}]}`))
		case r.URL.Path == "/api/ix":
			w.Write([]byte(`{"data":[]}`))
		case r.URL.Path == "/api/ix/14":
			fmt.Fprintf(w, `{"data":[{"id":14,"name":"NYIIX New York","website":"%s/site","url_stats":"%s/gone","status_dashboard":"","fac_set":[{"name":"60 Hudson","city":"New York"}],"org":{"name":"NYIIX"}}]}`, ts.URL, ts.URL)
		case r.URL.Path == "/api/ix/3607":
			fmt.Fprintf(w, `{"data":[{"id":3607,"name":"SONIX Stockholm","status_dashboard":"%s/dash"}]}`, ts.URL)
		case r.URL.Path == "/api/ix/99":
			w.WriteHeader(404)
		case r.URL.Path == "/api/v2/summary.json":
			w.Write([]byte(`{"page":{"name":"SONIX"},"status":{"indicator":"minor","description":"Minor"},"incidents":[{"name":"Port flaps","status":"investigating"}],"components":[]}`))
		case r.URL.Path == "/site", r.URL.Path == "/dash":
			w.Write([]byte("ok"))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(ts.Close)
	c := egress.New(false)
	return &app{c: c, pc: c, pdb: ts.URL + "/api", now: time.Now, checkTO: 5 * time.Second, details: map[int]cachedDetail{}}, &listCalls
}

func TestFindTypos(t *testing.T) {
	a, _ := env(t, true)
	for q, want := range map[string]int{"NYIXX": 14, "nyiix": 14, "New York": 804, "decix new york": 804, "Chicago": 2, "sonix": 3607} {
		rows, err := a.find(context.Background(), q, "", 3)
		if err != nil || len(rows) == 0 || rows[0].ID != want {
			t.Errorf("find(%q) = %v %v, want id %d first", q, rows, err, want)
		}
	}
	if rows, _ := a.find(context.Background(), "zzqqxx", "", 3); len(rows) != 0 {
		t.Errorf("nonsense matched %v", rows)
	}
	if rows, _ := a.find(context.Background(), "New York", "SE", 3); len(rows) != 0 {
		t.Errorf("country filter: %v", rows)
	}
}

func TestListThrottledFallsBack(t *testing.T) {
	a, calls := env(t, false)
	rows, err := a.find(context.Background(), "NYIIX", "", 3)
	if err != nil || len(rows) != 1 || rows[0].ID != 14 {
		t.Fatalf("fallback search: %v %v", rows, err)
	}
	a.find(context.Background(), "NYIIX", "", 3)
	if *calls != 1 {
		t.Errorf("throttled list retried %d times within 15 min", *calls)
	}
}

func TestStatusSignals(t *testing.T) {
	a, _ := env(t, true)
	v, err := a.statusTool(context.Background(), mcp.Args{"ix": "NYIXX"})
	if err != nil {
		t.Fatal(err)
	}
	o := v.(statusOut)
	if o.Health != "no_known_issues" || !o.Checks["website"].OK || !o.Checks["traffic_stats"].Missing {
		t.Errorf("NYIIX: %+v", o)
	}
	v, err = a.statusTool(context.Background(), mcp.Args{"ix": "3607"})
	if err != nil {
		t.Fatal(err)
	}
	if o := v.(statusOut); o.Health != "degraded" || o.StatusPage == nil {
		t.Errorf("statuspage dashboard: %+v", o)
	}
	if _, err := a.statusTool(context.Background(), mcp.Args{"ix": "99"}); err == nil || !strings.Contains(err.Error(), "no exchange") {
		t.Errorf("missing id: %v", err)
	}
}

func TestProbeRejectsOddURLs(t *testing.T) {
	a, _ := env(t, true)
	for _, u := range []string{"file:///etc/passwd", "gopher://x", "http://user:pw@example.com/", "not a url"} {
		if c := a.probe(context.Background(), u); c.OK || c.Error == "" {
			t.Errorf("%s: %+v", u, c)
		}
	}
}
