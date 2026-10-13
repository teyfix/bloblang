package help

import (
	"encoding/json"
	"fmt"
	"runtime/debug"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/redpanda-data/benthos/v4/public/bloblang"
)

type searchField struct {
	name, text string
	weight     int
	terms      map[string]string
}

type entry struct {
	doc    Documentation
	fields []searchField
}

// Index is an immutable snapshot of a Bloblang registry and is safe to share
// between requests. It does not call plugin constructors or execute examples.
type Index struct {
	entries []entry
	byID    map[string]int
	runtime RuntimeVersions
}

// NewIndex reads the installed registry directly, including parameter and
// category documentation. Hidden entries are always excluded. Deprecated
// entries are indexed but require explicit inclusion on each request.
func NewIndex(env *bloblang.Environment) *Index {
	idx := &Index{byID: map[string]int{}, runtime: runtimeVersions()}
	env.WalkFunctions(func(_ string, view *bloblang.FunctionView) {
		d := view.TemplateData()
		doc := documentation("function", d.Name, d.Description, d.Status, d.Version, d.Params, d.Examples)
		if d.Category != "" {
			doc.Categories = append(doc.Categories, Category{Name: d.Category, Examples: []Example{}})
		}
		idx.add(doc)
	})
	env.WalkMethods(func(_ string, view *bloblang.MethodView) {
		d := view.TemplateData()
		doc := documentation("method", d.Name, d.Description, d.Status, d.Version, d.Params, d.Examples)
		for _, c := range d.Categories {
			doc.Categories = append(doc.Categories, Category{Name: c.Category, Description: c.Description, Examples: examples(c.Examples)})
		}
		idx.add(doc)
	})
	sort.Slice(idx.entries, func(a, b int) bool { return idx.entries[a].doc.ID < idx.entries[b].doc.ID })
	for i := range idx.entries {
		idx.byID[idx.entries[i].doc.ID] = i
	}
	return idx
}

func documentation(kind, name, description, status, since string, params bloblang.TemplateParamsData, ex []bloblang.TemplateExampleData) Documentation {
	d := Documentation{ID: kind + ":" + name, Kind: kind, Name: name, Description: description, Status: status, Since: since,
		Variadic: params.Variadic, Parameters: []Parameter{}, Examples: examples(ex), Categories: []Category{}}
	var signature []string
	for _, p := range params.Definitions {
		optional := p.IsOptional || p.DefaultMarshalled != ""
		d.Parameters = append(d.Parameters, Parameter{Name: p.Name, Type: p.ValueType, Description: p.Description, Optional: optional, Default: p.DefaultMarshalled})
		label := p.Name
		if optional {
			label += "?"
		}
		label += ": " + p.ValueType
		if p.DefaultMarshalled != "" {
			label += " = " + p.DefaultMarshalled
		}
		signature = append(signature, label)
	}
	if params.Variadic {
		signature = append(signature, "...")
	}
	d.Signature = name + "(" + strings.Join(signature, ", ") + ")"
	return d
}

func examples(source []bloblang.TemplateExampleData) []Example {
	out := make([]Example, 0, len(source))
	for _, e := range source {
		x := Example{Summary: e.Summary, Mapping: e.Mapping, SkipTesting: e.SkipTesting, Results: []ExampleResult{}}
		for _, r := range e.Results {
			x.Results = append(x.Results, ExampleResult{Input: r[0], Output: r[1]})
		}
		out = append(out, x)
	}
	return out
}

func (idx *Index) add(doc Documentation) {
	if doc.Status == "hidden" {
		return
	}
	e := entry{doc: doc}
	add := func(name, text string, weight int) {
		if text != "" {
			e.fields = append(e.fields, searchField{name: name, text: text, weight: weight, terms: tokenMap(text)})
		}
	}
	add("name", doc.Name, 80)
	add("signature", doc.Signature, 40)
	add("description", doc.Description, 25)
	for _, p := range doc.Parameters {
		add("parameters", p.Name+": "+p.Description, 15)
	}
	addExamples := func(ex []Example) {
		for _, x := range ex {
			add("examples", x.Summary+"\n"+x.Mapping, 5)
		}
	}
	addExamples(doc.Examples)
	for _, c := range doc.Categories {
		add("categories", c.Name+"\n"+c.Description, 10)
		addExamples(c.Examples)
	}
	idx.entries = append(idx.entries, e)
}

func runtimeVersions() RuntimeVersions {
	v := RuntimeVersions{Benthos: "unknown", Connect: "unknown"}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			version := dep.Version
			if dep.Replace != nil {
				version = dep.Replace.Version
				if version == "" {
					version = "local replacement"
				}
			}
			switch dep.Path {
			case "github.com/redpanda-data/benthos/v4":
				v.Benthos = version
			case "github.com/redpanda-data/connect/v4":
				v.Connect = version
			}
		}
	}
	return v
}

func (idx *Index) meta() Meta {
	return Meta{Capabilities: Capabilities{Lexical: true, FuzzyNames: true, Batch: true}, Runtime: idx.runtime, Queries: []QueryMeta{}}
}

