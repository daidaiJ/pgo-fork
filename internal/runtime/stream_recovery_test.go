package runtime

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/provider"
)

// The five recovery points the folded hint must carry (step-Code five points,
// T1.2 spec §6.4).
var recoveryPointMarkers = []string{
	"NOT executed",
	"remains valid",
	"smaller",
	"incrementally",
	"permission constraints still apply",
}

// streamCall records what one StreamFn invocation saw in its request
// projection: the trailing user message's text and the shaped message count.
type streamCall struct {
	lastUserText string
	msgCount     int
}

// scriptedRecoveryStream serves seqs[i] for call i (the last sequence repeats),
// recording each request's trailing-user text. Sequences ride the same shapes
// the transport produces: StreamErrorEvent for a mid-stream failure,
// StreamDoneEvent for a completed response.
func scriptedRecoveryStream(seqs [][]provider.AssistantMessageEvent, calls *[]streamCall) provider.StreamFn {
	i := 0
	return func(ctx context.Context, model string, llm provider.LlmContext, cfg provider.StreamConfig) (*provider.AssistantMessageEventStream, error) {
		var lastUser string
		for j := len(llm.Messages) - 1; j >= 0; j-- {
			if u, ok := llm.Messages[j].(agentcore.UserMessage); ok {
				lastUser = agentcore.ContentToText(u.Content)
				break
			}
		}
		*calls = append(*calls, streamCall{lastUserText: lastUser, msgCount: len(llm.Messages)})
		seq := seqs[i]
		if i < len(seqs)-1 {
			i++
		}
		s := provider.NewAssistantMessageEventStream(0)
		go func() {
			for _, ev := range seq {
				if err := s.Emit(ctx, ev); err != nil {
					s.SetError(err)
					s.Close()
					return
				}
			}
			s.Close()
		}()
		return s, nil
	}
}

func streamErrEvent(msg string) provider.StreamErrorEvent {
	return provider.StreamErrorEvent{Message: agentcore.AssistantMessage{
		RoleField:    agentcore.RoleAssistant,
		StopReason:   agentcore.StopReasonError,
		ErrorMessage: msg,
	}}
}

func streamDoneEvent(text string) provider.StreamDoneEvent {
	return provider.StreamDoneEvent{Message: agentcore.AssistantMessage{
		RoleField:  agentcore.RoleAssistant,
		StopReason: agentcore.StopReasonEndTurn,
		Content:    agentcore.ContentList{agentcore.NewTextContent(text)},
	}}
}

func userMsg(text string) agentcore.UserMessage {
	return agentcore.UserMessage{
		RoleField: agentcore.RoleUser,
		Content:   agentcore.ContentList{agentcore.NewTextContent(text)},
	}
}

