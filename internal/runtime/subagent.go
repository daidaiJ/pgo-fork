// This file implements sub-agent orchestration (US-027, #45) with an optional
// process-isolation mode (US-019, #135).
//
// A sub-agent is a full agent loop with its own AgentContext (independent system
// prompt, message history and tool set), launched by the parent through a normal
// tool call. The child runs to completion and its final assistant text is fed
// back to the parent as the tool result - so from the parent loop's perspective
// a sub-agent is just another tool.
//
// Two isolation modes are supported, selected by SubAgentSpec.Isolation:
//
//   - Goroutine (default): the child loop runs in-process in a goroutine sharing
//     the parent process, matching the original "single-process goroutine" decision.
//   - Process: the parent spawns a fresh pigo subprocess (pigo --subagent-rpc)
//     and delegates the run over stdio JSON-RPC (reusing internal/jsonrpc). The
//     child runs in a separate process, so a crash or resource leak in the child
//     cannot affect the parent loop; a crash is surfaced as a tool error. The
//     subprocess resolves its own provider from the model/provider passed in the
//     request and inherits the parent environment for credentials.
//
// Because each Execute call spins up an independent run, multiple sub-agents can
// run concurrently (the batch executor already runs parallel tool calls in
// separate goroutines/processes).
package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/compaction"
	"github.com/smallnest/pigo/internal/jsonrpc"
	"github.com/smallnest/pigo/internal/provider"
)

// SubAgentIsolation selects how a sub-agent runs relative to its parent.
type SubAgentIsolation int

const (
	// SubAgentIsolationGoroutine runs the child agent loop in-process in a
	// goroutine. This is the default and the original behavior; it changes
	// nothing about how sub-agents previously ran.
	SubAgentIsolationGoroutine SubAgentIsolation = iota
	// SubAgentIsolationProcess runs the child in a fresh pigo subprocess,
	// delegating the run over stdio JSON-RPC. A subprocess crash is surfaced to
	// the parent as a tool error and never affects the parent loop.
	SubAgentIsolationProcess
)

// SubAgentProcessConfig configures process-isolated sub-agent execution. It
// carries the serializable provider config the subprocess needs to reconstruct
// the run: the in-process Stream/GetAPIKey functions a goroutine-mode
// NewRunConfig returns cannot cross a process boundary, so the parent forwards
// the model (and optional base URL/protocol) and the subprocess resolves the
// provider itself, inheriting the parent environment for API keys.
type SubAgentProcessConfig struct {
	// Command is the executable to spawn. When empty, os.Executable() (the pigo
	// binary itself) is used so a pigo process spawns another pigo.
	Command string
	// Args are appended to the command after the subagent-rpc flag. Rarely
	// needed; reserved for test doubles or non-standard layouts.
	Args []string
	// Model is the model id the subprocess runs against. Required. A preset id
	// (e.g. "openrouter/free", "anthropic/claude-...") or ollama/nvidia-prefixed
	// id resolves its own provider; a custom gateway needs BaseURL/Protocol.
	Model string
	// BaseURL and Protocol override the provider endpoint and wire protocol for
	// custom gateways (Protocol "anthropic"/"openai" forces that wire format).
	// Empty falls back to the same resolution the CLI uses.
	BaseURL  string
	Protocol string
	// ToolNames restricts the subprocess's builtin tool set to the named tools
	// (e.g. a read-only researcher). Empty keeps all builtins. Non-builtin names
	// are ignored: custom/plugin tools cannot cross a process boundary, so a
	// process-isolated child runs with builtins only.
	ToolNames []string
	// Env is the child's environment (os/exec form). When nil the child inherits
	// the parent environment, which is how it picks up provider API keys.
	Env []string
	// Dir is the child's working directory; empty means the parent's.
	Dir string
	// Stderr optionally receives the child's stderr. When nil it is discarded.
	Stderr io.Writer
}

// SubAgentRunParams is the JSON-RPC request payload for a process-isolated
// sub-agent run (the "subagent/run" method). It is the wire contract between
// the parent (SubAgentTool in process mode) and the pigo subprocess
// (cmd/pigo --subagent-rpc).
type SubAgentRunParams struct {
	Prompt       string   `json:"prompt"`
	SystemPrompt string   `json:"systemPrompt,omitempty"`
	Model        string   `json:"model"`
	BaseURL      string   `json:"baseUrl,omitempty"`
	Protocol     string   `json:"protocol,omitempty"`
	Tools        []string `json:"tools,omitempty"`
}

