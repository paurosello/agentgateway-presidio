package main

import (
	"sort"
	"strings"
)

// Mask replaces each finding in text with "<ENTITY_TYPE>". When findings
// overlap, the higher score wins, then the longer span. Offsets are code
// points, so the text is indexed as runes.
func Mask(text string, findings []Finding) string {
	if len(findings) == 0 {
		return text
	}
	runes := []rune(text)
	kept := resolveOverlaps(findings, len(runes))
	var b strings.Builder
	b.Grow(len(text))
	pos := 0
	for _, f := range kept {
		b.WriteString(string(runes[pos:f.Start]))
		b.WriteString("<" + f.Entity + ">")
		pos = f.End
	}
	b.WriteString(string(runes[pos:]))
	return b.String()
}

// resolveOverlaps drops invalid spans and every span that overlaps a better
// one, and returns the rest ordered by start.
func resolveOverlaps(findings []Finding, n int) []Finding {
	cand := make([]Finding, 0, len(findings))
	for _, f := range findings {
		if f.Start >= 0 && f.End <= n && f.Start < f.End {
			cand = append(cand, f)
		}
	}
	sort.SliceStable(cand, func(i, j int) bool {
		if cand[i].Score != cand[j].Score {
			return cand[i].Score > cand[j].Score
		}
		return cand[i].End-cand[i].Start > cand[j].End-cand[j].Start
	})
	var kept []Finding
	for _, f := range cand {
		overlaps := false
		for _, k := range kept {
			if f.Start < k.End && k.Start < f.End {
				overlaps = true
				break
			}
		}
		if !overlaps {
			kept = append(kept, f)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Start < kept[j].Start })
	return kept
}
