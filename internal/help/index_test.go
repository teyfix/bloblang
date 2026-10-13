package help

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/redpanda-data/benthos/v4/public/bloblang"
	"github.com/stretchr/testify/require"
	"github.com/teyfix/bloblang-lsp/internal/benthos"
)

func fixtureIndex(docs ...Documentation) *Index {
	idx := &Index{byID: map[string]int{}}
	for _, doc := range docs {
		idx.add(doc)
	}
	for i, entry := range idx.entries {
		idx.byID[entry.doc.ID] = i
	}
	return idx
}

func fixture(name, description string) Documentation {
	return Documentation{ID: "method:" + name, Kind: "method", Name: name, Signature: name + "()", Description: description,
		Parameters: []Parameter{}, Examples: []Example{}, Categories: []Category{}}
}

func TestInstalledRegistrySearchAndGet(t *testing.T) {
	idx := NewIndex(benthos.NewEnvironment())
	response, err := idx.Search(SearchRequest{Queries: []Query{
		{ID: "exact", Text: ".uppercase()"},
		{ID: "typo", Text: "uppercaes"},
		{ID: "intent", Text: "remove object fields"},
	}})
	require.NoError(t, err)
	require.Equal(t, "method:uppercase", response.Meta.Queries[0].ResultIDs[0])
	require.Equal(t, "method:uppercase", response.Meta.Queries[1].ResultIDs[0])
	require.Contains(t, response.Meta.Queries[2].ResultIDs, "method:without")
	require.False(t, response.Meta.Capabilities.Semantic)
	require.True(t, response.Meta.Capabilities.FuzzyNames)
	uppercase := response.Results[0]
	require.Len(t, uppercase.Matches, 2, "the union must deduplicate results while preserving query-specific evidence")
	require.Equal(t, "exact", uppercase.Matches[0].Evidence[0].MatchType)
	require.Equal(t, "fuzzy", uppercase.Matches[1].Evidence[0].MatchType)
	require.Equal(t, 1, *uppercase.Matches[1].Evidence[0].EditDistance)

	docs, err := idx.Get(GetRequest{IDs: []string{"method:uppercase", "method:without", "method:does_not_exist"}})
	require.NoError(t, err)
	require.Len(t, docs.Results, 2)
	require.Equal(t, "uppercase()", docs.Results[0].Signature)
	require.NotEmpty(t, docs.Results[0].Description)
	require.NotEmpty(t, docs.Results[0].Examples)
	require.Equal(t, "not_found", docs.Meta.Queries[2].Error.Code)
}

func TestRankingFieldsPrefixAndDeterministicTies(t *testing.T) {
	alpha := fixture("alpha", "Extracts zebra fields from objects")
	beta := fixture("beta", "Extracts zebra fields from objects")
	gamma := fixture("gamma", "Unrelated description")
	gamma.Examples = []Example{{Summary: strings.Repeat("zebra fields objects ", 100)}}
	idx := fixtureIndex(beta, gamma, alpha, fixture("alphabet", "unrelated"))
	for range 10 {
		response, err := idx.Search(SearchRequest{Queries: []Query{{ID: "intent", Text: "zebra field object"}, {ID: "prefix", Text: "alph"}}, LimitPerQuery: 2})
		require.NoError(t, err)
		require.Equal(t, []string{"method:alpha", "method:beta"}, response.Meta.Queries[0].ResultIDs)
		require.True(t, response.Meta.Queries[0].HasMore)
		require.Equal(t, []string{"method:alpha", "method:alphabet"}, response.Meta.Queries[1].ResultIDs)
		require.Equal(t, "description", response.Results[0].Matches[0].Evidence[0].Field)
		require.Equal(t, "prefix", response.Results[0].Matches[1].Evidence[0].MatchType)
	}
}

func TestPartialQueryErrorsAndRequestValidation(t *testing.T) {
	idx := fixtureIndex(fixture("uppercase", "Converts string case"))
	response, err := idx.Search(SearchRequest{Queries: []Query{
		{ID: "empty", Text: "  "}, {ID: "large", Text: strings.Repeat("x", MaxQueryBytes+1)},
		{ID: "absent", Text: "unfindablezzzzzzzz"}, {ID: "valid", Text: "uppercase"},
	}})
	require.NoError(t, err)
	require.Len(t, response.Meta.Queries, 4)
	require.Equal(t, "invalid_query", response.Meta.Queries[0].Error.Code)
	require.Equal(t, "invalid_query", response.Meta.Queries[1].Error.Code)
	require.Nil(t, response.Meta.Queries[2].Error)
	require.Empty(t, response.Meta.Queries[2].ResultIDs)
	require.Equal(t, []string{"method:uppercase"}, response.Meta.Queries[3].ResultIDs)
	for _, req := range []SearchRequest{
		{},
		{Queries: []Query{{ID: "x", Text: "x"}, {ID: "x", Text: "y"}}},
		{Queries: []Query{{ID: "", Text: "x"}}},
		{Queries: []Query{{ID: "x", Text: "x"}}, LimitPerQuery: MaxLimitPerQuery + 1},
		{Queries: []Query{{ID: "x", Text: "x"}}, LimitPerQuery: -1},
		{Queries: make([]Query, MaxQueries+1)},
	} {
		_, err := idx.Search(req)
		require.Error(t, err)
	}
	_, err = idx.Get(GetRequest{IDs: []string{"method:uppercase", "method:uppercase"}})
	require.Error(t, err)
}

