// Package help indexes the documentation of an installed Bloblang environment.
// It provides deterministic batched lexical search without executing mappings.
package help

const (
	MaxQueries       = 8
	MaxLimitPerQuery = 10
	MaxQueryBytes    = 256
	MaxIDBytes       = 64
	MaxDocumentIDs   = 16
	MaxResponseBytes = 512 * 1024
	DefaultLimit     = 5
)

type Query struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type SearchRequest struct {
	Queries           []Query `json:"queries"`
	LimitPerQuery     int     `json:"limitPerQuery,omitempty"`
	IncludeDeprecated bool    `json:"includeDeprecated,omitempty"`
}

type GetRequest struct {
	IDs               []string `json:"ids"`
	IncludeDeprecated bool     `json:"includeDeprecated,omitempty"`
}

type Capabilities struct {
	Lexical    bool `json:"lexical"`
	FuzzyNames bool `json:"fuzzyNames"`
	Batch      bool `json:"batch"`
	Semantic   bool `json:"semantic"`
}

type RuntimeVersions struct {
	Benthos string `json:"benthos"`
	Connect string `json:"connect"`
}

type QueryError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type QueryMeta struct {
	ID        string      `json:"id"`
	ResultIDs []string    `json:"resultIds"`
	HasMore   bool        `json:"hasMore"`
	Error     *QueryError `json:"error,omitempty"`
}

type Meta struct {
	Capabilities Capabilities    `json:"capabilities"`
	Runtime      RuntimeVersions `json:"runtime"`
	Queries      []QueryMeta     `json:"queries"`
	Truncated    bool            `json:"truncated"`
}

type Evidence struct {
	Field        string   `json:"field"`
	MatchType    string   `json:"matchType"`
	QueryTerms   []string `json:"queryTerms,omitempty"`
	MatchedTerms []string `json:"matchedTerms,omitempty"`
	Excerpt      string   `json:"excerpt"`
	EditDistance *int     `json:"editDistance,omitempty"`
}

type Match struct {
	QueryID  string     `json:"queryId"`
	Rank     int        `json:"rank"`
	Evidence []Evidence `json:"evidence"`
}

type SearchResult struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Name      string  `json:"name"`
	Signature string  `json:"signature"`
	Summary   string  `json:"summary"`
	Status    string  `json:"status,omitempty"`
	Matches   []Match `json:"matches"`
}

type SearchResponse struct {
	Results []SearchResult `json:"results"`
	Meta    Meta           `json:"meta"`
}

type Parameter struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Optional    bool   `json:"optional"`
	Default     string `json:"default,omitempty"`
}

type ExampleResult struct {
	Input  string `json:"input"`
	Output string `json:"output"`
}

type Example struct {
	Summary     string          `json:"summary"`
	Mapping     string          `json:"mapping"`
	Results     []ExampleResult `json:"results"`
	SkipTesting bool            `json:"skipTesting,omitempty"`
}

type Category struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Examples    []Example `json:"examples"`
}

type Documentation struct {
	ID          string      `json:"id"`
	Kind        string      `json:"kind"`
	Name        string      `json:"name"`
	Signature   string      `json:"signature"`
	Description string      `json:"description"`
	Status      string      `json:"status,omitempty"`
	Since       string      `json:"since,omitempty"`
	Variadic    bool        `json:"variadic"`
	Parameters  []Parameter `json:"parameters"`
	Examples    []Example   `json:"examples"`
	Categories  []Category  `json:"categories"`
}

type GetResponse struct {
	Results []Documentation `json:"results"`
	Meta    Meta            `json:"meta"`
}