// TestFoldStreamRecovery covers the pure projection function table-style: the
// interrupted shape folds, everything else (successes, connect-time failures,
// user aborts, shapes without a trailing user message) is returned unchanged.
func TestFoldStreamRecovery(t *testing.T) {
	cases := []struct {
		name   string
		msgs   agentcore.MessageList
		folded bool
	}{
		{
			name: "read error + trailing user folds",
			msgs: agentcore.MessageList{userMsg("hi"),
				agentcore.AssistantMessage{
					RoleField:    agentcore.RoleAssistant,
					StopReason:   agentcore.StopReasonError,
					ErrorMessage: "read error: transport: network error: connection reset by peer",
				},
				userMsg("continue")},
			folded: true,
		},
		{
			name: "silent-close EmptyResponse sentinel folds",
			msgs: agentcore.MessageList{userMsg("hi"),
				agentcore.AssistantMessage{
					RoleField:    agentcore.RoleAssistant,
					StopReason:   agentcore.StopReasonError,
					ErrorMessage: agentcore.ErrStreamIncomplete.Error(),
				},
				userMsg("continue")},
			folded: true,
		},
		{
			name: "natural end does not fold",
			msgs: agentcore.MessageList{userMsg("hi"),
				agentcore.AssistantMessage{RoleField: agentcore.RoleAssistant, StopReason: agentcore.StopReasonEndTurn},
				userMsg("continue")},
		},
		{
			name: "connect-time failure does not fold (nothing consumed)",
			msgs: agentcore.MessageList{userMsg("hi"),
				agentcore.AssistantMessage{
					RoleField:    agentcore.RoleAssistant,
					StopReason:   agentcore.StopReasonError,
					ErrorMessage: "transport: upstream 401: invalid api key",
				},
				userMsg("continue")},
		},
		{
			name: "user abort does not fold",
			msgs: agentcore.MessageList{userMsg("hi"),
				agentcore.AssistantMessage{
					RoleField:    agentcore.RoleAssistant,
					StopReason:   agentcore.StopReasonError,
					ErrorMessage: "stream aborted",
				},
				userMsg("continue")},
		},
		{
			name: "no trailing user message does not fold",
			msgs: agentcore.MessageList{userMsg("hi"),
				agentcore.AssistantMessage{
					RoleField:    agentcore.RoleAssistant,
					StopReason:   agentcore.StopReasonError,
					ErrorMessage: "read error: transport: network error: connection reset by peer",
				}},
			folded: false,
		},
		{
			name:   "context too short does not fold",
			msgs:   agentcore.MessageList{userMsg("hi")},
			folded: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pristine := append(agentcore.MessageList{}, c.msgs...)
			got := foldStreamRecovery(c.msgs)
			if !c.folded {
				if !reflect.DeepEqual(got, c.msgs) {
					t.Fatalf("foldStreamRecovery changed a non-matching projection")
				}
				return
			}
			user, ok := got[len(got)-1].(agentcore.UserMessage)
			if !ok {
				t.Fatalf("last message = %T, want UserMessage", got[len(got)-1])
			}
			last, ok := user.Content[len(user.Content)-1].(agentcore.TextContent)
			if !ok || last.Text != streamRecoveryHint {
				t.Fatalf("hint not folded verbatim as the last text block: %+v", user.Content)
			}
			if !reflect.DeepEqual(c.msgs, pristine) {
				t.Fatalf("foldStreamRecovery mutated its input (projection-only violation)")
			}
		})
	}
}

// TestStreamRecoveryInjectedIntoNextRequest covers acceptance criteria 1 and 2:
// after a mid-stream interruption the next request carries the hint with all
// five points, while the persisted context never does — so a session write
// (which serializes agentCtx.Messages) cannot contain it and nothing stale can
// revive on resume.
func TestStreamRecoveryInjectedIntoNextRequest(t *testing.T) {
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{userMsg("hi")}}
	var calls []streamCall
	cfg := RunConfig{LoopConfig: LoopConfig{
		Model: "fake",
		Stream: scriptedRecoveryStream([][]provider.AssistantMessageEvent{
			{streamErrEvent("read error: transport: network error: connection reset by peer")},
			{streamDoneEvent("ok")},
		}, &calls),
	}}
	ctx := context.Background()

	// Run 1: the request must NOT carry a hint (no interruption happened yet);
	// it ends with the terminal error and the loop stops.
	collectStream(t, agentLoop(ctx, agentCtx, cfg))

	// Run 2 after a fresh user prompt: the request projection must fold the
	// hint into the trailing user message, exactly once, with all five points.
	agentCtx.Messages = append(agentCtx.Messages, userMsg("continue please"))
	collectStream(t, agentLoop(ctx, agentCtx, cfg))

	if len(calls) != 2 {
		t.Fatalf("stream calls = %d, want 2", len(calls))
	}
	if strings.Contains(calls[0].lastUserText, streamRecoveryHint) {
		t.Fatalf("first request already carried a recovery hint: %q", calls[0].lastUserText)
	}
	if !strings.HasPrefix(calls[1].lastUserText, "continue please") {
		t.Fatalf("hint was not folded into the trailing user message: %q", calls[1].lastUserText)
	}
	if got := strings.Count(calls[1].lastUserText, streamRecoveryHint); got != 1 {
		t.Fatalf("hint occurrences in second request = %d, want 1", got)
	}
	for _, marker := range recoveryPointMarkers {
		if !strings.Contains(calls[1].lastUserText, marker) {
			t.Errorf("recovery hint missing point %q", marker)
		}
	}
	// Acceptance 2: the persisted context (what the session store serializes)
	// holds no hint — the fold touched only the request copy.
	for i, m := range agentCtx.Messages {
		if u, ok := m.(agentcore.UserMessage); ok && strings.Contains(agentcore.ContentToText(u.Content), streamRecoveryHint) {
			t.Errorf("agentCtx.Messages[%d] (persisted) contains the hint", i)
		}
	}
}

