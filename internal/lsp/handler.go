package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/owenrumney/go-lsp/document"
	protocol "github.com/owenrumney/go-lsp/lsp"
	"github.com/owenrumney/go-lsp/server"
	"github.com/redpanda-data/benthos/v4/public/bloblang"
	"github.com/teyfix/bloblang-lsp/internal/benthos"
	"github.com/teyfix/bloblang-lsp/internal/config"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
)

type Handler struct {
	config          *config.Config
	logger          *slog.Logger
	serverVersion   string
	benv            *bloblang.Environment
	parser          *benthos.Bloblang
	completionItems []protocol.CompletionItem
	functionDocs    map[string]protocol.MarkupContent
	methodDocs      map[string]protocol.MarkupContent

	documents *document.Store
	client    *server.Client
	executor  *benthos.Executor

	mu sync.Mutex

	workspaceRoot  string
	workspaceRoots []string
	importBaseDirs map[protocol.DocumentURI]string
	importBasesMu  sync.RWMutex

	samples   map[protocol.DocumentURI]*benthos.Sample
	samplesMu sync.RWMutex

	sampleDiagnostics   map[protocol.DocumentURI][]protocol.Diagnostic
	sampleDiagnosticsMu sync.RWMutex

	lastDiagnostics   map[protocol.DocumentURI][]protocol.Diagnostic
	lastDiagnosticsMu sync.RWMutex
	validationHook    func(protocol.DocumentURI, []protocol.Diagnostic)

	execDiagnostics   map[protocol.DocumentURI][]protocol.Diagnostic
	execDiagnosticsMu sync.RWMutex

	latestVersion    map[protocol.DocumentURI]uint64
	nextVersion      uint64
	documentContexts map[protocol.DocumentURI]context.Context
	documentCancels  map[protocol.DocumentURI]context.CancelFunc
	stopped          bool
	diagnosticOutbox diagnosticOutbox
	refreshInlays    bool
	refreshLenses    bool
}

// NewHandler defaults its advertised version to dev when build metadata is absent.
func NewHandler(cfg *config.Config, logger *slog.Logger, buildVersion ...string) (*Handler, error) {
	benv := benthos.NewEnvironment()
	parser, err := benthos.NewBloblang()

	if err != nil {
		return nil, err
	}

	items, fnData, methData := benthos.BuildCompletionCache(benv)
	fnDocs, methDocs := benthos.BuildAllDocs(fnData, methData, cfg.BloblangDocsURL)
	executor := benthos.NewExecutor(benv, cfg)
	version := "dev"
	if len(buildVersion) > 0 && buildVersion[0] != "" {
		version = buildVersion[0]
	}

	h := &Handler{
		config:            cfg,
		logger:            logger,
		serverVersion:     version,
		benv:              benv,
		parser:            parser,
		completionItems:   items,
		functionDocs:      fnDocs,
		methodDocs:        methDocs,
		documents:         document.NewStore(),
		executor:          executor,
		importBaseDirs:    make(map[protocol.DocumentURI]string),
		samples:           make(map[protocol.DocumentURI]*benthos.Sample),
		sampleDiagnostics: make(map[protocol.DocumentURI][]protocol.Diagnostic),
		lastDiagnostics:   make(map[protocol.DocumentURI][]protocol.Diagnostic),
		execDiagnostics:   make(map[protocol.DocumentURI][]protocol.Diagnostic),
		latestVersion:     make(map[protocol.DocumentURI]uint64),
		documentContexts:  make(map[protocol.DocumentURI]context.Context),
		documentCancels:   make(map[protocol.DocumentURI]context.CancelFunc),
	}
	executor.SetImporter(func(uri, name string) ([]byte, error) {
		return os.ReadFile(h.resolveImportPath(protocol.DocumentURI(uri), name))
	})
	return h, nil
}

func (h *Handler) SetClient(client *server.Client) {
	h.client = client
}

