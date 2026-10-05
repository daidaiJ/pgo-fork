// This file turns mvdan Words into static values. A word is either fully
// literal (its runtime value is known) or dynamic (an expansion participates,
// so the value is not statically knowable). Quoting matters for detection,
// not for escaping: `r"m"` IS rm at runtime, so concatenated parts form the
// literal value; but `'$(rm -rf x)'` is a plain string with no expansion
// node, so it stays inert.
package shellguard

import (
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// wordVal is the static classification of one word.
type wordVal struct {
	// lit is the concatenated literal value (unescaped). Only meaningful
	// when !dynamic.
	lit string
	// dynamic reports that the word contains an expansion whose value is not
	// statically known (parameter, arithmetic, command/process substitution,
	// brace/extglob expansion).
	dynamic bool
}

// classifyWord is the pure (side-effect free) classification used both by
// the analysis walk and by the cheap pipeline-interpreter scan.
func classifyWord(w *syntax.Word) wordVal {
	if w == nil {
		return wordVal{}
	}
	var v wordVal
	var b strings.Builder
	dynamic := false
	for _, p := range w.Parts {
		switch part := p.(type) {
		case *syntax.Lit:
			b.WriteString(unescapeLit(part.Value))
		case *syntax.SglQuoted:
			// $'...' ANSI-C quotes keep their escapes in Value; treating
			// them literally is conservative for head matching.
			b.WriteString(part.Value)
		case *syntax.DblQuoted:
			for _, q := range part.Parts {
				switch qp := q.(type) {
				case *syntax.Lit:
					b.WriteString(unescapeDQ(qp.Value))
				case *syntax.CmdSubst, *syntax.ProcSubst:
					dynamic = true
				default:
					dynamic = true
				}
			}
		case *syntax.CmdSubst, *syntax.ProcSubst:
			dynamic = true
		default:
			// ParamExp, ArithmExp, brace/extglob expansion, index expressions
			// and anything unmodeled: value unknown, fail closed.
			dynamic = true
		}
	}
	v.lit = b.String()
	v.dynamic = dynamic
	return v
}

// word classifies the word AND analyzes every command/process substitution
// nested inside it: those scripts execute when the word is expanded, no
// matter what the surrounding command does with the value
// (`echo "$(rm -rf x)"` runs the rm).
func (a *analyzer) word(w *syntax.Word) wordVal {
	wv := classifyWord(w)
	a.walkSubstitutions(w)
	return wv
}

// walkSubstitutions finds CmdSubst/ProcSubst nodes anywhere inside a word
// (including inside parameter expansions and double quotes) and analyzes
// their statement lists.
func (a *analyzer) walkSubstitutions(w *syntax.Word) {
	if w == nil {
		return
	}
	for _, p := range w.Parts {
		a.substitutionPart(p)
	}
}

func (a *analyzer) substitutionPart(p syntax.WordPart) {
	switch part := p.(type) {
	case *syntax.CmdSubst:
		if !a.scriptBudget() {
			return
		}
		defer func() { a.depth-- }()
		a.stmtList(part.Stmts)
	case *syntax.ProcSubst:
		if !a.scriptBudget() {
			return
		}
		defer func() { a.depth-- }()
		a.stmtList(part.Stmts)
	case *syntax.ParamExp:
		if part.Index != nil {
			a.arithm(part.Index)
		}
		if part.Repl != nil {
			if part.Repl.Orig != nil {
				a.walkSubstitutions(part.Repl.Orig)
			}
			if part.Repl.With != nil {
				a.walkSubstitutions(part.Repl.With)
			}
		}
		if part.Exp != nil && part.Exp.Word != nil {
			a.walkSubstitutions(part.Exp.Word)
		}
	case *syntax.DblQuoted:
		for _, q := range part.Parts {
			a.substitutionPart(q)
		}
	}
}

// arithm walks an arithmetic expression tree for command substitutions
// nested inside it.
func (a *analyzer) arithm(x syntax.ArithmExpr) {
	switch v := x.(type) {
	case *syntax.Word:
		a.word(v)
	case *syntax.BinaryArithm:
		a.arithm(v.X)
		if v.Y != nil {
			a.arithm(v.Y)
		}
	case *syntax.UnaryArithm:
		a.arithm(v.X)
	case *syntax.ParenArithm:
		a.arithm(v.X)
	}
}

// unescapeLit removes backslash escapes from an unquoted literal segment.
// mvdan preserves the escapes as written (`r\m` → "r\\m"); the runtime word
// value has them resolved, and detection must match the runtime value.
// Backslash-newline continuations are already gone from the value.
func unescapeLit(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\\' && i+1 < len(runes) {
			i++
			b.WriteRune(runes[i])
			continue
		}
		b.WriteRune(runes[i])
	}
	return b.String()
}

// unescapeDQ removes escapes inside double quotes, where a backslash only
// escapes $ ` " \ and newline; any other sequence keeps its backslash.
func unescapeDQ(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	special := map[rune]bool{'$': true, '`': true, '"': true, '\\': true}
	var b strings.Builder
	b.Grow(len(s))
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '\\' && i+1 < len(runes) && special[runes[i+1]] {
			i++
			b.WriteRune(runes[i])
			continue
		}
		b.WriteRune(runes[i])
	}
	return b.String()
}

// normHead normalizes a command word into a comparable program name:
// basename, lowercased, ".exe" suffix stripped (Windows interop, grok's
// normalized_command_head). `/bin/rm`, `RM`, `rm.exe` all normalize to "rm".
func normHead(lit string) string {
	base := filepath.Base(lit)
	base = strings.ToLower(base)
	return strings.TrimSuffix(base, ".exe")
}

// isFdSpec reports whether s is a file-descriptor duplicate target ("0"-"9"
// digits or "-").
func isFdSpec(s string) bool {
	if s == "-" {
		return true
	}
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}
