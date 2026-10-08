package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/session"
)

// TestSanitizeAndStripTitles pins the two title sanitizers: display
// composition replaces forbidden runes with U+FFFD (grok
// sanitize_display_text), while the /rename input path strips them outright
// but keeps tab so "--auto\tmore" still trips the sole-argument check.
func TestSanitizeAndStripTitles(t *testing.T) {
	clean := "session foo bar"
	if got := sanitizeTitle(clean); got != clean {
		t.Errorf("sanitizeTitle(clean) = %q, want unchanged", got)
	}
	attack := "\x1b]0;PWNED\x07\x1b[2J safe text"
	got := sanitizeTitle(attack)
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Errorf("sanitizeTitle leaked control runes: %q", got)
	}
	if !strings.Contains(got, "safe text") {
		t.Errorf("sanitizeTitle dropped the payload tail: %q", got)
	}
	if got := sanitizeTitle("bi\u202edi win"); !strings.Contains(got, "\uFFFD") {
		t.Errorf("sanitizeTitle(bidi) = %q, want a replacement rune", got)
	}

	if got := stripRenameTitle("--au\x1bto\x07"); got != "--auto" {
		t.Errorf("stripRenameTitle ESC/BEL = %q, want %q", got, "--auto")
	}
	if got := stripRenameTitle("--auto\tmore"); got != "--auto\tmore" {
		t.Errorf("stripRenameTitle dropped the tab: %q", got)
	}
}

// TestTruncateAndSessionTitleChain covers the title chain precedence (grok
// entry_title): manual rename wins over the first user prompt, which wins
// over "session <id8>"; truncation caps at 60 runes with an ellipsis.
func TestTruncateAndSessionTitleChain(t *testing.T) {
	long := strings.Repeat("é", 61)
	got := truncateTitle(long)
	if got != strings.Repeat("é", 60)+"…" {
		t.Errorf("truncateTitle(61 runes) = %d runes, want 60 + ellipsis", len([]rune(got)))
	}
	if got := truncateTitle("short"); got != "short" {
		t.Errorf("truncateTitle(short) = %q, want unchanged", got)
	}

	if got := sessionTitle("manual", "first prompt", "20261007-abcd1234"); got != "manual" {
		t.Errorf("manual tier lost: %q", got)
	}
	if got := sessionTitle("", "first prompt", "20261007-abcd1234"); got != "first prompt" {
		t.Errorf("first-prompt tier lost: %q", got)
	}
	if got := sessionTitle("   ", "", "20261007-abcd1234"); got != "session 20261007" {
		t.Errorf("id8 tier = %q, want %q", got, "session 20261007")
	}
}

// TestTerminalTitleComposition pins the OSC payload shape: app-name
// fallback when empty, the " - pigo" suffix, and the 80-rune total cap
// (grok terminal_title_string).
func TestTerminalTitleComposition(t *testing.T) {
	if got := terminalTitle(""); got != "pigo" {
		t.Errorf("terminalTitle(empty) = %q, want %q", got, "pigo")
	}
	if got := terminalTitle("My chat"); got != "My chat - pigo" {
		t.Errorf("terminalTitle = %q, want %q", got, "My chat - pigo")
	}
	long := strings.Repeat("a", 90)
	got := terminalTitle(long)
	if len([]rune(got)) > 80 {
		t.Errorf("terminalTitle = %d runes, want capped at 80", len([]rune(got)))
	}
	if !strings.HasSuffix(got, " - pigo") {
		t.Errorf("capped title lost the suffix: %q", got)
	}
}

