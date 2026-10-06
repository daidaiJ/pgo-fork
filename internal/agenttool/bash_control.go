// This file implements the two companion tools for background bash jobs:
// bash_output drains a job's new output and reports its status, and kill_bash
// terminates a running job. Both address a job by the bash_id returned from a
// `bash` call with run_in_background=true, sharing the same BashJobStore so a
// job launched by the bash tool is visible here. This mirrors Claude Code's
// BashOutput/KillShell tools.
package agenttool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/smallnest/pigo/internal/agentcore"
)

// BashOutputTool reads the output a background job has produced since the last
// read and reports whether it is still running or has exited. Jobs is the shared
// store the bash tool populates.
type BashOutputTool struct {
	Jobs *BashJobStore
}

// bashOutputArgs is the decoded argument shape for BashOutputTool.
type bashOutputArgs struct {
	// BashID is the job handle returned by a background `bash` call.
	BashID string `json:"bash_id"`
}

// Name implements AgentTool.
func (t *BashOutputTool) Name() string { return "bash_output" }

// Effect declares bash_output as read-only (T5.2): it only drains output
// from a job the agent itself started.
func (t *BashOutputTool) Effect() agentcore.ToolEffect {
	return agentcore.ToolEffect{ReadOnly: true, Scope: agentcore.ScopeNone}
}

// Description implements AgentTool.
func (t *BashOutputTool) Description() string {
	return "Read new output from a background command started with bash " +
		"run_in_background=true, addressed by its bash_id. Returns output " +
		"accumulated since the last read plus the command's status (running or " +
		"exited, with exit code). Call repeatedly to stream a long job's output."
}

// Schema implements AgentTool.
func (t *BashOutputTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "bash_id": {"type": "string", "description": "The bash_id returned by a background bash call."}
  },
  "required": ["bash_id"],
  "additionalProperties": false
}`)
}

// ExecutionMode implements AgentTool. Reading output has no side effects.
func (t *BashOutputTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}

// Execute implements AgentTool. It returns the job's new output and status.
func (t *BashOutputTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[bashOutputArgs](args, "bash_output")
	if bad != nil {
		return *bad, nil
	}
	if t.Jobs == nil {
		return errorResult("bash_output: background jobs are not available in this environment"), nil
	}
	job, ok := t.Jobs.Get(a.BashID)
	if !ok {
		return errorResult(fmt.Sprintf("bash_output: no background command with id %q", a.BashID)), nil
	}

	out := truncateBashOutput(job.readNew())
	status, exitCode, errMsg := job.snapshot()

	var statusLine string
	if status == BashRunning {
		statusLine = fmt.Sprintf("[%s: running]", a.BashID)
	} else if errMsg != "" && exitCode != 0 {
		statusLine = fmt.Sprintf("[%s: exited code %d: %s]", a.BashID, exitCode, errMsg)
	} else {
		statusLine = fmt.Sprintf("[%s: exited code %d]", a.BashID, exitCode)
	}

	text := statusLine
	if out != "" {
		text = out + "\n" + statusLine
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(text)},
		Details: map[string]any{"bash_id": a.BashID, "status": string(status), "exitCode": exitCode},
	}, nil
}

// BashKillTool terminates a running background job by canceling its context.
// Jobs is the shared store the bash tool populates.
type BashKillTool struct {
	Jobs *BashJobStore
}

// bashKillArgs is the decoded argument shape for BashKillTool.
type bashKillArgs struct {
	// BashID is the job handle returned by a background `bash` call.
	BashID string `json:"bash_id"`
}

// Name implements AgentTool.
func (t *BashKillTool) Name() string { return "kill_bash" }

// Effect declares kill_bash as a mutating tool scoped to the agent's own
// background jobs (T5.2): not read-only, so an untrusted directory gates it.
func (t *BashKillTool) Effect() agentcore.ToolEffect {
	return agentcore.ToolEffect{Scope: agentcore.ScopeNone}
}

// Description implements AgentTool.
func (t *BashKillTool) Description() string {
	return "Terminate a background command started with bash " +
		"run_in_background=true, addressed by its bash_id. The command's " +
		"process is killed; already-exited jobs report that they were not running."
}

// Schema implements AgentTool.
func (t *BashKillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "bash_id": {"type": "string", "description": "The bash_id returned by a background bash call."}
  },
  "required": ["bash_id"],
  "additionalProperties": false
}`)
}

// ExecutionMode implements AgentTool. Killing a process is a side effect.
func (t *BashKillTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionSequential
}

// Execute implements AgentTool. It kills the job and reports whether it had been
// running.
func (t *BashKillTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	a, bad := decodeArgs[bashKillArgs](args, "kill_bash")
	if bad != nil {
		return *bad, nil
	}
	if t.Jobs == nil {
		return errorResult("kill_bash: background jobs are not available in this environment"), nil
	}
	job, ok := t.Jobs.Get(a.BashID)
	if !ok {
		return errorResult(fmt.Sprintf("kill_bash: no background command with id %q", a.BashID)), nil
	}

	if job.kill() {
		return agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent(fmt.Sprintf("killed background command %s", a.BashID))},
			Details: map[string]any{"bash_id": a.BashID, "killed": true},
		}, nil
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(fmt.Sprintf("background command %s was not running", a.BashID))},
		Details: map[string]any{"bash_id": a.BashID, "killed": false},
	}, nil
}