func validIDs(ids []string, maximum int) error {
	if len(ids) == 0 || len(ids) > maximum {
		return fmt.Errorf("expected 1 to %d IDs", maximum)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || len(id) > MaxIDBytes || !utf8.ValidString(id) {
			return fmt.Errorf("IDs must be valid UTF-8, nonempty and at most %d bytes", MaxIDBytes)
		}
		if seen[id] {
			return fmt.Errorf("duplicate ID %q", id)
		}
		seen[id] = true
	}
	return nil
}

// Search ranks each query independently, then deduplicates the union by ID.
// Results follow first appearance in query/rank order. Invalid query text is a
// per-query error; ambiguous IDs and invalid batch limits reject the request.
func (idx *Index) Search(req SearchRequest) (SearchResponse, error) {
	response := SearchResponse{Results: []SearchResult{}, Meta: idx.meta()}
	ids := make([]string, len(req.Queries))
	for i, q := range req.Queries {
		ids[i] = q.ID
	}
	if err := validIDs(ids, MaxQueries); err != nil {
		return response, err
	}
	limit := req.LimitPerQuery
	if limit == 0 {
		limit = DefaultLimit
	}
	if limit < 1 || limit > MaxLimitPerQuery {
		return response, fmt.Errorf("limitPerQuery must be between 1 and %d", MaxLimitPerQuery)
	}
	positions := map[string]int{}
	// Reserve ample room for bounded per-query metadata and JSON delimiters.
	budget := MaxResponseBytes - 16*1024
	for _, q := range req.Queries {
		meta := QueryMeta{ID: q.ID, ResultIDs: []string{}}
		if strings.TrimSpace(q.Text) == "" || len(q.Text) > MaxQueryBytes || !utf8.ValidString(q.Text) {
			meta.Error = &QueryError{Code: "invalid_query", Message: fmt.Sprintf("text must be valid UTF-8, nonempty and at most %d bytes", MaxQueryBytes)}
			response.Meta.Queries = append(response.Meta.Queries, meta)
			continue
		}
		hits := idx.rank(q.Text, req.IncludeDeprecated)
		meta.HasMore = len(hits) > limit
		for rank, hit := range hits[:min(len(hits), limit)] {
			doc := idx.entries[hit.index].doc
			match := Match{QueryID: q.ID, Rank: rank + 1, Evidence: hit.evidence}
			position, exists := positions[doc.ID]
			result := SearchResult{ID: doc.ID, Kind: doc.Kind, Name: doc.Name, Signature: clip(doc.Signature, 512),
				Summary: clip(strings.SplitN(doc.Description, "\n", 2)[0], 320), Status: doc.Status, Matches: []Match{match}}
			var cost []byte
			if exists {
				cost, _ = json.Marshal(match)
			} else {
				cost, _ = json.Marshal(result)
			}
			if len(cost)+1 > budget {
				meta.HasMore = true
				response.Meta.Truncated = true
				break
			}
			budget -= len(cost) + 1
			if exists {
				response.Results[position].Matches = append(response.Results[position].Matches, match)
			} else {
				positions[doc.ID] = len(response.Results)
				response.Results = append(response.Results, result)
			}
			meta.ResultIDs = append(meta.ResultIDs, doc.ID)
		}
		response.Meta.Queries = append(response.Meta.Queries, meta)
	}
	return response, nil
}

// Get resolves exact canonical IDs and returns complete documentation, without
// executing the examples. Missing IDs and documents that exceed the remaining
// response budget are reported individually. Returned documents are copies.
func (idx *Index) Get(req GetRequest) (GetResponse, error) {
	response := GetResponse{Results: []Documentation{}, Meta: idx.meta()}
	if err := validIDs(req.IDs, MaxDocumentIDs); err != nil {
		return response, err
	}
	budget := MaxResponseBytes - 16*1024
	for _, id := range req.IDs {
		meta := QueryMeta{ID: id, ResultIDs: []string{}}
		position, exists := idx.byID[id]
		if !exists || !req.IncludeDeprecated && idx.entries[position].doc.Status == "deprecated" {
			meta.Error = &QueryError{Code: "not_found", Message: "documentation ID not found (deprecated entries require includeDeprecated)"}
		} else {
			encoded, err := json.Marshal(idx.entries[position].doc)
			if err != nil {
				return response, fmt.Errorf("encode documentation %s: %w", id, err)
			}
			if len(encoded)+1 > budget {
				meta.Error = &QueryError{Code: "response_limit", Message: "complete documentation exceeds the remaining response budget; request this ID separately"}
				response.Meta.Truncated = true
			} else {
				var doc Documentation
				if err := json.Unmarshal(encoded, &doc); err != nil {
					return response, err
				}
				budget -= len(encoded) + 1
				response.Results = append(response.Results, doc)
				meta.ResultIDs = append(meta.ResultIDs, id)
			}
		}
		response.Meta.Queries = append(response.Meta.Queries, meta)
	}
	return response, nil
}

func clip(text string, bytes int) string {
	if len(text) <= bytes {
		return text
	}
	end := max(0, bytes-len("…"))
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + "…"
}
