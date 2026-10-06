// Package questionnaire is the T4.2 multi-step ask-user schema (leaf package,
// stdlib only): the question/option/step types shared by the ask_user tool and
// its per-driver ports, plus normalization and result rendering.
//
// It is a trimmed port of minimax-code's questionnaire V2 schema
// (packages/shared/src/questionnaire.ts): one question per step, options with
// stable ids, at most one recommended option per step, an Others free-text
// entry, and explicit skip as the only exemption from the required invariant.
// Deviations are registered in wiki/port/questionnaire-ask-user.md §7 (no
// images, no feature-enable/plan modes, no daemon round-trip).
package questionnaire

import (
	"fmt"
	"strings"
)

// Bounds mirror the minimax ask_user tool schema: at most 4 steps per
// questionnaire and 4 options per step (a step with zero options is valid when
// it is free-text only).
const (
	MaxSteps   = 4
	MaxOptions = 4
)

// Reply source tags: who produced the answers.
const (
	// SourceUser marks answers an interactive user actually entered.
	SourceUser = "user"
	// SourceDegraded marks answers auto-selected because no interactive user
	// was available (headless/SDK/webhook; wiki spec §4.3).
	SourceDegraded = "degraded"
)

// SelectionMode controls how many options a step accepts.
type SelectionMode string

const (
	// Single accepts at most one option (the default).
	Single SelectionMode = "single"
	// Multiple accepts any subset of the options.
	Multiple SelectionMode = "multiple"
)

// Valid reports whether s is one of the two accepted modes.
func (s SelectionMode) Valid() bool { return s == Single || s == Multiple }

// Option is one selectable answer choice within a step.
type Option struct {
	// ID is stable within the step; reply payloads reference it.
	ID string
	// Label is the option text shown to the user.
	Label string
	// Description is optional extra hint text rendered next to the label.
	Description string
	// Recommended marks the safe default. Normalize keeps at most the first
	// recommended option per step and strips the rest (minimax semantics).
	Recommended bool
}

// Step holds exactly one question with its own options and selection mode.
type Step struct {
	// ID is stable within the questionnaire ("q1", "q2", … when the model did
	// not supply one); reply payloads reference it.
	ID string
	// Header is an optional short section label.
	Header string
	// Question is the question body (required).
	Question string
	// Description is optional extra context for the question.
	Description string
	// Options are the selectable choices (0..MaxOptions; empty with AllowOther
	// makes a free-text-only step).
	Options []Option
	// SelectionMode is single (default) or multiple.
	SelectionMode SelectionMode
	// AllowOther permits the free-text answer path (default true).
	AllowOther bool
}

// Questionnaire is the normalized multi-step question set handed to a port.
type Questionnaire struct {
	// Title is an optional overall heading.
	Title string
	// Steps are the 1..MaxSteps questions, asked in order.
	Steps []Step
	// RequiresExplicitResponse marks a terminal confirmation that a degraded
	// (no-user) port must NOT auto-answer (wiki spec §4.3).
	RequiresExplicitResponse bool
}

// Answer is the user's (or degraded) response to one step.
type Answer struct {
	// StepID references the answered step.
	StepID string
	// SelectedOptionIDs holds the chosen option ids (at most one for single).
	SelectedOptionIDs []string
	// OtherText carries the free-form entry when the Others path was taken.
	OtherText string
	// Skipped marks an explicit skip (overrides the required invariant).
	Skipped bool
}

// Reply is the complete answer set returned by a port.
type Reply struct {
	// Title echoes the questionnaire title ("" when none).
	Title string
	// Answers has one entry per step, in step order.
	Answers []Answer
	// Source is SourceUser or SourceDegraded.
	Source string
}

