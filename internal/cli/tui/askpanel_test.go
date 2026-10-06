package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/questionnaire"
)

func panelQuestionnaire() questionnaire.Questionnaire {
	return questionnaire.Questionnaire{
		Title: "Ship it",
		Steps: []questionnaire.Step{
			{
				ID:       "q1",
				Header:   "Strategy",
				Question: "How should we roll out?",
				Options: []questionnaire.Option{
					{ID: "o1", Label: "Blue-green", Recommended: true},
					{ID: "o2", Label: "Canary"},
				},
				SelectionMode: questionnaire.Single,
				AllowOther:    true,
			},
			{
				ID:            "q2",
				Question:      "Which services?",
				SelectionMode: questionnaire.Multiple,
				Options: []questionnaire.Option{
					{ID: "o1", Label: "api"},
					{ID: "o2", Label: "web"},
				},
				AllowOther: true,
			},
		},
	}
}

func askKey(s string) tea.KeyPressMsg {
	// KeyPressMsg with Text set is what the tea loop delivers for printable
	// input; the panel only reads String() and Text.
	return tea.KeyPressMsg{Code: 0, Text: s}
}

func TestAskPanelSingleStepFlow(t *testing.T) {
	p := newAskPanel(panelQuestionnaire())
	if !p.active() {
		t.Fatal("fresh panel must be active")
	}
	// "2" picks Canary and advances to the multi-select step in one keypress.
	consumed, done := p.handleKey(askKey("2"))
	if !consumed || done != nil {
		t.Fatalf("step1 '2': consumed=%v done=%v", consumed, done)
	}
	if p.step != 1 {
		t.Fatalf("want step 1 after first answer, got %d", p.step)
	}
	// Multi-select: toggle both, then Enter confirms the selection.
	for _, k := range []string{"1", "2"} {
		if consumed, done := p.handleKey(askKey(k)); !consumed || done != nil {
			t.Fatalf("step2 toggle %q: consumed=%v done=%v", k, consumed, done)
		}
	}
	consumed, done = p.handleKey(askKey("enter"))
	if !consumed || done == nil {
		t.Fatalf("step2 enter should finish: consumed=%v done=%v", consumed, done)
	}
	if p.active() {
		t.Error("panel must be inactive after the last step")
	}
	reply := *done
	if reply.Title != "Ship it" || reply.Source != questionnaire.SourceUser {
		t.Errorf("reply header wrong: %+v", reply)
	}
	if got := reply.Answers[0].SelectedOptionIDs; len(got) != 1 || got[0] != "o2" {
		t.Errorf("step1 answer = %v", got)
	}
	if got := reply.Answers[1].SelectedOptionIDs; len(got) != 2 || got[0] != "o1" || got[1] != "o2" {
		t.Errorf("step2 answer should be in declared order, got %v", got)
	}
}

func TestAskPanelEnterPicksRecommendedAndSkip(t *testing.T) {
	p := newAskPanel(panelQuestionnaire())
	// Enter without any selection picks the recommended option.
	consumed, done := p.handleKey(askKey("enter"))
	if !consumed || done != nil {
		t.Fatalf("enter on step1 should advance only: consumed=%v done=%v", consumed, done)
	}
	if got := p.answers[0].SelectedOptionIDs; len(got) != 1 || got[0] != "o1" {
		t.Errorf("enter should pick the recommended option, got %v", got)
	}
	// Skip the second step to finish.
	_, done = p.handleKey(askKey("s"))
	if done == nil {
		t.Fatal("skip on the last step should finish")
	}
	if !done.Answers[1].Skipped {
		t.Errorf("step2 should be skipped: %+v", done.Answers[1])
	}
}

func TestAskPanelOtherMode(t *testing.T) {
	p := newAskPanel(panelQuestionnaire())
	if consumed, _ := p.handleKey(askKey("o")); !consumed || !p.otherMode {
		t.Fatal("'o' should enter the free-text mode")
	}
	for _, r := range "ship it fast" {
		p.handleKey(askKey(string(r)))
	}
	consumed, done := p.handleKey(askKey("enter"))
	if !consumed || done != nil {
		t.Fatalf("other commit should advance, not finish: consumed=%v done=%v", consumed, done)
	}
	if p.answers[0].OtherText != "ship it fast" {
		t.Errorf("other text = %q", p.answers[0].OtherText)
	}
	// Esc leaves the mode without committing.
	p.handleKey(askKey("o"))
	p.handleKey(askKey("x"))
	p.handleKey(askKey("esc"))
	if p.otherMode {
		t.Fatal("esc should leave other mode")
	}
	if len(p.answers) != 1 {
		t.Errorf("esc must not commit an answer, got %d", len(p.answers))
	}
}

func TestAskPanelDisallowedOtherAndBadKeys(t *testing.T) {
	q := panelQuestionnaire()
	q.Steps[0].AllowOther = false
	p := newAskPanel(q)
	if consumed, _ := p.handleKey(askKey("o")); !consumed || p.otherMode {
		t.Fatal("'o' on a disallowed step must be swallowed with a note")
	}
	if p.note == "" {
		t.Error("expected a note explaining the missing free-text entry")
	}
	if consumed, _ := p.handleKey(askKey("9")); !consumed || p.note == "" {
		t.Error("out-of-range option must be consumed with feedback")
	}
}

func TestAskPanelViewBadgesAndLineCount(t *testing.T) {
	p := newAskPanel(panelQuestionnaire())
	v := p.view(DefaultTheme(), 80)
	for _, want := range []string{"waiting for your answer", "question 1/2", "Ship it", "(recommended)"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q in:\n%s", want, v)
		}
	}
	if n := p.lineCount(); n < 4 {
		t.Errorf("lineCount too small: %d", n)
	}
	p.commit(questionnaire.Answer{StepID: "q1", Skipped: true})
	p.commit(questionnaire.Answer{StepID: "q2", Skipped: true})
	if p.lineCount() != 0 {
		t.Errorf("inactive panel must reserve zero rows, got %d", p.lineCount())
	}
}
