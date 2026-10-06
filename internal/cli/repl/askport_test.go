package repl

import (
	"bufio"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/smallnest/pigo/internal/questionnaire"
)

func sampleQuestionnaire() questionnaire.Questionnaire {
	return questionnaire.Questionnaire{
		Title: "Deploy",
		Steps: []questionnaire.Step{
			{
				ID:       "q1",
				Header:   "Rollout",
				Question: "Strategy?",
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
				AllowOther: false,
			},
		},
	}
}

func TestStdinAskPortRoundTrip(t *testing.T) {
	// "o canary at 10%" exercises the same-line free text; "1 2" the
	// space-separated multi-select.
	in := bufio.NewReader(strings.NewReader("o canary at 10%\n1 2\n"))
	var out strings.Builder
	p := &stdinAskPort{out: &out, in: in, mu: &sync.Mutex{}}
	reply, err := p.Ask(context.Background(), sampleQuestionnaire())
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if reply.Source != questionnaire.SourceUser || reply.Title != "Deploy" {
		t.Errorf("reply header wrong: %+v", reply)
	}
	a := reply.Answers
	if len(a) != 2 {
		t.Fatalf("want 2 answers, got %d", len(a))
	}
	if a[0].OtherText != "canary at 10%" {
		t.Errorf("step1 other text = %q", a[0].OtherText)
	}
	if strings.Join(a[1].SelectedOptionIDs, ",") != "o1,o2" {
		t.Errorf("step2 selection = %v", a[1].SelectedOptionIDs)
	}
	render := out.String()
	for _, want := range []string{"pigo asks — Deploy", "[1/2] Rollout: Strategy?", "(recommended)", "[2/2] Which services?"} {
		if !strings.Contains(render, want) {
			t.Errorf("render missing %q in:\n%s", want, render)
		}
	}
}

func TestStdinAskPortEnterPicksRecommended(t *testing.T) {
	in := bufio.NewReader(strings.NewReader("\n1\n"))
	p := &stdinAskPort{out: &strings.Builder{}, in: in, mu: &sync.Mutex{}}
	reply, err := p.Ask(context.Background(), sampleQuestionnaire())
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if strings.Join(reply.Answers[0].SelectedOptionIDs, ",") != "o1" {
		t.Errorf("Enter should pick the recommended option, got %v", reply.Answers[0].SelectedOptionIDs)
	}
}

func TestStdinAskPortInvalidInputReprompts(t *testing.T) {
	// 99 out of range, then a valid pick; the multi-select step rejects the
	// single-number mode's extra token then takes one number.
	in := bufio.NewReader(strings.NewReader("99\n2\n5\n2\n"))
	var out strings.Builder
	p := &stdinAskPort{out: &out, in: in, mu: &sync.Mutex{}}
	reply, err := p.Ask(context.Background(), sampleQuestionnaire())
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if strings.Join(reply.Answers[0].SelectedOptionIDs, ",") != "o2" {
		t.Errorf("after re-prompt want o2, got %v", reply.Answers[0].SelectedOptionIDs)
	}
	if strings.Count(out.String(), "(invalid choice") < 2 {
		t.Errorf("expected re-prompt feedback, got:\n%s", out.String())
	}
}

func TestStdinAskPortSkipAndEOF(t *testing.T) {
	in := bufio.NewReader(strings.NewReader("s\ns\n"))
	p := &stdinAskPort{out: &strings.Builder{}, in: in, mu: &sync.Mutex{}}
	reply, err := p.Ask(context.Background(), sampleQuestionnaire())
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !reply.Answers[0].Skipped || !reply.Answers[1].Skipped {
		t.Errorf("both steps should be skipped: %+v", reply.Answers)
	}

	// EOF before the questionnaire completes → error, not a silent default.
	p2 := &stdinAskPort{out: &strings.Builder{}, in: bufio.NewReader(strings.NewReader("2\n")), mu: &sync.Mutex{}}
	if _, err := p2.Ask(context.Background(), sampleQuestionnaire()); err == nil {
		t.Errorf("EOF mid-questionnaire should error")
	}
}

func TestParseStepAnswerSingleRejectsMultiTokens(t *testing.T) {
	s := sampleQuestionnaire().Steps[0]
	if _, ok := parseStepAnswer(s, "1 2\n", false); ok {
		t.Errorf("single mode must reject multiple tokens")
	}
	ans, ok := parseStepAnswer(s, "2\n", false)
	if !ok || strings.Join(ans.SelectedOptionIDs, ",") != "o2" {
		t.Errorf("single mode pick failed: %+v ok=%v", ans, ok)
	}
	if ans, ok := parseStepAnswer(s, "s\n", false); !ok || !ans.Skipped {
		t.Errorf("skip failed: %+v ok=%v", ans, ok)
	}
	if _, ok := parseStepAnswer(s, "o\n", false); ok {
		t.Errorf("bare 'o' without text must re-prompt")
	}
	if ans, ok := parseStepAnswer(s, "O direct pin\n", false); !ok || ans.OtherText != "direct pin" {
		t.Errorf("case-insensitive other failed: %+v ok=%v", ans, ok)
	}
}
