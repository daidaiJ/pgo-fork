package lsp

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Diagnostic is one server-published diagnostic, flattened for tool output.
// Range endpoints are the server's (line, character) pair with character in
// UTF-16 code units — the LSP default position encoding this client speaks.
type Diagnostic struct {
	Line      int    // 0-based line
	Character int    // 0-based UTF-16 column
	Severity  int    // 1=error 2=warning 3=information 4=hint
	Source    string // e.g. "compile", "staticcheck" (gopls analyzers)
	Code      string
	Message   string
}

func (d Diagnostic) severityTag() string {
	switch d.Severity {
	case 1:
		return "E"
	case 2:
		return "W"
	case 3:
		return "I"
	case 4:
		return "H"
	default:
		return "?"
	}
}

// String renders one line: "E [12:4] message (source)".
func (d Diagnostic) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s [%d:%d] %s", d.severityTag(), d.Line+1, d.Character+1, d.Message)
	if d.Source != "" {
		b.WriteString(" (" + d.Source + ")")
	}
	if d.Code != "" {
		b.WriteString(" [" + d.Code + "]")
	}
	return b.String()
}

// Location is one definition/reference hit, already converted back to a
// filesystem path.
type Location struct {
	Path      string
	Line      int // 0-based
	Character int // 0-based UTF-16 column
}

func (l Location) String() string { return fmt.Sprintf("%s:%d:%d", l.Path, l.Line+1, l.Character+1) }

// Symbol is one documentSymbol entry flattened from the hierarchical or the
// flat (SymbolInformation) response shape. Kind carries the LSP
// SymbolKind number; KindName renders it for display.
type Symbol struct {
	Name      string
	Kind      int
	KindName  string
	Detail    string
	Line      int // 0-based selection start
	Character int
	Container string // flat shape only (SymbolInformation.containerName)
	Children  []Symbol
}

// WorkspaceSymbol is one workspace/symbol hit: a project-wide symbol with
// its real file location (the flat SymbolInformation wire shape).
type WorkspaceSymbol struct {
	Name      string
	Kind      int
	KindName  string
	Container string
	Location  Location
}

func symbolKindName(kind int) string {
	// LSP 3.17 SymbolKind values 1-26; the common ones named, the rest numeric.
	names := map[int]string{
		1: "file", 2: "module", 3: "namespace", 4: "package", 5: "class",
		6: "method", 7: "property", 8: "field", 9: "constructor", 10: "enum",
		11: "interface", 12: "function", 13: "variable", 14: "constant",
		15: "string", 16: "number", 17: "boolean", 18: "array", 19: "object",
		20: "key", 21: "null", 22: "enum-member", 23: "struct", 24: "event",
		25: "operator", 26: "type-parameter",
	}
	if n, ok := names[kind]; ok {
		return n
	}
	return fmt.Sprintf("kind-%d", kind)
}

// Hover is the flattened textDocument/hover response.
type Hover struct {
	// Markdown is the hover contents rendered as markdown: MarkupContent is
	// taken verbatim; MarkedString and []MarkedString variants are folded
	// into fenced or plain text.
	Markdown string
	// Range is the hover's source range (0-based), when the server sent one.
	Line      int
	Character int
	HasRange  bool
}

// --- wire shapes (decode-only) ---

type wirePosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type wireRange struct {
	Start wirePosition `json:"start"`
	End   wirePosition `json:"end"`
}

type wireLocation struct {
	URI   string    `json:"uri"`
	Range wireRange `json:"range"`
}

type wireDiagnostic struct {
	Range    wireRange `json:"range"`
	Severity int       `json:"severity"`
	Code     any       `json:"code"` // string or number per spec
	Source   string    `json:"source"`
	Message  string    `json:"message"`
}

// TextEdit is one replacement range decoded from a workspace edit: 0-based
// line / UTF-16 character offsets per the LSP wire format, with the
// replacement text.
type TextEdit struct {
	StartLine int
	StartChar int
	EndLine   int
	EndChar   int
	NewText   string
}

type wireTextEdit struct {
	Range   wireRange `json:"range"`
	NewText string    `json:"newText"`
}