// SubAgentRunResult is the JSON-RPC response payload carrying the child's final
// assistant text plus the normalized stop reason (T5.1 envelope): stopReason is
// always set on a settled run (empty only for a transport-level failure, which
// is an RPC error instead), so the parent can build the result envelope even
// when the child failed. errorMessage carries the child's diagnostic (loop
// error text) when stopReason is "error". The extra fields are omitempty, so
// the wire contract stays backward compatible with older peers.
type SubAgentRunResult struct {
	Text         string `json:"text"`
	StopReason   string `json:"stopReason,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// SubAgentRPCMethod is the JSON-RPC method name the parent calls on the
// subprocess: "subagent/run".
const SubAgentRPCMethod = "subagent/run"

// SubAgentRPCFlag is the command-line flag the parent launches the pigo
// subprocess with so it enters the sub-agent RPC server mode: "--subagent-rpc".
const SubAgentRPCFlag = "--subagent-rpc"

// SubAgentTarget identifies the provider/model triple a sub-agent run is pinned
// to (T7.1 resume, ruling P2). It is recorded in the settled run's meta and
// compared on resume: an identical triple rides the tool's normal factory (the
// same-process case, where the factory's triple is invariant), while a
// different one is re-resolved through NewRunConfigFor — a cross-process resume
// where the resuming process runs another model. ContextWindow is the effective
// window the resume budget policy checks against (0 = unknown, skip the check).
type SubAgentTarget struct {
	Model         string
	BaseURL       string
	Protocol      string
	ProviderName  string
	Proxy         string
	ContextWindow int
}

// SubAgentSpec declares a spawnable sub-agent: its identity (surfaced to the
// model as a tool), the system prompt and tools its child context runs with,
// and a factory for the child's run configuration (provider stream, batch
// registry, hooks). The factory is called once per spawn so each child gets an
// independent RunConfig; NewRunConfig must wire a ToolRegistry consistent with
// Tools. It is used by goroutine mode; process mode uses Process instead (the
// subprocess builds its own RunConfig from the serializable provider config).
type SubAgentSpec struct {
	// Name is the tool name the parent invokes to spawn this sub-agent.
	Name string
	// Description is injected into the parent's tool list / capability list so
	// the model knows when to delegate.
	Description string
	// SystemPrompt seeds the child context's system prompt. When empty the child
	// runs with no system prompt.
	SystemPrompt string
	// Tools is the child's independent tool set. It may differ from the parent's
	// (e.g. a read-only researcher sub-agent) and may be empty. In goroutine
	// mode these exact tools run in-process; in process mode only the tools'
	// NAMES are forwarded (the subprocess rebuilds builtins by name).
	Tools []agentcore.AgentTool
	// NewRunConfig builds the loop configuration for one child run in goroutine
	// mode. It is called per spawn; the returned config's Batch registry should
	// contain Tools. Ignored in process mode (the subprocess resolves its own).
	NewRunConfig func() RunConfig
	// NewRunConfigE, when non-nil, replaces NewRunConfig and may fail: a
	// spawn-time factory error (e.g. a skill's frontmatter model that cannot be
	// resolved) is normalized into the T5.1 result envelope (D-7 semantics —
	// failed status + the cause in the body) instead of a Go error, so the
	// parent model can read next_step and act. It takes precedence over
	// NewRunConfig when both are set.
	NewRunConfigE func() (RunConfig, error)
	// Isolation selects goroutine (default) vs process execution. Zero value is
	// goroutine, preserving the original behavior.
	Isolation SubAgentIsolation
	// Process configures process-isolated execution. Required when Isolation is
	// SubAgentIsolationProcess; ignored otherwise.
	Process SubAgentProcessConfig
	// Schema, when non-empty, overrides the default single-prompt argument schema
	// advertised to the model. The generic task tool uses this to also accept an
	// optional description; a nil/empty Schema keeps the original prompt-only
	// schema so existing specs are unaffected.
	Schema json.RawMessage
	// Sem, when non-nil, is a shared buffered channel used as a concurrency
	// semaphore for goroutine-mode runs: executeGoroutine acquires a slot before
	// spawning the child and releases it when the child settles. A full channel
	// blocks (queues) the acquire rather than erroring. nil disables limiting, so
	// existing sub-agent specs run unbounded exactly as before.
	Sem chan struct{}
	// Store, when non-nil and bound to a session, persists each settled child's
	// transcript and meta so a later dispatch can resume it (T7.1). nil (or an
	// unbound store) keeps persistence off, and a resume request then fails
	// closed rather than silently starting fresh.
	Store *SubagentStore
	// Target is the provider/model triple the tool's factory runs (the "current"
	// triple). A resume whose source triple equals it rides the factory; a
	// different one is re-resolved through NewRunConfigFor.
	Target SubAgentTarget
	// NewRunConfigFor builds a run configuration pinned to an explicit target.
	// It is consulted only when a resume's source triple differs from Target
	// (a cross-process resume after a model change); a nil resolver makes such a
	// resume degrade to a fresh run (P2) rather than silently un-pinning.
	NewRunConfigFor func(SubAgentTarget) (RunConfig, error)
}

// subAgentArgs is the JSON argument shape for a sub-agent tool call: a
// free-form prompt describing the delegated task, plus an optional short
// description used for status display (accepted by the generic task tool;
// ignored by prompt-only specs) and an optional resume handle.
type subAgentArgs struct {
	Prompt      string `json:"prompt"`
	Description string `json:"description,omitempty"`
	// Resume, when set, is the agent_id of a previously settled sub-agent in
	// this session whose transcript is replayed as the new run's prefix (T7.1).
	Resume string `json:"resume,omitempty"`
}

// subAgentSchema is the JSON Schema validating a sub-agent invocation.
var subAgentSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "prompt": {
      "type": "string",
      "description": "The task for the sub-agent to perform, described in full since the sub-agent runs with a fresh context."
    }
  },
  "required": ["prompt"],
  "additionalProperties": false
}`)

// SubAgentTool adapts a SubAgentSpec into an AgentTool. Executing it spawns a
// child agent run (in a goroutine or a subprocess, per Isolation) and returns
// the child's final text.
type SubAgentTool struct {
	spec SubAgentSpec
	// processCall, when non-nil, overrides the default subprocess transport for
	// process-isolated mode. Tests inject a fake to exercise the process-mode
	// logic (params shaping, crash-as-error, result forwarding) without building
	// a real binary; production leaves it nil so Execute uses defaultProcessCall.
	processCall func(ctx context.Context, cfg SubAgentProcessConfig, params SubAgentRunParams) (SubAgentRunResult, error)
}

