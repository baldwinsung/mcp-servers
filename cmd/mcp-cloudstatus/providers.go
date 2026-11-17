package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"mcpservers/internal/egress"
	"mcpservers/internal/textutil"
)

// Status levels, worst last.
const (
	Operational   = "operational"
	Maintenance   = "maintenance"
	Degraded      = "degraded"
	PartialOutage = "partial_outage"
	MajorOutage   = "major_outage"
)

var rank = map[string]int{Operational: 0, Maintenance: 1, Degraded: 2, PartialOutage: 3, MajorOutage: 4}

func worse(a, b string) string {
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// Incident is one active incident or notice.
type Incident struct {
	Title        string   `json:"title"`
	Status       string   `json:"status,omitempty"`
	Impact       string   `json:"impact,omitempty"`
	Started      string   `json:"started,omitempty"`
	Updated      string   `json:"updated,omitempty"`
	LatestUpdate string   `json:"latest_update,omitempty"`
	Affected     []string `json:"affected,omitempty"`
	Region       string   `json:"region,omitempty"`
	URL          string   `json:"url,omitempty"`
}

// Report is one service's state.
type Report struct {
	Service     string     `json:"service"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Summary     string     `json:"summary,omitempty"`
	Incidents   []Incident `json:"incidents"`
	Maintenance []Incident `json:"maintenance_in_progress,omitempty"`
	Components  []string   `json:"degraded_components,omitempty"`
	MoreComps   int        `json:"more_degraded_components,omitempty"`
	Filter      string     `json:"filtered_to,omitempty"`
	Note        string     `json:"note,omitempty"`
	StatusPage  string     `json:"status_page"`
	CheckedAt   string     `json:"checked_at"`
}

// Service is one provider's status source.
type Service struct {
	Key, Name, Kind, URL, Page, Note string
	Aliases                          []string
}

// The registry. Kinds: statuspage (Atlassian Statuspage /api/v2/summary.json),
// slack, m365, m365admin, azure (RSS), google (incidents.json), aws.
func registry() []Service {
	sp := func(key, name, host string, aliases ...string) Service {
		return Service{Key: key, Name: name, Kind: "statuspage", URL: "https://" + host + "/api/v2/summary.json", Page: "https://" + host, Aliases: aliases}
	}
	return []Service{
		{Key: "microsoft365", Name: "Microsoft 365 (consumer: Outlook.com, OneDrive, Teams Free, Office web, Copilot)", Kind: "m365",
			URL: "https://status.cloud.microsoft/api/posts/m365Consumer", Page: "https://status.cloud.microsoft",
			Aliases: []string{"microsoft", "m365", "office", "office365", "o365", "outlook", "outlook.com", "hotmail", "onedrive", "teams", "copilot", "word", "excel", "powerpoint", "to do", "todo"},
			Note:    "Microsoft publishes consumer service status publicly. Business/enterprise tenant health (Exchange Online, Teams for work) is only in each tenant's Microsoft 365 admin center; see microsoft365-admin for Microsoft's public notice when that admin center itself has problems."},
		{Key: "microsoft365-admin", Name: "Microsoft 365 admin center / enterprise notice", Kind: "m365admin",
			URL: "https://status.cloud.microsoft/api/posts/mac", Page: "https://status.cloud.microsoft",
			Aliases: []string{"exchange", "exchange online", "microsoft 365 business", "m365 business", "sharepoint"},
			Note:    "Microsoft posts here only when tenant admins can't reach Service health. Per-tenant incidents (Exchange Online, SharePoint, Teams for work) are visible only in the tenant's admin center."},
		{Key: "azure", Name: "Microsoft Azure", Kind: "azure", URL: "https://rssfeed.azure.status.microsoft/en-us/status/feed/", Page: "https://azure.status.microsoft/en-us/status", Aliases: []string{"microsoft azure"}},
		{Key: "google-workspace", Name: "Google Workspace", Kind: "google", URL: "https://www.google.com/appsstatus/dashboard/incidents.json", Page: "https://www.google.com/appsstatus/dashboard/",
			Aliases: []string{"google", "gmail", "google drive", "drive", "google docs", "docs", "google sheets", "sheets", "google meet", "meet", "google calendar", "calendar", "google chat", "workspace", "g suite"}},
		{Key: "google-cloud", Name: "Google Cloud", Kind: "google", URL: "https://status.cloud.google.com/incidents.json", Page: "https://status.cloud.google.com", Aliases: []string{"gcp", "google cloud platform"}},
		{Key: "aws", Name: "Amazon Web Services", Kind: "aws", URL: "https://health.aws.amazon.com/public/currentevents", Page: "https://health.aws.amazon.com/health/status", Aliases: []string{"amazon web services", "amazon aws"}},
		{Key: "slack", Name: "Slack", Kind: "slack", URL: "https://slack-status.com/api/v2.0.0/current", Page: "https://slack-status.com"},
		sp("github", "GitHub", "www.githubstatus.com"),
		sp("zoom", "Zoom", "www.zoomstatus.com"),
		sp("dropbox", "Dropbox", "status.dropbox.com"),
		sp("discord", "Discord", "discordstatus.com"),
		sp("openai", "OpenAI", "status.openai.com", "chatgpt"),
		sp("claude", "Claude (Anthropic)", "status.claude.com", "anthropic", "claude.ai"),
		sp("cloudflare", "Cloudflare", "www.cloudflarestatus.com"),
		sp("atlassian", "Atlassian", "status.atlassian.com"),
		sp("jira", "Jira", "jira-software.status.atlassian.com"),
		sp("confluence", "Confluence", "confluence.status.atlassian.com"),
		sp("bitbucket", "Bitbucket", "bitbucket.status.atlassian.com"),
		sp("trello", "Trello", "status.trello.com"),
		sp("reddit", "Reddit", "www.redditstatus.com"),
		sp("twilio", "Twilio", "status.twilio.com"),
		sp("digitalocean", "DigitalOcean", "status.digitalocean.com"),
		sp("npm", "npm", "status.npmjs.org"),
		sp("notion", "Notion", "www.notion-status.com"),
		sp("figma", "Figma", "status.figma.com"),
		sp("hubspot", "HubSpot", "status.hubspot.com"),
		sp("box", "Box", "status.box.com"),
		sp("shopify", "Shopify", "status.shopify.com"),
		sp("vercel", "Vercel", "www.vercel-status.com"),
		sp("netlify", "Netlify", "status.netlify.com"),
		sp("akamai", "Akamai (incl. Linode)", "status.akamai.com", "linode"),
		sp("asana", "Asana", "status.asana.com"),
	}
}

// lookup finds a service by key, alias or name; product is the alias when it
// names one product of a multi-product service (e.g. gmail).
func lookup(svcs []Service, q string) (*Service, string) {
	q = strings.ToLower(strings.TrimSpace(q))
	q = strings.TrimSuffix(strings.TrimSuffix(q, " status"), ".com")
	if q == "" {
		return nil, ""
	}
	for i := range svcs {
		if svcs[i].Key == q || strings.ToLower(svcs[i].Name) == q {
			return &svcs[i], ""
		}
	}
	for i := range svcs {
		for _, a := range svcs[i].Aliases {
			a2 := strings.TrimSuffix(a, ".com")
			if a2 == q {
				return &svcs[i], productFilter(svcs[i].Key, a)
			}
		}
	}
	for i := range svcs {
		if strings.HasPrefix(svcs[i].Key, q) || strings.Contains(strings.ToLower(svcs[i].Name), q) {
			return &svcs[i], ""
		}
	}
	return nil, ""
}

// productFilter maps an alias to the product name used in the feed.
func productFilter(key, alias string) string {
	generic := map[string]bool{"google": true, "workspace": true, "g suite": true, "microsoft": true, "m365": true, "office365": true, "o365": true}
	if generic[alias] {
		return ""
	}
	switch key {
	case "google-workspace":
		return strings.TrimPrefix(alias, "google ")
	case "microsoft365":
		return map[string]string{"outlook": "outlook", "outlook.com": "outlook", "hotmail": "outlook", "onedrive": "onedrive",
			"teams": "teams", "copilot": "copilot", "word": "office", "excel": "office", "powerpoint": "office", "office": "office", "to do": "to-do", "todo": "to-do"}[alias]
	}
	return ""
}

type fetcher struct {
	c   *egress.Client
	now func() time.Time
}

func (f *fetcher) check(ctx context.Context, s *Service, filter string) (*Report, error) {
	r := &Report{Service: s.Key, Name: s.Name, Status: Operational, Incidents: []Incident{}, StatusPage: s.Page,
		Note: s.Note, Filter: filter, CheckedAt: f.now().UTC().Format(time.RFC3339)}
	var err error
	switch s.Kind {
	case "statuspage":
		err = f.statuspage(ctx, s, r)
	case "slack":
		err = f.slack(ctx, s, r)
	case "m365":
		err = f.m365(ctx, s, r, filter)
	case "m365admin":
		err = f.m365admin(ctx, s, r)
	case "azure":
		err = f.azure(ctx, s, r)
	case "google":
		err = f.google(ctx, s, r, filter)
	case "aws":
		err = f.aws(ctx, s, r)
	default:
		err = fmt.Errorf("unknown kind %s", s.Kind)
	}
	if err != nil {
		return nil, err
	}
	if r.Summary == "" {
		r.Summary = map[string]string{Operational: "No known issues", Maintenance: "Maintenance in progress", Degraded: "Degraded or informational notices",
			PartialOutage: "Partial outage", MajorOutage: "Major outage"}[r.Status]
	}
	return r, nil
}

// ---- Atlassian Statuspage ----

func (f *fetcher) statuspage(ctx context.Context, s *Service, r *Report) error {
	var d struct {
		Status struct {
			Indicator   string `json:"indicator"`
			Description string `json:"description"`
		} `json:"status"`
		Components []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Group  bool   `json:"group"`
		} `json:"components"`
		Incidents []spIncident `json:"incidents"`
		Maint     []spIncident `json:"scheduled_maintenances"`
	}
	if err := f.c.GetJSON(ctx, s.URL, &d); err != nil {
		return err
	}
	r.Summary = d.Status.Description
	r.Status = map[string]string{"none": Operational, "minor": Degraded, "major": PartialOutage, "critical": MajorOutage, "maintenance": Maintenance}[d.Status.Indicator]
	if r.Status == "" {
		r.Status = Degraded
	}
	for _, c := range d.Components {
		if c.Group || c.Status == "operational" {
			continue
		}
		st := map[string]string{"degraded_performance": "degraded", "partial_outage": "partial outage", "major_outage": "major outage", "under_maintenance": "maintenance"}[c.Status]
		if len(r.Components) < 25 {
			r.Components = append(r.Components, c.Name+" ("+or(st, c.Status)+")")
		} else {
			r.MoreComps++
		}
	}
	for _, i := range d.Incidents {
		r.Incidents = append(r.Incidents, i.toIncident())
	}
	for _, m := range d.Maint {
		if m.Status == "in_progress" || m.Status == "verifying" {
			r.Maintenance = append(r.Maintenance, m.toIncident())
		}
	}
	return nil
}

type spIncident struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Impact    string `json:"impact"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Shortlink string `json:"shortlink"`
	Updates   []struct {
		Body string `json:"body"`
	} `json:"incident_updates"`
	Components []struct {
		Name string `json:"name"`
	} `json:"components"`
}

func (i spIncident) toIncident() Incident {
	in := Incident{Title: i.Name, Status: i.Status, Impact: i.Impact, Started: i.CreatedAt, Updated: i.UpdatedAt, URL: i.Shortlink}
	if len(i.Updates) > 0 {
		in.LatestUpdate = textutil.Trim(textutil.Plain(i.Updates[0].Body), 600)
	}
	for _, c := range i.Components {
		if len(in.Affected) < 15 {
			in.Affected = append(in.Affected, c.Name)
		}
	}
	return in
}

// ---- Slack ----

func (f *fetcher) slack(ctx context.Context, s *Service, r *Report) error {
	var d struct {
		Status    string `json:"status"`
		Incidents []struct {
			Title    string   `json:"title"`
			Type     string   `json:"type"`
			Status   string   `json:"status"`
			Created  string   `json:"date_created"`
			Updated  string   `json:"date_updated"`
			URL      string   `json:"url"`
			Services []string `json:"services"`
			Notes    []struct {
				Body string `json:"body"`
			} `json:"notes"`
		} `json:"active_incidents"`
	}
	if err := f.c.GetJSON(ctx, s.URL, &d); err != nil {
		return err
	}
	for _, i := range d.Incidents {
		in := Incident{Title: i.Title, Status: i.Status, Impact: i.Type, Started: i.Created, Updated: i.Updated, URL: i.URL, Affected: i.Services}
		if len(i.Notes) > 0 {
			in.LatestUpdate = textutil.Trim(textutil.Plain(i.Notes[len(i.Notes)-1].Body), 600)
		}
		r.Incidents = append(r.Incidents, in)
		r.Status = worse(r.Status, map[string]string{"outage": MajorOutage, "incident": PartialOutage}[i.Type])
		if i.Type == "notice" {
			r.Status = worse(r.Status, Degraded)
		}
	}
	if len(d.Incidents) > 0 && r.Status == Operational {
		r.Status = Degraded
	}
	return nil
}

// ---- Microsoft 365 ----

type m365Post struct {
	ServiceDisplayName string `json:"ServiceDisplayName"`
	Title              string `json:"Title"`
	Message            string `json:"Message"`
	Status             string `json:"Status"`
	LastUpdatedTime    string `json:"LastUpdatedTime"`
}

func (f *fetcher) m365(ctx context.Context, s *Service, r *Report, filter string) error {
	var posts []m365Post
	if err := f.c.GetJSON(ctx, s.URL, &posts); err != nil {
		return err
	}
	matched := 0
	for _, p := range posts {
		if filter != "" && !strings.Contains(strings.ToLower(p.ServiceDisplayName), filter) {
			continue
		}
		matched++
		if strings.EqualFold(p.Status, "Operational") || strings.EqualFold(p.Status, "Available") {
			continue
		}
		r.Incidents = append(r.Incidents, Incident{Title: or(p.Title, p.ServiceDisplayName+": "+p.Status), Status: p.Status,
			Updated: p.LastUpdatedTime, Affected: []string{p.ServiceDisplayName}, LatestUpdate: textutil.Trim(textutil.Plain(p.Message), 600)})
		r.Status = worse(r.Status, msStatus(p.Status))
	}
	if filter != "" && matched == 0 {
		r.Filter = ""
	}
	return nil
}

func msStatus(s string) string {
	s = strings.ToLower(s)
	switch {
	case strings.Contains(s, "interruption") || strings.Contains(s, "outage") || strings.Contains(s, "down"):
		return PartialOutage
	default:
		return Degraded
	}
}

func (f *fetcher) m365admin(ctx context.Context, s *Service, r *Report) error {
	var p m365Post
	if err := f.c.GetJSON(ctx, s.URL, &p); err != nil {
		return err
	}
	if !strings.EqualFold(p.Status, "Available") && !strings.EqualFold(p.Status, "Operational") {
		r.Incidents = append(r.Incidents, Incident{Title: or(p.Title, "Microsoft 365 admin center: "+p.Status), Status: p.Status,
			Updated: p.LastUpdatedTime, LatestUpdate: textutil.Trim(textutil.Plain(p.Message), 800)})
		r.Status = msStatus(p.Status)
	}
	return nil
}

// ---- Azure (RSS of active incidents) ----

func (f *fetcher) azure(ctx context.Context, s *Service, r *Report) error {
	body, err := f.c.Get(ctx, s.URL, "application/rss+xml")
	if err != nil {
		return err
	}
	var d struct {
		Items []struct {
			Title string `xml:"title"`
			Desc  string `xml:"description"`
			Date  string `xml:"pubDate"`
			Link  string `xml:"link"`
		} `xml:"channel>item"`
	}
	if err := xml.Unmarshal(body, &d); err != nil {
		return fmt.Errorf("azure feed: %v", err)
	}
	for _, i := range d.Items {
		r.Incidents = append(r.Incidents, Incident{Title: i.Title, Updated: i.Date, URL: i.Link, LatestUpdate: textutil.Trim(textutil.Plain(i.Desc), 800)})
		r.Status = PartialOutage
	}
	return nil
}

// ---- Google (Workspace and Cloud share the format) ----

func (f *fetcher) google(ctx context.Context, s *Service, r *Report, filter string) error {
	var d []struct {
		Begin    string `json:"begin"`
		End      string `json:"end"`
		Desc     string `json:"external_desc"`
		Impact   string `json:"status_impact"`
		Severity string `json:"severity"`
		Service  string `json:"service_name"`
		URI      string `json:"uri"`
		Modified string `json:"modified"`
		Latest   struct {
			Text   string `json:"text"`
			Status string `json:"status"`
		} `json:"most_recent_update"`
		Products []struct {
			Title string `json:"title"`
		} `json:"affected_products"`
	}
	if err := f.c.GetJSON(ctx, s.URL, &d); err != nil {
		return err
	}
	base := strings.TrimSuffix(s.Page, "/")
	for _, i := range d {
		if i.End != "" {
			continue
		}
		var prods []string
		for _, p := range i.Products {
			prods = append(prods, p.Title)
		}
		if len(prods) == 0 {
			prods = []string{i.Service}
		}
		if filter != "" && !strings.Contains(strings.ToLower(strings.Join(prods, "|")+"|"+i.Service), filter) {
			continue
		}
		in := Incident{Title: textutil.Trim(firstLine(textutil.Plain(i.Desc)), 200), Status: strings.ToLower(i.Latest.Status), Impact: i.Severity,
			Started: i.Begin, Updated: i.Modified, Affected: prods, LatestUpdate: textutil.Trim(textutil.Plain(i.Latest.Text), 600)}
		if i.URI != "" {
			in.URL = base + "/" + strings.TrimPrefix(i.URI, "/")
		}
		r.Incidents = append(r.Incidents, in)
		r.Status = worse(r.Status, map[string]string{"high": MajorOutage, "medium": PartialOutage}[i.Severity])
		r.Status = worse(r.Status, Degraded)
	}
	return nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "**Summary:**"))
	s = strings.Trim(s, "* \n")
	if i := strings.IndexByte(s, '\n'); i > 0 {
		return s[:i]
	}
	return s
}

