package spans

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// resetForTest restores the package to a pristine state so each test controls
// its own enablement. The production env is read exactly once per process,
// which is what the once seam exists for; tests bypass it directly.
func resetForTest(t *testing.T, dir string) {
	t.Helper()
	once = sync.Once{}
	outDir = dir
	enabled = dir != ""
	mu.Lock()
	stack = nil
	records = nil
	paths = make(map[string]struct{})
	dropped = 0
	label = "main"
	traceW = nil
	mu.Unlock()
	interruptSuppressed.Store(false)
	t.Cleanup(func() {
		once = sync.Once{}
		outDir = ""
		enabled = false
	})
}

// fixedClock pins now() so durations are deterministic and no test sleeps.
type fixedClock struct{ t time.Time }

func (c *fixedClock) nowFn() time.Time { return c.t }

func (c *fixedClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// useFixedClock swaps in the fake clock and anchors startTS to it; tests that
// assert trace offsets or output file names need both.
func useFixedClock(t *testing.T, at time.Time) *fixedClock {
	t.Helper()
	clk := &fixedClock{t: at}
	now = clk.nowFn
	oldTS := startTS
	startTS = at
	t.Cleanup(func() {
		now = time.Now
		startTS = oldTS
	})
	return clk
}

func TestDisabledBeginIsNilAndFree(t *testing.T) {
	resetForTest(t, "")

	s := Begin("startup.anything")
	if s != nil {
		t.Fatalf("Begin on disabled spans returned %v, want nil", s)
	}
	// The nil-receiver contract: End/Mark must be safe no-ops.
	s.End()
	s.Mark("nothing")

	if allocs := testing.AllocsPerRun(1000, func() { Begin("x").End() }); allocs != 0 {
		t.Errorf("disabled Begin+End allocated %v times per run, want 0", allocs)
	}
	if Enabled() {
		t.Error("Enabled() true with no env dir and no trace writer")
	}
}

func TestNestedSpansFoldAccounting(t *testing.T) {
	resetForTest(t, t.TempDir())
	clk := useFixedClock(t, time.Unix(0, 0))

	root := Begin("startup.total")
	child := Begin("startup.setup_env")
	clk.advance(3 * time.Millisecond)
	leaf2 := Begin("startup.setup_env.memory")
	clk.advance(2 * time.Millisecond)
	leaf2.End()
	clk.advance(4 * time.Millisecond)
	child.End()
	clk.advance(1 * time.Millisecond)
	root.End()

	Flush()
	folded, err := os.ReadFile(filepath.Join(outDir, foldedName()))
	if err != nil {
		t.Fatalf("read folded: %v", err)
	}
	got := make(map[string]int64)
	for _, l := range strings.Split(strings.TrimSpace(string(folded)), "\n") {
		i := strings.LastIndex(l, " ")
		var us int64
		if _, err := fmt.Sscanf(l[i+1:], "%d", &us); err != nil {
			t.Fatalf("parse folded line %q: %v", l, err)
		}
		got[strings.Trim(l[:i], "{}")] = us
	}
	// memory: 2ms wall; setup_env: 9ms wall − 2ms child = 7ms;
	// total: 10ms wall − 9ms child = 1ms.
	want := map[string]int64{
		"startup.total":                   1000,
		"startup.total;startup.setup_env": 7000,
		"startup.total;startup.setup_env;startup.setup_env.memory": 2000,
	}
	if len(got) != len(want) {
		t.Fatalf("folded rows %v, want %v", got, want)
	}
	for p, us := range want {
		if got[p] != us {
			t.Errorf("path %q self=%dµs, want %dµs", p, got[p], us)
		}
	}
}

func TestOverlappingChildrenSaturateSelf(t *testing.T) {
	resetForTest(t, t.TempDir())
	clk := useFixedClock(t, time.Unix(0, 0))

	parent := Begin("p")
	c1 := Begin("c1")
	c2 := Begin("c2") // c2 spans c1's whole wall, so c1's self hits zero
	clk.advance(5 * time.Millisecond)
	c2.End()
	c1.End()     // wall 5ms, children 5ms → self saturates at 0
	parent.End() // wall 5ms, children 5ms → self saturates at 0

	Flush()
	data, err := os.ReadFile(filepath.Join(outDir, foldedName()))
	if err != nil {
		t.Fatalf("read folded: %v", err)
	}
	// The zero-self rows are omitted (grok convention) and no negative self
	// may appear anywhere: only the leaf row survives.
	if string(data) != "{p;c1;c2} 5000\n" {
		t.Errorf("folded output = %q, want only the leaf row \"{p;c1;c2} 5000\"", data)
	}
}

func TestJSONLEventsAndMarks(t *testing.T) {
	resetForTest(t, t.TempDir())
	clk := useFixedClock(t, time.Unix(0, 0))

	s := Begin("startup.session_load")
	clk.advance(2 * time.Millisecond)
	s.Mark("entries loaded")
	clk.advance(1 * time.Millisecond)
	s.End()

	Flush()
	data, err := os.ReadFile(filepath.Join(outDir, jsonlName()))
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d jsonl lines, want 2 (mark, then span row): %q", len(lines), data)
	}
	if !strings.Contains(lines[0], `"type":"mark"`) || !strings.Contains(lines[0], `"name":"entries loaded"`) {
		t.Errorf("mark row wrong: %s", lines[0])
	}
	for _, want := range []string{`"type":"span"`, `"name":"startup.session_load"`, `"path":"startup.session_load"`, `"dur_us":3000`, `"self_us":3000`} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("span row missing %s: %s", want, lines[1])
		}
	}
}

