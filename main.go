// presidio-guard implements agentgateway's Guardrail Webhook API with
// Presidio: agentgateway posts the prompt messages to /request before the LLM
// sees them and the completions to /response before the client does. Each
// text is analyzed by a presidio-analyzer server; PII is masked, or the
// request is rejected when it holds an entity listed in REJECT_ENTITIES.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

type Guard struct {
	Analyzer     *Analyzer
	Cache        *Cache // nil: no cache, every text is analyzed
	Reject       map[string]bool
	RejectStatus int
}

// Stats of one call: cache hits and misses, counted over distinct non-empty
// texts. Both stay 0 without a cache.
type Stats struct {
	Hits, Misses int
}

// analyze returns the findings per text. Without a cache every text goes to
// the analyzer. With one, identical texts are looked up once, and only the
// misses are analyzed, in one batch, and written back.
func (g *Guard) analyze(ctx context.Context, texts []string) ([][]Finding, Stats, error) {
	var st Stats
	if g.Cache == nil {
		findings, err := g.Analyzer.Analyze(ctx, texts)
		return findings, st, err
	}
	index := map[string]int{}
	var uniq []string
	for _, t := range texts {
		if _, seen := index[t]; t != "" && !seen {
			index[t] = len(uniq)
			uniq = append(uniq, t)
		}
	}
	found, ok := g.Cache.Get(uniq)
	var miss []string
	var missAt []int
	for i, t := range uniq {
		if ok[i] {
			st.Hits++
		} else {
			miss = append(miss, t)
			missAt = append(missAt, i)
		}
	}
	st.Misses = len(miss)
	if len(miss) > 0 {
		analyzed, err := g.Analyzer.Analyze(ctx, miss)
		if err != nil {
			return nil, st, err
		}
		for j, f := range analyzed {
			found[missAt[j]] = f
		}
		g.Cache.Set(miss, analyzed)
	}
	out := make([][]Finding, len(texts))
	for i, t := range texts {
		if t != "" {
			out[i] = found[index[t]]
		}
	}
	return out, st, nil
}

// check analyzes texts and returns them masked, with the entity counts and
// the entities that call for a rejection.
func (g *Guard) check(ctx context.Context, texts []string) ([]string, map[string]int, []string, Stats, error) {
	findings, st, err := g.analyze(ctx, texts)
	if err != nil {
		return nil, nil, nil, st, err
	}
	masked := make([]string, len(texts))
	counts := map[string]int{}
	blocked := map[string]bool{}
	for i, t := range texts {
		masked[i] = Mask(t, findings[i])
		for _, f := range findings[i] {
			counts[f.Entity]++
			if g.Reject[f.Entity] {
				blocked[f.Entity] = true
			}
		}
	}
	return masked, counts, sortedKeys(blocked), st, nil
}

func (g *Guard) handleRequest(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req PromptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	msgs := req.Body.Messages
	texts := make([]string, len(msgs))
	for i, m := range msgs {
		texts[i] = m.Content
	}
	masked, counts, blocked, st, err := g.check(r.Context(), texts)
	if err != nil {
		slog.Error("request", "err", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	reason := summary(counts)
	var action any = PassAction{}
	switch {
	case len(blocked) > 0:
		action = RejectAction{
			Body:       "Request blocked: it contains " + strings.Join(blocked, ", "),
			StatusCode: g.RejectStatus,
			Reason:     reason,
		}
	case len(counts) > 0:
		out := make([]Message, len(msgs))
		for i, m := range msgs {
			out[i] = Message{Role: m.Role, Content: masked[i]}
		}
		action = MaskAction{Body: map[string]any{"messages": out}, Reason: reason}
	}
	slog.Info("request", "messages", len(msgs), "found", reason, "blocked", blocked,
		"cache_hits", st.Hits, "cache_misses", st.Misses, "ms", ms(start))
	writeJSON(w, ActionResponse{Action: action})
}

func (g *Guard) handleResponse(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req ResponseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	choices := req.Body.Choices
	texts := make([]string, len(choices))
	for i, c := range choices {
		texts[i] = c.Message.Content
	}
	masked, counts, _, st, err := g.check(r.Context(), texts)
	if err != nil {
		slog.Error("response", "err", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	reason := summary(counts)
	var action any = PassAction{}
	if len(counts) > 0 {
		out := make([]ResponseChoice, len(choices))
		for i, c := range choices {
			out[i] = ResponseChoice{Message: Message{Role: c.Message.Role, Content: masked[i]}}
		}
		action = MaskAction{Body: map[string]any{"choices": out}, Reason: reason}
	}
	slog.Info("response", "choices", len(choices), "found", reason,
		"cache_hits", st.Hits, "cache_misses", st.Misses, "ms", ms(start))
	writeJSON(w, ActionResponse{Action: action})
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	timeout := envDuration("PRESIDIO_TIMEOUT", 10*time.Second)
	g := &Guard{
		Analyzer: &Analyzer{
			URL:       strings.TrimSuffix(env("PRESIDIO_URL", "http://127.0.0.1:3000"), "/"),
			Language:  env("LANGUAGE", "en"),
			Entities:  envList("ENTITIES"),
			Threshold: envFloat("SCORE_THRESHOLD", 0.5),
			Client: &http.Client{
				Timeout: timeout,
				Transport: &http.Transport{
					MaxIdleConns:        256,
					MaxIdleConnsPerHost: 256,
					IdleConnTimeout:     90 * time.Second,
				},
			},
		},
		Reject:       map[string]bool{},
		RejectStatus: envInt("REJECT_STATUS", http.StatusForbidden),
	}
	for _, e := range envList("REJECT_ENTITIES") {
		g.Reject[e] = true
	}
	if size := envInt("CACHE_SIZE", 50000); size > 0 {
		ttl := envDuration("CACHE_TTL", time.Hour)
		g.Cache = NewCache(size, ttl)
		slog.Info("cache", "size", size, "ttl", ttl.String())
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /request", g.handleRequest)
	mux.HandleFunc("POST /response", g.handleResponse)
	// Ready once the analyzer has answered once. After that, don't ask it
	// again: under load its /health waits in the same queue as the analyze
	// calls, a slow probe takes the pod out of the Service, and agentgateway
	// then fails every request closed ("no healthy backends").
	var ready atomic.Bool
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if ready.Load() {
			fmt.Fprintln(w, "ok")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, g.Analyzer.URL+"/health", nil)
		resp, err := g.Analyzer.Client.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			http.Error(w, "presidio analyzer: "+resp.Status, http.StatusServiceUnavailable)
			return
		}
		ready.Store(true)
		fmt.Fprintln(w, "ok")
	})

	addr := env("LISTEN", ":8000")
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	slog.Info("listening", "addr", addr, "presidio", g.Analyzer.URL,
		"entities", g.Analyzer.Entities, "reject", sortedKeys(g.Reject))
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func summary(counts map[string]int) string {
	parts := make([]string, 0, len(counts))
	for _, k := range sortedKeys(counts) {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	return strings.Join(parts, ", ")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func ms(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envList(key string) []string {
	var out []string
	for _, v := range strings.Split(os.Getenv(key), ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func envFloat(key string, def float64) float64 {
	if f, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil {
		return f
	}
	return def
}

func envInt(key string, def int) int {
	if i, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return i
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(key)); err == nil {
		return d
	}
	return def
}