// NewSubAgentTool builds a sub-agent tool from a spec. In goroutine mode
// NewRunConfig is required (it supplies the provider stream that drives the
// child); in process mode Process.Model is required instead.
func NewSubAgentTool(spec SubAgentSpec) *SubAgentTool {
	return &SubAgentTool{spec: spec}
}

func (t *SubAgentTool) Name() string { return t.spec.Name }

// Effect declares the sub-agent tool (T5.2): not read-only — its children
// can mutate anything the parent could — and its reach escapes the
// workspace (ScopeSystem).
func (t *SubAgentTool) Effect() agentcore.ToolEffect {
	return agentcore.ToolEffect{Scope: agentcore.ScopeSystem}
}

func (t *SubAgentTool) Description() string { return t.spec.Description }

func (t *SubAgentTool) Schema() json.RawMessage {
	if len(t.spec.Schema) > 0 {
		return t.spec.Schema
	}
	return subAgentSchema
}

// ExecutionMode is parallel: independent sub-agents may run concurrently, since
// each spawns its own context and run (goroutine or process) with no shared
// mutable state.
func (t *SubAgentTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}

// Execute spawns the child agent run and blocks until it settles, then returns
// the child's final assistant text as the tool result. The parent's ctx governs
// the child, so cancelling the parent run cancels in-flight sub-agents (in
// goroutine mode via ctx; in process mode via ctx cancelling the JSON-RPC call
// and Close killing the child).
func (t *SubAgentTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	// Goroutine mode requires NewRunConfig (it supplies the in-process provider
	// stream). Process mode does not - the subprocess resolves its own provider
	// from Process.Model - so the check is guarded to goroutine mode. This
	// preserves the original precedence (nil NewRunConfig reported before an
	// empty prompt) for the unchanged goroutine path.
	if t.spec.Isolation != SubAgentIsolationProcess && t.spec.NewRunConfig == nil && t.spec.NewRunConfigE == nil {
		return agentcore.AgentToolResult{}, fmt.Errorf("sub-agent %q: no run configuration", t.spec.Name)
	}
	var a subAgentArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return agentcore.AgentToolResult{}, fmt.Errorf("sub-agent %q: decode args: %w", t.spec.Name, err)
		}
	}
	if a.Prompt == "" {
		return agentcore.AgentToolResult{}, fmt.Errorf("sub-agent %q: empty prompt", t.spec.Name)
	}

	if t.spec.Isolation == SubAgentIsolationProcess {
		// Process mode returns only the child's final text (the JSON-RPC protocol
		// does not stream partial updates), so onUpdate is intentionally not
		// forwarded here; a caller supplying a sink gets no deltas in this mode.
		return t.executeProcess(ctx, id, a.Prompt)
	}
	return t.executeGoroutine(ctx, id, a, onUpdate)
}

// childPlan is the resolved input for one child run: the run configuration, the
// context messages the child starts from (a fresh prompt, or a replayed prefix
// plus the new prompt), and the resume/degrade bookkeeping the settle path
// needs.
type childPlan struct {
	cfg      RunConfig
	messages agentcore.MessageList
	// target is the provider/model triple the child actually runs (the factory's
	// target for a fresh run, the pinned source's for a resume).
	target SubAgentTarget
	// resumed is true when the messages continue a previous transcript.
	resumed bool
	// resumedFrom is the source agent id for a resumed run (lineage in meta).
	resumedFrom string
	// note, when non-empty, is prepended to the result: the resume could not be
	// honored and a fresh run was substituted, with the reason.
	note string
}

// resumeDecision is the outcome of resolving which run configuration drives a
// resumed child.
type resumeDecision struct {
	cfg RunConfig
	// target is the triple the child runs (the pinned source's).
	target SubAgentTarget
	// window is the effective context window for the resume budget (0 = unknown).
	window int
	// degradeReason, when non-empty, means the source could not be resolved and
	// the resume must fall back to a fresh run on the process's own model (P2).
	degradeReason string
}