func (h *Handler) Initialize(_ context.Context, params *protocol.InitializeParams) (*protocol.InitializeResult, error) {
	for _, folder := range params.WorkspaceFolders {
		if path, err := uriToPath(folder.URI); err == nil {
			h.workspaceRoots = append(h.workspaceRoots, path)
		}
	}
	if len(params.WorkspaceFolders) > 0 {
		if path, err := uriToPath(params.WorkspaceFolders[0].URI); err == nil {
			h.workspaceRoot = path
		}
	} else if params.RootURI != nil {
		if path, err := uriToPath(*params.RootURI); err == nil {
			h.workspaceRoot = path
		}
	}
	if w := params.Capabilities.Workspace; w != nil {
		if w.InlayHint != nil && w.InlayHint.RefreshSupport != nil {
			h.refreshInlays = *w.InlayHint.RefreshSupport
		}
		if w.CodeLens != nil && w.CodeLens.RefreshSupport != nil {
			h.refreshLenses = *w.CodeLens.RefreshSupport
		}
	}
	openClose := true
	return &protocol.InitializeResult{
		Capabilities: protocol.ServerCapabilities{
			TextDocumentSync: &protocol.TextDocumentSyncOptions{
				OpenClose: &openClose,
				Change:    protocol.SyncFull,
			},
			CompletionProvider: &protocol.CompletionOptions{
				TriggerCharacters: []string{".", "@", "$"},
			},
			SignatureHelpProvider:      &protocol.SignatureHelpOptions{TriggerCharacters: []string{"(", ",", ":"}},
			DocumentSymbolProvider:     &protocol.DocumentSymbolOptions{},
			FoldingRangeProvider:       new(true),
			HoverProvider:              new(true),
			DefinitionProvider:         new(true),
			ReferencesProvider:         new(true),
			RenameProvider:             &protocol.RenameOptions{PrepareProvider: new(true)},
			DocumentFormattingProvider: new(true),
			CodeActionProvider:         &protocol.CodeActionOptions{CodeActionKinds: []protocol.CodeActionKind{protocol.CodeActionQuickFix}},
			InlayHintProvider:          &protocol.InlayHintOptions{},
			CodeLensProvider:           &protocol.CodeLensOptions{},
			ExecuteCommandProvider:     &protocol.ExecuteCommandOptions{Commands: executeCommandNames()},
		},
		ServerInfo: &protocol.ServerInfo{Name: "bloblang-lsp", Version: h.serverVersion},
	}, nil
}

func (h *Handler) Shutdown(_ context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopped = true
	for uri, cancel := range h.documentCancels {
		cancel()
		h.executor.InvalidateDocument(string(uri))
	}
	clear(h.documentCancels)
	clear(h.documentContexts)
	clear(h.latestVersion)
	h.diagnosticOutbox.stop()
	h.parser.Close()
	return nil
}

func (h *Handler) SetTrace(_ context.Context, _ *protocol.SetTraceParams) error {
	return nil
}

func (h *Handler) DidOpen(ctx context.Context, params *protocol.DidOpenTextDocumentParams) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return errors.New("language server is shut down")
	}
	doc, err := h.documents.Open(params)
	if err != nil {
		return err
	}
	uri := params.TextDocument.URI
	validation, ver := h.beginDocumentVersion(uri)
	baseDir := h.baseDirForURI(uri)
	h.importBasesMu.Lock()
	h.importBaseDirs[uri] = baseDir
	h.importBasesMu.Unlock()
	if yamlDocument(uri) {
		h.syncRegions(uri, doc.Text())
	} else {
		h.updateSample(uri, doc.Text())
	}
	h.executor.InvalidateDocument(string(uri))
	h.clearExecDiagnostics(uri)

	h.scheduleValidation(validation, uri, ver)
	return nil
}

func (h *Handler) DidChange(ctx context.Context, params *protocol.DidChangeTextDocumentParams) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return errors.New("language server is shut down")
	}
	doc, err := h.documents.Change(params)
	if err != nil {
		return err
	}
	uri := params.TextDocument.URI
	validation, ver := h.beginDocumentVersion(uri)
	if yamlDocument(uri) {
		h.syncRegions(uri, doc.Text())
	} else {
		h.updateSample(uri, doc.Text())
	}
	h.executor.InvalidateDocument(string(uri))
	h.clearExecDiagnostics(uri)

	h.scheduleValidation(validation, uri, ver)
	return nil
}

