package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/lsp"
)

// The LSP tool family (T8.2): gopls-backed queries over the workspace's
// language server. Seven of the eight are read-only (diagnostics /
// definition / references / hover / symbols / implementations); lsp_rename
// (B5, T7.6) is the family's one effect tool — a multi-file write that rides
// the T5.2 side-effect contract (deny/allow rules, directory trust, per-call
// ask), which is the approval face it waited for.
//
// The family is registered into the deferred tier by default (T4.1
// declaration machinery; the model claims the tools it needs via
// search_tools) so a session that never touches Go costs nothing per turn.

// LSPToolNames lists the family in its canonical order (the deferred-tier
// registration and the [lsp.gopls] tools filter both use these full names).
var LSPToolNames = []string{"lsp_diagnostics", "lsp_definition", "lsp_references", "lsp_hover", "lsp_symbols", "lsp_implementations", "lsp_workspace_symbols", "lsp_rename"}

// LSPTools materializes the tool family over mgr. filter carries the bare
// names from [lsp.gopls] tools (empty = all eight; entries match with or
// without the lsp_ prefix, case-insensitively; unknown entries are ignored).
func LSPTools(mgr *lsp.Manager, filter []string) []agentcore.AgentTool {
	if mgr == nil {
		return nil
	}
	want := map[string]bool{}
	for _, f := range filter {
		want[normalizeLSPToolName(f)] = true
	}
	include := func(full string) bool {
		if len(want) == 0 {
			return true
		}
		return want[normalizeLSPToolName(full)]
	}
	var out []agentcore.AgentTool
	for _, t := range []agentcore.AgentTool{
		&LSPDiagnosticsTool{Mgr: mgr},
		&LSPDefinitionTool{Mgr: mgr},
		&LSPReferencesTool{Mgr: mgr},
		&LSPHoverTool{Mgr: mgr},
		&LSPSymbolsTool{Mgr: mgr},
		&LSPImplementationsTool{Mgr: mgr},
		&LSPWorkspaceSymbolsTool{Mgr: mgr},
		&LSPRenameTool{Mgr: mgr},
	} {
		if include(t.Name()) {
			out = append(out, t)
		}
	}
	return out
}

func normalizeLSPToolName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.TrimPrefix(s, "lsp_")
}

// InjectLSPOverlay wires the overlay sink into the run's edit and write
// tools so every successful mutation pushes the file's new content to the
// language server (the T8.2 overlay workflow: diagnostics follow the edit
// immediately, no file-watcher latency). Non edit/write tools are untouched.
func InjectLSPOverlay(tools []agentcore.AgentTool, mgr *lsp.Manager) {
	if mgr == nil {
		return
	}
	for _, t := range tools {
		switch tool := t.(type) {
		case *EditTool:
			tool.LSP = mgr
		case *WriteTool:
			tool.LSP = mgr
		}
	}
}

// InjectLSPSnapshot wires the file-snapshot recorder into lsp_rename so the
// multi-file write joins /rewind's journal exactly like edit/write (B5).
// Non-rename tools are untouched.
func InjectLSPSnapshot(tools []agentcore.AgentTool, snap *FileSnapshotRecorder) {
	if snap == nil {
		return
	}
	for _, t := range tools {
		if tool, ok := t.(*LSPRenameTool); ok {
			tool.Snap = snap
		}
	}
}

// disabledNote is the common failure text when the manager is off — the
// model needs the enable path to report back to the user.
const lspDisabledNote = "lsp: disabled — enable it with `[lsp] enabled = true` in config.toml or " +
	"`{\"lsp\": {\"enabled\": true}}` in ./.pigo/config.json (project layer), then restart or run /lsp enable"

// lspOk builds a success result with one text block.
func lspOk(text string) agentcore.AgentToolResult {
	return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(text)}}
}

