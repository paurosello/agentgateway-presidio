package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMaskRunesAndOverlaps(t *testing.T) {
	// "José" has a multi-byte rune: offsets after it are code points.
	text := "José at jose@example.com"
	got := Mask(text, []Finding{
		{Entity: "PERSON", Start: 0, End: 4, Score: 0.85},
		{Entity: "EMAIL_ADDRESS", Start: 8, End: 24, Score: 1.0},
		{Entity: "URL", Start: 13, End: 24, Score: 0.5}, // inside the email
	})
	if want := "<PERSON> at <EMAIL_ADDRESS>"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestMaskDropsOutOfRange(t *testing.T) {
	if got := Mask("abc", []Finding{{Entity: "X", Start: 2, End: 9}}); got != "abc" {
		t.Fatalf("got %q", got)
	}
}

// fakeAnalyzer flags every "4111 1111 1111 1111" as CREDIT_CARD and "Ann" as PERSON.
func fakeAnalyzer(t *testing.T) *httptest.Server {
	return fakeAnalyzerRec(t, nil)
}

// fakeAnalyzerRec is fakeAnalyzer that also appends each batch it receives to rec.
func fakeAnalyzerRec(t *testing.T, rec *batches) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req analyzeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if rec != nil {
			rec.add(req.Text)
		}
		out := make([][]Finding, len(req.Text))
		for i, text := range req.Text {
			out[i] = []Finding{}
			for _, p := range []struct{ s, e string }{{"Ann", "PERSON"}, {"4111 1111 1111 1111", "CREDIT_CARD"}} {
				if j := strings.Index(text, p.s); j >= 0 {
					start := len([]rune(text[:j]))
					out[i] = append(out[i], Finding{Entity: p.e, Start: start, End: start + len([]rune(p.s)), Score: 0.9})
				}
			}
		}
		json.NewEncoder(w).Encode(out)
	}))
}

func guardFor(url string) *Guard {
	return &Guard{
		Analyzer:     &Analyzer{URL: url, Language: "en", Threshold: 0.5, Client: http.DefaultClient},
		Reject:       map[string]bool{"CREDIT_CARD": true},
		RejectStatus: 403,
	}
}

func post(t *testing.T, h http.HandlerFunc, body string) map[string]any {
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return out["action"].(map[string]any)
}

func TestRequestActions(t *testing.T) {
	srv := fakeAnalyzer(t)
	defer srv.Close()
	g := guardFor(srv.URL)

	pass := post(t, g.handleRequest, `{"body":{"messages":[{"role":"system","content":""},{"role":"user","content":"hello"}]}}`)
	if len(pass) != 0 {
		t.Fatalf("pass: %v", pass)
	}

	mask := post(t, g.handleRequest, `{"body":{"messages":[{"role":"system","content":"be nice"},{"role":"user","content":"I am Ann"}]}}`)
	msgs := mask["body"].(map[string]any)["messages"].([]any)
	if len(msgs) != 2 || msgs[1].(map[string]any)["content"] != "I am <PERSON>" {
		t.Fatalf("mask: %v", mask)
	}

	reject := post(t, g.handleRequest, `{"body":{"messages":[{"role":"user","content":"Ann, card 4111 1111 1111 1111"}]}}`)
	if reject["status_code"] != float64(403) || !strings.Contains(reject["body"].(string), "CREDIT_CARD") {
		t.Fatalf("reject: %v", reject)
	}
}

func TestResponseMasksInsteadOfRejecting(t *testing.T) {
	srv := fakeAnalyzer(t)
	defer srv.Close()
	g := guardFor(srv.URL)
	mask := post(t, g.handleResponse, `{"body":{"choices":[{"message":{"role":"assistant","content":"card 4111 1111 1111 1111"}}]}}`)
	choices := mask["body"].(map[string]any)["choices"].([]any)
	if got := choices[0].(map[string]any)["message"].(map[string]any)["content"]; got != "card <CREDIT_CARD>" {
		t.Fatalf("got %v", got)
	}
}
