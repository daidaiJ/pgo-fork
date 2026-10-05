// This file implements the AST walk: statements, pipelines, compound
// commands, redirects and heredocs. The walk never trusts that seeing a
// construct is enough to call it safe — constructs outside the modeled
// subset mark the decision Incomplete rather than being skipped silently.
package shellguard

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// analyzer accumulates findings and fail-closed reasons over one Analyze run.
type analyzer struct {
	src      string
	calls    int
	depth    int
	findings []Finding
	reasons  []string
}

// haz records a matched dangerous rule (dedup by token).
func (a *analyzer) haz(token, desc string) {
	for _, f := range a.findings {
		if f.Token == token {
			return
		}
	}
	a.findings = append(a.findings, Finding{Token: token, Description: desc})
}

// inc records a fail-closed trigger (dedup by reason token).
func (a *analyzer) inc(reason string) {
	for _, r := range a.reasons {
		if r == reason {
			return
		}
	}
	a.reasons = append(a.reasons, reason)
}

// scriptBudget enters one nested script re-analysis (interpreter -c strings,
// heredoc bodies). It reports false when the depth budget is exhausted (and
// records the analysis-limit reason).
func (a *analyzer) scriptBudget() bool {
	a.depth++
	if a.depth > maxScriptDepth {
		a.inc(RAnalysisLimit)
		a.depth--
		return false
	}
	return true
}

// testExpr walks a [[ ]] test expression for embedded command substitutions.
func (a *analyzer) testExpr(x syntax.TestExpr) {
	switch v := x.(type) {
	case *syntax.Word:
		a.word(v)
	case *syntax.BinaryTest:
		a.testExpr(v.X)
		if v.Y != nil {
			a.testExpr(v.Y)
		}
	case *syntax.UnaryTest:
		a.testExpr(v.X)
	case *syntax.ParenTest:
		a.testExpr(v.X)
	}
}

// ifClause walks an if/elif/else chain (Else is the nested else-if chain in
// mvdan, not a statement list).
func (a *analyzer) ifClause(c *syntax.IfClause) {
	for _, cond := range c.Cond {
		a.stmtPipeline(cond)
	}
	a.stmtList(c.Then)
	if c.Else != nil {
		a.ifClause(c.Else)
	}
}

// stmtList walks a statement list sequentially.
func (a *analyzer) stmtList(list []*syntax.Stmt) {
	for _, s := range list {
		a.stmtPipeline(s)
	}
}

// stmtPipeline walks the top of a pipeline. Pipelines (| and |&) are
// right-associative BinaryCmds; they are flattened into a chain first so the
// heredoc-interpreter rule can see the whole pipeline: a heredoc body fed to
// a shell interpreter is code, even when the delimiter is quoted
// (`cat <<'EOF' | sh` — step's test).
func (a *analyzer) stmtPipeline(s *syntax.Stmt) {
	if s == nil {
		return
	}
	chain := flattenPipe(s)
	stdinInterpreter := false
	for _, st := range chain {
		if a.stmtHeadIsStdinInterpreter(st) {
			stdinInterpreter = true
			break
		}
	}
	for _, st := range chain {
		a.stmt(st, stdinInterpreter)
	}
}

// flattenPipe returns the statements of a (possibly single-statement)
// pipeline in execution order. `a | b | c` parses as BinaryCmd(|, a,
// BinaryCmd(|, b, c)), so flattening recurses into X (the earlier stages)
// and appends Y (the final stage).
func flattenPipe(s *syntax.Stmt) []*syntax.Stmt {
	if bin, ok := s.Cmd.(*syntax.BinaryCmd); ok &&
		(bin.Op == syntax.Pipe || bin.Op == syntax.PipeAll) {
		return append(flattenPipe(bin.X), bin.Y)
	}
	return []*syntax.Stmt{s}
}