// lspFail renders a manager failure as an error tool result.
func lspFail(err error) agentcore.AgentToolResult {
	if err == nil {
		return errorResult("lsp: unknown error")
	}
	if err == lsp.ErrDisabled {
		return errorResult(lspDisabledNote)
	}
	return errorResult("lsp: " + err.Error())
}

// lspResolve resolves a workspace-relative tool path against the manager's
// workspace root (the same boundary the file tools enforce).
func lspResolve(mgr *lsp.Manager, p string) (string, error) {
	return resolveWithin(mgr.Root(), p)
}

// lspEditReport attaches the post-edit diagnostics report to an edit/write
// result (T8.2, the syntax-check half of the overlay workflow; opencode's
// edit.ts appends the same block). Only errors are reported — warnings ride
// the explicit lsp_diagnostics query — capped per file. The wait is the
// short edit budget: a publish that misses it is still pullable.
func lspEditReport(mgr *lsp.Manager, full string) string {
	if mgr == nil {
		return ""
	}
	diags, err := mgr.DiagnosticsWithWait(full, lsp.EditDiagnosticsWait)
	if err != nil {
		return ""
	}
	return lspErrorBlock(diags[full])
}

// lspErrorBlock renders the file's error-level diagnostics as the block
// appended to an edit result; nil when there is nothing to say.
func lspErrorBlock(diags []lsp.Diagnostic) string {
	var errs []lsp.Diagnostic
	for _, d := range diags {
		if d.Severity == 1 {
			errs = append(errs, d)
		}
	}
	if len(errs) == 0 {
		return ""
	}
	const cap = 10
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nLSP errors detected in this file, please fix:")
	for i, d := range errs {
		if i == cap {
			fmt.Fprintf(&b, "\n  … and %d more (call lsp_diagnostics for the full list)", len(errs)-cap)
			break
		}
		fmt.Fprintf(&b, "\n  %s", d.String())
	}
	return b.String()
}

// --- lsp_diagnostics ---

// LSPDiagnosticsTool reports the server's collected diagnostics, optionally
// waiting for a fresh publish of one file (the edit-then-check workflow).
type LSPDiagnosticsTool struct {
	Mgr *lsp.Manager
}

func (t *LSPDiagnosticsTool) Name() string { return "lsp_diagnostics" }

func (t *LSPDiagnosticsTool) Effect() agentcore.ToolEffect {
	return agentcore.ToolEffect{ReadOnly: true, Scope: agentcore.ScopeWorkspace}
}

func (t *LSPDiagnosticsTool) Description() string {
	return "Syntax and type checking for Go files (compile errors, type errors, analyzer warnings), served by the " +
		"language server. The primary post-edit check: diagnostics reflect the edited content immediately, " +
		"including changes that are only in memory — call it right after editing instead of building."
}