// ---- AWS ----

var awsMsg = regexp.MustCompile(`'message':\s*'((?:[^'\\]|\\.)*)'|"message":\s*"((?:[^"\\]|\\.)*)"`)

func decodeUTF16(b []byte) []byte {
	if len(b) < 2 {
		return b
	}
	var be bool
	switch {
	case b[0] == 0xfe && b[1] == 0xff:
		be = true
	case b[0] == 0xff && b[1] == 0xfe:
		be = false
	default:
		return b
	}
	b = b[2:]
	u := make([]uint16, len(b)/2)
	for i := range u {
		if be {
			u[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
		} else {
			u[i] = uint16(b[2*i+1])<<8 | uint16(b[2*i])
		}
	}
	return []byte(string(utf16.Decode(u)))
}

func (f *fetcher) aws(ctx context.Context, s *Service, r *Report) error {
	body, err := f.c.Get(ctx, s.URL, "application/json")
	if err != nil {
		return err
	}
	body = bytes.TrimSpace(decodeUTF16(body))
	var d []struct {
		Date        json.RawMessage `json:"date"`
		Region      string          `json:"region_name"`
		Status      json.RawMessage `json:"status"`
		ServiceName string          `json:"service_name"`
		Summary     string          `json:"summary"`
		EventLog    json.RawMessage `json:"event_log"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &d); err != nil {
			return fmt.Errorf("aws feed: %v", err)
		}
	}
	sort.SliceStable(d, func(i, j int) bool { return unq(d[i].Date) > unq(d[j].Date) })
	for _, e := range d {
		st := unq(e.Status)
		level := map[string]string{"1": Degraded, "2": PartialOutage, "3": MajorOutage}[st]
		if level == "" {
			continue
		}
		in := Incident{Title: e.ServiceName + ": " + e.Summary, Region: e.Region,
			Impact: map[string]string{"1": "informational", "2": "degraded", "3": "disruption"}[st]}
		if sec, err := strconv.ParseInt(unq(e.Date), 10, 64); err == nil {
			in.Started = time.Unix(sec, 0).UTC().Format(time.RFC3339)
		}
		if m := awsMsg.FindAllStringSubmatch(unq(e.EventLog), -1); len(m) > 0 {
			last := m[len(m)-1]
			in.LatestUpdate = textutil.Trim(strings.ReplaceAll(or(last[1], last[2]), `\'`, "'"), 600)
		}
		r.Incidents = append(r.Incidents, in)
		r.Status = worse(r.Status, level)
	}
	return nil
}

// unq returns a JSON scalar as a string ("3" and 3 both give 3).
func unq(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