// executeGoroutine runs the child agent loop in-process and returns its final
// text. This is the default mode and the original sub-agent behavior.
//
// id is the parent tool call's id and a.Description is the (optional) task
// description; both are threaded onto any SubAgentProgressEvent emitted for this
// run so a consumer can key status by the parent task call. When the parent loop
// injected a run-level progress emitter into ctx (WithProgressEmitter), the
// child's tool-execution / turn boundaries are translated into
// SubAgentProgressEvent and surfaced up the parent stream; when no emitter is
// present (e.g. the tool is called directly in a unit test) progress reporting is
// silently skipped.
//
// When a.Resume is set and a transcript store is bound, the referenced settled
// child's transcript is replayed as this run's prefix (T7.1): the prefix is
// project-viewed (dangling calls repaired), side-effect results are replaced by
// an "already executed" marker (T5.2), and the new prompt is appended as the
// latest user turn. Every terminal outcome persists the transcript.
func (t *SubAgentTool) executeGoroutine(ctx context.Context, id string, a subAgentArgs, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	// Concurrency guard: when a shared semaphore is configured, acquire a slot
	// before spawning the child and release it via defer so a panic or error
	// still frees the slot. A full channel blocks (queues) the acquire; a
	// cancelled parent ctx abandons the wait instead of blocking forever.
	if t.spec.Sem != nil {
		select {
		case t.spec.Sem <- struct{}{}:
			defer func() { <-t.spec.Sem }()
		case <-ctx.Done():
			return agentcore.AgentToolResult{}, ctx.Err()
		}
	}
	started := time.Now().UTC()
	plan, spawnFail, err := t.planChild(ctx, a)
	if err != nil {
		return agentcore.AgentToolResult{}, err
	}
	if spawnFail != nil {
		// D-7: a spawn-time factory failure (e.g. a skill's frontmatter model
		// that cannot resolve) is a normal envelope result, not a Go error — the
		// parent model reads the cause and acts on next_step.
		env, body := buildEnvelope(id, spawnFail, spawnFail.ErrorMessage, false)
		return agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent(env.Format(body))},
			Details: env,
		}, nil
	}
	h := t.streamHandler(ctx, id, a.Description, onUpdate)
	var childCtx *agentcore.AgentContext
	runChild := func(p childPlan) (*agentcore.AssistantMessage, error) {
		childCtx = &agentcore.AgentContext{
			SystemPrompt: t.spec.SystemPrompt,
			Messages:     p.messages,
			Tools:        t.childTools(p.cfg),
		}
		return DrainStream(ctx, StartRun(ctx, childCtx, p.cfg), h)
	}
	final, derr := runChild(plan)
	// P2 rate-limit degrade: a resumed run that dies on the upstream
	// 429/503/529 family (the transport's retries exhausted) re-runs fresh on
	// this process's model, once. A fresh run that fails again is reported as
	// usual — never a second degrade.
	if plan.resumed && derr == nil && ctx.Err() == nil && rateLimitFailure(final) {
		if fp, fail := t.freshPlan(a); fail == nil {
			fp.note = degradeNote(id, plan.target.Model, rateLimitDegradeReason)
			final, derr = runChild(fp)
			plan = fp
		}
	}
	// Settle persistence (T7.1): every terminal outcome — completed, failed,
	// cancelled, transport error — best-effort records the transcript and a
	// terminal meta before any early return, so a run cancelled with its parent
	// is still resumable in a later process. A write failure is logged and
	// swallowed (log-and-continue, checkpoint's contract): a broken sidecar must
	// never turn a settled run into an error.
	metaReason := stopReasonOf(final)
	switch {
	case ctx.Err() != nil:
		metaReason = "cancelled"
	case derr != nil:
		metaReason = "error"
	}
	t.persistSettle(id, childCtx, plan, metaReason, started)

	if ctx.Err() != nil {
		// A cancelled parent returns the context error unwrapped, exactly as the
		// pre-resume path did (the whole run is going down; an envelope inviting
		// the model to re-dispatch would be wrong).
		return agentcore.AgentToolResult{}, ctx.Err()
	}
	if derr != nil {
		return agentcore.AgentToolResult{}, fmt.Errorf("sub-agent %q: %w", t.spec.Name, derr)
	}
	text := ""
	if final != nil {
		text = agentcore.ContentToText(final.Content)
	}
	env, body := buildEnvelope(id, final, text, t.resumable())
	if plan.note != "" {
		body = plan.note + "\n\n" + body
	}
	// Completed runs keep the pre-envelope contract: the child's final message
	// is the sole handoff and the result text is returned verbatim (the
	// envelope rides in Details for the TUI/telemetry), so existing consumers
	// of successful task results see no change. A degraded resume prepends its
	// note to that verbatim body — the one deliberate exception, so the parent
	// model never mistakes a fresh re-run for a continuation.
	if env.Status == SubAgentStatusCompleted {
		return agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent(body)},
			Details: env,
		}, nil
	}
	// Every non-completed outcome (max_tokens / error / cancelled /
	// no_final_message) formats as the fixed-field envelope text — a normal
	// (non-error) tool result so the parent model can read next_step and act,
	// instead of the executor collapsing the failure into an opaque error
	// string (kimi semantics: the structured contract replaces free-form
	// failure interpretation).
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(env.Format(body))},
		Details: env,
	}, nil
}

// freshPlan resolves the normal (non-resume) child plan through the tool's
// factory. A factory error is returned as the D-7 synthetic final message (the
// caller renders it as an envelope), never as a Go error.
func (t *SubAgentTool) freshPlan(a subAgentArgs) (childPlan, *agentcore.AssistantMessage) {
	cfg := RunConfig{}
	if t.spec.NewRunConfigE != nil {
		rc, err := t.spec.NewRunConfigE()
		if err != nil {
			return childPlan{}, &agentcore.AssistantMessage{StopReason: agentcore.StopReasonError, ErrorMessage: err.Error()}
		}
		cfg = rc
	} else {
		cfg = t.spec.NewRunConfig()
	}
	return childPlan{cfg: cfg, messages: agentcore.MessageList{promptMessage(a.Prompt)}, target: t.spec.Target}, nil
}

// planChild resolves the child plan for one call: a fresh plan, or — when a
// resume handle is present — the replayed-and-sanitized prefix plus the pinned
// run configuration.
func (t *SubAgentTool) planChild(ctx context.Context, a subAgentArgs) (childPlan, *agentcore.AssistantMessage, error) {
	if a.Resume == "" {
		p, fail := t.freshPlan(a)
		return p, fail, nil
	}
	prefix, meta, err := t.loadResumable(a.Resume)
	if err != nil {
		return childPlan{}, nil, err
	}
	dec, err := t.resolveResume(meta)
	if err != nil {
		// A pinned factory that broke is a spawn failure (D-7 envelope), not a
		// reason to silently continue on another model.
		return childPlan{}, &agentcore.AssistantMessage{StopReason: agentcore.StopReasonError, ErrorMessage: err.Error()}, nil
	}
	if dec.degradeReason != "" {
		p, fail := t.freshPlan(a)
		if fail != nil {
			return childPlan{}, fail, nil
		}
		p.note = degradeNote(a.Resume, meta.Model, dec.degradeReason)
		return p, nil, nil
	}
	msgs := t.sanitizeResumePrefix(prefix, dec.cfg)
	msgs, err = t.applyResumeBudget(ctx, msgs, dec.cfg, dec.window, a.Prompt)
	if err != nil {
		return childPlan{}, nil, err
	}
	msgs = append(msgs, promptMessage(a.Prompt))
	return childPlan{cfg: dec.cfg, messages: msgs, target: dec.target, resumed: true, resumedFrom: a.Resume}, nil, nil
}