func (t *LSPDiagnosticsTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File to report diagnostics for, relative to the workspace root. Omit for every file the server has analyzed."}
  },
  "additionalProperties": false
}`)
}

func (t *LSPDiagnosticsTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

type lspDiagnosticsArgs struct {
	Path string `json:"path"`
}

func (t *LSPDiagnosticsTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[lspDiagnosticsArgs](args, "lsp_diagnostics")
	if bad != nil {
		return *bad, nil
	}
	path := ""
	if a.Path != "" {
		full, err := lspResolve(t.Mgr, a.Path)
		if err != nil {
			return errorResult("lsp_diagnostics: " + err.Error()), nil
		}
		path = full
	}
	diags, err := t.Mgr.Diagnostics(path)
	if err != nil {
		return lspFail(err), nil
	}
	if len(diags) == 0 {
		return lspOk("no diagnostics"), nil
	}
	paths := make([]string, 0, len(diags))
	for p := range diags {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b strings.Builder
	total := 0
	for _, p := range paths {
		list := diags[p]
		total += len(list)
		fmt.Fprintf(&b, "%s\n", p)
		for _, d := range list {
			fmt.Fprintf(&b, "  %s\n", d.String())
		}
	}
	out := strings.TrimRight(b.String(), "\n")
	if total > 300 {
		lines := strings.Split(out, "\n")
		if len(lines) > 300 {
			out = fmt.Sprintf("%d diagnostics (first %d lines shown):\n%s", total, 300, strings.Join(lines[:300], "\n"))
		}
	}
	return lspOk(out), nil
}

// --- position-based queries (definition / references / hover) ---

// lspPositionArgs is the shared argument shape of the position-based
// queries: a 1-based line plus an optional query substring locating the
// column (the model knows line numbers from read/grep output, rarely
// columns).
type lspPositionArgs struct {
	Path  string `json:"path"`
	Line  int    `json:"line"`
	Query string `json:"query"`
}

type lspPositionTool struct {
	Mgr  *lsp.Manager
	name string
}

func (t *lspPositionTool) Effect() agentcore.ToolEffect {
	return agentcore.ToolEffect{ReadOnly: true, Scope: agentcore.ScopeWorkspace}
}

func (t *lspPositionTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

func (t *lspPositionTool) positionSchema(positionDesc string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "properties": {
    "path":  {"type": "string", "description": "File path, relative to the workspace root."},
    "line":  {"type": "integer", "description": "1-based line number of the symbol."},
    "query": {"type": "string", "description": %q}
  },
  "required": ["path", "line"],
  "additionalProperties": false
}`, positionDesc))
}

// resolve decodes the shared args and resolves the path.
func (t *lspPositionTool) resolve(args json.RawMessage) (lspPositionArgs, string, *agentcore.AgentToolResult) {
	a, bad := decodeArgs[lspPositionArgs](args, t.name)
	if bad != nil {
		return a, "", bad
	}
	full, res := t.resolveDecoded(a.Path, a.Line)
	if res != nil {
		return a, "", res
	}
	return a, full, nil
}

// resolveDecoded validates path/line and resolves the path (the references
// tool decodes its extended arg shape and calls this directly).
func (t *lspPositionTool) resolveDecoded(path string, line int) (string, *agentcore.AgentToolResult) {
	if path == "" {
		res := errorResult(t.name + ": path is required")
		return "", &res
	}
	if line < 1 {
		res := errorResult(t.name + ": line is required (1-based)")
		return "", &res
	}
	full, err := lspResolve(t.Mgr, path)
	if err != nil {
		res := errorResult(t.name + ": " + err.Error())
		return "", &res
	}
	return full, nil
}

// --- lsp_definition ---

type LSPDefinitionTool struct{ lspPositionTool }

func (t *LSPDefinitionTool) Name() string { return "lsp_definition" }

func (t *LSPDefinitionTool) Description() string {
	return "Go to the definition of the symbol at a position, via the language server — exact across " +
		"packages and interface dispatch, where text search guesses. Fast retrieval for locating types, " +
		"functions and identifiers."
}

func (t *LSPDefinitionTool) Schema() json.RawMessage {
	return t.positionSchema("Optional: a substring of the symbol on that line, used to locate the column. Empty = column 0.")
}

func (t *LSPDefinitionTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, full, bad := t.resolve(args)
	if bad != nil {
		return *bad, nil
	}
	locations, err := t.Mgr.Definition(ctx, full, a.Line, a.Query)
	if err != nil {
		return lspFail(err), nil
	}
	return lspOk(renderLocations(locations)), nil
}

// --- lsp_references ---

type lspReferencesArgs struct {
	lspPositionArgs
	IncludeDeclaration bool `json:"include_declaration"`
}

type LSPReferencesTool struct{ lspPositionTool }

func (t *LSPReferencesTool) Name() string { return "lsp_references" }

func (t *LSPReferencesTool) Description() string {
	return "Find every reference to the symbol at a position, via the language server — the complete, " +
		"rename-safe usage survey text search cannot produce (checks before editing an API)."
}