// InputOption is an option as authored by the model (ids optional —
// Normalize fills them).
type InputOption struct {
	ID          string `json:"id,omitempty"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
}

// InputStep is a step as authored by the model; every field except question is
// optional.
type InputStep struct {
	ID            string        `json:"id,omitempty"`
	Header        string        `json:"header,omitempty"`
	Question      string        `json:"question"`
	Description   string        `json:"description,omitempty"`
	Options       []InputOption `json:"options,omitempty"`
	SelectionMode SelectionMode `json:"selection_mode,omitempty"`
	// AllowOther defaults to true; the pointer distinguishes an explicit
	// false from an omitted field.
	AllowOther *bool `json:"allow_other,omitempty"`
}

// Input is the full ask_user tool input as authored by the model.
type Input struct {
	Title                    string      `json:"title,omitempty"`
	Steps                    []InputStep `json:"steps,omitempty"`
	RequiresExplicitResponse bool        `json:"requires_explicit_response,omitempty"`
}

// Normalize validates the model-authored input and produces the canonical
// Questionnaire: fills missing step/option ids, applies defaults
// (selection_mode=single, allow_other=true), bounds the shape (1..MaxSteps,
// 0..MaxOptions), and keeps only the first recommended option per step. It
// returns an error describing the first structural violation.
func Normalize(in Input) (Questionnaire, error) {
	var q Questionnaire
	q.Title = strings.TrimSpace(in.Title)
	q.RequiresExplicitResponse = in.RequiresExplicitResponse
	if n := len(in.Steps); n < 1 || n > MaxSteps {
		return q, fmt.Errorf("steps must contain 1..%d items, got %d", MaxSteps, n)
	}
	for i, si := range in.Steps {
		step := Step{
			ID:          strings.TrimSpace(si.ID),
			Header:      strings.TrimSpace(si.Header),
			Question:    strings.TrimSpace(si.Question),
			Description: strings.TrimSpace(si.Description),
		}
		if step.ID == "" {
			step.ID = fmt.Sprintf("q%d", i+1)
		}
		if step.Question == "" {
			return q, fmt.Errorf("step %d (%s): question is required", i+1, step.ID)
		}
		if si.SelectionMode == "" {
			step.SelectionMode = Single
		} else if si.SelectionMode.Valid() {
			step.SelectionMode = si.SelectionMode
		} else {
			return q, fmt.Errorf("step %d (%s): invalid selection_mode %q (want single|multiple)", i+1, step.ID, si.SelectionMode)
		}
		if si.AllowOther != nil {
			step.AllowOther = *si.AllowOther
		} else {
			step.AllowOther = true
		}
		if n := len(si.Options); n > MaxOptions {
			return q, fmt.Errorf("step %d (%s): at most %d options, got %d", i+1, step.ID, MaxOptions, n)
		}
		recommendedSeen := false
		for j, oi := range si.Options {
			opt := Option{
				ID:          strings.TrimSpace(oi.ID),
				Label:       strings.TrimSpace(oi.Label),
				Description: strings.TrimSpace(oi.Description),
			}
			if opt.ID == "" {
				opt.ID = fmt.Sprintf("o%d", j+1)
			}
			if opt.Label == "" {
				return q, fmt.Errorf("step %d (%s): option %d (%s) has an empty label", i+1, step.ID, j+1, opt.ID)
			}
			if oi.Recommended && !recommendedSeen {
				opt.Recommended = true
				recommendedSeen = true
			}
			step.Options = append(step.Options, opt)
		}
		q.Steps = append(q.Steps, step)
	}
	return q, nil
}

// DegradedReply auto-answers q for the no-user case: per step the recommended
// option, else the first option; a step with no options (free-text only) is
// skipped — a machine must not invent user prose. Callers must not use this
// when q.RequiresExplicitResponse is set (wiki spec §4.3).
func DegradedReply(q Questionnaire) Reply {
	reply := Reply{Title: q.Title, Source: SourceDegraded}
	for _, s := range q.Steps {
		ans := Answer{StepID: s.ID}
		for _, o := range s.Options {
			if o.Recommended {
				ans.SelectedOptionIDs = []string{o.ID}
				break
			}
		}
		if len(ans.SelectedOptionIDs) == 0 && len(s.Options) > 0 {
			ans.SelectedOptionIDs = []string{s.Options[0].ID}
		}
		if len(ans.SelectedOptionIDs) == 0 {
			ans.Skipped = true
		}
		reply.Answers = append(reply.Answers, ans)
	}
	return reply
}

// ResultText renders q + reply as the human-readable text returned to the
// model as the tool result (one line per step, in step order).
func ResultText(q Questionnaire, reply Reply) string {
	var b strings.Builder
	b.WriteString("Questionnaire answered")
	if q.Title != "" {
		fmt.Fprintf(&b, " (%s)", q.Title)
	}
	switch reply.Source {
	case SourceDegraded:
		b.WriteString(" [source: degraded — no interactive user; answers were auto-selected, not user-provided]")
	default:
		b.WriteString(" [source: user]")
	}
	byStep := make(map[string]Answer, len(reply.Answers))
	for _, a := range reply.Answers {
		byStep[a.StepID] = a
	}
	for i, s := range q.Steps {
		a, ok := byStep[s.ID]
		b.WriteString("\n")
		label := s.Question
		if s.Header != "" {
			label = s.Header + " — " + s.Question
		}
		if !ok {
			fmt.Fprintf(&b, "%d. %s: (no answer)", i+1, label)
			continue
		}
		switch {
		case a.Skipped:
			fmt.Fprintf(&b, "%d. %s: (skipped)", i+1, label)
		case a.OtherText != "":
			fmt.Fprintf(&b, "%d. %s: %q (other)", i+1, label, a.OtherText)
		default:
			names := make([]string, 0, len(a.SelectedOptionIDs))
			for _, id := range a.SelectedOptionIDs {
				names = append(names, optionLabel(s, id))
			}
			fmt.Fprintf(&b, "%d. %s: %s", i+1, label, strings.Join(names, ", "))
		}
	}
	return b.String()
}

// optionLabel resolves an option id to its label, falling back to the raw id
// when the port returned an unknown id (defensive: ports are pigo-owned).
func optionLabel(s Step, id string) string {
	for _, o := range s.Options {
		if o.ID == id {
			return o.Label
		}
	}
	return id
}
