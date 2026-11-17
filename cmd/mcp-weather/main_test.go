package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mcpservers/internal/egress"
	"mcpservers/internal/mcp"
)

func fake(t *testing.T) *api {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/geo":
			if r.URL.Query().Get("name") != "Portland" {
				w.Write([]byte(`{}`))
				return
			}
			w.Write([]byte(`{"results":[
				{"name":"Portland","admin1":"Oregon","country":"United States","country_code":"US","latitude":45.5,"longitude":-122.7,"timezone":"America/Los_Angeles"},
				{"name":"Portland","admin1":"Maine","country":"United States","country_code":"US","latitude":43.7,"longitude":-70.3},
				{"name":"Portland","admin1":"Victoria","country":"Australia","country_code":"AU","latitude":-38.3,"longitude":141.6}]}`))
		case "/forecast":
			if r.URL.Query().Get("temperature_unit") != "fahrenheit" {
				t.Errorf("imperial not passed: %s", r.URL.RawQuery)
			}
			w.Write([]byte(`{"timezone":"America/Los_Angeles","elevation":15,
				"current":{"time":"2026-11-17T10:00","interval":900,"temperature_2m":50.1,"weather_code":61,"is_day":1},
				"daily":{"time":["2026-11-17","2026-11-18"],"weather_code":[3,95],"temperature_2m_max":[55,52]}}`))
		case "/alerts":
			w.Write([]byte(`{"features":[{"properties":{"event":"Flood Watch","severity":"Moderate","areaDesc":"Multnomah","ends":"2026-11-18T12:00:00-08:00"}}]}`))
		}
	}))
	t.Cleanup(ts.Close)
	return &api{c: egress.New(false), forecastURL: ts.URL + "/forecast", geoURL: ts.URL + "/geo", nwsURL: ts.URL + "/alerts"}
}

func TestGeocodeHints(t *testing.T) {
	a := fake(t)
	for q, want := range map[string]string{"Portland, OR": "Oregon", "Portland, Maine": "Maine", "Portland, Australia": "Victoria", "Portland": "Oregon"} {
		ps, err := a.geocode(context.Background(), q, 1)
		if err != nil || len(ps) != 1 || ps[0].Region != want {
			t.Errorf("%q: got %v %v, want %s", q, ps, err, want)
		}
	}
	if ps, _ := a.geocode(context.Background(), "Portland, Texas", 1); len(ps) != 0 {
		t.Errorf("hint should filter: %v", ps)
	}
}

func TestForecast(t *testing.T) {
	v, err := fake(t).forecast(context.Background(), mcp.Args{"place": "Portland, OR", "units": "imperial", "days": 2.0})
	if err != nil {
		t.Fatal(err)
	}
	m := v.(map[string]any)
	cur := m["current"].(map[string]any)
	if cur["conditions"] != "light rain" || cur["is_day"] != true || cur["interval"] != nil {
		t.Errorf("current: %v", cur)
	}
	d := m["daily"].([]map[string]any)
	if len(d) != 2 || d[1]["conditions"] != "thunderstorm" || d[1]["date"] != "2026-11-18" {
		t.Errorf("daily: %v", d)
	}
	if m["units"].(map[string]string)["temperature"] != "°F" {
		t.Error("units")
	}
}

func TestLocationErrors(t *testing.T) {
	a := fake(t)
	for _, args := range []mcp.Args{{}, {"latitude": 10.0}, {"latitude": 91.0, "longitude": 0.0}, {"place": "Atlantis"}} {
		_, err := a.forecast(context.Background(), args)
		if _, ok := err.(mcp.UserError); !ok {
			t.Errorf("%v: want a user error, got %v", args, err)
		}
	}
}

func TestAlerts(t *testing.T) {
	a := fake(t)
	v, err := a.alerts(context.Background(), mcp.Args{"place": "Portland, OR"})
	if err != nil || v.(map[string]any)["active_alerts"] != 1 {
		t.Fatalf("%v %v", v, err)
	}
	_, err = a.alerts(context.Background(), mcp.Args{"place": "Portland, Australia"})
	if err == nil || !strings.Contains(err.Error(), "US") {
		t.Errorf("non-US: %v", err)
	}
}
