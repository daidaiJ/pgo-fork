// Tests for the /dump contract command (spec wiki/port/reqdump.md): Parse
// refuses arguments, and the Execute face writes the recorded provider failure
// to <PIGO_DUMP_DIR>/<session id>-<timestamp>/dump.json and reports the path.
package prompts

import (
	"os"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/reqdump"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/testenv"
)

func TestDumpCommandParsesAndWrites(t *testing.T) {
	reqdump.Clear()
	t.Cleanup(func() { reqdump.Clear(); reqdump.SetSession("") })
	root := testenv.Dir(t)
	t.Setenv(reqdump.DirEnv, root)

	reg := bareRegistry(t)
	out, err := reg.ResolveOutcome("/dump")
	if err != nil {
		t.Fatalf("ResolveOutcome /dump: %v", err)
	}
	if out.Kind != runtime.SlashIntent || out.Intent.Kind != runtime.IntentDump {
		t.Fatalf("/dump should resolve to IntentDump, got %+v", out)
	}

	ex := &Executor{DumpSession: func() string { return "20261009-213005-ab12" }}
	withNone := ex.Execute(out.Intent)
	if !strings.Contains(withNone.Message, "no failed provider request recorded yet") {
		t.Errorf("no-record message = %q, want the explicit notice", withNone.Message)
	}

	reqdump.RecordFailure(reqdump.Record{
		Error:   "transport: upstream 500: boom",
		Request: reqdump.Request{Method: "POST", URL: "https://api.example.com/v1/chat/completions"},
	})
	got := ex.Execute(out.Intent)
	path := strings.TrimPrefix(got.Message, "dumped the last failed provider request to ")
	if path == got.Message {
		t.Fatalf("execute message = %q, want the dump path", got.Message)
	}
	if !strings.Contains(path, "20261009-213005-ab12-") {
		t.Errorf("dump path %q should carry the session id + timestamp", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("dump file missing: %v", err)
	}
}

// TestDumpCommandFallsBackToRecorderSession pins the fallback: with no session
// hook (or an empty one) the recorder's in-flight session names the directory.
func TestDumpCommandFallsBackToRecorderSession(t *testing.T) {
	reqdump.Clear()
	t.Cleanup(func() { reqdump.Clear(); reqdump.SetSession("") })
	root := testenv.Dir(t)
	t.Setenv(reqdump.DirEnv, root)
	reqdump.SetSession("in-flight-session")
	reqdump.RecordFailure(reqdump.Record{Error: "transport: upstream 503: down"})

	got := (&Executor{}).Execute(runtime.Intent{Kind: runtime.IntentDump})
	if !strings.Contains(got.Message, "in-flight-session-") {
		t.Errorf("message = %q, want the recorder session in the path", got.Message)
	}
}

// TestDumpCommandPointsAtOnDiskDumpFromAnEarlierProcess pins the cross-process
// case: a fresh process has no in-memory record, so /dump names the newest dump
// on disk for the session instead of claiming there is none.
func TestDumpCommandPointsAtOnDiskDumpFromAnEarlierProcess(t *testing.T) {
	reqdump.Clear()
	t.Cleanup(func() { reqdump.Clear(); reqdump.SetSession("") })
	root := testenv.Dir(t)
	t.Setenv(reqdump.DirEnv, root)

	// "Earlier process": record + auto-dump under the session.
	reqdump.SetSession("20261009-213005-ab12")
	reqdump.RecordFailure(reqdump.Record{Error: "transport: upstream 500: boom"})
	// "This process": nothing recorded, but the session is known.
	reqdump.Clear()
	reqdump.SetSession("")

	got := (&Executor{DumpSession: func() string { return "20261009-213005-ab12" }}).
		Execute(runtime.Intent{Kind: runtime.IntentDump})
	if !strings.Contains(got.Message, "latest dump on disk: ") ||
		!strings.Contains(got.Message, "20261009-213005-ab12-") {
		t.Errorf("message = %q, want the on-disk dump path for the session", got.Message)
	}
}

func TestDumpCommandRefusesArguments(t *testing.T) {
	reg := bareRegistry(t)
	if _, err := reg.ResolveOutcome("/dump now"); err == nil || err.Error() != "dump: takes no arguments" {
		t.Errorf("/dump now error = %v, want \"dump: takes no arguments\"", err)
	}
}
