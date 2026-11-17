// Command mcp-ixstatus: internet exchange (IX) lookups and health signals
// (mcp.baldwinsung.com/ixstatus). Directory data from PeeringDB; health from
// the exchange's own status dashboard when it is an Atlassian Statuspage,
// otherwise whether its dashboard, website and traffic-stats pages answer.
// Those URLs come from PeeringDB (third-party data), so in public mode every
// fetch goes through egress's private-address guard.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mcpservers/internal/egress"
	"mcpservers/internal/mcp"
	"mcpservers/internal/textutil"
)

type cachedDetail struct {
	d  ixDetail
	at time.Time
}

// pdbErr turns PeeringDB's throttling into a message for the caller.
func pdbErr(err error) error {
	var se *egress.StatusError
	if errors.As(err, &se) && se.Code == 429 {
		return mcp.Errorf("PeeringDB is rate-limiting this server right now; please try again in a minute")
	}
	return err
}

type ixRow struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	NameLong  string `json:"name_long"`
	City      string `json:"city"`
	Country   string `json:"country"`
	NetCount  int    `json:"net_count"`
	Dashboard string `json:"status_dashboard"`
}

type app struct {
	c         *egress.Client // the exchanges' own pages
	pc        *egress.Client // PeeringDB (may carry an API key)
	pdb       string         // PeeringDB API base
	details   map[int]cachedDetail
	now       func() time.Time
	mu        sync.Mutex
	list      []ixRow
	listAt    time.Time
	listTried time.Time
	checkTO   time.Duration
}

func (a *app) all(ctx context.Context) ([]ixRow, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Anonymous PeeringDB allows about one large query an hour, so the full
	// list (needed for typo matching) is refreshed daily, and a failed
	// refresh isn't retried for 15 minutes.
	if a.list != nil && a.now().Sub(a.listAt) < 24*time.Hour {
		return a.list, nil
	}
	if a.now().Sub(a.listTried) < 15*time.Minute {
		if a.list != nil {
			return a.list, nil
		}
		return nil, errNoList
	}
	a.listTried = a.now()
	var r struct {
		Data []ixRow `json:"data"`
	}
	if err := a.pc.GetJSON(ctx, a.pdb+"/ix?fields=id,name,name_long,city,country,net_count,status_dashboard", &r); err != nil {
		if a.list != nil {
			return a.list, nil // stale beats nothing
		}
		return nil, err
	}
	a.list, a.listAt = r.Data, a.now()
	return a.list, nil
}

var errNoList = errors.New("exchange list not loaded yet")

// search asks PeeringDB to filter by name and city (small queries, which
// anonymous clients may make often) when the full list isn't available.
func (a *app) search(ctx context.Context, q string) ([]ixRow, error) {
	seen := map[int]bool{}
	var out []ixRow
	for _, f := range []string{"name__contains", "name_long__contains", "city__contains"} {
		var r struct {
			Data []ixRow `json:"data"`
		}
		u := a.pdb + "/ix?" + url.Values{f: {q}, "fields": {"id,name,name_long,city,country,net_count,status_dashboard"}}.Encode()
		if err := a.pc.GetJSON(ctx, u, &r); err != nil {
			return nil, pdbErr(err)
		}
		for _, x := range r.Data {
			if !seen[x.ID] {
				seen[x.ID] = true
				out = append(out, x)
			}
		}
	}
	return out, nil
}

