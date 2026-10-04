// Package spans is a zero-dependency leaf package for startup/exit profiling
// (T1.1, wiki/port/startup-exit-probes.md): manual Begin/End intervals with an
// explicit parent stack, folded output ("<root;...;leaf> <self µs>") plus a
// JSONL event stream, and flush hooks on all three exit paths (normal return,
// signal, panic unwind).
//
// It is off by default and must cost nothing on that path: with no
// PIGO_SPAN_PROFILE_OUT directory and no --trace-startup flag, Begin returns a
// nil *Span without reading the clock or allocating, and every method is a
// nil-receiver no-op — call sites never need an if. The package depends only
// on the standard library so the assembly chain (cmd/pigo, internal/cli/run,
// the TUI/REPL) can import it without pulling anything into their trees (R2).
//
// Semantics follow the grok-build-proxy span profile layer (the ported
// precedent): self time = wall − child wall (saturating at zero, so
// overlapping children never produce negative rows); folded lines group by the
// root→leaf span path so `sort -k2 -rn *.folded` is the hotspot ranking; only
// the startup.*/exit.* prefixes are meaningful when reading (long-lived loop
// spans legitimately exceed wall time). Flush keeps its records on a write
// failure so a later exit point retries — losing the profile to a kill (the
// first grok lesson) is exactly what the three exit points exist to prevent.
package spans

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// maxPaths caps the number of distinct folded paths a process may record. A
// runaway call site (spans inside a hot loop) would otherwise balloon the
// profile; past the cap new paths are dropped and counted, and the count is
// written to stderr at flush. A var, not a const, so tests can tighten it.
var maxPaths = 8192

// now is the clock seam: replaced in tests to make durations deterministic.
var now = time.Now

// startTS anchors the per-process timestamp used in output file names and the
// trace timeline offsets, captured once at package init.
var startTS = now()

type recKind int

const (
	recSpan recKind = iota
	recMark
)

// record is one completed span or one milestone mark, in End order.
type record struct {
	kind  recKind
	name  string
	path  string // root;...;leaf, span kind only
	start time.Time
	dur   time.Duration // wall, span kind only
	self  time.Duration // wall − children, span kind only
}

type Span struct {
	name     string
	parent   *Span
	start    time.Time
	children time.Duration // accumulated child wall, guarded by mu
	ended    bool
}

var (
	once    sync.Once
	enabled bool
	outDir  string

	// mu guards everything below. Startup instrumentation is effectively
	// single-goroutine, but the flush paths and the trace writer run
	// concurrently with recording, so one mutex keeps the accounting honest.
	mu      sync.Mutex
	stack   []*Span
	records []record
	paths   = make(map[string]struct{})
	dropped int
	label   = "main"
	traceW  io.Writer

	// interruptSuppressed marks a front-end (the line REPL) that registered its
	// own os.Interrupt handler to cancel runs: the exit-flush handler then must
	// neither flush mid-run nor exit, leaving the normal-return flush to fire.
	interruptSuppressed atomic.Bool
)

// Enabled reports whether span recording is on: a non-empty
// PIGO_SPAN_PROFILE_OUT directory (file output) or a trace writer installed by
// SetTrace (--trace-startup). The environment is resolved once.
func Enabled() bool {
	once.Do(func() {
		if dir := strings.TrimSpace(os.Getenv("PIGO_SPAN_PROFILE_OUT")); dir != "" {
			outDir = dir
			enabled = true
		}
	})
	return enabled
}

// SetTrace installs w (os.Stderr under --trace-startup) as the human-readable
// timeline sink and enables recording even without PIGO_SPAN_PROFILE_OUT, so
// spans that begin before flag.Parse completes are captured. Must be called
// before the first Begin to be meaningful.
func SetTrace(w io.Writer) {
	if w == nil {
		return
	}
	Enabled() // resolve the env first so the two enable sources compose
	mu.Lock()
	traceW = w
	enabled = true
	mu.Unlock()
}

// SetLabel stamps the run mode into the output file names
// (pigo-<label>-<pid>-<ts>.folded): dispatch sets it per branch (tui, repl,
// headless, ...) before any flush can happen. Empty labels are ignored.
func SetLabel(l string) {
	if l == "" {
		return
	}
	mu.Lock()
	label = l
	mu.Unlock()
}

