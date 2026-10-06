// askport.go adapts the REPL's stdin confirmation UX to the toolrules ask
// channel (T5.2). The permission engine is zero-IO; this file is the
// interactive half: it renders the reason-specific prompt and maps the
// user's answer onto an AskDecision.
//
// Options per prompt:
//
//	y — allow this one call
//	n — deny (default)
//	a — allow AND trust the directory for this session (the old "always"
//	    grant; coarse, session-scoped, never persisted)
//	s — allow AND settle the proposed rule into the permissions file
//	    (only offered when the engine proposed a pattern; REPL-only)
package trust

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/toolrules"
)

// StdinAskPort builds the toolrules.AskPort backed by out/in. mgr may be nil
// (the "a" option is then not offered). mu is the session's confirmation
// mutex (shared with shellguard and ask_user prompts so all stdin readers
// serialize); nil is accepted and simply skips locking. cwd is the directory
// an "a" answer grants.
func StdinAskPort(out io.Writer, in *bufio.Reader, mu *sync.Mutex, mgr *Manager, cwd string) toolrules.AskPort {
	return func(ctx context.Context, call agentcore.AgentToolCall, reason toolrules.AskReason, hint toolrules.ProposedHint) (toolrules.AskDecision, toolrules.Rule) {
		if ctx.Err() != nil {
			return toolrules.AskDeny, toolrules.Rule{}
		}
		if mu != nil {
			mu.Lock()
			defer mu.Unlock()
		}
		switch reason {
		case toolrules.AskSelfEdit:
			fmt.Fprintf(out, "\npigo wants to run %q against pigo's own configuration (the self-edit surface).\n", call.Name)
			if summary := ToolCallSummary(call); summary != "" {
				fmt.Fprintf(out, "  %s\n", summary)
			}
			fmt.Fprintln(out, "  This file IS the permission boundary; rules and trust never auto-approve it.")
		default:
			fmt.Fprintf(out, "\npigo wants to run %q in an untrusted directory.\n", call.Name)
			if summary := ToolCallSummary(call); summary != "" {
				fmt.Fprintf(out, "  %s\n", summary)
			}
		}
		options := "[y]es / [n]o"
		if mgr != nil {
			options += " / [a]lways (trust this directory for the session)"
		}
		if hint.Pattern != "" {
			options += fmt.Sprintf("\n  [s]ave rule: allow %s %q from now on (persisted to permissions.json)", hint.Tool, hint.Pattern)
		}
		fmt.Fprintf(out, "Allow? %s [y/N]: ", options)
		line, _ := in.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return toolrules.AskApprove, toolrules.Rule{}
		case "a", "always":
			if mgr != nil {
				mgr.SetSessionTrust(cwd)
			}
			return toolrules.AskApprove, toolrules.Rule{}
		case "s", "save":
			if hint.Pattern != "" {
				return toolrules.AskApproveWithRule, toolrules.Rule{
					Tool:    hint.Tool,
					Pattern: hint.Pattern,
					Action:  toolrules.ActionAllow,
					Scope:   toolrules.ScopePersisted,
				}
			}
			return toolrules.AskDeny, toolrules.Rule{}
		default:
			return toolrules.AskDeny, toolrules.Rule{}
		}
	}
}