func norm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r > 127 {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func lev(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// score ranks how well row matches q (lower is better, -1 = no match).
func score(row ixRow, q string) int {
	nq := norm(q)
	if nq == "" {
		return -1
	}
	fields := []string{row.Name, row.NameLong, row.City}
	best := -1
	for i, f := range fields {
		nf := norm(f)
		s := -1
		switch {
		case nf == nq:
			s = 0
		case strings.Contains(nf, nq):
			s = 1 // prefix or substring: ranked by size
		default:
			// Typos ("NYIXX" for NYIIX): compare with each word of the name.
			for _, w := range strings.Fields(f) {
				nw := norm(w)
				if len(nq) >= 4 && len(nw) >= 3 {
					if d := lev(nq, nw); d <= 1+len(nq)/6 {
						s = 3 + d
						break
					}
				}
			}
		}
		if s >= 0 {
			s += i * 10 // name beats long name beats city
			if best < 0 || s < best {
				best = s
			}
		}
	}
	return best
}

func (a *app) find(ctx context.Context, q, country string, n int) ([]ixRow, error) {
	rows, err := a.all(ctx)
	if err != nil {
		if rows, err = a.search(ctx, q); err != nil {
			return nil, err
		}
	}
	type hit struct {
		r ixRow
		s int
	}
	var hits []hit
	for _, r := range rows {
		if country != "" && !strings.EqualFold(r.Country, country) {
			continue
		}
		if s := score(r, q); s >= 0 {
			hits = append(hits, hit{r, s})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].s != hits[j].s {
			return hits[i].s < hits[j].s
		}
		return hits[i].r.NetCount > hits[j].r.NetCount
	})
	var out []ixRow
	for i := 0; i < len(hits) && i < n; i++ {
		out = append(out, hits[i].r)
	}
	return out, nil
}

func (a *app) findTool(ctx context.Context, args mcp.Args) (any, error) {
	q := args.String("query")
	if len([]rune(q)) < 2 {
		return nil, mcp.Errorf("give an exchange name or city (at least 2 characters)")
	}
	rows, err := a.find(ctx, q, args.String("country"), args.Int("count", 10, 1, 25))
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []ixRow{}
	}
	type out struct {
		ID       int    `json:"peeringdb_id"`
		Name     string `json:"name"`
		NameLong string `json:"name_long,omitempty"`
		City     string `json:"city"`
		Country  string `json:"country"`
		Networks int    `json:"networks"`
		HasDash  bool   `json:"has_status_dashboard"`
	}
	res := []out{}
	for _, r := range rows {
		res = append(res, out{r.ID, r.Name, r.NameLong, r.City, r.Country, r.NetCount, r.Dashboard != ""})
	}
	return map[string]any{"exchanges": res, "source": "PeeringDB"}, nil
}

// ---- ix_status ----

type ixDetail struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	NameLong   string `json:"name_long"`
	City       string `json:"city"`
	Country    string `json:"country"`
	Region     string `json:"region_continent"`
	Website    string `json:"website"`
	Dashboard  string `json:"status_dashboard"`
	Stats      string `json:"url_stats"`
	NetCount   int    `json:"net_count"`
	FacCount   int    `json:"fac_count"`
	Media      string `json:"media"`
	IPv6       bool   `json:"proto_ipv6"`
	TechEmail  string `json:"tech_email"`
	TechPhone  string `json:"tech_phone"`
	PolicyMail string `json:"policy_email"`
	Notes      string `json:"notes"`
	Status     string `json:"status"`
	Updated    string `json:"updated"`
	Org        struct {
		Name string `json:"name"`
	} `json:"org"`
	Facs []struct {
		Name string `json:"name"`
		City string `json:"city"`
	} `json:"fac_set"`
}