// TestTerminalWindowTitleRunningPrefix drives the View payload through the
// model: a session-less frame shows the bare app name, a live frame resolves
// the chain, and the running prefix flips with the run state (qwen
// titleStatusPrefix).
func TestTerminalWindowTitleRunningPrefix(t *testing.T) {
	m := NewModel(Options{})
	if got := m.terminalWindowTitle(); got != "pigo" {
		t.Errorf("session-less title = %q, want %q", got, "pigo")
	}

	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{Model: "m", ProviderName: "p"})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	m = apply(t, NewModel(Options{Model: "m", ProviderName: "p"}).withSession(s, nil),
		tea.WindowSizeMsg{Width: 80, Height: 30})
	want := "session " + shortID(s.header.ID) + " - pigo"
	if got := m.terminalWindowTitle(); got != want {
		t.Errorf("fresh title = %q, want %q", got, want)
	}

	m.running = true
	want = "◐ " + want
	if got := m.terminalWindowTitle(); got != want {
		t.Errorf("running title = %q, want %q", got, want)
	}
	if v := m.View(); v.WindowTitle != want {
		t.Errorf("View.WindowTitle = %q, want %q", v.WindowTitle, want)
	}
}

// TestStoreSetTitlePersists exercises Store.SetTitle against a real store:
// an existing session keeps its entries and UpdatedAt while gaining the title
// fields, a missing session file is created header-only (rename before the
// first turn), and the --auto reset round-trips.
func TestStoreSetTitlePersists(t *testing.T) {
	store := newTestStore(t)
	id := saveSession(t, store, sessMsg("alpha question", "alpha reply"))
	before, _, err := store.LoadEntries(id)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}

	if err := store.SetTitle(before, "Fix Login Bug", true); err != nil {
		t.Fatalf("SetTitle: %v", err)
	}
	header, entries, err := store.LoadEntries(id)
	if err != nil {
		t.Fatalf("LoadEntries after rename: %v", err)
	}
	if header.Title != "Fix Login Bug" || !header.TitleIsManual {
		t.Errorf("header title = %q manual=%v, want %q true", header.Title, header.TitleIsManual, "Fix Login Bug")
	}
	if len(entries) != 2 {
		t.Errorf("entries = %d, want 2 preserved", len(entries))
	}
	if !header.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("UpdatedAt moved: %v -> %v", before.UpdatedAt, header.UpdatedAt)
	}

	// --auto reset clears both fields on disk.
	if err := store.SetTitle(header, "", false); err != nil {
		t.Fatalf("SetTitle(--auto): %v", err)
	}
	header, _, err = store.LoadEntries(id)
	if err != nil {
		t.Fatalf("LoadEntries after reset: %v", err)
	}
	if header.Title != "" || header.TitleIsManual {
		t.Errorf("reset header title = %q manual=%v, want empty", header.Title, header.TitleIsManual)
	}

	// A rename on a never-persisted session creates the file header-only.
	fresh := session.SessionHeader{
		ID:        session.NewID(time.Now().UTC()),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := store.SetTitle(fresh, "early rename", true); err != nil {
		t.Fatalf("SetTitle(fresh): %v", err)
	}
	header, entries, err = store.LoadEntries(fresh.ID)
	if err != nil {
		t.Fatalf("LoadEntries(fresh): %v", err)
	}
	if header.Title != "early rename" || len(entries) != 0 {
		t.Errorf("fresh header = %q (%d entries), want title with 0 entries", header.Title, len(entries))
	}
}

