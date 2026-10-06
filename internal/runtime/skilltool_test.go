package runtime

// T6.5 skill-as-tool: a skill's run-config factory may fail (frontmatter model
// that cannot resolve). The failure must surface as the T5.1 result envelope
// (D-7) — a normal tool result the parent model can read — never as a Go error
// and never as a silent fallback to the parent model.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
)

func TestSubAgentFactoryErrorBecomesEnvelope(t *testing.T) {
	sub := NewSubAgentTool(SubAgentSpec{
		Name: "roled",
		NewRunConfigE: func() (RunConfig, error) {
			return RunConfig{}, errors.New(`resolve model "nope": unknown provider`)
		},
	})
	res, err := sub.Execute(context.Background(), "call-1", json.RawMessage(`{"prompt":"do it"}`), nil)
	if err != nil {
		t.Fatalf("Execute = error %v, want a normal envelope result (D-7)", err)
	}
	text := agentcore.ContentToText(res.Content)
	for _, want := range []string{"[subagent result]", "status: failed", "stop_reason: error", `resolve model "nope"`} {
		if !strings.Contains(text, want) {
			t.Errorf("envelope text missing %q:\n%s", want, text)
		}
	}
	env, ok := res.Details.(SubAgentEnvelope)
	if !ok {
		t.Fatalf("Details = %T, want SubAgentEnvelope", res.Details)
	}
	if env.Status != SubAgentStatusFailed || env.AgentID != "call-1" {
		t.Errorf("envelope = %+v, want failed status keyed to call-1", env)
	}
}

func TestSkillToolUsesErrorCapableFactory(t *testing.T) {
	sk := &Skill{
		Frontmatter: SkillFrontmatter{Name: "reader", Description: "reads"},
		Body:        "you read files",
	}
	sub := sk.SkillTool(nil, func([]agentcore.AgentTool) (RunConfig, error) {
		return RunConfig{}, errors.New("boom")
	})
	res, err := sub.Execute(context.Background(), "call-2", json.RawMessage(`{"prompt":"hi"}`), nil)
	if err != nil {
		t.Fatalf("Execute = error %v, want envelope", err)
	}
	if env, ok := res.Details.(SubAgentEnvelope); !ok || env.Status != SubAgentStatusFailed {
		t.Errorf("Details = %+v/%T, want failed envelope", res.Details, res.Details)
	}
	if text := agentcore.ContentToText(res.Content); !strings.Contains(text, "boom") {
		t.Errorf("envelope body lost the factory cause:\n%s", text)
	}
}