// workspaceEdit is the textDocument/rename response (3.17 both shapes:
// changes keyed by URI, or the documentChanges list).
type workspaceEdit struct {
	Changes         map[string][]wireTextEdit `json:"changes"`
	DocumentChanges []textDocumentEdit        `json:"documentChanges"`
}

type textDocumentEdit struct {
	TextDocument optionalVersionedTextDocumentIdentifier `json:"textDocument"`
	Edits        []wireTextEdit                          `json:"edits"`
}

type optionalVersionedTextDocumentIdentifier struct {
	URI string `json:"uri"`
}

// EditsByFile flattens both WorkspaceEdit shapes into path → edits (URIs
// resolved through URIToPath; a path appearing in both shapes merges).
func (w workspaceEdit) EditsByFile() map[string][]TextEdit {
	out := map[string][]TextEdit{}
	add := func(uri string, edits []wireTextEdit) {
		path := URIToPath(uri)
		if path == "" {
			return
		}
		for _, e := range edits {
			out[path] = append(out[path], TextEdit{
				StartLine: e.Range.Start.Line,
				StartChar: e.Range.Start.Character,
				EndLine:   e.Range.End.Line,
				EndChar:   e.Range.End.Character,
				NewText:   e.NewText,
			})
		}
	}
	for uri, edits := range w.Changes {
		add(uri, edits)
	}
	for _, d := range w.DocumentChanges {
		add(d.TextDocument.URI, d.Edits)
	}
	return out
}

func (w wireDiagnostic) flatten() Diagnostic {
	d := Diagnostic{
		Line:      w.Range.Start.Line,
		Character: w.Range.Start.Character,
		Severity:  w.Severity,
		Source:    w.Source,
		Message:   w.Message,
	}
	switch c := w.Code.(type) {
	case string:
		d.Code = c
	case float64:
		d.Code = fmt.Sprintf("%d", int(c))
	}
	return d
}

// publishDiagnosticsParams is the textDocument/publishDiagnostics payload.
type publishDiagnosticsParams struct {
	URI         string           `json:"uri"`
	Diagnostics []wireDiagnostic `json:"diagnostics"`
	Version     *int             `json:"version"` // present on change-triggered publishes
}

// decodeHover flattens the Hover result variants (3.17: MarkupContent |
// MarkedString | MarkedString[]).
func decodeHover(raw json.RawMessage) Hover {
	if len(raw) == 0 {
		return Hover{}
	}
	var contents struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
		// MarkedString variants (untagged union members are absent in JSON
		// when the server sends the MarkupContent shape).
	}
	if err := json.Unmarshal(raw, &contents); err == nil && contents.Value != "" && contents.Kind != "" {
		h := Hover{Markdown: contents.Value}
		return h
	}
	// MarkedString: {language, value} or a plain string, possibly in an array.
	var single struct {
		Language string `json:"language"`
		Value    string `json:"value"`
	}
	if err := json.Unmarshal(raw, &single); err == nil && single.Value != "" {
		return Hover{Markdown: fencedOrPlain(single.Language, single.Value)}
	}
	var plain string
	if err := json.Unmarshal(raw, &plain); err == nil && plain != "" {
		return Hover{Markdown: plain}
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err == nil {
		var parts []string
		for _, item := range list {
			var one struct {
				Language string `json:"language"`
				Value    string `json:"value"`
			}
			if err := json.Unmarshal(item, &one); err == nil && one.Value != "" {
				parts = append(parts, fencedOrPlain(one.Language, one.Value))
				continue
			}
			var s string
			if err := json.Unmarshal(item, &s); err == nil && s != "" {
				parts = append(parts, s)
			}
		}
		return Hover{Markdown: strings.Join(parts, "\n\n")}
	}
	return Hover{}
}

func fencedOrPlain(lang, value string) string {
	if lang == "" {
		return value
	}
	return "```" + lang + "\n" + value + "\n```"
}