func TestVisibilityAndExactIDs(t *testing.T) {
	hidden := fixture("hidden", "secret")
	hidden.Status = "hidden"
	old := fixture("old", "obsolete")
	old.Status = "deprecated"
	idx := fixtureIndex(hidden, old, fixture("active", "current"))
	response, err := idx.Search(SearchRequest{Queries: []Query{{ID: "old", Text: "old"}, {ID: "hidden", Text: "hidden"}}})
	require.NoError(t, err)
	require.Empty(t, response.Results)
	response, err = idx.Search(SearchRequest{Queries: []Query{{ID: "old", Text: "old"}}, IncludeDeprecated: true})
	require.NoError(t, err)
	require.Equal(t, "method:old", response.Results[0].ID)
	docs, err := idx.Get(GetRequest{IDs: []string{"method:old", "method:hidden", "active"}, IncludeDeprecated: true})
	require.NoError(t, err)
	require.Len(t, docs.Results, 1)
	require.Equal(t, "not_found", docs.Meta.Queries[1].Error.Code)
	require.Equal(t, "not_found", docs.Meta.Queries[2].Error.Code, "get must require exact canonical IDs")
}

func TestCompleteDocumentationAndCopyIsolation(t *testing.T) {
	doc := documentation("function", "example", "full description", "stable", "4.0.0", bloblang.TemplateParamsData{
		Variadic: true,
		Definitions: []bloblang.TemplateParamData{{Name: "value", ValueType: "string", Description: "a value"},
			{Name: "mode", ValueType: "string", DefaultMarshalled: `"fast"`}},
	}, []bloblang.TemplateExampleData{{Mapping: "root = this", Results: [][2]string{{`{"a":1}`, `{"a":1}`}}}})
	doc.Categories = []Category{{Name: "objects", Description: "category description", Examples: []Example{{Summary: "category example"}}}}
	idx := fixtureIndex(doc)
	response, err := idx.Get(GetRequest{IDs: []string{"function:example"}})
	require.NoError(t, err)
	require.Equal(t, `example(value: string, mode?: string = "fast", ...)`, response.Results[0].Signature)
	require.True(t, response.Results[0].Parameters[1].Optional)
	require.Equal(t, `{"a":1}`, response.Results[0].Examples[0].Results[0].Output)
	response.Results[0].Parameters[0].Description = "mutated"
	response.Results[0].Examples[0].Results[0].Output = "mutated"
	response.Results[0].Categories[0].Examples[0].Summary = "mutated"
	again, err := idx.Get(GetRequest{IDs: []string{"function:example"}})
	require.NoError(t, err)
	require.Equal(t, doc, again.Results[0])
}

func TestResponseBudgetAndUnicodeEvidence(t *testing.T) {
	large := fixture("large", strings.Repeat("x", MaxResponseBytes))
	small := fixture("small", strings.Repeat("雪", 200)+" useful 例子")
	idx := fixtureIndex(large, small)
	docs, err := idx.Get(GetRequest{IDs: []string{"method:large", "method:small"}})
	require.NoError(t, err)
	require.Equal(t, "response_limit", docs.Meta.Queries[0].Error.Code)
	require.True(t, docs.Meta.Truncated)
	require.Len(t, docs.Results, 1, "one oversized document must not prevent another ID from resolving")
	encoded, err := json.Marshal(docs)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), MaxResponseBytes)
	response, err := idx.Search(SearchRequest{Queries: []Query{{ID: "unicode", Text: "useful"}}})
	require.NoError(t, err)
	evidence := response.Results[0].Matches[0].Evidence[0]
	require.True(t, utf8.ValidString(evidence.Excerpt))
	require.Contains(t, evidence.Excerpt, "useful")
	require.LessOrEqual(t, len(evidence.Excerpt), 240)
}