func TestFlushRetriesAfterWriteFailure(t *testing.T) {
	// outDir points at a regular file, so MkdirAll fails and the records must
	// survive for the next exit point (grok's kill-loses-profile lesson).
	fileDir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(fileDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	resetForTest(t, fileDir)
	clk := useFixedClock(t, time.Unix(0, 0))

	s := Begin("startup.flag_parse")
	clk.advance(1 * time.Millisecond)
	s.End() // self 1000µs: a zero-self row would be omitted from folded output
	Flush() // write fails: the record must be kept for the next exit point
	mu.Lock()
	kept := len(records)
	mu.Unlock()
	if kept != 1 {
		t.Fatalf("records=%d after a failed flush, want 1 (retry semantics)", kept)
	}

	// Repair the sink and flush again: the first span must be in the file.
	realDir := t.TempDir()
	outDir = realDir
	Flush()
	data, err := os.ReadFile(filepath.Join(realDir, foldedName()))
	if err != nil {
		t.Fatalf("read folded after retry: %v", err)
	}
	if !strings.Contains(string(data), "{startup.flag_parse} 1000") {
		t.Errorf("records lost across write failure: %q", data)
	}
}

func TestFlushWithoutRecordsIsNoop(t *testing.T) {
	dir := t.TempDir()
	resetForTest(t, dir)
	Flush() // must not write anything
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("empty Flush wrote %d entries, want 0", len(entries))
	}
}

func TestMaxPathsCapDropsNewPaths(t *testing.T) {
	resetForTest(t, t.TempDir())
	old := maxPaths
	maxPaths = 2
	t.Cleanup(func() { maxPaths = old })

	for i := 0; i < 5; i++ {
		Begin("p" + strconv.Itoa(i)).End() // five distinct paths, only two fit
	}
	if dropped != 3 {
		t.Fatalf("dropped=%d, want 3", dropped)
	}
	// A repeat of an already-registered path is still free after the cap.
	Begin("p0").End()
	if dropped != 3 {
		t.Fatalf("dropped grew to %d on a repeated path, want 3", dropped)
	}
}

func TestTraceTimeline(t *testing.T) {
	resetForTest(t, "")
	var buf bytes.Buffer
	SetTrace(&buf)
	if !Enabled() {
		t.Fatal("SetTrace must enable recording")
	}
	clk := useFixedClock(t, time.Unix(0, 0))

	s := Begin("startup.ui_init")
	clk.advance(2 * time.Millisecond)
	s.Mark("first frame")
	clk.advance(1 * time.Millisecond)
	s.End()

	out := buf.String()
	for _, want := range []string{"[spans] +0s startup.ui_init dur=3ms self=3ms", "[spans] +2ms MARK first frame"} {
		if !strings.Contains(out, want) {
			t.Errorf("trace output missing %q; got:\n%s", want, out)
		}
	}
	// Trace-only mode writes no files.
	Flush()
}

func TestFoldPathOrdering(t *testing.T) {
	resetForTest(t, t.TempDir())
	useFixedClock(t, time.Unix(0, 0))

	a := Begin("a")
	b := Begin("a.b")
	c := Begin("a.b.c")
	c.End()
	b.End()
	a.End()
	mu.Lock()
	got := records[0].path
	mu.Unlock()
	if got != "a;a.b;a.b.c" {
		t.Errorf("foldPath=%q, want a;a.b;a.b.c", got)
	}
}

func TestEndIsIdempotent(t *testing.T) {
	resetForTest(t, t.TempDir())
	useFixedClock(t, time.Unix(0, 0))

	s := Begin("once-only")
	s.End()
	s.End() // second call must be a no-op
	mu.Lock()
	n := len(records)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("records=%d after double End, want 1", n)
	}
}

// foldedName/jsonlName mirror the file-name scheme of writeProfile so tests
// fail loudly if the scheme drifts between the package and its tests.
func foldedName() string {
	return "pigo-main-" + strconv.Itoa(os.Getpid()) + "-" + startTS.Format("20060102-150405.000") + ".folded"
}

func jsonlName() string {
	return "pigo-main-" + strconv.Itoa(os.Getpid()) + "-" + startTS.Format("20060102-150405.000") + ".jsonl"
}
