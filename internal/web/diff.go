package web

import "strings"

// computeDiff returns the non-empty lines added in newText and removed from
// oldText, as a line set (order-preserving, duplicates collapsed). It is a
// simple, readable diff suited to zone files rather than a full LCS diff.
func computeDiff(oldText, newText string) (added, removed []string) {
	oldSet := lineSet(oldText)
	newSet := lineSet(newText)
	for _, l := range orderedLines(newText) {
		if !oldSet[l] {
			added = append(added, l)
		}
	}
	for _, l := range orderedLines(oldText) {
		if !newSet[l] {
			removed = append(removed, l)
		}
	}
	return added, removed
}

func lineSet(text string) map[string]bool {
	set := map[string]bool{}
	for _, l := range orderedLines(text) {
		set[l] = true
	}
	return set
}

func orderedLines(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(text, "\n") {
		l := strings.TrimSpace(raw)
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}
