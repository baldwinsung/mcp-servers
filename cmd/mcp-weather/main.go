// Command mcp-weather: weather forecasts and US weather alerts as MCP tools
// (mcp.baldwinsung.com/weather). Forecasts and place search from Open-Meteo
// (CC BY 4.0, no key), alerts from the US National Weather Service.
package main

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"

	"mcpservers/internal/egress"
	"mcpservers/internal/mcp"
)

type api struct {
	c                           *egress.Client
	forecastURL, geoURL, nwsURL string
}

func newAPI(c *egress.Client) *api {
	return &api{c: c,
		forecastURL: "https://api.open-meteo.com/v1/forecast",
		geoURL:      "https://geocoding-api.open-meteo.com/v1/search",
		nwsURL:      "https://api.weather.gov/alerts/active",
	}
}

type place struct {
	Name      string  `json:"name"`
	Region    string  `json:"region,omitempty"`
	Country   string  `json:"country,omitempty"`
	Code      string  `json:"country_code,omitempty"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Timezone  string  `json:"timezone,omitempty"`
	Pop       int     `json:"population,omitempty"`
}

func (a *api) geocode(ctx context.Context, name string, n int) ([]place, error) {
	// Open-Meteo matches the name only; "Paris, France" finds nothing, so
	// search the first part and use the rest to filter.
	parts := strings.Split(name, ",")
	q := strings.TrimSpace(parts[0])
	if len([]rune(q)) < 2 {
		return nil, mcp.Errorf("place name is too short")
	}
	var r struct {
		Results []struct {
			Name      string  `json:"name"`
			Admin1    string  `json:"admin1"`
			Country   string  `json:"country"`
			Code      string  `json:"country_code"`
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
			Timezone  string  `json:"timezone"`
			Pop       int     `json:"population"`
		} `json:"results"`
	}
	u := a.geoURL + "?" + url.Values{"name": {q}, "count": {"20"}, "language": {"en"}, "format": {"json"}}.Encode()
	if err := a.c.GetJSON(ctx, u, &r); err != nil {
		return nil, err
	}
	var hints []string
	for _, p := range parts[1:] {
		if h := strings.ToLower(strings.TrimSpace(p)); h != "" {
			hints = append(hints, h)
		}
	}
	var out []place
	for _, x := range r.Results {
		p := place{x.Name, x.Admin1, x.Country, x.Code, x.Latitude, x.Longitude, x.Timezone, x.Pop}
		if !matches(p, hints) {
			continue
		}
		out = append(out, p)
		if len(out) == n {
			break
		}
	}
	return out, nil
}

var usStates = map[string]string{"al": "alabama", "ak": "alaska", "az": "arizona", "ar": "arkansas", "ca": "california", "co": "colorado", "ct": "connecticut", "de": "delaware", "fl": "florida", "ga": "georgia", "hi": "hawaii", "id": "idaho", "il": "illinois", "in": "indiana", "ia": "iowa", "ks": "kansas", "ky": "kentucky", "la": "louisiana", "me": "maine", "md": "maryland", "ma": "massachusetts", "mi": "michigan", "mn": "minnesota", "ms": "mississippi", "mo": "missouri", "mt": "montana", "ne": "nebraska", "nv": "nevada", "nh": "new hampshire", "nj": "new jersey", "nm": "new mexico", "ny": "new york", "nc": "north carolina", "nd": "north dakota", "oh": "ohio", "ok": "oklahoma", "or": "oregon", "pa": "pennsylvania", "ri": "rhode island", "sc": "south carolina", "sd": "south dakota", "tn": "tennessee", "tx": "texas", "ut": "utah", "vt": "vermont", "va": "virginia", "wa": "washington", "wv": "west virginia", "wi": "wisconsin", "wy": "wyoming", "dc": "district of columbia"}

func matches(p place, hints []string) bool {
	for _, h := range hints {
		if full, ok := usStates[h]; ok && p.Code == "US" {
			h = full
		}
		fields := []string{strings.ToLower(p.Region), strings.ToLower(p.Country), strings.ToLower(p.Code)}
		if h == "usa" || h == "us" || h == "united states" {
			h = "us"
		}
		ok := false
		for _, f := range fields {
			if f != "" && (f == h || strings.Contains(f, h)) {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// location resolves latitude/longitude or place.
func (a *api) location(ctx context.Context, args mcp.Args) (place, error) {
	lat, okLat := args.Float("latitude")
	lon, okLon := args.Float("longitude")
	if okLat || okLon {
		if !okLat || !okLon {
			return place{}, mcp.Errorf("give both latitude and longitude")
		}
		if math.Abs(lat) > 90 || math.Abs(lon) > 180 {
			return place{}, mcp.Errorf("latitude must be -90..90 and longitude -180..180")
		}
		return place{Name: fmt.Sprintf("%.4f, %.4f", lat, lon), Latitude: lat, Longitude: lon}, nil
	}
	name := args.String("place")
	if name == "" {
		return place{}, mcp.Errorf("give a place name, or latitude and longitude (this server can't see the user's location: ask them)")
	}
	ps, err := a.geocode(ctx, name, 1)
	if err != nil {
		return place{}, err
	}
	if len(ps) == 0 {
		return place{}, mcp.Errorf("no place found for %q; try the city name alone, or use geocode", name)
	}
	return ps[0], nil
}

var wmo = map[int]string{
	0: "clear sky", 1: "mainly clear", 2: "partly cloudy", 3: "overcast", 45: "fog", 48: "depositing rime fog",
	51: "light drizzle", 53: "drizzle", 55: "dense drizzle", 56: "light freezing drizzle", 57: "freezing drizzle",
	61: "light rain", 63: "rain", 65: "heavy rain", 66: "light freezing rain", 67: "freezing rain",
	71: "light snow", 73: "snow", 75: "heavy snow", 77: "snow grains", 80: "light rain showers", 81: "rain showers",
	82: "violent rain showers", 85: "light snow showers", 86: "heavy snow showers", 95: "thunderstorm",
	96: "thunderstorm with light hail", 99: "thunderstorm with heavy hail",
}

func describe(code float64) string {
	if s, ok := wmo[int(code)]; ok {
		return s
	}
	return fmt.Sprintf("weather code %d", int(code))
}

func (a *api) forecast(ctx context.Context, args mcp.Args) (any, error) {
	loc, err := a.location(ctx, args)
	if err != nil {
		return nil, err
	}
	days := args.Int("days", 3, 1, 16)
	imperial := strings.EqualFold(args.String("units"), "imperial")
	q := url.Values{
		"latitude": {fmt.Sprintf("%.4f", loc.Latitude)}, "longitude": {fmt.Sprintf("%.4f", loc.Longitude)},
		"current":       {"temperature_2m,apparent_temperature,relative_humidity_2m,precipitation,weather_code,cloud_cover,wind_speed_10m,wind_gusts_10m,wind_direction_10m,is_day"},
		"daily":         {"weather_code,temperature_2m_max,temperature_2m_min,precipitation_sum,precipitation_probability_max,wind_speed_10m_max,wind_gusts_10m_max,sunrise,sunset,uv_index_max"},
		"timezone":      {"auto"},
		"forecast_days": {fmt.Sprint(days)},
	}
	units := map[string]string{"temperature": "°C", "wind": "km/h", "precipitation": "mm"}
	if imperial {
		q.Set("temperature_unit", "fahrenheit")
		q.Set("wind_speed_unit", "mph")
		q.Set("precipitation_unit", "inch")
		units = map[string]string{"temperature": "°F", "wind": "mph", "precipitation": "inch"}
	}
	var r struct {
		Timezone  string         `json:"timezone"`
		Elevation float64        `json:"elevation"`
		Current   map[string]any `json:"current"`
		Daily     map[string]any `json:"daily"`
	}
	if err := a.c.GetJSON(ctx, a.forecastURL+"?"+q.Encode(), &r); err != nil {
		return nil, err
	}
	cur := map[string]any{}
	for k, v := range r.Current {
		if k == "interval" {
			continue
		}
		cur[k] = v
	}
	if c, ok := r.Current["weather_code"].(float64); ok {
		cur["conditions"] = describe(c)
		delete(cur, "weather_code")
	}
	if d, ok := r.Current["is_day"].(float64); ok {
		cur["is_day"] = d == 1
	}
	var daily []map[string]any
	times, _ := r.Daily["time"].([]any)
	for i := range times {
		d := map[string]any{}
		for k, v := range r.Daily {
			arr, ok := v.([]any)
			if !ok || i >= len(arr) {
				continue
			}
			switch k {
			case "time":
				d["date"] = arr[i]
			case "weather_code":
				if c, ok := arr[i].(float64); ok {
					d["conditions"] = describe(c)
				}
			default:
				d[k] = arr[i]
			}
		}
		daily = append(daily, d)
	}
	return map[string]any{
		"location": loc, "timezone": r.Timezone, "elevation_m": r.Elevation, "units": units,
		"current": cur, "daily": daily,
		"source": "Open-Meteo.com (CC BY 4.0)",
	}, nil
}

func (a *api) alerts(ctx context.Context, args mcp.Args) (any, error) {
	loc, err := a.location(ctx, args)
	if err != nil {
		return nil, err
	}
	if loc.Code != "" && loc.Code != "US" && loc.Code != "PR" && loc.Code != "GU" && loc.Code != "VI" && loc.Code != "AS" && loc.Code != "MP" {
		return nil, mcp.Errorf("%s is in %s: weather alerts come from the US National Weather Service and cover the US and its territories only", loc.Name, loc.Country)
	}
	var r struct {
		Features []struct {
			Properties struct {
				Event       string `json:"event"`
				Headline    string `json:"headline"`
				Severity    string `json:"severity"`
				Urgency     string `json:"urgency"`
				Certainty   string `json:"certainty"`
				AreaDesc    string `json:"areaDesc"`
				Onset       string `json:"onset"`
				Ends        string `json:"ends"`
				Expires     string `json:"expires"`
				Description string `json:"description"`
				Instruction string `json:"instruction"`
				Sender      string `json:"senderName"`
			} `json:"properties"`
		} `json:"features"`
	}
	u := fmt.Sprintf("%s?point=%.4f,%.4f", a.nwsURL, loc.Latitude, loc.Longitude)
	body, err := a.c.Get(ctx, u, "application/geo+json")
	if err != nil {
		if se, ok := err.(*egress.StatusError); ok && se.Code == 400 {
			return nil, mcp.Errorf("the National Weather Service has no alerts for that point (outside the US?)")
		}
		return nil, err
	}
	if err := jsonUnmarshal(body, &r); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, f := range r.Features {
		p := f.Properties
		end := p.Ends
		if end == "" {
			end = p.Expires
		}
		out = append(out, map[string]any{
			"event": p.Event, "headline": p.Headline, "severity": p.Severity, "urgency": p.Urgency,
			"certainty": p.Certainty, "areas": p.AreaDesc, "onset": p.Onset, "ends": end,
			"description": trim(p.Description, 1500), "instruction": trim(p.Instruction, 800), "issued_by": p.Sender,
		})
	}
	return map[string]any{"location": loc, "active_alerts": len(out), "alerts": out,
		"checked_at": time.Now().UTC().Format(time.RFC3339), "source": "US National Weather Service (api.weather.gov)"}, nil
}

func trim(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func tools(a *api) []mcp.Tool {
	locProps := func() map[string]any {
		return map[string]any{
			"place":     mcp.Prop("string", `City or place name, optionally with region/country: "Scarsdale", "Portland, OR", "Paris, France"`),
			"latitude":  mcp.Prop("number", "Latitude in decimal degrees (use with longitude instead of place, e.g. from GPS)"),
			"longitude": mcp.Prop("number", "Longitude in decimal degrees"),
		}
	}
	fp := locProps()
	fp["days"] = mcp.Prop("integer", "Days of daily forecast, 1-16 (default 3)")
	fp["units"] = map[string]any{"type": "string", "enum": []string{"metric", "imperial"}, "description": "metric (°C, km/h, mm; default) or imperial (°F, mph, inch)"}
	return []mcp.Tool{
		{
			Name: "weather_forecast", Title: "Weather forecast",
			Description: "Current conditions and a daily forecast (up to 16 days) for a place name or GPS coordinates: temperature, feels-like, precipitation and its probability, wind and gusts, UV, sunrise/sunset. This server can't see where the user is; ask them, or use coordinates they give.",
			Schema:      mcp.Schema(fp), CacheFor: 10 * time.Minute, Handler: a.forecast,
		},
		{
			Name: "weather_alerts", Title: "Weather alerts (US)",
			Description: "Active watches, warnings and advisories from the US National Weather Service for a place or GPS point in the US or its territories.",
			Schema:      mcp.Schema(locProps()), CacheFor: 5 * time.Minute, Handler: a.alerts,
		},
		{
			Name: "geocode", Title: "Find a place",
			Description: "Look up places by name and get their coordinates, region, country and time zone (to pick the right one before a forecast).",
			Schema: mcp.Schema(map[string]any{
				"place": mcp.Prop("string", `Place name, optionally ", region" or ", country"`),
				"count": mcp.Prop("integer", "Maximum results, 1-10 (default 5)"),
			}, "place"),
			CacheFor: time.Hour,
			Handler: func(ctx context.Context, args mcp.Args) (any, error) {
				ps, err := a.geocode(ctx, args.String("place"), args.Int("count", 5, 1, 10))
				if err != nil {
					return nil, err
				}
				return map[string]any{"places": ps, "source": "Open-Meteo.com geocoding (GeoNames)"}, nil
			},
		},
	}
}

func main() {
	a := newAPI(egress.New(mcp.PublicMode()))
	mcp.Run(&mcp.Server{
		Name: "weather", Version: "1.0.0", Tools: tools(a),
		Instructions: "Weather forecasts worldwide (Open-Meteo) and US weather alerts (NWS) by place name or coordinates. The server can't locate the user; ask for a place.",
	}, "127.0.0.1:9102")
}