func (t *LSPReferencesTool) Schema() json.RawMessage {
	return t.positionSchema("Optional: a substring of the symbol on that line, used to locate the column. Empty = column 0.")
}

func (t *LSPReferencesTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[lspReferencesArgs](args, "lsp_references")
	if bad != nil {
		return *bad, nil
	}
	full, res := t.resolveDecoded(a.Path, a.Line)
	if res != nil {
		return *res, nil
	}
	locations, err := t.Mgr.References(ctx, full, a.Line, a.Query, a.IncludeDeclaration)
	if err != nil {
		return lspFail(err), nil
	}
	return lspOk(renderLocations(locations)), nil
}

// --- lsp_hover ---

type LSPHoverTool struct{ lspPositionTool }

func (t *LSPHoverTool) Name() string { return "lsp_hover" }

func (t *LSPHoverTool) Description() string {
	return "Hover information (type, signature, docs) for the symbol at a position, via the language server."
}

func (t *LSPHoverTool) Schema() json.RawMessage {
	return t.positionSchema("Optional: a substring of the symbol on that line, used to locate the column. Empty = column 0.")
}

func (t *LSPHoverTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, full, bad := t.resolve(args)
	if bad != nil {
		return *bad, nil
	}
	hover, err := t.Mgr.Hover(ctx, full, a.Line, a.Query)
	if err != nil {
		return lspFail(err), nil
	}
	if strings.TrimSpace(hover.Markdown) == "" {
		return lspOk("no hover information"), nil
	}
	return lspOk(hover.Markdown), nil
}

// --- lsp_symbols ---

// LSPSymbolsTool lists a document's symbols (outline).
type LSPSymbolsTool struct {
	Mgr *lsp.Manager
}

func (t *LSPSymbolsTool) Name() string { return "lsp_symbols" }

func (t *LSPSymbolsTool) Effect() agentcore.ToolEffect {
	return agentcore.ToolEffect{ReadOnly: true, Scope: agentcore.ScopeWorkspace}
}