type check struct {
	URL     string `json:"url"`
	OK      bool   `json:"reachable"`
	Missing bool   `json:"page_missing,omitempty"`
	HTTP    int    `json:"http_status,omitempty"`
	MS      int64  `json:"ms,omitempty"`
	Error   string `json:"error,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Summary string `json:"status,omitempty"`
}

func validURL(u string) bool {
	p, err := url.Parse(strings.TrimSpace(u))
	return err == nil && (p.Scheme == "https" || p.Scheme == "http") && p.Host != "" && p.User == nil
}

func (a *app) probe(ctx context.Context, u string) check {
	c := check{URL: u}
	if !validURL(u) {
		c.Error = "not an http(s) URL"
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, a.checkTO)
	defer cancel()
	start := time.Now()
	_, err := a.c.Get(ctx, u, "")
	c.MS = time.Since(start).Milliseconds()
	var se *egress.StatusError
	switch {
	case err == nil:
		c.OK, c.HTTP = true, 200
	case errors.As(err, &se):
		c.HTTP = se.Code
		// The site answered; 4xx (e.g. bot protection) still means it's up.
		c.OK = se.Code < 500
		if se.Code == 404 || se.Code == 410 {
			c.Missing, c.Error = true, "page not found (the link in PeeringDB may be stale)"
		} else if !c.OK {
			c.Error = err.Error()
		}
	default:
		c.Error = err.Error()
	}
	return c
}

// statuspage tries dashboard as an Atlassian Statuspage.
func (a *app) statuspage(ctx context.Context, dash string) (map[string]any, bool) {
	p, err := url.Parse(dash)
	if err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, a.checkTO)
	defer cancel()
	var d struct {
		Page struct {
			Name string `json:"name"`
		} `json:"page"`
		Status struct {
			Indicator   string `json:"indicator"`
			Description string `json:"description"`
		} `json:"status"`
		Incidents []struct {
			Name    string `json:"name"`
			Status  string `json:"status"`
			Impact  string `json:"impact"`
			Created string `json:"created_at"`
			Updates []struct {
				Body string `json:"body"`
			} `json:"incident_updates"`
		} `json:"incidents"`
		Components []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Group  bool   `json:"group"`
		} `json:"components"`
	}
	if err := a.c.GetJSON(ctx, p.Scheme+"://"+p.Host+"/api/v2/summary.json", &d); err != nil || d.Status.Indicator == "" {
		return nil, false
	}
	inc := []map[string]any{}
	for _, i := range d.Incidents {
		m := map[string]any{"title": i.Name, "status": i.Status, "impact": i.Impact, "started": i.Created}
		if len(i.Updates) > 0 {
			m["latest_update"] = textutil.Trim(textutil.Plain(i.Updates[0].Body), 500)
		}
		inc = append(inc, m)
	}
	var comps []string
	for _, c := range d.Components {
		if !c.Group && c.Status != "operational" && len(comps) < 25 {
			comps = append(comps, c.Name+" ("+strings.ReplaceAll(c.Status, "_", " ")+")")
		}
	}
	out := map[string]any{"page": d.Page.Name, "indicator": d.Status.Indicator, "description": d.Status.Description, "incidents": inc}
	if len(comps) > 0 {
		out["degraded_components"] = comps
	}
	return out, true
}

func (a *app) resolve(ctx context.Context, q string) (int, []ixRow, error) {
	if id, err := strconv.Atoi(strings.TrimSpace(q)); err == nil && id > 0 {
		return id, nil, nil
	}
	rows, err := a.find(ctx, q, "", 5)
	if err != nil {
		return 0, nil, err
	}
	if len(rows) == 0 {
		return 0, nil, mcp.Errorf("no exchange matches %q; try find_ix with a city name", q)
	}
	return rows[0].ID, rows[1:], nil
}

// detail returns PeeringDB's record for id, cached for an hour (directory
// data changes rarely; PeeringDB throttles anonymous clients).
func (a *app) detail(ctx context.Context, id int) (ixDetail, error) {
	a.mu.Lock()
	c, ok := a.details[id]
	a.mu.Unlock()
	if ok && a.now().Sub(c.at) < time.Hour {
		return c.d, nil
	}
	var r struct {
		Data []ixDetail `json:"data"`
	}
	if err := a.pc.GetJSON(ctx, fmt.Sprintf("%s/ix/%d?depth=2", a.pdb, id), &r); err != nil {
		var se *egress.StatusError
		if errors.As(err, &se) && se.Code == 404 {
			return ixDetail{}, mcp.Errorf("no exchange with PeeringDB id %d", id)
		}
		if ok {
			return c.d, nil
		}
		return ixDetail{}, pdbErr(err)
	}
	if len(r.Data) == 0 {
		return ixDetail{}, mcp.Errorf("no exchange with PeeringDB id %d", id)
	}
	a.mu.Lock()
	if len(a.details) > 500 {
		a.details = map[int]cachedDetail{}
	}
	a.details[id] = cachedDetail{r.Data[0], a.now()}
	a.mu.Unlock()
	return r.Data[0], nil
}

func (a *app) statusTool(ctx context.Context, args mcp.Args) (any, error) {
	id, others, err := a.resolve(ctx, args.String("ix"))
	if err != nil {
		return nil, err
	}
	x, err := a.detail(ctx, id)
	if err != nil {
		return nil, err
	}

	// Health signals, in parallel.
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		checks = map[string]check{}
		sp     map[string]any
	)
	run := func(name, u string) {
		if strings.TrimSpace(u) == "" {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := a.probe(ctx, u)
			mu.Lock()
			checks[name] = c
			mu.Unlock()
		}()
	}
	run("status_dashboard", x.Dashboard)
	run("website", x.Website)
	run("traffic_stats", x.Stats)
	if validURL(x.Dashboard) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s, ok := a.statuspage(ctx, x.Dashboard); ok {
				mu.Lock()
				sp = s
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	health := "unknown"
	var why []string
	if sp != nil {
		health = map[string]string{"none": "operational", "minor": "degraded", "major": "partial_outage", "critical": "major_outage", "maintenance": "maintenance"}[fmt.Sprint(sp["indicator"])]
		why = append(why, "from the exchange's status page")
	} else {
		reach, total := 0, 0
		for _, c := range checks {
			total++
			if c.OK {
				reach++
			}
		}
		switch {
		case total == 0:
			why = append(why, "PeeringDB lists no website, status dashboard or stats page")
		case reach == total:
			health = "no_known_issues"
			why = append(why, "its web pages answer; this exchange publishes no machine-readable status")
		case reach == 0:
			health = "pages_unreachable"
			why = append(why, "none of its web pages answer (could be the web host, not the exchange fabric)")
		default:
			health = "no_known_issues"
			why = append(why, fmt.Sprintf("%d of %d web pages answer", reach, total))
		}
	}
	var facs []string
	for _, f := range x.Facs {
		facs = append(facs, f.Name+" ("+f.City+")")
	}
	ex := map[string]any{
		"peeringdb_id": x.ID, "name": x.Name, "name_long": x.NameLong, "operator": x.Org.Name,
		"city": x.City, "country": x.Country, "region": x.Region,
		"networks": x.NetCount, "facilities": facs, "ipv6": x.IPv6, "media": x.Media,
		"website": x.Website, "status_dashboard": x.Dashboard, "traffic_stats": x.Stats,
		"tech_contact": strings.TrimSpace(x.TechEmail + " " + x.TechPhone), "peeringdb_updated": x.Updated,
	}
	if notes := textutil.Trim(textutil.Plain(x.Notes), 400); notes != "" {
		ex["notes"] = notes
	}
	var alt []string
	for _, o := range others {
		alt = append(alt, fmt.Sprintf("%s (%s, id %d)", o.Name, o.City, o.ID))
	}
	out := statusOut{
		Exchange: x.Name, Health: health, Basis: strings.Join(why, "; "), StatusPage: sp,
		Details: ex, Checks: checks, OtherMatches: alt, CheckedAt: a.now().UTC().Format(time.RFC3339),
		Note:   "Few exchanges publish live fabric status. Reachable web pages don't prove the switching fabric is healthy; for a suspected outage also check the exchange's NOC/tech contact and its member mailing list.",
		Source: "PeeringDB + the exchange's own pages",
	}
	return out, nil
}

// statusOut keeps the verdict first in the JSON.
type statusOut struct {
	Exchange     string           `json:"exchange"`
	Health       string           `json:"health"`
	Basis        string           `json:"basis"`
	StatusPage   map[string]any   `json:"status_page,omitempty"`
	Details      map[string]any   `json:"details"`
	Checks       map[string]check `json:"checks"`
	OtherMatches []string         `json:"other_matches,omitempty"`
	Note         string           `json:"note"`
	CheckedAt    string           `json:"checked_at"`
	Source       string           `json:"source"`
}

func tools(a *app) []mcp.Tool {
	return []mcp.Tool{
		{
			Name: "ix_status", Title: "Internet exchange status",
			Description: "Status and details of an internet exchange (IX), e.g. NYIIX, DE-CIX New York, AMS-IX, LINX: its status page when it has one (otherwise whether its website, dashboard and traffic-stats pages answer), operator, facilities, member network count and NOC contact. Name typos are tolerated.",
			Schema: mcp.Schema(map[string]any{
				"ix": mcp.Prop("string", "Exchange name (e.g. NYIIX, DE-CIX Frankfurt) or PeeringDB id"),
			}, "ix"),
			CacheFor: 5 * time.Minute, Handler: a.statusTool,
		},
		{
			Name: "find_ix", Title: "Find internet exchanges",
			Description: "Search internet exchanges by name or city (e.g. \"New York\", \"Equinix\", \"Frankfurt\"), largest first, with PeeringDB ids for ix_status.",
			Schema: mcp.Schema(map[string]any{
				"query":   mcp.Prop("string", "Exchange name or city"),
				"country": mcp.Prop("string", "Optional ISO country code filter, e.g. US, DE"),
				"count":   mcp.Prop("integer", "Maximum results, 1-25 (default 10)"),
			}, "query"),
			CacheFor: 30 * time.Minute, Handler: a.findTool,
		},
	}
}

func main() {
	pc := egress.New(mcp.PublicMode())
	if k := os.Getenv("PEERINGDB_API_KEY"); k != "" { // free PeeringDB account: higher rate limit
		pc.Headers = map[string]string{"Authorization": "Api-Key " + k}
	}
	a := &app{c: egress.New(mcp.PublicMode()), pc: pc, pdb: "https://www.peeringdb.com/api", now: time.Now,
		checkTO: 8 * time.Second, details: map[int]cachedDetail{}}
	mcp.Run(&mcp.Server{
		Name: "ixstatus", Version: "1.0.0", Tools: tools(a), Timeout: 40 * time.Second,
		Instructions: "Internet exchange (IX) directory and health signals from PeeringDB and each exchange's own pages.",
	}, "127.0.0.1:9104")
}
