package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testServer(calls *atomic.Int32) *Server {
	return &Server{Name: "t", Version: "1", Logf: func(string, ...any) {}, Tools: []Tool{
		{Name: "echo", Schema: Schema(map[string]any{"x": Prop("string", "")}), CacheFor: time.Minute,
			Handler: func(ctx context.Context, a Args) (any, error) {
				calls.Add(1)
				if a.String("x") == "bad" {
					return nil, Errorf("bad input")
				}
				return map[string]string{"x": a.String("x")}, nil
			}},
	}}
}

func post(t *testing.T, h http.Handler, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/", strings.NewReader(body)))
	var m map[string]any
	json.Unmarshal(rr.Body.Bytes(), &m)
	return rr, m
}

func TestProtocol(t *testing.T) {
	var calls atomic.Int32
	h := testServer(&calls).Handler()
	_, m := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	if m["result"].(map[string]any)["protocolVersion"] != "2025-03-26" {
		t.Errorf("initialize: %v", m)
	}
	_, m = post(t, h, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	if m["result"].(map[string]any)["protocolVersion"] != latestProtocol {
		t.Errorf("unknown version not answered with latest: %v", m)
	}
	if rr, _ := post(t, h, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); rr.Code != 202 {
		t.Errorf("notification: %d", rr.Code)
	}
	if rr, _ := post(t, h, `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`); rr.Code != 400 {
		t.Errorf("batch: %d", rr.Code)
	}
	if rr, _ := post(t, h, `{`); rr.Code != 400 {
		t.Errorf("parse error: %d", rr.Code)
	}
	if _, m = post(t, h, `{"jsonrpc":"2.0","id":2,"method":"nope"}`); m["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Errorf("unknown method: %v", m)
	}
	_, m = post(t, h, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	if len(m["result"].(map[string]any)["tools"].([]any)) != 1 {
		t.Errorf("tools/list: %v", m)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != 405 {
		t.Errorf("GET: %d", rr.Code)
	}
	big := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"p":"` + strings.Repeat("a", maxBody) + `"}}`
	if rr, _ := post(t, h, big); rr.Code != 413 {
		t.Errorf("big body: %d", rr.Code)
	}
}

func text(m map[string]any) (string, bool) {
	r := m["result"].(map[string]any)
	return r["content"].([]any)[0].(map[string]any)["text"].(string), r["isError"].(bool)
}

func TestToolCallAndCache(t *testing.T) {
	var calls atomic.Int32
	h := testServer(&calls).Handler()
	for range 3 {
		_, m := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"x":"Hi"}}}`)
		if s, isErr := text(m); isErr || !strings.Contains(s, `"Hi"`) {
			t.Fatalf("call: %v", m)
		}
	}
	// Same arguments differing only in case share a cache entry.
	post(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"x":"hi"}}}`)
	if calls.Load() != 1 {
		t.Errorf("handler ran %d times, want 1 (cache)", calls.Load())
	}
	_, m := post(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"x":"bad"}}}`)
	if s, isErr := text(m); !isErr || s != "bad input" {
		t.Errorf("user error: %v", m)
	}
	_, m = post(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"missing"}}`)
	if m["error"] == nil {
		t.Errorf("unknown tool should be a JSON-RPC error: %v", m)
	}
}

func TestArgs(t *testing.T) {
	a := Args{"n": 50.0, "s": " 12.5 ", "b": true}
	if a.Int("n", 3, 1, 16) != 16 || a.Int("missing", 3, 1, 16) != 3 {
		t.Error("Int clamp/default")
	}
	if f, ok := a.Float("s"); !ok || f != 12.5 {
		t.Error("Float from string")
	}
}