// promptMessage wraps a prompt as the child's user turn.
func promptMessage(prompt string) agentcore.Message {
	return agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent(prompt)}}
}

// loadResumable reads and validates the transcript behind a resume handle. Every
// failure is fail-closed (a Go error, never a silent fresh start): an unbound
// store, a missing handle, a non-terminal record, or a corrupt transcript.
func (t *SubAgentTool) loadResumable(agentID string) (agentcore.MessageList, SubagentMeta, error) {
	st := t.spec.Store
	if st == nil || st.SessionID() == "" {
		return nil, SubagentMeta{}, fmt.Errorf("sub-agent resume %q unavailable: this run has no sub-agent transcript store bound to a session", agentID)
	}
	msgs, meta, err := st.Load(agentID)
	if err != nil {
		return nil, SubagentMeta{}, fmt.Errorf("sub-agent resume %q: %w", agentID, err)
	}
	if meta == nil {
		if len(msgs) == 0 {
			return nil, SubagentMeta{}, fmt.Errorf("sub-agent resume %q: no such sub-agent in this session (it was never dispatched, or the handle is not an agent_id from a settled result)", agentID)
		}
		return nil, SubagentMeta{}, fmt.Errorf("sub-agent resume %q: the sub-agent has no settled record yet; it may still be running", agentID)
	}
	if meta.Status != SubAgentStatusCompleted && meta.Status != SubAgentStatusFailed {
		return nil, SubagentMeta{}, fmt.Errorf("sub-agent resume %q refused: the sub-agent is still running", agentID)
	}
	if len(msgs) == 0 {
		return nil, SubagentMeta{}, fmt.Errorf("sub-agent resume %q: the stored transcript is empty", agentID)
	}
	return msgs, *meta, nil
}

// resolveResume decides which run configuration drives a resumed child. The
// source triple recorded in meta is pinned (P2): an identical triple rides the
// tool's own factory, a different one is re-resolved through NewRunConfigFor. A
// nil resolver or a resolution failure is reported as a degrade reason (the
// caller re-runs fresh with a note) instead of aborting the spawn.
func (t *SubAgentTool) resolveResume(meta SubagentMeta) (resumeDecision, error) {
	src := SubAgentTarget{
		Model:        meta.Model,
		BaseURL:      meta.BaseURL,
		Protocol:     meta.Protocol,
		ProviderName: meta.Provider,
		Proxy:        meta.Proxy,
	}
	if t.sameTarget(src) {
		// Same process / same config: the factory already runs the pinned triple,
		// and its triple is frozen for the process's lifetime, so a pin needs no
		// re-resolution.
		if t.spec.NewRunConfigE != nil {
			rc, err := t.spec.NewRunConfigE()
			if err != nil {
				return resumeDecision{}, err
			}
			return resumeDecision{cfg: rc, target: t.spec.Target, window: t.spec.Target.ContextWindow}, nil
		}
		if t.spec.NewRunConfig == nil {
			return resumeDecision{}, fmt.Errorf("sub-agent %q: no run configuration", t.spec.Name)
		}
		return resumeDecision{cfg: t.spec.NewRunConfig(), target: t.spec.Target, window: t.spec.Target.ContextWindow}, nil
	}
	if t.spec.NewRunConfigFor == nil {
		return resumeDecision{degradeReason: fmt.Sprintf("the source model %s cannot be re-resolved here", displayModelName(meta.Model))}, nil
	}
	rc, err := t.spec.NewRunConfigFor(src)
	if err != nil {
		return resumeDecision{degradeReason: fmt.Sprintf("the source model %s is no longer available (%v)", displayModelName(meta.Model), err)}, nil
	}
	window := rc.ContextWindow
	if window == 0 {
		window = t.spec.Target.ContextWindow
	}
	return resumeDecision{cfg: rc, target: src, window: window}, nil
}

// sameTarget reports whether src is the triple the tool's factory runs.
func (t *SubAgentTool) sameTarget(src SubAgentTarget) bool {
	tgt := t.spec.Target
	return src.Model == tgt.Model && src.BaseURL == tgt.BaseURL && src.Protocol == tgt.Protocol &&
		src.ProviderName == tgt.ProviderName && src.Proxy == tgt.Proxy
}

// displayModelName names a model in a message, with a placeholder when the
// record left it empty.
func displayModelName(model string) string {
	if strings.TrimSpace(model) == "" {
		return "(unknown)"
	}
	return model
}

// sanitizeResumePrefix turns a stored transcript into the request prefix a
// resumed child starts from. It runs the same projection the loop uses (T3.3),
// which drops projection-only markers and repairs dangling tool calls with a
// synthetic error result — a strict provider rejects a request containing any
// unanswered tool_use, so the prefix itself must be well formed (the crush
// lesson, §2.5). Side-effect results are then replaced, on this in-memory copy
// only, by an "already executed" marker (T5.2: the prefix must never grant the
// child side effects it did not just perform); read-only results stay verbatim.
// The stored transcript keeps the real records.
func (t *SubAgentTool) sanitizeResumePrefix(prefix agentcore.MessageList, cfg RunConfig) agentcore.MessageList {
	view := compaction.ProjectView(prefix)
	readOnly := t.readOnlyToolNames(cfg)
	out := make(agentcore.MessageList, len(view))
	copy(out, view)
	for i, m := range out {
		tr, ok := m.(agentcore.ToolResultMessage)
		if !ok {
			continue
		}
		if readOnly[tr.ToolName] {
			continue
		}
		if tr.IsError {
			tr.Content = agentcore.ContentList{agentcore.NewTextContent(resumeFailedSideEffectMarker)}
		} else {
			tr.Content = agentcore.ContentList{agentcore.NewTextContent(resumeSideEffectMarker)}
		}
		out[i] = tr
	}
	return out
}