// stmtHeadIsStdinInterpreter cheaply reports whether the statement is a shell
// interpreter that would read its script from stdin (sh/bash/... with no -c).
// It is a heuristic for the heredoc-interpreter rule only; the full analysis
// in stmt handles the exact semantics. Dynamic or stripped-into heads fall
// back to false (their expansions are still analyzed by the normal walk).
func (a *analyzer) stmtHeadIsStdinInterpreter(s *syntax.Stmt) bool {
	if s == nil || s.Cmd == nil {
		return false
	}
	ce, ok := s.Cmd.(*syntax.CallExpr)
	if !ok || len(ce.Args) == 0 {
		return false
	}
	wv := classifyWord(ce.Args[0])
	if wv.dynamic {
		return false
	}
	if !isInterpreterName(normHead(wv.lit)) {
		return false
	}
	// Look for a literal -c anywhere in the remaining literal words.
	for _, w := range ce.Args[1:] {
		v := classifyWord(w)
		if v.dynamic {
			continue
		}
		if v.lit == "-c" || (len(v.lit) > 2 && v.lit[0] == '-' && containsRune(v.lit[1:], 'c')) {
			return false
		}
	}
	return true
}

// stmt walks one statement: its redirects, then its command.
func (a *analyzer) stmt(s *syntax.Stmt, heredocCode bool) {
	if s == nil {
		return
	}
	a.redirects(s.Redirs, heredocCode)
	if s.Cmd == nil {
		return
	}
	switch c := s.Cmd.(type) {
	case *syntax.CallExpr:
		a.call(s, c, heredocCode)
	case *syntax.BinaryCmd:
		// && || & ; — both sides are independent pipelines. Pipe operators
		// were already consumed by flattenPipe, but compound bodies can hold
		// fresh pipelines.
		a.stmtPipeline(c.X)
		a.stmtPipeline(c.Y)
	case *syntax.Subshell:
		a.stmtList(c.Stmts)
	case *syntax.Block:
		a.stmtList(c.Stmts)
	case *syntax.IfClause:
		a.ifClause(c)
	case *syntax.WhileClause: // UntilClause shares this type (Until bool)
		for _, cond := range c.Cond {
			a.stmtPipeline(cond)
		}
		a.stmtList(c.Do)
	case *syntax.ForClause:
		// The loop list's words are values, but their expansions still
		// execute (`for f in $(cmd)`).
		if wi, ok := c.Loop.(*syntax.WordIter); ok {
			for _, item := range wi.Items {
				a.word(item)
			}
		}
		a.stmtList(c.Do)
	case *syntax.CaseClause:
		a.word(c.Word)
		for _, item := range c.Items {
			for _, pat := range item.Patterns {
				a.word(pat) // patterns may contain expansions
			}
			a.stmtList(item.Stmts)
		}
	case *syntax.TimeClause:
		a.stmtPipeline(c.Stmt) // time is a transparent prefix
	case *syntax.CoprocClause:
		a.stmtPipeline(c.Stmt)
	case *syntax.DeclClause:
		a.declArgs(c.Args)
	case *syntax.TestClause:
		a.testExpr(c.X)
	case *syntax.ArithmCmd:
		a.arithm(c.X)
	case *syntax.LetClause:
		for _, e := range c.Exprs {
			a.arithm(e)
		}
	case *syntax.FuncDecl:
		a.inc(RUnsupportedConstruct)
	default:
		a.inc(RUnsupportedConstruct)
	}
}

// declArgs walks declare/export/local argument assignments. Literal
// declarations are skipped (they cannot execute programs); anything dynamic
// or array-shaped beyond literals marks the decision Incomplete.
func (a *analyzer) declArgs(assigns []*syntax.Assign) {
	for _, as := range assigns {
		a.assign(as)
	}
}

// assign walks one variable assignment's value expression tree.
func (a *analyzer) assign(as *syntax.Assign) {
	if as == nil {
		return
	}
	if as.Value != nil {
		a.word(as.Value)
	}
	if as.Index != nil {
		a.arithm(as.Index)
	}
	if as.Array != nil {
		for _, el := range as.Array.Elems {
			if el == nil {
				continue
			}
			if el.Index != nil {
				a.arithm(el.Index)
			}
			if el.Value != nil {
				a.word(el.Value)
			}
		}
	}
}

