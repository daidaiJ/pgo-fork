// Tests for the two-level switch semantics (T6.9): the server-level switch
// outranks the tool-level one (a disabled server has no connection, so every
// tool of its is off the face regardless of disabled_tools), and the live
// toggles mutate the manager without dropping the connection unnecessarily.
package mcp

import (
	"context"
	"testing"
)

func TestServerDisabledBeatsToolLevel(t *testing.T) {
	off := false
	disabledSrv := fakeServerConfig(t, "off", false)
	disabledSrv.Enabled = &off // server-level: never launched
	enabledSrv := fakeServerConfig(t, "on", false)
	enabledSrv.DisabledTools = []string{"read_thing"} // tool-level: connection kept

	m := Connect(context.Background(), []ServerConfig{disabledSrv, enabledSrv}, nil, nil)
	defer m.Close()

	tools := m.Tools()
	for _, tl := range tools {
		if want := ToolName("off", tl.Name()); tl.Name() == want {
			t.Errorf("tool %q came from a DISABLED server: server switch must outrank tool level", tl.Name())
		}
	}
	found := map[string]bool{}
	for _, tl := range tools {
		found[tl.Name()] = true
	}
	if found[ToolName("on", "read_thing")] {
		t.Error("read_thing should be hidden by disabled_tools while its server stays connected")
	}
	if !found[ToolName("on", "write_thing")] {
		t.Error("write_thing should stay in the face (only read_thing was disabled)")
	}
	// Status must show the asymmetry: one disabled server (0 visible), one
	// connected with a hidden tool.
	sts := m.Status()
	if sts[0].Enabled || sts[0].Connected {
		t.Errorf("off server status = %+v, want enabled=false connected=false", sts[0])
	}
	if !sts[1].Connected || sts[1].ToolCount != len(fakeTools) || sts[1].DisabledCount != 1 {
		t.Errorf("on server status = %+v, want connected with %d tools and 1 hidden", sts[1], len(fakeTools))
	}
}

func TestLiveToggles(t *testing.T) {
	cfg := fakeServerConfig(t, "fs", false)
	m := Connect(context.Background(), []ServerConfig{cfg}, nil, nil)
	defer m.Close()

	// Tool toggle: hides the registration, keeps the connection (stash).
	if !m.SetToolDisabled("fs", "read_thing", true) {
		t.Fatal("SetToolDisabled on a live server reported no such server")
	}
	for _, tl := range m.Tools() {
		if tl.Name() == ToolName("fs", "read_thing") {
			t.Error("read_thing still in the face after live disable")
		}
	}
	st := m.Status()
	if !st[0].Connected {
		t.Error("tool disable must keep the connection")
	}
	if !m.SetToolDisabled("fs", "read_thing", false) {
		t.Fatal("re-enable reported no such server")
	}

	// Server toggle: disable closes the connection; enable relaunches it.
	if ok, err := m.SetServerEnabled(context.Background(), "fs", false); !ok || err != nil {
		t.Fatalf("SetServerEnabled(disable) = %v, %v", ok, err)
	}
	if n := len(m.Tools()); n != 0 {
		t.Errorf("tools after server disable = %d, want 0", n)
	}
	if ok, err := m.SetServerEnabled(context.Background(), "fs", true); !ok || err != nil {
		t.Fatalf("SetServerEnabled(enable) = %v, %v; want reconnect", ok, err)
	}
	st = m.Status()
	if !st[0].Connected || st[0].ToolCount != len(fakeTools) {
		t.Errorf("status after re-enable = %+v, want reconnected with all tools", st[0])
	}
	// Reload: the fake's tool list is static, so the snapshot must be unchanged.
	changed, err := m.Reload(context.Background(), "fs")
	if err != nil || changed {
		t.Errorf("Reload = changed=%v err=%v, want unchanged/no error", changed, err)
	}
}