// Begin opens a span whose parent is the innermost open span (the explicit
// parent stack: Begin pushes, End pops). Disabled, it returns nil without
// reading the clock or allocating, and all Span methods tolerate nil — the
// call-site pattern is unconditional: `s := spans.Begin("startup.x"); …;
// s.End()`.
func Begin(name string) *Span {
	if !Enabled() {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	var parent *Span
	if n := len(stack); n > 0 {
		parent = stack[n-1]
	}
	s := &Span{name: name, parent: parent, start: now()}
	stack = append(stack, s)
	return s
}

// End closes the span: it computes self = wall − children (saturating at
// zero), credits its wall time to the parent's children counter, records a
// folded row, and pops the explicit stack when this span is on top. Safe to
// call twice (the second call is a no-op) and on a nil receiver.
func (s *Span) End() {
	if s == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if s.ended {
		return
	}
	s.ended = true
	dur := now().Sub(s.start)
	self := dur - s.children
	if self < 0 {
		self = 0
	}
	if n := len(stack); n > 0 && stack[n-1] == s {
		stack = stack[:n-1]
	} else {
		// An out-of-order End (this span sits below the top): remove it so a
		// later Begin cannot parent to an already-ended span.
		for i := len(stack) - 1; i >= 0; i-- {
			if stack[i] == s {
				stack = append(stack[:i], stack[i+1:]...)
				break
			}
		}
	}
	if s.parent != nil {
		s.parent.children += dur
	}
	if traceW != nil {
		fmt.Fprintf(traceW, "[spans] +%s %s dur=%s self=%s\n",
			s.start.Sub(startTS).Truncate(time.Microsecond), s.name, dur.Truncate(time.Microsecond), self.Truncate(time.Microsecond))
	}
	addRecord(record{kind: recSpan, name: s.name, path: foldPath(s), start: s.start, dur: dur, self: self})
}

// Mark records a milestone event on the span (e.g. "first frame") — a JSONL
// and trace line only; folded output is unaffected. Nil receiver no-op.
func (s *Span) Mark(note string) {
	if s == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if traceW != nil {
		fmt.Fprintf(traceW, "[spans] +%s MARK %s\n", now().Sub(startTS).Truncate(time.Microsecond), note)
	}
	addRecord(record{kind: recMark, name: note, start: now()})
}

// FirstFrame is the once-guarded ui_init probe: the TUI/REPL call it from
// their first render site, and only the first call Marks "first frame" and
// closes the span (View/prompt sites run many times). Nil receiver no-op.
type Probe struct {
	span *Span
	once sync.Once
}

func NewProbe(span *Span) *Probe { return &Probe{span: span} }

func (p *Probe) FirstFrame() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		p.span.Mark("first frame")
		p.span.End()
	})
}

// foldPath renders the root→leaf path with ";" separators, the folded frame
// name. A span whose parent ended first (out-of-order End) still walks its
// parent pointers, so the path stays intact.
func foldPath(s *Span) string {
	var parts []string
	for cur := s; cur != nil; cur = cur.parent {
		parts = append(parts, cur.name)
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, ";")
}

// addRecord appends under a held mu, enforcing the distinct-path cap. The cap
// is cumulative across flushes: a repeated path stays free after the first.
func addRecord(r record) {
	if r.kind == recSpan {
		if _, ok := paths[r.path]; !ok {
			if len(paths) >= maxPaths {
				dropped++
				return
			}
			paths[r.path] = struct{}{}
		}
	}
	records = append(records, r)
}

// Flush writes the recorded spans and marks to
// <PIGO_SPAN_PROFILE_OUT>/pigo-<label>-<pid>-<ts>.{folded,jsonl}. It is the
// single exit-point hook: main defers it (panic unwind), calls it before
// os.Exit (normal return), and the signal handler calls it (SIGINT/SIGTERM).
// A write failure keeps the records so a later exit point retries — data is
// never dropped on a transient error. With no records or file output disabled
// (trace-only mode) it does nothing.
func Flush() {
	if !Enabled() {
		return
	}
	mu.Lock()
	if len(records) == 0 {
		mu.Unlock()
		return
	}
	recs := records
	recDropped := dropped
	records = nil
	mu.Unlock()

	err := writeProfile(recs)
	mu.Lock()
	if err != nil {
		// Put the records back in front so the next exit point retries; the
		// path table was never cleared, so nothing is re-registered.
		records = append(recs, records...)
		dropWarn := recDropped != dropped
		mu.Unlock()
		fmt.Fprintf(os.Stderr, "pigo: spans: flush failed, will retry at the next exit point: %v\n", err)
		if dropWarn {
			fmt.Fprintf(os.Stderr, "pigo: spans: %d spans dropped past the %d-path cap\n", recDropped, maxPaths)
		}
		return
	}
	mu.Unlock()
	if recDropped > 0 {
		fmt.Fprintf(os.Stderr, "pigo: spans: %d spans dropped past the %d-path cap\n", recDropped, maxPaths)
	}
}

