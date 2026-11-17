// Package mcp is a small MCP server over Streamable HTTP (stateless, POST
// only, one JSON response per request), shared by the servers in cmd/. They
// run on loopback behind mcp-gateway, which does TLS, limits and tokens.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const latestProtocol = "2025-06-18"

var protocols = map[string]bool{"2024-11-05": true, "2025-03-26": true, "2025-06-18": true, "2025-11-25": true}

const maxBody = 64 << 10

// Args are a tools/call's arguments.
type Args map[string]any

// UserError is shown to the caller as a tool error (bad input, not found).
type UserError struct{ Msg string }

func (e UserError) Error() string { return e.Msg }

// Errorf makes a UserError.
func Errorf(format string, a ...any) error { return UserError{fmt.Sprintf(format, a...)} }

// Tool is one MCP tool. Handler returns a value that is sent as indented JSON.
type Tool struct {
	Name        string
	Title       string
	Description string
	Schema      map[string]any // JSON Schema for the arguments
	CacheFor    time.Duration  // 0 = no caching
	Handler     func(ctx context.Context, a Args) (any, error)
}

// Server dispatches JSON-RPC to tools.
type Server struct {
	Name, Version, Instructions string
	Tools                       []Tool
	Timeout                     time.Duration // per tool call, default 30s
	MaxConcurrent               int           // default 8
	Logf                        func(string, ...any)

	once   sync.Once
	byName map[string]*Tool
	sem    chan struct{}
	mu     sync.Mutex
	cache  map[string]cached
}

type cached struct {
	text    string
	expires time.Time
}

func (s *Server) init() {
	s.once.Do(func() {
		s.byName = map[string]*Tool{}
		for i := range s.Tools {
			s.byName[s.Tools[i].Name] = &s.Tools[i]
		}
		if s.MaxConcurrent == 0 {
			s.MaxConcurrent = 8
		}
		if s.Timeout == 0 {
			s.Timeout = 30 * time.Second
		}
		if s.Logf == nil {
			s.Logf = log.Printf
		}
		s.sem = make(chan struct{}, s.MaxConcurrent)
		s.cache = map[string]cached{}
	})
}

type rpcIn struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func rpcErr(w http.ResponseWriter, status int, id json.RawMessage, code int, msg string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	writeJSON(w, status, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
}

// Handler returns the HTTP handler: POST / for MCP, GET /healthz.
func (s *Server) Handler() http.Handler {
	s.init()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("/{$}", s.serve)
	return mux
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !protocols[v] {
		http.Error(w, "unsupported MCP-Protocol-Version", http.StatusBadRequest)
		return
	}
	body, err := readBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	if t := bytes.TrimLeft(body, " \t\r\n"); len(t) > 0 && t[0] == '[' {
		rpcErr(w, http.StatusBadRequest, nil, -32600, "batch requests are not supported")
		return
	}
	var m rpcIn
	if err := json.Unmarshal(body, &m); err != nil {
		rpcErr(w, http.StatusBadRequest, nil, -32700, "parse error")
		return
	}
	if len(m.ID) == 0 { // notification
		w.WriteHeader(http.StatusAccepted)
		return
	}
	res, code, msg := s.dispatch(r.Context(), m)
	if code != 0 {
		rpcErr(w, http.StatusOK, m.ID, code, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": res})
}

func readBody(r *http.Request) ([]byte, error) {
	var b bytes.Buffer
	n, err := b.ReadFrom(http.MaxBytesReader(nil, r.Body, maxBody))
	if err != nil || n > maxBody {
		return nil, errors.New("request body too large")
	}
	return b.Bytes(), nil
}

func (s *Server) dispatch(ctx context.Context, m rpcIn) (any, int, string) {
	switch m.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(m.Params, &p)
		v := latestProtocol
		if protocols[p.ProtocolVersion] {
			v = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
			"instructions":    s.Instructions,
		}, 0, ""
	case "ping":
		return map[string]any{}, 0, ""
	case "tools/list":
		var list []map[string]any
		for _, t := range s.Tools {
			list = append(list, map[string]any{
				"name": t.Name, "title": t.Title, "description": t.Description, "inputSchema": t.Schema,
				"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": true},
			})
		}
		return map[string]any{"tools": list}, 0, ""
	case "tools/call":
		var p struct {
			Name      string `json:"name"`
			Arguments Args   `json:"arguments"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return nil, -32602, "invalid params"
		}
		t := s.byName[p.Name]
		if t == nil {
			return nil, -32602, "unknown tool: " + p.Name
		}
		return s.call(ctx, t, p.Arguments), 0, ""
	}
	return nil, -32601, "method not found: " + m.Method
}

func toolResult(text string, isErr bool) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isErr}
}

func cacheKey(name string, a Args) string {
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(name)
	for _, k := range keys {
		v, _ := json.Marshal(a[k])
		fmt.Fprintf(&b, "\x00%s=%s", k, strings.ToLower(string(v)))
	}
	return b.String()
}

func (s *Server) call(ctx context.Context, t *Tool, a Args) map[string]any {
	if a == nil {
		a = Args{}
	}
	start := time.Now()
	key := cacheKey(t.Name, a)
	if t.CacheFor > 0 {
		s.mu.Lock()
		c, ok := s.cache[key]
		s.mu.Unlock()
		if ok && time.Now().Before(c.expires) {
			s.Logf("tool=%s outcome=cached ms=0", t.Name)
			return toolResult(c.text, false)
		}
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-time.After(5 * time.Second):
		return toolResult("Server busy, please retry in a moment.", true)
	}
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	v, err := t.Handler(ctx, a)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		var ue UserError
		if errors.As(err, &ue) {
			s.Logf("tool=%s outcome=user-error ms=%d", t.Name, ms)
			return toolResult(ue.Msg, true)
		}
		s.Logf("tool=%s outcome=error ms=%d err=%v", t.Name, ms, err)
		return toolResult("Lookup failed: "+err.Error(), true)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return toolResult("internal error", true)
	}
	text := string(out)
	if t.CacheFor > 0 {
		s.mu.Lock()
		if len(s.cache) > 2000 {
			s.cache = map[string]cached{}
		}
		s.cache[key] = cached{text, time.Now().Add(t.CacheFor)}
		s.mu.Unlock()
	}
	s.Logf("tool=%s outcome=ok ms=%d", t.Name, ms)
	return toolResult(text, false)
}

// ---- argument helpers ----

// String returns a trimmed string argument ("" if absent).
func (a Args) String(k string) string {
	v, _ := a[k].(string)
	return strings.TrimSpace(v)
}

// Float returns a numeric argument; numbers sent as strings are accepted.
func (a Args) Float(k string) (float64, bool) {
	switch v := a[k].(type) {
	case float64:
		return v, true
	case string:
		var f float64
		if _, err := fmt.Sscan(strings.TrimSpace(v), &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

// Int returns an integer argument clamped to [lo, hi], def if absent.
func (a Args) Int(k string, def, lo, hi int) int {
	f, ok := a.Float(k)
	if !ok {
		return def
	}
	n := int(f)
	return max(lo, min(hi, n))
}

// Bool returns a boolean argument.
func (a Args) Bool(k string) bool {
	v, _ := a[k].(bool)
	return v
}

// Schema builds an object schema from properties and required names.
func Schema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// Prop is a schema property.
func Prop(typ, desc string) map[string]any { return map[string]any{"type": typ, "description": desc} }