// decodeSymbols flattens documentSymbol results: the hierarchical
// DocumentSymbol[] shape or the flat SymbolInformation[] shape (both legal
// 3.17 responses; gopls sends hierarchical when the client declares the
// hierarchicalDocumentSymbolSupport capability).
func decodeSymbols(raw json.RawMessage) []Symbol {
	if len(raw) == 0 {
		return nil
	}
	// Hierarchical DocumentSymbol: self-recursive wire node with a
	// selectionRange (the flat shape carries a location instead — the
	// discriminator is the key name in the raw body).
	var nodes []hierSymbolNode
	if strings.Contains(string(raw), "\"selectionRange\"") && json.Unmarshal(raw, &nodes) == nil && len(nodes) > 0 {
		var walk func(ns []hierSymbolNode) []Symbol
		walk = func(ns []hierSymbolNode) []Symbol {
			out := make([]Symbol, 0, len(ns))
			for _, n := range ns {
				s := Symbol{Name: n.Name, Kind: n.Kind, KindName: symbolKindName(n.Kind), Detail: n.Detail,
					Line: n.SelectionRange.Start.Line, Character: n.SelectionRange.Start.Character}
				s.Children = walk(n.Children)
				out = append(out, s)
			}
			return out
		}
		return walk(nodes)
	}
	// Flat SymbolInformation shape.
	var flat []struct {
		Name          string       `json:"name"`
		Kind          int          `json:"kind"`
		ContainerName string       `json:"containerName"`
		Location      wireLocation `json:"location"`
	}
	if err := json.Unmarshal(raw, &flat); err == nil && len(flat) > 0 {
		out := make([]Symbol, 0, len(flat))
		for _, f := range flat {
			out = append(out, Symbol{Name: f.Name, Kind: f.Kind, KindName: symbolKindName(f.Kind),
				Line: f.Location.Range.Start.Line, Character: f.Location.Range.Start.Character, Container: f.ContainerName})
		}
		return out
	}
	return nil
}

// hierSymbolNode is the recursive DocumentSymbol wire shape.
type hierSymbolNode struct {
	Name           string           `json:"name"`
	Kind           int              `json:"kind"`
	Detail         string           `json:"detail"`
	Range          wireRange        `json:"range"`
	SelectionRange wireRange        `json:"selectionRange"`
	Children       []hierSymbolNode `json:"children"`
}

// decodeDocumentDiagnosticReport flattens the textDocument/diagnostic result
// (3.17): a FullDocumentDiagnosticReport {"kind":"full","items":[…]} (the
// relatedDocuments member is ignored), the legacy bare Diagnostic[] shape,
// or "unchanged" (nothing new — the caller keeps what it has). ok is false
// when the body carries no usable item list.
func decodeDocumentDiagnosticReport(raw json.RawMessage) ([]Diagnostic, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	var full struct {
		Kind  string           `json:"kind"`
		Items []wireDiagnostic `json:"items"`
	}
	if err := json.Unmarshal(raw, &full); err == nil && full.Kind == "full" {
		out := make([]Diagnostic, 0, len(full.Items))
		for _, w := range full.Items {
			out = append(out, w.flatten())
		}
		return out, true
	}
	var legacy []wireDiagnostic
	if err := json.Unmarshal(raw, &legacy); err == nil && legacy != nil {
		out := make([]Diagnostic, 0, len(legacy))
		for _, w := range legacy {
			out = append(out, w.flatten())
		}
		return out, true
	}
	return nil, false
}

// decodeWorkspaceSymbols flattens the workspace/symbol result: always the
// flat SymbolInformation[] shape (workspace symbols carry real file
// locations, so the location is kept, unlike decodeSymbols' flat branch).
func decodeWorkspaceSymbols(raw json.RawMessage) []WorkspaceSymbol {
	if len(raw) == 0 {
		return nil
	}
	var flat []struct {
		Name          string       `json:"name"`
		Kind          int          `json:"kind"`
		ContainerName string       `json:"containerName"`
		Location      wireLocation `json:"location"`
	}
	if err := json.Unmarshal(raw, &flat); err != nil {
		return nil
	}
	out := make([]WorkspaceSymbol, 0, len(flat))
	for _, f := range flat {
		out = append(out, WorkspaceSymbol{
			Name:      f.Name,
			Kind:      f.Kind,
			KindName:  symbolKindName(f.Kind),
			Container: f.ContainerName,
			Location:  Location{Path: URIToPath(f.Location.URI), Line: f.Location.Range.Start.Line, Character: f.Location.Range.Start.Character},
		})
	}
	return out
}
