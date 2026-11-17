// Command mcp-cloudstatus: is a cloud app or provider having an outage?
// (mcp.baldwinsung.com/cloudstatus). Reads each provider's own public status
// feed: Atlassian Statuspage JSON for most, plus Slack's API, Microsoft's
// status.cloud.microsoft feeds, Azure's RSS, Google's incidents.json and the
// AWS Health Dashboard. Fixed URLs only, nothing taken from the caller.
package main

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"mcpservers/internal/egress"
	"mcpservers/internal/mcp"
)

type app struct {
	f    *fetcher
	svcs []Service
}

func (a *app) list(ctx context.Context, _ mcp.Args) (any, error) {
	type row struct {
		Service string   `json:"service"`
		Name    string   `json:"name"`
		Aliases []string `json:"also_matches,omitempty"`
	}
	var out []row
	for _, s := range a.svcs {
		out = append(out, row{s.Key, s.Name, s.Aliases})
	}
	return map[string]any{"services": out, "count": len(out)}, nil
}

func (a *app) status(ctx context.Context, args mcp.Args) (any, error) {
	q := args.String("service")
	s, filter := lookup(a.svcs, q)
	if s == nil {
		return nil, mcp.Errorf("no status source for %q. Call list_services for the %d supported services", q, len(a.svcs))
	}
	return a.f.check(ctx, s, filter)
}

func (a *app) outages(ctx context.Context, _ mcp.Args) (any, error) {
	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		bad    []*Report
		failed = map[string]string{}
		sem    = make(chan struct{}, 8)
	)
	for i := range a.svcs {
		s := &a.svcs[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r, err := a.f.check(ctx, s, "")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed[s.Key] = err.Error()
				return
			}
			if r.Status != Operational || len(r.Incidents) > 0 {
				r.Note = ""
				bad = append(bad, r)
			}
		}()
	}
	wg.Wait()
	sort.Slice(bad, func(i, j int) bool {
		if rank[bad[i].Status] != rank[bad[j].Status] {
			return rank[bad[i].Status] > rank[bad[j].Status]
		}
		return bad[i].Service < bad[j].Service
	})
	if bad == nil {
		bad = []*Report{}
	}
	out := map[string]any{"checked": len(a.svcs), "with_issues": len(bad), "services": bad,
		"checked_at": a.f.now().UTC().Format(time.RFC3339)}
	if len(failed) > 0 {
		out["could_not_check"] = failed
	}
	return out, nil
}

func tools(a *app) []mcp.Tool {
	var names []string
	for _, s := range a.svcs {
		names = append(names, s.Key)
	}
	return []mcp.Tool{
		{
			Name: "service_status", Title: "Cloud service status",
			Description: "Is a cloud app or provider down? Current status, active incidents with their latest update, and degraded components, from the provider's own status page. Services: " + strings.Join(names, ", ") + ". Product names work too (outlook, teams, gmail, google meet, chatgpt, jira...).",
			Schema:      mcp.Schema(map[string]any{"service": mcp.Prop("string", "Service or product name, e.g. outlook, slack, github, aws, gmail")}, "service"),
			CacheFor:    2 * time.Minute, Handler: a.status,
		},
		{
			Name: "current_outages", Title: "All current cloud outages",
			Description: "Check every supported provider at once and list only those with an active incident, degradation or maintenance.",
			Schema:      mcp.Schema(map[string]any{}), CacheFor: 2 * time.Minute, Handler: a.outages,
		},
		{
			Name: "list_services", Title: "Supported services",
			Description: "The services this server can check, with the product names each one also answers to.",
			Schema:      mcp.Schema(map[string]any{}), CacheFor: time.Hour, Handler: a.list,
		},
	}
}

func main() {
	a := &app{f: &fetcher{c: egress.New(mcp.PublicMode()), now: time.Now}, svcs: registry()}
	mcp.Run(&mcp.Server{
		Name: "cloudstatus", Version: "1.0.0", Tools: tools(a), Timeout: 40 * time.Second,
		Instructions: "Outage and incident status for cloud apps and providers (Microsoft 365/Outlook, Google Workspace, AWS, Azure, Slack, GitHub, Zoom and more), read from each provider's public status page.",
	}, "127.0.0.1:9103")
}