func (t *LSPSymbolsTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

func (t *LSPSymbolsTool) Description() string {
	return "List a file's symbols (functions, types, methods) as an outline, via the language server — " +
		"a fast structure map of an unfamiliar file, cheaper than reading it whole."
}

func (t *LSPSymbolsTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path, relative to the workspace root."}
  },
  "required": ["path"],
  "additionalProperties": false
}`)
}

type lspSymbolsArgs struct {
	Path string `json:"path"`
}

func (t *LSPSymbolsTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[lspSymbolsArgs](args, "lsp_symbols")
	if bad != nil {
		return *bad, nil
	}
	if a.Path == "" {
		return errorResult("lsp_symbols: path is required"), nil
	}
	full, err := lspResolve(t.Mgr, a.Path)
	if err != nil {
		return errorResult("lsp_symbols: " + err.Error()), nil
	}
	symbols, err := t.Mgr.Symbols(ctx, full)
	if err != nil {
		return lspFail(err), nil
	}
	if len(symbols) == 0 {
		return lspOk("no symbols"), nil
	}
	var b strings.Builder
	var walk func(ss []lsp.Symbol, depth int)
	walk = func(ss []lsp.Symbol, depth int) {
		for _, s := range ss {
			fmt.Fprintf(&b, "%s%s %s", strings.Repeat("  ", depth), s.KindName, s.Name)
			if s.Detail != "" {
				fmt.Fprintf(&b, " — %s", s.Detail)
			}
			fmt.Fprintf(&b, " (%d:%d)\n", s.Line+1, s.Character+1)
			walk(s.Children, depth+1)
		}
	}
	walk(symbols, 0)
	return lspOk(strings.TrimRight(b.String(), "\n")), nil
}

// renderLocations renders a definition/references result.
func renderLocations(locations []lsp.Location) string {
	if len(locations) == 0 {
		return "no results"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d location(s):", len(locations))
	for _, loc := range locations {
		fmt.Fprintf(&b, "\n  %s", loc.String())
	}
	return b.String()
}

// --- lsp_implementations ---

// LSPImplementationsTool resolves implementations of the symbol at a
// position (interface → implementors, method → overrides).
type LSPImplementationsTool struct{ lspPositionTool }

func (t *LSPImplementationsTool) Name() string { return "lsp_implementations" }

func (t *LSPImplementationsTool) Description() string {
	return "Find implementations of the symbol at a position (interface methods, overriding methods) via the language server."
}

func (t *LSPImplementationsTool) Schema() json.RawMessage {
	return t.positionSchema("Optional: a substring of the symbol on that line, used to locate the column. Empty = column 0.")
}

func (t *LSPImplementationsTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, full, bad := t.resolve(args)
	if bad != nil {
		return *bad, nil
	}
	locations, err := t.Mgr.Implementations(ctx, full, a.Line, a.Query)
	if err != nil {
		return lspFail(err), nil
	}
	return lspOk(renderLocations(locations)), nil
}

// --- lsp_workspace_symbols ---

// LSPWorkspaceSymbolsTool searches the project-wide symbol index
// (workspace/symbol): the whole-module "where does this live" query.
type LSPWorkspaceSymbolsTool struct {
	Mgr *lsp.Manager
}

func (t *LSPWorkspaceSymbolsTool) Name() string { return "lsp_workspace_symbols" }

func (t *LSPWorkspaceSymbolsTool) Effect() agentcore.ToolEffect {
	return agentcore.ToolEffect{ReadOnly: true, Scope: agentcore.ScopeWorkspace}
}

func (t *LSPWorkspaceSymbolsTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

func (t *LSPWorkspaceSymbolsTool) Description() string {
	return "Search the workspace's symbol index (functions, types, methods across the whole module) by fuzzy name, via the language server — " +
		"the fastest way to locate where a symbol is declared in an unfamiliar codebase."
}

func (t *LSPWorkspaceSymbolsTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "Symbol name fragment to search for (fuzzy match)."}
  },
  "required": ["query"],
  "additionalProperties": false
}`)
}

type lspWorkspaceSymbolsArgs struct {
	Query string `json:"query"`
}

func (t *LSPWorkspaceSymbolsTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[lspWorkspaceSymbolsArgs](args, "lsp_workspace_symbols")
	if bad != nil {
		return *bad, nil
	}
	if strings.TrimSpace(a.Query) == "" {
		return errorResult("lsp_workspace_symbols: query is required"), nil
	}
	symbols, err := t.Mgr.WorkspaceSymbols(ctx, a.Query)
	if err != nil {
		return lspFail(err), nil
	}
	if len(symbols) == 0 {
		return lspOk("no symbols matching " + a.Query), nil
	}
	const cap = 50
	var b strings.Builder
	fmt.Fprintf(&b, "%d symbol(s) matching %q:", len(symbols), a.Query)
	for i, s := range symbols {
		if i == cap {
			fmt.Fprintf(&b, "\n  … and %d more (narrow the query)", len(symbols)-cap)
			break
		}
		loc := s.Location.String()
		if s.Container != "" {
			fmt.Fprintf(&b, "\n  %s %s (%s) — %s", s.KindName, s.Name, s.Container, loc)
		} else {
			fmt.Fprintf(&b, "\n  %s %s — %s", s.KindName, s.Name, loc)
		}
	}
	return lspOk(b.String()), nil
}

// --- lsp_rename (B5, T7.6: the family's one effect tool, riding T5.2) ---

// LSPRenameTool renames the symbol at a position across the workspace
// (textDocument/rename): the server computes every reference — including
// cross-package hits text search cannot see — and the tool applies the
// returned workspace edit to the files. The write is what makes it an effect
// tool: deny/allow rules, directory trust and the per-call ask gate all
// apply, which is the approval face this tool waited for.
type LSPRenameTool struct {
	Mgr *lsp.Manager
	// Snap records each file's prior content so /rewind can restore the
	// whole rename; nil (tests) skips the journal like a recorder-less
	// edit tool.
	Snap *FileSnapshotRecorder
}