func (h *Handler) DidClose(ctx context.Context, params *protocol.DidCloseTextDocumentParams) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	uri := params.TextDocument.URI
	if cancel := h.documentCancels[uri]; cancel != nil {
		cancel()
	}
	delete(h.documentCancels, uri)
	delete(h.documentContexts, uri)
	delete(h.latestVersion, uri)
	h.clearRegions(uri)
	h.documents.Close(params)
	h.importBasesMu.Lock()
	delete(h.importBaseDirs, uri)
	h.importBasesMu.Unlock()
	h.samplesMu.Lock()
	delete(h.samples, uri)
	h.samplesMu.Unlock()
	h.sampleDiagnosticsMu.Lock()
	delete(h.sampleDiagnostics, uri)
	h.sampleDiagnosticsMu.Unlock()
	h.executor.InvalidateDocument(string(uri))
	h.parser.InvalidateDocument(string(uri))
	h.clearExecDiagnostics(uri)
	h.publishDiagnostics(ctx, uri, nil, 0)
	h.lastDiagnosticsMu.Lock()
	delete(h.lastDiagnostics, uri)
	h.lastDiagnosticsMu.Unlock()
	return nil
}

func (h *Handler) Completion(ctx context.Context, params *protocol.CompletionParams) (*protocol.CompletionList, error) {
	ctx, _, text, sample, done, ok := h.requestDocument(ctx, params.TextDocument.URI)
	defer done()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if yamlDocument(params.TextDocument.URI) && !strings.Contains(string(params.TextDocument.URI), "#bloblang-") {
		r, pos, ok := h.regionAt(params.TextDocument.URI, params.Position)
		if !ok {
			return &protocol.CompletionList{Items: []protocol.CompletionItem{}}, nil
		}
		cp := *params
		cp.TextDocument.URI = r.uri
		cp.Position = pos
		return h.Completion(ctx, &cp)
	}
	items := h.completionItems
	if ok {
		lines := strings.Split(text, "\n")
		pos := byteColumn(text, params.Position)
		switch getCompletionContext(lines, pos.Line, pos.Character) {
		case "method":
			items = filterCompletions(items, protocol.CompletionItemKindMethod)
			items = h.methodCompletion(ctx, params.TextDocument.URI, sample, text, params.Position, items)
		case "function":
			items = filterCompletions(items, protocol.CompletionItemKindFunction)
		case "variable":
			items = h.variableCompletions(params.TextDocument.URI, text, params.Position)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &protocol.CompletionList{Items: items}, nil
}

func (h *Handler) Hover(ctx context.Context, params *protocol.HoverParams) (hover *protocol.Hover, err error) {
	uri := params.TextDocument.URI
	ctx, _, text, sample, done, ok := h.requestDocument(ctx, uri)
	defer done()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	defer func() {
		if ctx.Err() != nil {
			hover, err = nil, ctx.Err()
		}
	}()
	if yamlDocument(uri) && !strings.Contains(string(uri), "#bloblang-") {
		r, pos, ok := h.regionAt(uri, params.Position)
		if !ok {
			return nil, nil
		}
		cp := *params
		cp.TextDocument.URI = r.uri
		cp.Position = pos
		v, err := h.Hover(ctx, &cp)
		if v != nil && v.Range != nil {
			host, _ := h.documents.Text(uri)
			*v.Range = r.hostRange(host, *v.Range)
		}
		return v, err
	}
	if !ok {
		return nil, nil
	}
	position := byteColumn(text, params.Position)
	lineIdx := position.Line
	col := position.Character

	tree, parseErr := h.parser.Parse(string(uri), text)
	if parseErr != nil || tree == nil {
		return nil, nil
	}

	wrappedExpression := false
	defer func() {
		if wrappedExpression && hover != nil && hover.Range != nil {
			if hover.Range.Start.Line == 0 {
				hover.Range.Start.Character = max(0, hover.Range.Start.Character-7)
			}
			if hover.Range.End.Line == 0 {
				hover.Range.End.Character = max(0, hover.Range.End.Character-7)
			}
		}
	}()
	if tree.RootNode().HasError() && strings.Contains(string(uri), "#bloblang-") {
		wrapped, e := h.parser.Parse(string(uri)+":expression", "root = "+text)
		if e == nil && wrapped != nil {
			if !wrapped.RootNode().HasError() {
				tree.Close()
				tree = wrapped
				text = "root = " + text
				wrappedExpression = true
				if lineIdx == 0 {
					col += 7
				}
			} else {
				wrapped.Close()
			}
		}
	}
	defer tree.Close()
	root := tree.RootNode()
	point := tree_sitter.Point{
		Row:    uint(lineIdx),
		Column: uint(col),
	}

	node := root.DescendantForPointRange(point, point)
	if node == nil {
		return nil, nil
	}

	if sample != nil {
		statement := node
		for statement != nil && !benthosStatement(statement.Kind()) {
			statement = statement.Parent()
		}
		if statement != nil && statement.Parent() != nil && statement.Kind() != "map_declaration" {
			var result *benthos.PartialResult
			var err error
			if node.Kind() == "root" && node.Parent() != nil && node.Parent().Kind() == "root_assignment" {
				if statement.Parent().Kind() == "source" {
					result, err = h.executor.ExecuteThroughContext(ctx, h.parser, string(uri), sample, text, statement.StartByte())
				} else {
					result, err = h.executor.EvaluateExpressionContext(ctx, h.parser, string(uri), sample, text, statement, node)
				}
			} else {
				expression := hoverExpression(node)
				if node.Kind() == "identifier" && node.Parent() != nil && node.Parent().Kind() == "let_assignment" && node.Parent().ChildByFieldName("name").Id() == node.Id() {
					expression = node.Parent().ChildByFieldName("value")
				}
				if expression != nil {
					result, err = h.executor.EvaluateExpressionContext(ctx, h.parser, string(uri), sample, text, statement, expression)
				}
			}
			if err == nil && result != nil {
				v := h.valueHover(uri, result, node)
				if v.Range != nil {
					*v.Range = rangeUTF16(text, *v.Range)
				}
				return v, nil
			}
		}
	}

	if node.Kind() == "deleted" || (node.Parent() != nil && node.Parent().Kind() == "deleted") {
		doc := h.functionDocs["deleted"]
		if doc.Value == "" {
			doc = protocol.MarkupContent{Kind: protocol.Markdown, Value: "**deleted()**\nReturns a deletion marker. Assigning it to a field removes that field; assigning it to root filters the whole message."}
		}
		return &protocol.Hover{Contents: protocol.NewHoverContents(doc.Kind, doc.Value)}, nil
	}
	// 2. Functions & Methods hover
	if node.Kind() == "identifier" && node.Parent() != nil {
		parent := node.Parent()
		token := node.Utf8Text([]byte(text))
		var doc protocol.MarkupContent
		var found bool
		if parent.Kind() == "method_call" {
			doc, found = h.methodDocs[token]
		} else if parent.Kind() == "call_expr" {
			doc, found = h.functionDocs[token]
		}
		if found {
			return &protocol.Hover{
				Contents: protocol.NewHoverContents(doc.Kind, doc.Value),
				Range: &protocol.Range{
					Start: bytePosition(text, int(node.StartByte())),
					End:   bytePosition(text, int(node.EndByte())),
				},
			}, nil
		}
	}

	return nil, nil
}

func (h *Handler) baseDirForURI(uri protocol.DocumentURI) string {
	h.importBasesMu.RLock()
	if base := h.importBaseDirs[uri]; base != "" {
		h.importBasesMu.RUnlock()
		return base
	}
	h.importBasesMu.RUnlock()
	if path, err := uriToPath(uri); err == nil {
		return filepath.Dir(path)
	}
	if h.workspaceRoot != "" {
		return h.workspaceRoot
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return os.TempDir()
}

func (h *Handler) updateSample(uri protocol.DocumentURI, text string) {
	sample, err := benthos.ExtractSample(h.parser, string(uri), text, h.baseDirForURI(uri))
	h.storeSample(uri, text, sample, err)
}
func (h *Handler) storeSample(uri protocol.DocumentURI, text string, sample *benthos.Sample, err error) {
	h.samplesMu.Lock()
	h.samples[uri] = sample
	h.samplesMu.Unlock()
	h.sampleDiagnosticsMu.Lock()
	defer h.sampleDiagnosticsMu.Unlock()
	if err == nil {
		delete(h.sampleDiagnostics, uri)
		if sample == nil {
			severity := protocol.SeverityInformation
			stem := "mapping"
			if p, e := uriToPath(uri); e == nil {
				stem = strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
			}
			h.sampleDiagnostics[uri] = []protocol.Diagnostic{{Range: protocol.Range{}, Severity: &severity, Source: "bloblang sample", Message: stem + ".sample.json not found; add one to get hints"}}
		}
		return
	}
	severity := protocol.SeverityError
	source := "bloblang"
	line := sampleDirectiveLine(text)
	if se, ok := err.(*benthos.SampleError); ok {
		line = se.Line
	}
	h.sampleDiagnostics[uri] = []protocol.Diagnostic{{
		Range:    protocol.Range{Start: protocol.Position{Line: line, Character: 0}, End: protocol.Position{Line: line, Character: 0}},
		Severity: &severity,
		Source:   source,
		Message:  err.Error(),
	}}
}

func (h *Handler) getSample(uri protocol.DocumentURI) *benthos.Sample {
	h.samplesMu.RLock()
	defer h.samplesMu.RUnlock()
	return h.samples[uri]
}

func (h *Handler) sampleDiagnosticsFor(uri protocol.DocumentURI) []protocol.Diagnostic {
	h.sampleDiagnosticsMu.RLock()
	defer h.sampleDiagnosticsMu.RUnlock()
	return append([]protocol.Diagnostic(nil), h.sampleDiagnostics[uri]...)
}

// clearExecDiagnostics removes any previously stored execution diagnostics for uri.
// Called on DidOpen/DidChange so stale errors don't persist after the document is edited.
func (h *Handler) clearExecDiagnostics(uri protocol.DocumentURI) {
	h.execDiagnosticsMu.Lock()
	for key := range h.execDiagnostics {
		if benthos.DocumentKeyMatches(string(key), string(uri)) {
			delete(h.execDiagnostics, key)
		}
	}
	h.execDiagnosticsMu.Unlock()
}

// publishExecDiagnostics stores the given execution-time diagnostics for uri and
// re-publishes the merged set (last known parse/import diagnostics + exec diagnostics).
// Passing an empty or nil slice clears any previously stored exec diagnostics.
func (h *Handler) publishExecDiagnostics(ctx context.Context, uri protocol.DocumentURI, diags []protocol.Diagnostic, generation ...uint64) {
	h.execDiagnosticsMu.Lock()
	if len(diags) == 0 {
		delete(h.execDiagnostics, uri)
	} else {
		h.execDiagnostics[uri] = append([]protocol.Diagnostic(nil), diags...)
	}
	h.execDiagnosticsMu.Unlock()

	// Merge with the last known parse/import diagnostics and republish.
	h.lastDiagnosticsMu.RLock()
	base := append([]protocol.Diagnostic(nil), h.lastDiagnostics[uri]...)
	h.lastDiagnosticsMu.RUnlock()

	h.execDiagnosticsMu.RLock()
	exec := append([]protocol.Diagnostic(nil), h.execDiagnostics[uri]...)
	h.execDiagnosticsMu.RUnlock()

	merged := mergeDiagnostics(base, exec)
	if h.client == nil || strings.Contains(string(uri), "#bloblang-") {
		return
	}
	h.enqueueDiagnostics(uri, merged, generation...)
}

func sampleDirectiveLine(text string) int {
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#!sample ") || strings.HasPrefix(line, "#!sample_from ") {
			return i
		}
		if line != "" && !strings.HasPrefix(line, "#") {
			break
		}
	}
	return 0
}

func rawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