// TestStreamRecoveryNoStacking covers acceptance criterion 3: two consecutive
// interruptions still fold exactly one hint per request — earlier hints never
// persisted, so nothing can accumulate.
func TestStreamRecoveryNoStacking(t *testing.T) {
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{userMsg("hi")}}
	var calls []streamCall
	cfg := RunConfig{LoopConfig: LoopConfig{
		Model: "fake",
		Stream: scriptedRecoveryStream([][]provider.AssistantMessageEvent{
			{streamErrEvent("idle timeout: no data received")},
			{streamErrEvent("content stall timeout")},
			{streamDoneEvent("ok")},
		}, &calls),
	}}
	ctx := context.Background()

	collectStream(t, agentLoop(ctx, agentCtx, cfg))            // run 1: interrupted
	agentCtx.Messages = append(agentCtx.Messages, userMsg("r1"))
	collectStream(t, agentLoop(ctx, agentCtx, cfg))            // run 2: interrupted again
	agentCtx.Messages = append(agentCtx.Messages, userMsg("r2"))
	collectStream(t, agentLoop(ctx, agentCtx, cfg))            // run 3: succeeds

	if len(calls) != 3 {
		t.Fatalf("stream calls = %d, want 3", len(calls))
	}
	for i, want := range []int{0, 1, 1} {
		if got := strings.Count(calls[i].lastUserText, streamRecoveryHint); got != want {
			t.Errorf("request %d hint occurrences = %d, want %d", i+1, got, want)
		}
	}
}

// TestStreamRecoveryClearedAfterSuccess covers acceptance criterion 4's
// positive-completion half: once a run ends naturally, later requests carry no
// hint even though an interrupted failure sits deeper in the history.
func TestStreamRecoveryClearedAfterSuccess(t *testing.T) {
	agentCtx := &agentcore.AgentContext{Messages: agentcore.MessageList{userMsg("hi")}}
	var calls []streamCall
	cfg := RunConfig{LoopConfig: LoopConfig{
		Model: "fake",
		Stream: scriptedRecoveryStream([][]provider.AssistantMessageEvent{
			{streamErrEvent("read error: transport: network error: connection reset by peer")},
			{streamDoneEvent("ok")},
			{streamDoneEvent("ok again")},
		}, &calls),
	}}
	ctx := context.Background()

	collectStream(t, agentLoop(ctx, agentCtx, cfg))
	agentCtx.Messages = append(agentCtx.Messages, userMsg("r1"))
	collectStream(t, agentLoop(ctx, agentCtx, cfg)) // recovery request: folds
	agentCtx.Messages = append(agentCtx.Messages, userMsg("r2"))
	collectStream(t, agentLoop(ctx, agentCtx, cfg)) // after success: no hint

	if got := strings.Count(calls[2].lastUserText, streamRecoveryHint); got != 0 {
		t.Fatalf("request after successful turn still carried %d hint(s)", got)
	}
}