func (t *LSPRenameTool) Name() string { return "lsp_rename" }

func (t *LSPRenameTool) Effect() agentcore.ToolEffect {
	// Multi-file write: neither read-only nor destructive (a rename is
	// recoverable via /rewind), so allow rules may settle it and trust may
	// fast-path it — the contract the engine judges by.
	return agentcore.ToolEffect{Scope: agentcore.ScopeWorkspace}
}

func (t *LSPRenameTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

func (t *LSPRenameTool) Description() string {
	return "Rename the symbol at a position across the whole workspace, via the language server — every reference including cross-package ones " +
		"text search cannot see. Applies the returned edits to the files and reports each touched file."
}

func (t *LSPRenameTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "path":     {"type": "string", "description": "File path, relative to the workspace root."},
    "line":     {"type": "integer", "description": "1-based line number of the symbol."},
    "new_name": {"type": "string", "description": "The new identifier name."},
    "query":    {"type": "string", "description": "Optional: a substring of the symbol on that line, used to locate the column. Empty = column 0."}
  },
  "required": ["path", "line", "new_name"],
  "additionalProperties": false
}`)
}

type lspRenameArgs struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	NewName string `json:"new_name"`
	Query   string `json:"query"`
}

func (t *LSPRenameTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[lspRenameArgs](args, "lsp_rename")
	if bad != nil {
		return *bad, nil
	}
	if a.NewName == "" {
		return errorResult("lsp_rename: new_name is required"), nil
	}
	if a.Path == "" {
		return errorResult("lsp_rename: path is required"), nil
	}
	if a.Line < 1 {
		return errorResult("lsp_rename: line is required (1-based)"), nil
	}
	full, err := lspResolve(t.Mgr, a.Path)
	if err != nil {
		return errorResult("lsp_rename: " + err.Error()), nil
	}
	edits, err := t.Mgr.Rename(ctx, full, a.Line, a.NewName, a.Query)
	if err != nil {
		return lspFail(err), nil
	}
	// Apply bottom-up per file, snapshotting each prior content so /rewind
	// can restore the whole rename. A mid-way write failure stops the loop
	// and names the files already written (honest partial-application
	// report; the snapshot journal lets /rewind undo it).
	paths := make([]string, 0, len(edits))
	for p := range edits {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var written []string
	var b strings.Builder
	for _, p := range paths {
		original, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintf(&b, "lsp_rename: cannot read %q: %v\napplied so far: %s", p, err, strings.Join(written, ", "))
			return errorResult(b.String()), nil
		}
		updated, err := lsp.ApplyEdits(string(original), edits[p])
		if err != nil {
			fmt.Fprintf(&b, "lsp_rename: %q: %v\napplied so far: %s", p, err, strings.Join(written, ", "))
			return errorResult(b.String()), nil
		}
		if t.Snap != nil {
			t.Snap.Record(p)
		}
		if err := os.WriteFile(p, []byte(updated), filePerm); err != nil {
			fmt.Fprintf(&b, "lsp_rename: cannot write %q: %v\napplied so far: %s", p, err, strings.Join(written, ", "))
			return errorResult(b.String()), nil
		}
		written = append(written, p)
		if t.Mgr != nil {
			t.Mgr.Overlay(p, updated)
		}
	}
	var total int
	for _, list := range edits {
		total += len(list)
	}
	fmt.Fprintf(&b, "renamed to %q: %d edit(s) across %d file(s)", a.NewName, total, len(written))
	for _, p := range written {
		rel, relErr := filepath.Rel(t.Mgr.Root(), p)
		if relErr != nil {
			rel = p
		}
		fmt.Fprintf(&b, "\n  %s (%d edit(s))", rel, len(edits[p]))
	}
	return lspOk(b.String()), nil
}