// writeProfile writes both output files via temp+rename. An empty outDir
// (trace-only mode) writes nothing.
func writeProfile(recs []record) error {
	if outDir == "" {
		return nil
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	base := filepath.Join(outDir, fmt.Sprintf("pigo-%s-%d-%s", label, os.Getpid(), startTS.Format("20060102-150405.000")))

	// Folded: one line per path, self-times summed in µs; zero-self spans are
	// omitted (grok convention — they carry no hotspot information).
	fold := make(map[string]int64)
	for _, r := range recs {
		if r.kind == recSpan {
			fold[r.path] += r.self.Microseconds()
		}
	}
	pathsOut := make([]string, 0, len(fold))
	for p, us := range fold {
		if us > 0 {
			pathsOut = append(pathsOut, p)
		}
	}
	sort.Slice(pathsOut, func(i, j int) bool {
		if fold[pathsOut[i]] != fold[pathsOut[j]] {
			return fold[pathsOut[i]] > fold[pathsOut[j]]
		}
		return pathsOut[i] < pathsOut[j]
	})
	var b strings.Builder
	for _, p := range pathsOut {
		fmt.Fprintf(&b, "{%s} %d\n", p, fold[p])
	}
	if err := writeFileAtomic(base+".folded", []byte(b.String())); err != nil {
		return err
	}

	// JSONL: one line per event, span rows with ts/name/dur/self (µs) plus the
	// folded path, mark rows for milestones.
	jl := make([]byte, 0, 128*len(recs))
	for _, r := range recs {
		if r.kind == recMark {
			jl = append(jl, `{"type":"mark","ts":`...)
			jl = appendJSONString(jl, r.start)
			jl = append(jl, `,"name":`...)
			jl = appendJSONText(jl, r.name)
			jl = append(jl, "}\n"...)
			continue
		}
		jl = append(jl, `{"type":"span","ts":`...)
		jl = appendJSONString(jl, r.start)
		jl = append(jl, `,"name":`...)
		jl = appendJSONText(jl, r.name)
		jl = append(jl, `,"path":`...)
		jl = appendJSONText(jl, r.path)
		jl = fmt.Appendf(jl, `,"dur_us":%d,"self_us":%d}`, r.dur.Microseconds(), r.self.Microseconds())
		jl = append(jl, '\n')
	}
	return writeFileAtomic(base+".jsonl", jl)
}

func appendJSONString(b []byte, t time.Time) []byte {
	v, _ := json.Marshal(t)
	return append(b, v...)
}

func appendJSONText(b []byte, s string) []byte {
	v, _ := json.Marshal(s)
	return append(b, v...)
}

// writeFileAtomic writes data to a temp file in the same directory and
// renames it into place, so a crash mid-write never leaves a torn profile
// behind (and a re-flush replaces the previous one cleanly).
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// HandleExitSignals installs the signal-path flush: on SIGINT/SIGTERM the
// profile is flushed before the process exits (code 130/143). It is a no-op
// when recording is off, so the default kill behavior is untouched for
// un-instrumented runs. A front-end that owns os.Interrupt (the line REPL
// cancels runs with it) must call SuppressInterruptExit.
func HandleExitSignals() {
	if !Enabled() {
		return
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-ch
		if sig == os.Interrupt && interruptSuppressed.Load() {
			return
		}
		Flush()
		if sig == os.Interrupt {
			os.Exit(130)
		}
		os.Exit(143)
	}()
}

// SuppressInterruptExit hands os.Interrupt back to the calling front-end: the
// signal handler keeps SIGTERM for itself but stops flushing-and-exiting on
// SIGINT, so Ctrl-C keeps its run-cancel meaning and the profile still lands
// via the normal-return flush when the front-end quits.
func SuppressInterruptExit() {
	interruptSuppressed.Store(true)
}