// resumeSideEffectMarker replaces a successful side-effect tool result in a
// replayed prefix: the call did happen, but its effect is not re-applied and
// the child must re-verify anything it relies on.
const resumeSideEffectMarker = "[This call already ran before the interruption; its effect was NOT re-applied. " +
	"Re-verify the current state before relying on this result.]"

// resumeFailedSideEffectMarker is the failed-call counterpart: the call ran but
// did not succeed, so it may need re-running.
const resumeFailedSideEffectMarker = "[This call ran and failed before the interruption. Re-run it if it is still needed.]"

// readOnlyToolNames maps each tool the child can run to whether it is read-only
// (T5.2 effect table). A tool name absent from the set is treated as
// side-effecting, the conservative direction.
func (t *SubAgentTool) readOnlyToolNames(cfg RunConfig) map[string]bool {
	tools := t.childTools(cfg)
	out := make(map[string]bool, len(tools))
	for _, tl := range tools {
		out[tl.Name()] = agentcore.EffectOf(tl).ReadOnly
	}
	return out
}

// resumeWindowPct is the share of the model's context window a replayed prefix
// may occupy before it must be distilled — grok's ResumeWindowPolicy (window ×
// 95%).
const resumeWindowPct = 95

// applyResumeBudget enforces the window policy on a replayed prefix (T7.1 slice
// 2). Within budget — or an unknown window (0), which skips the check — the
// prefix is returned unchanged. Over budget, the older history is distilled with
// the loop's own compaction primitive while a recent tail is kept verbatim
// (opencode's preserve_recent_tokens shape); if the distilled prefix still
// exceeds the budget the resume is refused fail-closed, teaching the parent
// model to re-dispatch fresh (or use a larger-window model).
func (t *SubAgentTool) applyResumeBudget(ctx context.Context, msgs agentcore.MessageList, cfg RunConfig, window int, prompt string) (agentcore.MessageList, error) {
	if window <= 0 {
		return msgs, nil
	}
	budget := window * resumeWindowPct / 100
	if budget <= 0 {
		return msgs, nil
	}
	if compaction.EstimateContextTokens(withTrailingPrompt(msgs, prompt)).Tokens <= budget {
		return msgs, nil
	}
	// Over budget: distilling needs a summarization stream (the run config's
	// provider). Without one the resume cannot be made to fit — refuse instead
	// of sending an over-window request or calling a nil stream.
	if cfg.Stream == nil {
		return nil, fmt.Errorf("sub-agent resume refused: the previous transcript exceeds the resume limit (%d tokens of a %d-token window) and this run has no summarization model to distill it; re-dispatch a fresh sub-agent or use a model with a larger context window",
			compaction.EstimateContextTokens(msgs).Tokens, window)
	}
	// Keep the recent tail proportional to the window, clamped to the range
	// opencode uses (25% usable, 2k..15k).
	keep := window / 4
	if keep < 2000 {
		keep = 2000
	}
	if keep > 15000 {
		keep = 15000
	}
	scfg := cfg
	scfg.Compaction = compaction.CompactionSettings{
		Enabled:          true,
		ReserveTokens:    compaction.DefaultCompactionSettings.ReserveTokens,
		KeepRecentTokens: keep,
	}
	res, err := runCompaction(ctx, msgs, &scfg, -1, "", nil)
	if err != nil {
		return nil, fmt.Errorf("sub-agent resume: distill the previous transcript: %w", err)
	}
	if res == nil {
		return nil, fmt.Errorf("sub-agent resume refused: the previous transcript exceeds the resume limit (%d tokens of a %d-token window) and holds nothing distillable; re-dispatch a fresh sub-agent with a smaller task",
			compaction.EstimateContextTokens(msgs).Tokens, window)
	}
	distilled := res.RebuildContext(msgs, nowMillis())
	if compaction.EstimateContextTokens(withTrailingPrompt(distilled, prompt)).Tokens > budget {
		return nil, fmt.Errorf("sub-agent resume refused: the previous transcript still exceeds the resume limit (%d tokens of a %d-token window) after distillation; re-dispatch a fresh sub-agent or use a model with a larger context window",
			compaction.EstimateContextTokens(distilled).Tokens, window)
	}
	return distilled, nil
}

// withTrailingPrompt returns msgs followed by the pending prompt as a final user
// turn, without mutating msgs: the budget must count what the request carries.
func withTrailingPrompt(msgs agentcore.MessageList, prompt string) agentcore.MessageList {
	out := make(agentcore.MessageList, 0, len(msgs)+1)
	out = append(out, msgs...)
	out = append(out, promptMessage(prompt))
	return out
}

// childTools is the tool set advertised to and runnable by the child: an
// explicit spec set when pinned, otherwise the run config's registry (the tools
// the executor can actually run).
func (t *SubAgentTool) childTools(cfg RunConfig) []agentcore.AgentTool {
	if len(t.spec.Tools) > 0 {
		return t.spec.Tools
	}
	if reg := cfg.Batch.ToolExecutorConfig.Registry; reg != nil {
		return reg.List()
	}
	return nil
}