// TestRenameCommandFlow drives the /rename slash path end to end against a
// real store: rename persists + feeds the terminal title, --auto falls back
// to the first-prompt tier, and the grok grammar edges (sole-argument
// --auto, case-sensitivity, blank, overlong) behave as specified.
func TestRenameCommandFlow(t *testing.T) {
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{Model: "m", ProviderName: "p"})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	m := apply(t, NewModel(Options{Model: "m", ProviderName: "p"}).withSession(s, nil),
		tea.WindowSizeMsg{Width: 80, Height: 30})

	// Rename: transcript feedback, in-memory header, disk header, tab title.
	m = typeInto(t, m, "/rename Fix Login Bug").(Model)
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	content := stripANSI(m.transcript.renderAll())
	if !strings.Contains(content, `renamed session to "Fix Login Bug"`) {
		t.Errorf("rename feedback missing:\n%s", content)
	}
	if m.session.header.Title != "Fix Login Bug" || !m.session.header.TitleIsManual {
		t.Fatalf("header after rename = %q manual=%v", m.session.header.Title, m.session.header.TitleIsManual)
	}
	if header, _, err := store.LoadEntries(m.session.header.ID); err != nil || header.Title != "Fix Login Bug" {
		t.Errorf("disk header after rename = %q (err %v)", header.Title, err)
	}
	if want := "Fix Login Bug - pigo"; m.View().WindowTitle != want {
		t.Errorf("tab title after rename = %q, want %q", m.View().WindowTitle, want)
	}

	// --auto resets to the auto chain; the fresh session has no prompt yet,
	// so the id8 tier shows.
	m = typeInto(t, m, "/rename --auto").(Model)
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.session.header.Title != "" || m.session.header.TitleIsManual {
		t.Fatalf("--auto left title=%q manual=%v", m.session.header.Title, m.session.header.TitleIsManual)
	}
	if want := "session " + shortID(m.session.header.ID) + " - pigo"; m.View().WindowTitle != want {
		t.Errorf("tab title after --auto = %q, want %q", m.View().WindowTitle, want)
	}

	// grok grammar edges via the message layer (the input cannot type tabs).
	if got := renameMessage(m.session, "--auto Something"); got != "(rename: --auto takes no title)" {
		t.Errorf("--auto with title = %q", got)
	}
	if got := renameMessage(m.session, "--auto\tmore"); got != "(rename: --auto takes no title)" {
		t.Errorf("tab-separated --auto = %q", got)
	}
	if got := renameMessage(m.session, "--AUTO"); !strings.Contains(got, `renamed session to "--AUTO"`) {
		t.Errorf("--AUTO must be an ordinary rename, got %q", got)
	}
	m.session.header.Title, m.session.header.TitleIsManual = "", false
	if got := renameMessage(m.session, "   "); !strings.Contains(got, "usage: /rename") {
		t.Errorf("whitespace-only arg = %q, want the usage line", got)
	}
	if got := renameMessage(m.session, "\x07"); got != "(rename: title must not be blank)" {
		t.Errorf("control-only title = %q", got)
	}
	if got := renameMessage(m.session, strings.Repeat("é", 101)); got != "(rename: title too long (max 100 characters))" {
		t.Errorf("overlong title = %q", got)
	}

	// --auto on a session with a first prompt resolves to that prompt.
	s.agentCtx.Messages = agentcore.MessageList{
		agentcore.UserMessage{RoleField: agentcore.RoleUser, Content: agentcore.ContentList{agentcore.NewTextContent("alpha question about maps")}},
	}
	if got, want := m.session.displayTitle(), "alpha question about maps"; got != want {
		t.Errorf("auto chain = %q, want %q", got, want)
	}
}

// TestSessionsPickerUsesManualTitle checks the S2/S8 seam: the picker's title
// column prefers the persisted manual title over the first user prompt.
func TestSessionsPickerUsesManualTitle(t *testing.T) {
	store := newTestStore(t)
	id := saveSession(t, store, sessMsg("alpha question about maps", "alpha reply"))
	header, _, err := store.LoadEntries(id)
	if err != nil {
		t.Fatalf("LoadEntries: %v", err)
	}
	if err := store.SetTitle(header, "manual title", true); err != nil {
		t.Fatalf("SetTitle: %v", err)
	}

	entries, note := gatherSessions(store, id)
	if note != "" {
		t.Fatalf("gatherSessions note = %q", note)
	}
	if entries[0].title != "manual title" {
		t.Errorf("picker title = %q, want the manual title", entries[0].title)
	}

	// An unsanitized header title falls back safely to the auto chain.
	header.Title = "   "
	if err := store.SetTitle(header, "   ", true); err != nil {
		t.Fatalf("SetTitle(blank): %v", err)
	}
	entries, _ = gatherSessions(store, id)
	if entries[0].title != "alpha question about maps" {
		t.Errorf("picker title after blank manual = %q, want the first prompt", entries[0].title)
	}
}