// redirects walks a statement's redirects. Write redirects are findings
// (grok's redirect_write floor): only /dev/null is a safe sink, /dev/* is a
// device truncate, everything else is an unverifiable file write. Heredoc
// bodies are analyzed per the heredoc rules.
func (a *analyzer) redirects(redirs []*syntax.Redirect, heredocCode bool) {
	for _, r := range redirs {
		if r == nil {
			continue
		}
		switch r.Op {
		case syntax.RdrOut, syntax.AppOut, syntax.RdrClob, syntax.RdrAll,
			syntax.AppAll, syntax.RdrAllClob, syntax.AppAllClob, syntax.RdrInOut:
			a.writeTarget(r.Word)
		case syntax.DplOut:
			// >&N and >&- duplicate or close a descriptor; anything else is a
			// file write in disguise.
			if r.Word == nil {
				continue
			}
			wv := a.word(r.Word)
			if !wv.dynamic && isFdSpec(wv.lit) {
				continue
			}
			a.writeTarget(r.Word)
		case syntax.Hdoc, syntax.DashHdoc:
			a.heredoc(r, heredocCode)
		case syntax.WordHdoc:
			if r.Word != nil {
				a.word(r.Word) // herestring contents are data; expansions run
			}
		default: // RdrIn, DplIn, anything exotic
			// Reading redirects are safe; exotic operators fail closed.
			if r.Op != syntax.RdrIn && r.Op != syntax.DplIn {
				a.inc(RUnsupportedConstruct)
			}
		}
	}
}

// writeTarget classifies one write-redirect target.
func (a *analyzer) writeTarget(w *syntax.Word) {
	if w == nil {
		a.inc(RShellSyntax)
		return
	}
	wv := a.word(w)
	if wv.dynamic {
		a.haz(FTokenRedirectWrite, "output is redirected to a destination that cannot be verified")
		return
	}
	if wv.lit == "/dev/null" {
		return
	}
	if len(wv.lit) > 5 && wv.lit[:5] == "/dev/" {
		a.haz(FTokenTruncateDevice, "output is redirected into a device node")
		return
	}
	a.haz(FTokenRedirectWrite, "output is redirected into a file")
}

// heredoc analyzes one heredoc body. Two rules apply:
//
//  1. Expansions in an unquoted heredoc execute when the shell builds the
//     body (`cat <<EOF / $(rm -rf x) / EOF`) — the word walk catches them.
//  2. A body fed to a shell interpreter is code regardless of quoting
//     (`sh <<EOF`, `cat <<'EOF' | sh`) — the body text is re-parsed as a
//     script. heredocCode is the pipeline-level interpreter flag.
//
// Divergence from step: the byte-level heredoc boundary re-verification is
// not ported — it exists in step because tree-sitter heredoc spans are
// untrustworthy; mvdan is a typed parser whose Hdoc word spans the exact
// body, so the re-check has nothing to catch. Registered in the spec.
func (a *analyzer) heredoc(r *syntax.Redirect, heredocCode bool) {
	if r.Word == nil {
		a.inc(RShellSyntax)
		return
	}
	if r.Hdoc == nil {
		// Missing body (e.g. truncated input) — the parse succeeded but the
		// redirect is not complete.
		a.inc(RShellSyntax)
		return
	}
	a.word(r.Hdoc)
	if !heredocCode {
		return
	}
	if !a.scriptBudget() {
		return
	}
	defer func() { a.depth-- }()
	body := a.src[r.Hdoc.Pos().Offset():r.Hdoc.End().Offset()]
	a.scriptText(body)
}

// scriptText parses and analyzes a raw script fragment (interpreter -c
// strings, heredoc bodies). Parse failures are fail-closed.
func (a *analyzer) scriptText(text string) {
	file, err := syntax.NewParser().Parse(strings.NewReader(text), "")
	if err != nil {
		a.inc(RShellSyntax)
		return
	}
	a.stmtList(file.Stmts)
}