// streamHandler builds the DrainStream handler for one child run: text deltas
// are forwarded to onUpdate when a sink is set, and (when the parent loop
// injected a progress emitter into ctx) the child's tool/turn boundaries are
// surfaced as SubAgentProgressEvent up the parent stream. Reporting is at
// activity granularity, NOT per text delta, so event volume stays proportional
// to the child's tool calls.
func (t *SubAgentTool) streamHandler(ctx context.Context, id, description string, onUpdate agentcore.ToolUpdateFunc) StreamHandler {
	var h StreamHandler
	if onUpdate != nil {
		h.OnText = func(delta string) {
			onUpdate(agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(delta)}})
		}
	}
	parentEmit := agentcore.ProgressEmitterFromContext(ctx)
	if parentEmit == nil {
		return h
	}
	// chars accumulates the child's streamed text length so a coarse output
	// token estimate can ride along on each progress event (0 = unknown).
	chars := 0
	if prev := h.OnText; prev != nil {
		h.OnText = func(delta string) {
			chars += len(delta)
			prev(delta)
		}
	} else {
		h.OnText = func(delta string) { chars += len(delta) }
	}
	h.OnEvent = func(ev agentcore.AgentEvent) {
		act := activityOf(ev)
		if act == "" {
			return
		}
		_ = parentEmit(ctx, agentcore.SubAgentProgressEvent{
			ToolCallID:  id,
			Description: description,
			Activity:    act,
			Tokens:      estimateTokens(chars),
		})
	}
	return h
}

// resumable reports whether this tool can advertise a resume handle: the run
// must have a transcript store bound to a session.
func (t *SubAgentTool) resumable() bool {
	st := t.spec.Store
	return st != nil && st.SessionID() != ""
}

// persistSettle best-effort records the settled child transcript and meta. It is
// inert without a bound store or a child context. Writes are independent of the
// result path: a failure is logged and swallowed.
func (t *SubAgentTool) persistSettle(agentID string, childCtx *agentcore.AgentContext, plan childPlan, reason string, started time.Time) {
	st := t.spec.Store
	if st == nil || childCtx == nil {
		return
	}
	if err := st.Append(agentID, childCtx.Messages); err != nil {
		fmt.Fprintf(os.Stderr, "pigo: sub-agent transcript not saved: %v\n", err)
	}
	status := SubAgentStatusFailed
	if reason == SubAgentStatusCompleted {
		status = SubAgentStatusCompleted
	}
	target := plan.target
	if target.Model == "" {
		target = t.spec.Target
	}
	now := time.Now().UTC()
	meta := SubagentMeta{
		Status:      status,
		StopReason:  reason,
		Model:       target.Model,
		BaseURL:     target.BaseURL,
		Protocol:    target.Protocol,
		Provider:    target.ProviderName,
		Proxy:       target.Proxy,
		Messages:    len(childCtx.Messages),
		ResumedFrom: plan.resumedFrom,
		CreatedAt:   started,
		UpdatedAt:   now,
	}
	if err := st.Finalize(agentID, meta); err != nil {
		fmt.Fprintf(os.Stderr, "pigo: sub-agent meta not saved: %v\n", err)
	}
}

// rateLimitDegradeReason is the note a rate-limit degrade renders, so the parent
// model knows the continuation it asked for was substituted.
const rateLimitDegradeReason = "the source model returned a rate-limit / unavailable error (429/503/529)"

// degradeNote is the line prepended to a result when a resume could not be
// honored and a fresh run was substituted.
func degradeNote(agentID, sourceModel, reason string) string {
	return fmt.Sprintf("[resume degraded: could not continue sub-agent %s (model %s) — %s. Ran as a fresh sub-agent instead.]",
		agentID, displayModelName(sourceModel), reason)
}

// rateLimitFailure reports whether a settled run died on the upstream
// rate-limit/unavailable family (429/503/529) after the transport's retries were
// exhausted — the P2 trigger for degrading a resume to a fresh run. The failure
// surfaces as the synthesized error turn's ErrorMessage (the loop converts a
// provider connect failure into an error assistant message), so that is what is
// inspected.
func rateLimitFailure(final *agentcore.AssistantMessage) bool {
	if final == nil {
		return false
	}
	text := final.ErrorMessage
	if text == "" {
		return false
	}
	for _, code := range []string{"upstream 429", "upstream 503", "upstream 529"} {
		if strings.Contains(text, code) {
			return true
		}
	}
	return false
}

