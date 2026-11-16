package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// batches records the text batches the fake analyzer receives.
type batches struct {
	mu  sync.Mutex
	all [][]string
}

func (b *batches) add(texts []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.all = append(b.all, texts)
}

func (b *batches) take() [][]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.all
	b.all = nil
	return out
}

func cachedGuard(analyzerURL string) *Guard {
	g := guardFor(analyzerURL)
	g.Analyzer.Entities = []string{"PERSON", "CREDIT_CARD"}
	g.Cache = NewCache(1000, time.Hour)
	return g
}

// call posts body to h and returns the response body.
func call(t *testing.T, h http.HandlerFunc, body string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	return rec.Body.String()
}

func conversation(contents ...string) string {
	msgs := make([]Message, len(contents))
	for i, c := range contents {
		role := "user"
		if i == 0 {
			role = "system"
		} else if i%2 == 0 {
			role = "assistant"
		}
		msgs[i] = Message{Role: role, Content: c}
	}
	b, _ := json.Marshal(map[string]any{"body": map[string]any{"messages": msgs}})
	return string(b)
}

func TestCacheMissHitPartial(t *testing.T) {
	rec := &batches{}
	an := fakeAnalyzerRec(t, rec)
	defer an.Close()
	g := cachedGuard(an.URL)

	turn1 := conversation("be nice", "I am Ann")
	cold := call(t, g.handleRequest, turn1)
	if b := rec.take(); len(b) != 1 || len(b[0]) != 2 {
		t.Fatalf("turn 1 cold batches: %v", b)
	}

	warm := call(t, g.handleRequest, turn1)
	if b := rec.take(); len(b) != 0 {
		t.Fatalf("analyzer called on a full hit: %v", b)
	}
	if warm != cold {
		t.Fatalf("warm body %s != cold body %s", warm, cold)
	}

	// Turn 2 resends turn 1 and adds two messages: only those are analyzed.
	turn2 := call(t, g.handleRequest, conversation("be nice", "I am Ann", "Hi <PERSON>", "my friend is Ann too"))
	if b := rec.take(); len(b) != 1 || strings.Join(b[0], "|") != "Hi <PERSON>|my friend is Ann too" {
		t.Fatalf("turn 2 batches: %v", b)
	}
	if !strings.Contains(turn2, "my friend is \\u003cPERSON\\u003e too") {
		t.Fatalf("turn 2 body: %s", turn2)
	}
}

func TestCacheStats(t *testing.T) {
	an := fakeAnalyzer(t)
	defer an.Close()
	g := cachedGuard(an.URL)
	texts := []string{"", "I am Ann", "ok", "I am Ann"}
	for _, want := range []Stats{{Hits: 0, Misses: 2}, {Hits: 2, Misses: 0}} {
		if _, st, err := g.analyze(context.Background(), texts); err != nil || st != want {
			t.Fatalf("stats %+v (err %v), want %+v", st, err, want)
		}
	}
}

func TestCacheDuplicateTexts(t *testing.T) {
	rec := &batches{}
	an := fakeAnalyzerRec(t, rec)
	defer an.Close()
	g := cachedGuard(an.URL)

	body := call(t, g.handleRequest, conversation("", "I am Ann", "ok", "I am Ann", "ok"))
	if b := rec.take(); len(b) != 1 || len(b[0]) != 2 {
		t.Fatalf("batches %v", b)
	}
	if got := strings.Count(body, "I am \\u003cPERSON\\u003e"); got != 2 {
		t.Fatalf("both copies should be masked: %s", body)
	}
}

// noCache returns the guard's output without a cache for comparison.
func noCache(t *testing.T, analyzerURL string, h func(*Guard) http.HandlerFunc, body string) string {
	g := guardFor(analyzerURL)
	g.Analyzer.Entities = []string{"PERSON", "CREDIT_CARD"}
	return call(t, h(g), body)
}

var bodies = []string{
	conversation("", "hello"),
	conversation("be nice", "I am Ann"),
	conversation("", "Ann, card 4111 1111 1111 1111"),
	conversation("José", "I am Ann", "Hi Ann", "I am Ann", "José again, Ann"),
}

func reqHandler(g *Guard) http.HandlerFunc  { return g.handleRequest }
func respHandler(g *Guard) http.HandlerFunc { return g.handleResponse }

const responseBody = `{"body":{"choices":[{"message":{"role":"assistant","content":"card 4111 1111 1111 1111 for Ann"}},{"message":{"role":"assistant","content":"nothing"}}]}}`

func TestMaskedOutputSameWithAndWithoutCache(t *testing.T) {
	rec := &batches{}
	an := fakeAnalyzerRec(t, rec)
	defer an.Close()
	g := cachedGuard(an.URL)
	for _, body := range bodies {
		want := noCache(t, an.URL, reqHandler, body)
		for _, pass := range []string{"cold", "warm"} {
			if got := call(t, g.handleRequest, body); got != want {
				t.Fatalf("%s: got %s, want %s", pass, got, want)
			}
		}
	}
	want := noCache(t, an.URL, respHandler, responseBody)
	for _, pass := range []string{"cold", "warm"} {
		rec.take()
		if got := call(t, g.handleResponse, responseBody); got != want {
			t.Fatalf("response %s: got %s, want %s", pass, got, want)
		}
		if b := rec.take(); pass == "warm" && len(b) != 0 {
			t.Fatalf("response warm: analyzer called with %v", b)
		}
	}
}

func TestCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c := NewCache(2, time.Hour)
	f := func(e string) [][]Finding { return [][]Finding{{{Entity: e, Start: 0, End: 1, Score: 1}}} }
	c.Set([]string{"a"}, f("A"))
	c.Set([]string{"b"}, f("B"))
	c.Get([]string{"a"}) // a is now more recent than b
	c.Set([]string{"c"}, f("C"))
	_, found := c.Get([]string{"a", "b", "c"})
	if !found[0] || found[1] || !found[2] {
		t.Fatalf("found %v, want a and c", found)
	}
	if c.Len() != 2 {
		t.Fatalf("len %d", c.Len())
	}
}

func TestCacheExpires(t *testing.T) {
	now := time.Unix(1000, 0)
	c := NewCache(10, time.Minute)
	c.now = func() time.Time { return now }
	c.Set([]string{"a"}, [][]Finding{{}})
	if _, found := c.Get([]string{"a"}); !found[0] {
		t.Fatal("fresh entry missed")
	}
	now = now.Add(2 * time.Minute)
	if _, found := c.Get([]string{"a"}); found[0] {
		t.Fatal("expired entry hit")
	}
	if c.Len() != 0 {
		t.Fatalf("expired entry kept: len %d", c.Len())
	}
}

func TestCacheConcurrent(t *testing.T) {
	an := fakeAnalyzer(t)
	defer an.Close()
	g := cachedGuard(an.URL)
	g.Cache = NewCache(3, time.Hour) // small, so evictions race with lookups
	want := map[string]string{}
	for _, b := range bodies {
		want[b] = noCache(t, an.URL, reqHandler, b)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				b := bodies[(i+j)%len(bodies)]
				rec := httptest.NewRecorder()
				g.handleRequest(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(b)))
				if rec.Body.String() != want[b] {
					t.Errorf("got %s, want %s", rec.Body.String(), want[b])
					return
				}
			}
		}(i)
	}
	wg.Wait()
}
