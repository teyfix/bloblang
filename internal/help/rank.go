package help

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type ranked struct {
	index    int
	score    int
	evidence []Evidence
}

func normalizeWord(s string) string {
	s = strings.ToLower(s)
	// A small plural normalizer makes "fields" and "field" match without
	// applying an English stemmer to code identifiers.
	if len(s) > 3 && strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss") {
		s = strings.TrimSuffix(s, "s")
	}
	return s
}

func words(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

func tokenMap(text string) map[string]string {
	terms := map[string]string{}
	for _, word := range words(text) {
		key := normalizeWord(word)
		if _, exists := terms[key]; !exists {
			terms[key] = word
		}
	}
	return terms
}

func queryTerms(text string) []string {
	seen := map[string]bool{}
	terms := []string{}
	for _, word := range words(text) {
		key := normalizeWord(word)
		switch key {
		case "a", "an", "the", "to", "of", "and", "or", "in", "on", "for", "with", "from", "how", "do", "i":
			continue
		}
		if !seen[key] {
			seen[key] = true
			terms = append(terms, word)
		}
	}
	return terms
}

func (idx *Index) rank(text string, deprecated bool) []ranked {
	name := strings.ToLower(strings.TrimSpace(text))
	name = strings.TrimPrefix(strings.TrimSuffix(name, "()"), ".")
	terms := queryTerms(text)
	hits := []ranked{}
	for i := range idx.entries {
		e := &idx.entries[i]
		if e.doc.Status == "deprecated" && !deprecated {
			continue
		}
		hit := ranked{index: i, evidence: []Evidence{}}
		nameEvidence := Evidence{Field: "name", QueryTerms: []string{strings.TrimSpace(text)}, MatchedTerms: []string{e.doc.Name}, Excerpt: e.doc.Name}
		switch {
		case name == e.doc.Name || name == e.doc.ID:
			hit.score = 1000000
			nameEvidence.MatchType = "exact"
		case len(name) >= 2 && strings.HasPrefix(e.doc.Name, name):
			hit.score = 900000
			nameEvidence.MatchType = "prefix"
		default:
			// Fuzzy matching is deliberately limited to a single identifier.
			// Do not apply edit distance to arbitrary documentation prose.
			if len(name) >= 4 && isIdentifier(name) {
				limit := 1
				if len(name) >= 7 {
					limit = 2
				}
				if distance := editDistance(name, e.doc.Name, limit); distance <= limit {
					hit.score = 800000 - 1000*distance
					nameEvidence.MatchType = "fuzzy"
					nameEvidence.EditDistance = &distance
				}
			}
		}
		if nameEvidence.MatchType != "" {
			hit.evidence = append(hit.evidence, nameEvidence)
		}
		type fieldMatch struct {
			evidence Evidence
			weight   int
		}
		fields := []fieldMatch{}
		coverage := map[string]bool{}
		// Match presence, not term frequency: repeating a word in a large
		// example must not outrank a concise relevant description.
		bestWeights := map[string]int{}
		for _, field := range e.fields {
			evidence := Evidence{Field: field.name, MatchType: "token", QueryTerms: []string{}, MatchedTerms: []string{}}
			for _, term := range terms {
				key := normalizeWord(term)
				if actual, ok := field.terms[key]; ok {
					coverage[key] = true
					bestWeights[key] = max(bestWeights[key], field.weight)
					evidence.QueryTerms = append(evidence.QueryTerms, term)
					evidence.MatchedTerms = append(evidence.MatchedTerms, actual)
				}
			}
			if len(evidence.MatchedTerms) > 0 {
				evidence.Excerpt = excerpt(field.text, evidence.MatchedTerms, 240)
				fields = append(fields, fieldMatch{evidence: evidence, weight: field.weight * len(evidence.MatchedTerms)})
			}
		}
		hit.score += len(coverage) * 100
		for _, weight := range bestWeights {
			hit.score += weight
		}
		sort.SliceStable(fields, func(a, b int) bool { return fields[a].weight > fields[b].weight })
		for _, field := range fields {
			if len(hit.evidence) >= 3 {
				break
			}
			if field.evidence.Field == "name" && nameEvidence.MatchType != "" {
				continue
			}
			hit.evidence = append(hit.evidence, field.evidence)
		}
		if hit.score > 0 {
			hits = append(hits, hit)
		}
	}
	sort.Slice(hits, func(a, b int) bool {
		if hits[a].score == hits[b].score {
			return idx.entries[hits[a].index].doc.ID < idx.entries[hits[b].index].doc.ID
		}
		return hits[a].score > hits[b].score
	})
	return hits
}

func isIdentifier(s string) bool {
	for _, r := range s {
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsNumber(r) {
			return false
		}
	}
	return true
}

func excerpt(text string, terms []string, maximum int) string {
	// Terms retain their original spelling, so byte offsets are valid even
	// when Unicode case-folding changes a character's encoded length.
	position := len(text)
	for _, term := range terms {
		if i := strings.Index(text, term); i >= 0 {
			position = min(position, i)
		}
	}
	if position > maximum/2 && position < len(text) {
		start := position - maximum/3
		for start > 0 && !utf8.RuneStart(text[start]) {
			start--
		}
		return "…" + clip(strings.TrimSpace(text[start:]), maximum-3)
	}
	return clip(text, maximum)
}

// editDistance computes restricted Damerau-Levenshtein distance, including
// adjacent transpositions such as uppercaes -> uppercase. Inputs are bounded
// by the request limit and registry identifier length.
func editDistance(a, b string, maximum int) int {
	x, y := []rune(a), []rune(b)
	if len(x)-len(y) > maximum || len(y)-len(x) > maximum {
		return maximum + 1
	}
	previous := make([]int, len(y)+1)
	previousPrevious := make([]int, len(y)+1)
	current := make([]int, len(y)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(x); i++ {
		current[0] = i
		for j := 1; j <= len(y); j++ {
			cost := 0
			if x[i-1] != y[j-1] {
				cost = 1
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
			if i > 1 && j > 1 && x[i-1] == y[j-2] && x[i-2] == y[j-1] {
				current[j] = min(current[j], previousPrevious[j-2]+1)
			}
		}
		previousPrevious, previous, current = previous, current, previousPrevious
	}
	return previous[len(y)]
}