// executeProcess runs the child agent loop in a fresh pigo subprocess over stdio
// JSON-RPC and returns its final outcome. A subprocess crash or transport error
// is surfaced as a tool error; a settled child run (including a failed one) is
// normalized into the T5.1 result envelope. Streamed child text is not
// forwarded (the process protocol returns only the final result); the parent
// sees the complete result when the child settles.
func (t *SubAgentTool) executeProcess(ctx context.Context, id, prompt string) (agentcore.AgentToolResult, error) {
	cfg := t.spec.Process
	if cfg.Model == "" {
		return agentcore.AgentToolResult{}, fmt.Errorf("sub-agent %q: process mode requires Process.Model", t.spec.Name)
	}
	// Forward the child's tool names so the subprocess can rebuild a matching
	// builtin set; an explicit ToolNames list wins over deriving from Tools.
	toolNames := cfg.ToolNames
	if len(toolNames) == 0 {
		for _, tl := range t.spec.Tools {
			toolNames = append(toolNames, tl.Name())
		}
	}
	params := SubAgentRunParams{
		Prompt:       prompt,
		SystemPrompt: t.spec.SystemPrompt,
		Model:        cfg.Model,
		BaseURL:      cfg.BaseURL,
		Protocol:     cfg.Protocol,
		Tools:        toolNames,
	}
	call := t.processCall
	if call == nil {
		call = defaultProcessCall
	}
	res, err := call(ctx, cfg, params)
	if err != nil {
		return agentcore.AgentToolResult{}, fmt.Errorf("sub-agent %q (process): %w", t.spec.Name, err)
	}
	// Body fallback chain mirroring the child-side diagnostic: final text, then
	// the loop's synthesized error message, then the raw stop reason — so a
	// failed envelope always carries its cause.
	body := res.Text
	if body == "" {
		body = res.ErrorMessage
	}
	if body == "" {
		body = res.StopReason
	}
	// Process-isolated runs do not persist a transcript and cannot be resumed
	// (P5: the child would need its own store and the parent's path in the RPC
	// params), so the envelope never advertises a resume handle here.
	env, body := buildEnvelope(id, envelopeFinalOf(res), body, false)
	if env.Status == SubAgentStatusCompleted {
		return agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent(body)},
			Details: env,
		}, nil
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(env.Format(body))},
		Details: env,
	}, nil
}

// envelopeFinalOf adapts a wire-level SubAgentRunResult into the final-message
// view buildEnvelope normalizes: the RPC result carries the child's normalized
// stopReason directly (the subprocess ran RunSubAgentOnce, which reports it),
// so it is folded into a synthetic stop-reason carrier.
func envelopeFinalOf(res SubAgentRunResult) *agentcore.AssistantMessage {
	if res.StopReason == "" {
		return nil
	}
	return &agentcore.AssistantMessage{StopReason: res.StopReason, ErrorMessage: res.ErrorMessage}
}

// defaultProcessCall is the production subprocess transport: it launches the
// pigo binary (or cfg.Command) with the subagent-rpc flag, sends a single
// "subagent/run" JSON-RPC request over the child's stdin, and returns the
// child's final outcome (text + normalized stop reason). The child is closed
// (killed if it does not exit on its own) before returning. A crash, transport
// error, or RPC error is returned as a Go error so executeProcess surfaces it
// as a tool error; a settled child run — failed or not — comes back as a
// result with StopReason set (T5.1 envelope contract).
func defaultProcessCall(ctx context.Context, cfg SubAgentProcessConfig, params SubAgentRunParams) (SubAgentRunResult, error) {
	command := cfg.Command
	if command == "" {
		exe, err := os.Executable()
		if err != nil {
			return SubAgentRunResult{}, fmt.Errorf("resolve pigo executable: %w", err)
		}
		command = exe
	}
	args := append([]string{SubAgentRPCFlag}, cfg.Args...)
	// Credential hygiene (issue #568): when the caller does not build the child
	// environment explicitly, default to a scrubbed one — the child must not
	// inherit credential-shaped variables it did not ask for. A parent that
	// knows the child needs a specific key builds cfg.Env from
	// provider.ScrubEnv with an allow entry.
	env := cfg.Env
	if env == nil {
		env = provider.ScrubEnv(os.Environ(), nil)
	}
	client, err := jsonrpc.NewClient(jsonrpc.Config{
		Command: command,
		Args:    args,
		Env:     env,
		Dir:     cfg.Dir,
		Stderr:  cfg.Stderr,
	})
	if err != nil {
		return SubAgentRunResult{}, err
	}
	defer client.Close()
	raw, err := client.Call(ctx, SubAgentRPCMethod, params)
	if err != nil {
		return SubAgentRunResult{}, err
	}
	var res SubAgentRunResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return SubAgentRunResult{}, fmt.Errorf("decode sub-agent result: %w", err)
	}
	return res, nil
}

// RunSubAgentOnce runs one sub-agent loop to completion and returns the child's
// final outcome: the last assistant text plus the normalized stop reason (T5.1
// envelope). It is the execution core shared by the process-isolated
// subprocess (cmd/pigo --subagent-rpc): given a resolved RunConfig (provider
// stream, tool registry) and the prompt/system prompt, it builds a fresh child
// context and drains the run.
//
// A settled run is returned as a result with StopReason set, regardless of
// whether the child succeeded — the error return is reserved for transport-level
// failures (a drained stream error, e.g. a provider connection failure or a
// cancelled context). The caller (the subprocess RPC handler) answers those
// with an RPC error and forwards settled results to the parent, which renders
// the failure as an envelope. When the loop synthesizes an error turn (e.g. a
// provider failure) the diagnostic lands in ErrorMessage, not Content; it is
// surfaced there so the parent's envelope shows the real cause rather than a
// bare "error" stop reason. It does not stream partial updates: the process
// protocol returns only the final result.
func RunSubAgentOnce(ctx context.Context, systemPrompt, prompt string, tools []agentcore.AgentTool, runCfg RunConfig) (SubAgentRunResult, error) {
	childCtx := &agentcore.AgentContext{
		SystemPrompt: systemPrompt,
		Messages: agentcore.MessageList{
			agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent(prompt)}},
		},
		Tools: tools,
	}
	stream := StartRun(ctx, childCtx, runCfg)
	final, err := DrainStream(ctx, stream, StreamHandler{})
	if err != nil {
		return SubAgentRunResult{}, err
	}
	res := SubAgentRunResult{}
	if final != nil {
		res.Text = agentcore.ContentToText(final.Content)
		res.StopReason = final.StopReason
		res.ErrorMessage = final.ErrorMessage
	}
	return res, nil
}
