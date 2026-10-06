// This file implements the REPL's ask_user port (T4.2, questionnaire-ask-user.md
// §4.1): the agenttool.QuestionPort that renders a questionnaire on the terminal
// and reads the answers from the shared stdin reader.
//
// It follows the trust.ConfirmToolCall discipline exactly: prompts are written
// from the run-loop producer goroutine, which is safe because streamRun uses an
// unbuffered event stream (EventBuffer=0), and reads go through the REPL's
// single *bufio.Reader under confirmMu so input typed ahead is never split
// between the main loop, a confirmation and a questionnaire. The SIGINT caveat
// is the trust one: a Ctrl+C during a read takes effect only after the answer
// is entered (a post-cancel submission still aborts — the tool checks ctx and
// discards the reply).
package repl

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/questionnaire"
)

// stdinAskPort asks the questionnaire on out / in. mu must be the session's
// confirmMu (shared with the trust and shellguard prompts).
type stdinAskPort struct {
	out io.Writer
	in  *bufio.Reader
	mu  *sync.Mutex
}

// compile-time check: the REPL port satisfies the tool-side seam.
var _ agenttool.QuestionPort = (*stdinAskPort)(nil)

// Ask renders the questionnaire step by step and blocks until every step is
// answered or stdin hits EOF (error). Per step the user enters option numbers
// (space-separated for multiple), `o` for the free-text entry, `s` to skip, or
// Enter for the recommended option.
func (p *stdinAskPort) Ask(ctx context.Context, q questionnaire.Questionnaire) (questionnaire.Reply, error) {
	if err := ctx.Err(); err != nil {
		return questionnaire.Reply{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if q.Title != "" {
		fmt.Fprintf(p.out, "\npigo asks — %s\n", q.Title)
	} else {
		fmt.Fprintln(p.out, "\npigo asks:")
	}
	reply := questionnaire.Reply{Title: q.Title, Source: questionnaire.SourceUser}
	for i, s := range q.Steps {
		ans, err := p.askStep(ctx, i, len(q.Steps), s)
		if err != nil {
			return questionnaire.Reply{}, err
		}
		reply.Answers = append(reply.Answers, ans)
	}
	return reply, nil
}

// askStep renders one step and reads its answer, re-prompting on invalid input.
func (p *stdinAskPort) askStep(ctx context.Context, idx, total int, s questionnaire.Step) (questionnaire.Answer, error) {
	fmt.Fprintf(p.out, "\n[%d/%d]", idx+1, total)
	if s.Header != "" {
		fmt.Fprintf(p.out, " %s:", s.Header)
	}
	fmt.Fprintf(p.out, " %s\n", s.Question)
	if s.Description != "" {
		fmt.Fprintf(p.out, "  %s\n", s.Description)
	}
	rec := -1
	for j, o := range s.Options {
		tag := ""
		if o.Recommended {
			tag = " (recommended)"
			rec = j
		}
		if o.Description != "" {
			fmt.Fprintf(p.out, "  %d) %s — %s%s\n", j+1, o.Label, o.Description, tag)
		} else {
			fmt.Fprintf(p.out, "  %d) %s%s\n", j+1, o.Label, tag)
		}
	}
	hint := "number(s)"
	if s.SelectionMode == questionnaire.Single {
		hint = "a number"
	}
	other := ""
	if s.AllowOther {
		other = ", o=other"
	}
	fmt.Fprintf(p.out, "  Choose %s%s, s=skip", hint, other)
	if rec >= 0 {
		fmt.Fprintf(p.out, ", Enter=recommended (%s)", s.Options[rec].Label)
	}
	fmt.Fprint(p.out, ": ")

	multiple := s.SelectionMode == questionnaire.Multiple
	for {
		if err := ctx.Err(); err != nil {
			return questionnaire.Answer{}, err
		}
		line, err := p.in.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			return questionnaire.Answer{}, fmt.Errorf("input ended before the questionnaire was answered")
		}
		// Enter picks the recommended option when there is one (the hint line
		// advertises it); with none it falls through to the re-prompt below.
		if strings.TrimSpace(line) == "" && rec >= 0 {
			return questionnaire.Answer{StepID: s.ID, SelectedOptionIDs: []string{s.Options[rec].ID}}, nil
		}
		ans, ok := parseStepAnswer(s, line, multiple)
		if ok {
			return ans, nil
		}
		if strings.TrimSpace(line) == "" {
			// Empty input with no recommended option: re-prompt (readMenuChoice
			// semantics would default, but an answer here must be deliberate).
			fmt.Fprint(p.out, "  (enter a choice): ")
		} else {
			fmt.Fprint(p.out, "  (invalid choice, try again): ")
		}
		if err != nil {
			return questionnaire.Answer{}, fmt.Errorf("input ended before the questionnaire was answered")
		}
	}
}

// parseStepAnswer turns one input line into a step answer, reporting whether
// the line was a valid choice. `o` reads the free text on the SAME line after
// "o " (so a paste lands whole) — "o <text>"; bare "o" re-prompts. `s` skips.
func parseStepAnswer(s questionnaire.Step, line string, multiple bool) (questionnaire.Answer, bool) {
	input := strings.TrimSpace(line)
	lower := strings.ToLower(input)
	switch {
	case lower == "s" || lower == "skip":
		return questionnaire.Answer{StepID: s.ID, Skipped: true}, true
	case strings.HasPrefix(lower, "o "):
		text := strings.TrimSpace(input[2:])
		if text == "" {
			return questionnaire.Answer{}, false
		}
		return questionnaire.Answer{StepID: s.ID, OtherText: text}, true
	case lower == "o" || lower == "other":
		return questionnaire.Answer{}, false
	}
	if input == "" {
		return questionnaire.Answer{}, false
	}
	fields := strings.Fields(input)
	if !multiple && len(fields) > 1 {
		return questionnaire.Answer{}, false
	}
	var ids []string
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 || n > len(s.Options) {
			return questionnaire.Answer{}, false
		}
		ids = append(ids, s.Options[n-1].ID)
	}
	return questionnaire.Answer{StepID: s.ID, SelectedOptionIDs: ids}, true
}
