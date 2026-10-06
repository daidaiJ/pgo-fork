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

	"github.com/smallnest/pigo/internal/agentcore"
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
}

// subAgentArgs is the JSON argument shape for a sub-agent tool call: a
// free-form prompt describing the delegated task, plus an optional short
// description used for status display (accepted by the generic task tool;
// ignored by prompt-only specs).
type subAgentArgs struct {
	Prompt      string `json:"prompt"`
	Description string `json:"description,omitempty"`
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
	if t.spec.Isolation != SubAgentIsolationProcess && t.spec.NewRunConfig == nil {
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
	return t.executeGoroutine(ctx, id, a.Prompt, a.Description, onUpdate)
}

// executeGoroutine runs the child agent loop in-process and returns its final
// text. This is the default mode and the original sub-agent behavior.
//
// id is the parent tool call's id and description is the (optional) task
// description; both are threaded onto any SubAgentProgressEvent emitted for this
// run so a consumer can key status by the parent task call. When the parent loop
// injected a run-level progress emitter into ctx (WithProgressEmitter), the
// child's tool-execution / turn boundaries are translated into
// SubAgentProgressEvent and surfaced up the parent stream; when no emitter is
// present (e.g. the tool is called directly in a unit test) progress reporting is
// silently skipped.
func (t *SubAgentTool) executeGoroutine(ctx context.Context, id, prompt, description string, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
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
	runCfg := t.spec.NewRunConfig()
	// Advertise the child's tools to the model. A spec may pin an explicit set
	// (spec.Tools); otherwise fall back to the run config's registry — the tools
	// the executor can actually run — so a factory that wires only the registry
	// (like the generic task tool) still tells the child what it can call.
	// Without this the model is handed an empty tool list, can only reply with
	// text, and a delegated task that needs tools comes back empty.
	tools := t.spec.Tools
	if len(tools) == 0 && runCfg.Batch.ToolExecutorConfig.Registry != nil {
		tools = runCfg.Batch.ToolExecutorConfig.Registry.List()
	}
	childCtx := &agentcore.AgentContext{
		SystemPrompt: t.spec.SystemPrompt,
		Messages: agentcore.MessageList{
			agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent(prompt)}},
		},
		Tools: tools,
	}

	stream := StartRun(ctx, childCtx, runCfg)
	// Drain events (DrainStream never returns early, so the producer goroutine is
	// never blocked on back-pressure); forward streamed child text as
	// tool-execution updates when a sink is set.
	var h StreamHandler
	if onUpdate != nil {
		h.OnText = func(delta string) {
			onUpdate(agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(delta)}})
		}
	}
	// Progress reporting: when the parent loop injected a run-level emitter into
	// ctx, translate the child's tool-execution / turn boundaries into
	// SubAgentProgressEvent and emit them up the parent stream. Reporting is at
	// activity granularity (per child tool start / turn boundary), NOT per text
	// delta, so event volume stays proportional to the child's tool calls. When
	// no emitter is present the OnEvent hook is left nil and progress is skipped.
	if parentEmit := agentcore.ProgressEmitterFromContext(ctx); parentEmit != nil {
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
	}
	final, err := DrainStream(ctx, stream, h)
	if err != nil {
		return agentcore.AgentToolResult{}, fmt.Errorf("sub-agent %q: %w", t.spec.Name, err)
	}
	// T5.1 envelope: normalize the settled run into the structured outcome. The
	// parent's ctx governs the child, so a cancelled parent short-circuits as a
	// tool error (the whole run is going down; an envelope inviting the model to
	// re-dispatch would be wrong).
	if ctx.Err() != nil {
		return agentcore.AgentToolResult{}, ctx.Err()
	}
	text := ""
	if final != nil {
		text = agentcore.ContentToText(final.Content)
	}
	env, body := buildEnvelope(id, final, text)
	// Completed runs keep the pre-envelope contract: the child's final message
	// is the sole handoff and the result text is returned verbatim (the
	// envelope rides in Details for the TUI/telemetry), so existing consumers
	// of successful task results see no change.
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
	env, body := buildEnvelope(id, envelopeFinalOf(res), body)
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
