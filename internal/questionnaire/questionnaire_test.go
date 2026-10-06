package questionnaire

import (
	"strings"
	"testing"
)

// normalizeOK normalizes in and fails t on error (helper for happy-path cases).
func normalizeOK(t *testing.T, in Input) Questionnaire {
	t.Helper()
	q, err := Normalize(in)
	if err != nil {
		t.Fatalf("Normalize: unexpected error: %v", err)
	}
	return q
}

func TestNormalizeFillsDefaults(t *testing.T) {
	q := normalizeOK(t, Input{Steps: []InputStep{{Question: "Which?"}}})
	if len(q.Steps) != 1 {
		t.Fatalf("want 1 step, got %d", len(q.Steps))
	}
	s := q.Steps[0]
	if s.ID != "q1" {
		t.Errorf("step id default: want q1, got %q", s.ID)
	}
	if s.SelectionMode != Single {
		t.Errorf("selection_mode default: want single, got %q", s.SelectionMode)
	}
	if !s.AllowOther {
		t.Errorf("allow_other default: want true")
	}
	if q.RequiresExplicitResponse {
		t.Errorf("requires_explicit_response default: want false")
	}
}

func TestNormalizeBoundsAndErrors(t *testing.T) {
	tests := []struct {
		name   string
		in     Input
		errHas string
	}{
		{"no steps", Input{}, "steps"},
		{"too many steps", Input{Steps: make([]InputStep, MaxSteps+1)}, "steps"},
		{"empty question", Input{Steps: []InputStep{{Header: "x"}}}, "question is required"},
		{"too many options", Input{Steps: []InputStep{{Question: "q", Options: make([]InputOption, MaxOptions+1)}}}, "options"},
		{"empty label", Input{Steps: []InputStep{{Question: "q", Options: []InputOption{{Description: "d"}}}}}, "empty label"},
		{"bad mode", Input{Steps: []InputStep{{Question: "q", SelectionMode: "all"}}}, "selection_mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Normalize(tt.in)
			if err == nil || !strings.Contains(err.Error(), tt.errHas) {
				t.Fatalf("want error containing %q, got %v", tt.errHas, err)
			}
		})
	}
}

func TestNormalizeKeepsFirstRecommendedOnly(t *testing.T) {
	q := normalizeOK(t, Input{Steps: []InputStep{{
		Question: "q",
		Options: []InputOption{
			{Label: "b", Recommended: true},
			{Label: "a", Recommended: true},
		},
	}}})
	rec := 0
	for _, o := range q.Steps[0].Options {
		if o.Recommended {
			rec++
		}
	}
	if rec != 1 {
		t.Fatalf("want exactly 1 recommended option, got %d", rec)
	}
	if q.Steps[0].Options[0].Label != "b" {
		t.Errorf("kept recommended should be the first one, got %q", q.Steps[0].Options[0].Label)
	}
}

func TestNormalizeExplicitAllowOtherFalse(t *testing.T) {
	f := false
	q := normalizeOK(t, Input{Steps: []InputStep{{Question: "q", AllowOther: &f}}})
	if q.Steps[0].AllowOther {
		t.Errorf("explicit allow_other=false was overridden")
	}
}

func TestDegradedReplyPrefersRecommendedThenFirst(t *testing.T) {
	q := Questionnaire{Steps: []Step{
		{ID: "q1", Options: []Option{{ID: "o1", Label: "first"}, {ID: "o2", Label: "second", Recommended: true}}},
		{ID: "q2", Options: []Option{{ID: "o1", Label: "only"}}},
		{ID: "q3", Options: nil, AllowOther: true},
	}}
	r := DegradedReply(q)
	if r.Source != SourceDegraded {
		t.Errorf("source: want degraded, got %q", r.Source)
	}
	a := r.Answers
	if len(a) != 3 {
		t.Fatalf("want 3 answers, got %d", len(a))
	}
	if strings.Join(a[0].SelectedOptionIDs, ",") != "o2" {
		t.Errorf("step1: want recommended o2, got %v", a[0].SelectedOptionIDs)
	}
	if strings.Join(a[1].SelectedOptionIDs, ",") != "o1" {
		t.Errorf("step2: want first option o1, got %v", a[1].SelectedOptionIDs)
	}
	if !a[2].Skipped {
		t.Errorf("step3: free-text-only step must be skipped in degraded mode")
	}
}

func TestResultTextBranches(t *testing.T) {
	q := Questionnaire{Title: "Deploy", Steps: []Step{
		{ID: "q1", Header: "Rollout", Question: "Strategy?", Options: []Option{
			{ID: "o1", Label: "Blue-green"}, {ID: "o2", Label: "Canary"},
		}, SelectionMode: Multiple},
		{ID: "q2", Question: "Notes?", AllowOther: true},
		{ID: "q3", Question: "Deadline?"},
	}}
	reply := Reply{Title: "Deploy", Source: SourceUser, Answers: []Answer{
		{StepID: "q1", SelectedOptionIDs: []string{"o2", "o1"}},
		{StepID: "q2", OtherText: "use grpc"},
		{StepID: "q3", Skipped: true},
	}}
	got := ResultText(q, reply)
	for _, want := range []string{
		"Questionnaire answered (Deploy)",
		"[source: user]",
		"Rollout — Strategy?: Canary, Blue-green",
		"Notes?: \"use grpc\" (other)",
		"Deadline?: (skipped)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ResultText missing %q in:\n%s", want, got)
		}
	}

	// Degraded source must be flagged so the model knows answers are not
	// user-provided.
	degraded := ResultText(q, Reply{Source: SourceDegraded, Answers: reply.Answers})
	if !strings.Contains(degraded, "[source: degraded") {
		t.Errorf("degraded result not flagged:\n%s", degraded)
	}

	// A missing answer entry renders as "(no answer)" instead of panicking.
	partial := ResultText(q, Reply{Source: SourceUser, Answers: []Answer{{StepID: "q1", SelectedOptionIDs: []string{"o1"}}}})
	if !strings.Contains(partial, "Deadline?: (no answer)") {
		t.Errorf("missing answer not handled:\n%s", partial)
	}
}

func TestResultTextUnknownOptionIDFallsBack(t *testing.T) {
	q := Questionnaire{Steps: []Step{{ID: "q1", Question: "q", Options: []Option{{ID: "o1", Label: "A"}}}}}
	got := ResultText(q, Reply{Source: SourceUser, Answers: []Answer{{StepID: "q1", SelectedOptionIDs: []string{"bogus"}}}})
	if !strings.Contains(got, "bogus") {
		t.Errorf("unknown option id should fall back to the raw id:\n%s", got)
	}
}
