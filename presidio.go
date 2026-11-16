package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Finding is one entity from Presidio's analyzer. Start and End are offsets in
// Unicode code points (Python string indices), not bytes.
type Finding struct {
	Entity string  `json:"entity_type"`
	Start  int     `json:"start"`
	End    int     `json:"end"`
	Score  float64 `json:"score"`
}

type analyzeRequest struct {
	Text           []string `json:"text"`
	Language       string   `json:"language"`
	Entities       []string `json:"entities,omitempty"`
	ScoreThreshold float64  `json:"score_threshold"`
}

// Analyzer calls the presidio-analyzer REST server.
type Analyzer struct {
	URL       string
	Language  string
	Entities  []string
	Threshold float64
	Client    *http.Client
}

// Analyze sends every text in one batch call and returns the findings per text.
// Empty texts are not sent.
func (a *Analyzer) Analyze(ctx context.Context, texts []string) ([][]Finding, error) {
	out := make([][]Finding, len(texts))
	var batch []string
	var index []int
	for i, t := range texts {
		if t != "" {
			batch = append(batch, t)
			index = append(index, i)
		}
	}
	if len(batch) == 0 {
		return out, nil
	}
	body, err := json.Marshal(analyzeRequest{
		Text: batch, Language: a.Language, Entities: a.Entities, ScoreThreshold: a.Threshold,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.URL+"/analyze", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("presidio analyzer: %s: %s", resp.Status, bytes.TrimSpace(raw))
	}
	var results [][]Finding
	if err := json.Unmarshal(raw, &results); err != nil {
		return nil, fmt.Errorf("presidio analyzer: %w", err)
	}
	if len(results) != len(batch) {
		return nil, fmt.Errorf("presidio analyzer: %d results for %d texts", len(results), len(batch))
	}
	for j, r := range results {
		out[index[j]] = r
	}
	return out, nil
}
