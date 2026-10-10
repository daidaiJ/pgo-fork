package tui

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/cli"
	"github.com/smallnest/pigo/internal/cli/memstatus"
	"github.com/smallnest/pigo/internal/cli/prompts"
	"github.com/smallnest/pigo/internal/cli/status"
	"github.com/smallnest/pigo/internal/cli/ui"
	"github.com/smallnest/pigo/internal/memory"
	"github.com/smallnest/pigo/internal/provider"
	"github.com/smallnest/pigo/internal/runtime"
	"github.com/smallnest/pigo/internal/spans"
	"github.com/smallnest/pigo/internal/toolrules"
)

// Model is the root Bubble Tea model for the full-screen TUI. It composes a
// scrolling transcript (US-005) with the persistent status bar (#386) and a
// minimal input line, and owns the run lifecycle: on prompt submit it starts an
// agent run through the event bridge (bridge.go) and pumps the resulting tea.Msg
// stream into the transcript one message at a time. Downstream nodes grow the
// input into a full textarea (#390), render tool cards (#389), and wire the real
// session/run assembly (#392); the Init/Update/View contract and the alt-screen
// + quit-key handling stay stable.
type Model struct {
	opts  Options
	theme Theme

	// uiProbe closes the startup.ui_init span on the first View call (T1.1):
	// Run seeds it before tea.NewProgram; it stays nil in tests and when span
	// recording is off (spans.Probe is nil-safe either way).
	uiProbe *spans.Probe

	// width and height track the terminal size reported by tea.WindowSizeMsg.
	// They are zero until the first size message arrives; View degrades to a
	// minimal render in that window.
	width  int
	height int

	// transcript is the scrolling message log (user / assistant / system turns).
	transcript transcript

	// input is the multi-line prompt editor (#390). It wraps a bubbles textarea
	// so CJK / emoji are edited by rune (no dropped-byte bug), Enter submits and
	// Shift+Enter inserts a newline. It is blurred while a run is in flight.
	input input

	// history holds previously submitted inputs (prompts and slash commands, in
	// order), and histIdx is the browse cursor into it: len(history) means "not
	// browsing — on the live draft", any smaller index points at a recalled entry.
	// histDraft stashes the in-progress buffer when browsing begins so ↓ past the
	// newest entry restores it. ↑/↓ walk history when the caret is on the first /
	// last line of the composer, so multi-line editing is unaffected.
	history   []string
	histIdx   int
	histDraft string

	// running is true while an agent run is draining through runCh. Submit is
	// gated on it so a new run cannot start mid-run (Enter enqueues instead);
	// typing stays live so the next prompt can be drafted while output streams
	// — and because the composer is never blurred mid-run, Windows IME
	// composition state survives (a blur/focus cycle resets it to English).
	running bool
	// queued holds prompts entered while a run was in flight (Enter during a
	// run enqueues instead of submitting). runEndMsg pops the first entry and
	// starts it, so the queue drains one prompt per ended run.
	queued []string
	// sendNow holds prompts submitted with Alt+Enter while a run streams
	// (T8.3, grok interjection): they start when the current run ends, ahead
	// of queued. runEndMsg drains them before queued.
	sendNow []string
	// queueHeld freezes the queue after an interrupt (T8.3 ruling): runEndMsg
	// keeps the queued prompts instead of auto-starting the next one, and the
	// next manual action — a bare Enter promoting the front prompt, or a
	// fresh submit — resumes the normal drain. Set by the Esc/Ctrl+C
	// interrupt, cleared in startPrompt.
	queueHeld bool
	// qpane is the queue pane's selection state (T8.3): rows render whenever
	// prompts are queued; with an empty composer the arrow keys select a row
	// for deletion (see queuepane.go).
	qpane queuePane
	// runCh is the bridge channel for the in-flight run, or nil when idle. Update
	// re-issues waitForEvent(runCh) after every bridged msg except runEndMsg.
	runCh chan tea.Msg

	// startRunFn launches an agent run for the submitted prompt, returning the
	// bridge channel and the first waitForEvent Cmd (see bridge.startRun). It is
	// bound to runSession.startRun by withSession (#392): the real binding
	// constructs an AgentContext + RunConfig from opts and the live session. It is
	// nil for a session-less model (the pure constructor / tests), in which case a
	// submit records the prompt but starts no run.
	startRunFn func(prompt string) (chan tea.Msg, tea.Cmd)

	// session is the assembled run/persistence state (store, header, growing
	// context, live config). It is nil for a session-less model; when set, the
	// model persists the conversation to ~/.pigo/sessions after each turn ends.
	session *runSession

	// interruptFn cancels the in-flight run (the first stage of the two-stage
	// interrupt, FR-14): pressing Esc / Ctrl+C while running signals the run to
	// stop rather than quitting the program. It is a seam wired alongside
	// startRunFn by session assembly (#392) — typically the run ctx's cancel
	// func. Until then it may be nil, in which case an interrupt while running is
	// a safe no-op (the pump keeps draining until it ends on its own).
	interruptFn func()

	// quitting is set when a quit key (Ctrl+C / Ctrl+D) is seen, so View can be a
	// no-op on the final frame while the program tears down and restores the
	// terminal.
	quitting bool

	// statusBar renders the usage row below the input (S12; historically the
	// bottom status bar — the type name survives the migration). It is fed the
	// terminal width, per-turn usage payloads, and the stream's first-token
	// timing; renderContent draws it below the input editor.
	statusBar statusBar

	// header renders the page header line (S1): git branch + cwd left, context
	// tokens / window right. It consumes the git probe and telemetry events the
	// status bar used to own.
	header header

	// ctxPanel is the context-usage overlay panel (grok context panel
	// reference): toggled by /context, opened at its plan-quota tab by /usage,
	// modal while open.
	ctxPanel contextPanel

	// usagePanel is the last provider plan-quota lookup the /usage tab
	// renders; nil before the first one. usageWaiting marks a lookup in
	// flight: the probe runs off the tea loop, so the panel draws at once and
	// fills in when it lands.
	usagePanel   *cli.QuotaSection
	usageWaiting bool

	// turnStart anchors the running line's current-turn elapsed readout; reset
	// at run start and at each turn boundary.
	turnStart time.Time

	// cwd is the launch directory, captured once at construction and reused for
	// the git probe and the status bar's path display.
	cwd string

	// slash is the shared slash-command registry (#383) the TUI consults exactly
	// as the REPL does: /model, /help, user templates, plugin commands and skills.
	// It is bound to live so a /model switch mutates the same config the run loop
	// reads. Built in NewModel (built-ins + disk templates) and rebuilt in
	// withSession against the session's live config.
	slash *runtime.SlashRegistry
	// slashDeps and slashCreds are the surface face and credential store the
	// registry was assembled with (T7.7): the session-less constructor keeps
	// its own (arg forms like /skills disable work before a session binds);
	// withSession rebinds both to the session's, matching the registry
	// rebinding. The intent executor reads them.
	slashDeps  *prompts.SurfaceDeps
	slashCreds *provider.CredentialStore
	// live is the mutable run configuration the /model command switches. In a
	// session-bound model it is the SAME pointer the run loop reads (set by
	// withSession), so a switch takes effect on the next turn.
	live *cli.LiveConfig
	// menu is the autocomplete popup shown while a "/name" is being typed (#391).
	// It filters slash by the typed prefix; the model intercepts arrow/Tab/Enter
	// keys to drive it before delegating to the textarea.
	menu slashMenu
	// modelMenu is the /model argument-completion popup (T7.3 S1, grok
	// switcher alignment): active while the buffer is a "/model …" invocation
	// in its argument stage, listing preset + fetched models with the current
	// one marked. It owns the arrow/Tab/Enter/Esc keys while open.
	modelMenu modelMenu
	// sessionsP is the /sessions picker overlay (T7.3 S8): a modal panel
	// listing persisted sessions (title/time/model/cwd) with filter, resume on
	// Enter and an armed delete on d+y.
	sessionsP sessionsPanel
	// skillsP / mcpP are the interactive panels behind bare /skills and /mcp
	// (T7.3 interactive redesign): grok ExtensionsModal alignment — list,
	// filter, Enter toggles the row's enabled state. Inactive when closed.
	skillsP listPanel
	mcpP    listPanel
	// lspP is the /lsp panel (T8.2, two-level like mcpP: server row + the
	// deferred tool family as children).
	lspP listPanel
	// shellP is the /shell panel (T8.4): the backend list with the live one
	// marked; Space switches (config write + live swap).
	shellP listPanel
	// modeP is the /mode panel (T7.6): the approval-posture list with the
	// live one marked; Space switches (session-scoped, not persisted).
	modeP listPanel

	// toolCards indexes the rich tool-call cards (#389, US-006) by tool-call id so
	// a toolEndMsg can locate the card started earlier and flip its state / attach
	// the parsed response. Each card is also appended to the transcript as an
	// ordered block (by pointer), so mutating one here re-renders it inline on the
	// next reflow.
	toolCards map[string]*toolCard

	// draggingScrollbar is set while the left mouse button is held after pressing
	// on the transcript scrollbar column, so subsequent motion events drag the
	// thumb (and scroll the viewport) until the button is released.
	draggingScrollbar bool

	// sel is the current mouse text selection over the rendered shell (screen
	// cells). A left-press off the scrollbar starts it, drag extends it, and it
	// persists after release so Ctrl+C can copy the highlighted text.
	sel selection

	// pendingClick holds the cell of a left press awaiting its release: a click
	// is only confirmed when the button comes back up on the same cell (grok
	// two-phase click), so a text-selection drag that starts on a block header
	// never folds the block.
	pendingClick pendingMouseClick

	// lastClick is the most recent confirmed bare click; a second confirmed
	// click on the same cell within mouseMultiClickWindow is a double click,
	// which toggles the fold of the block under the cursor (grok parity).
	lastClick lastMouseClick

	// spinner is the animated "working" indicator (verb + elapsed/token/effort
	// stats) shown on the row above the input while a run is in flight.
	spinner spinner

	// subagents is the ordered set of live sub-agents dispatched by the `task`
	// tool (SPEC 4.4, US-006). A toolStartMsg with name=="task" adds a row (and
	// records its start time), subagentProgressMsg refreshes activity/tokens, and
	// the task's toolEndMsg removes it. View renders it as a multi-line panel just
	// above the spinner; it contributes zero rows when empty.
	subagents subagentPanel

	// askPort is the ask_user questionnaire port (T4.2), wired by Run alongside
	// the session; nil for session-less models. ask is the live question panel
	// state while the user is answering a questionnaire (nil otherwise).
	askPort *teaAskPort
	ask     *askPanel
	// approval is the live per-call approval panel state (T7.6 D-C1) while
	// the permission engine waits on a local answer (nil otherwise).
	approval *approvalPanel

	// pastes stores the full text of collapsed multi-line pastes, keyed by the id
	// shown in the "[Pasted text #N +M lines]" placeholder left in the composer.
	// submit expands the placeholders back to their content before sending, so a
	// large paste never floods the editor (mirroring Claude Code).
	pastes map[int]string
	// pasteSeq is the monotonic counter behind the paste placeholder ids. It keeps
	// climbing across submits so ids stay unique for the session.
	pasteSeq int

	// images maps the id shown in an "[Image #N]" placeholder to the temp PNG a
	// Ctrl+V / Cmd+V image paste was saved to. submit expands the placeholder to an
	// "@image:<path>" reference so BuildUserContent attaches the image as
	// multimodal content (mirroring Claude Code's image paste).
	images map[int]string
	// imageSeq is the monotonic counter behind the image placeholder ids.
	imageSeq int
}

// NewModel builds the root model from the assembled Options. It reads the
// current working directory (for the status bar's path display and git probe)
// and assembles the shared slash-command registry (#391), which reads the user
// prompt-template dirs (~/.pigo/{commands,prompts}) and the pre-loaded skills;
// missing dirs are not an error. The registry is bound here to a live config
// derived from Options; withSession rebinds it to the session's live config so a
// /model switch reaches the run loop.
func NewModel(opts Options) Model {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	theme := DefaultTheme()
	live := &cli.LiveConfig{
		Model:           opts.Model,
		ProviderName:    opts.ProviderName,
		Provider:        opts.Provider,
		BaseURL:         opts.BaseURL,
		Protocol:        opts.Protocol,
		ThinkingLevel:   opts.ThinkingLevel,
		MaxContext:      opts.MaxContext,
		ModelProfiles:   opts.Models,
		ProviderConfigs: opts.ProviderConfigs,
		Proxy:           opts.Proxy,
		// A startup config profile's explicit window/output-cap declarations
		// win over the catalog-derived values (0 = derive, as before).
		ContextWindow:   cli.SeedContextWindow(opts.Provider, opts.Model, opts.MaxContext, opts.ContextWindow),
		MaxOutputTokens: cli.SeedMaxOutputTokens(opts.Provider, opts.Model, opts.MaxOutputTokens),
	}
	// The session-less model still gets a registry (menu completion works
	// before withSession binds the session); its surface deps and creds stay
	// on the model — the intent executor serves the arg forms (/skills
	// disable x) session-less, while the panels need a live session.
	slashReg, slashDeps, slashCreds := newSlashRegistry(opts, live, nil)
	return Model{
		opts:       opts,
		theme:      theme,
		transcript: newTranscript(theme),
		input:      newInput(),
		cwd:        cwd,
		statusBar:  newStatusBar(theme, opts, cwd),
		header:     header{cwd: abbreviateHome(cwd)},
		toolCards:  make(map[string]*toolCard),
		slash:      slashReg,
		slashDeps:  &slashDeps,
		slashCreds: slashCreds,
		live:       live,
		menu:       newSlashMenu(theme),
		modelMenu:  modelMenu{theme: theme},
		spinner:    newSpinner(theme),
		pastes:     make(map[int]string),
		images:     make(map[int]string),
	}
}

// withSession binds the assembled run session to the model: it wires the real
// run seam (startRunFn) and, for a resumed session, replays the prior history
// into the transcript so the user sees the conversation so far before entering
// interactive mode. Run calls it right after NewModel; the session-less
// constructor path (tests, pure construction) leaves startRunFn nil.
func (m Model) withSession(s *runSession, history []agentcore.Message) Model {
	m.session = s
	m.startRunFn = s.startRun
	m.interruptFn = s.interrupt
	// Rebind the registry to the session's own one (assembled against s.live, the
	// very config the run loop reads via buildConfig) so /model mutates the live
	// config, /trust reaches the session's trust manager (registered in
	// newRunSessionWithStore), and /status can list skill/plugin/user commands.
	m.live = s.live
	m.slash = s.slash
	m.slashDeps = &s.surface
	m.slashCreds = s.slashCreds
	// Seed the header's context readout (S1/S2) so it is visible from the first
	// frame: the window is known from the live config and the used tokens come
	// from the same live estimate the /context panel falls back to, instead of
	// waiting for the end-of-run TelemetryEvent.
	m.header.setTelemetry(estimateTokens(s.agentCtx.SystemPrompt)+messageTokens(s.agentCtx.Messages), s.live.ContextWindow)
	// Seed the usage row with the session's cumulative accounting (O1/T7.3c) so
	// a resumed session shows its totals from the first frame instead of
	// restarting at zero. The live per-turn anchors stay reset.
	m.statusBar.usage.seed(s.usage.Stats())
	m.transcript.addBanner(renderBanner(m.theme, m.opts, m.cwd))
	seedTranscript(&m.transcript, history)
	return m
}

// Init implements tea.Model. It kicks off the async git probe so the status bar
// can show the branch/dirty state as soon as it resolves; the alt-screen is
// requested declaratively via the AltScreen field on the View returned by View.
func (m Model) Init() tea.Cmd {
	return tea.Batch(fetchGitCmd(m.cwd), m.input.Focus(), m.waitAsk(), m.waitApproval(), func() tea.Msg {
		return tea.RequestBackgroundColor()
	})
}

// Update implements tea.Model. It tracks the terminal size, drives the minimal
// input line, starts runs on submit, and pumps bridged run events into the
// transcript and status bar. It quits on the standard exit keys (Ctrl+C /
// Ctrl+D).
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.relayout()
		return m, nil

	case gitInfoMsg:
		m.header.setGit(msg)
		return m, nil

	case tea.BackgroundColorMsg:
		// Feed the terminal's real background to the Markdown renderer so glamour
		// picks a matching light/dark palette WITHOUT issuing its own terminal
		// query (which would leak its reply into the input — see SetMarkdownDark).
		// Re-flow so any already-finalized assistant block re-renders in the right
		// palette.
		SetMarkdownDark(msg.IsDark())
		m.transcript.reflow()
		return m, nil

	case tea.MouseWheelMsg:
		// Mouse-wheel scrolling reaches the transcript viewport whether idle or
		// running, so history stays scrollable with the wheel — not just PgUp/PgDn.
		// The viewport (MouseWheelEnabled by default) turns the wheel event into a
		// scroll; enabling MouseModeCellMotion in View is what makes the terminal
		// deliver these events under the alt-screen at all.
		cmd := m.transcript.update(msg)
		m.sel = selection{}
		return m, cmd

	case tea.MouseClickMsg:
		// A left press on the scrollbar column grabs the thumb (jump + drag). A
		// left press anywhere else records a pending click and starts a text
		// selection at that cell; the press itself never folds — grok parity
		// (two-phase click + double-click fold, 2026-10-07 对齐原型): the fold
		// decision happens on release at the same cell, so dragging off a
		// header row never toggles a block, and a double click on any row of a
		// foldable block toggles it while single clicks stay selection-only.
		if msg.Button == tea.MouseLeft {
			if m.onScrollbar(msg.X, msg.Y) {
				m.draggingScrollbar = true
				m.pendingClick.ok = false
				m.transcript.scrollToRow(msg.Y)
				return m, nil
			}
			if !m.ctxPanel.open && !m.sessionsP.open && m.width > 0 && m.height > 0 {
				m.pendingClick = pendingMouseClick{ok: true, pt: point{msg.X, msg.Y}}
			}
			m.sel = selection{active: true, anchor: point{msg.X, msg.Y}, cursor: point{msg.X, msg.Y}}
			return m, nil
		}
		return m, nil

	case tea.MouseMotionMsg:
		// While the thumb is grabbed, vertical motion drags it regardless of the
		// cursor's column. Otherwise, motion after a left press extends the text
		// selection to the current cell.
		if m.draggingScrollbar {
			m.transcript.scrollToRow(msg.Y)
			return m, nil
		}
		if m.sel.active {
			m.sel.cursor = point{msg.X, msg.Y}
		}
		return m, nil

	case tea.MouseReleaseMsg:
		m.draggingScrollbar = false
		if m.sel.active {
			m.sel.cursor = point{msg.X, msg.Y}
		}
		// Confirm the pending click only when the button came back up on the
		// same cell with no drag in between; anything else was a selection
		// drag (or a scrollbar/other-surface release) and never folds.
		if m.pendingClick.ok {
			pt := m.pendingClick.pt
			m.pendingClick.ok = false
			if m.sel.empty() && msg.X == pt.x && msg.Y == pt.y {
				m.confirmClick(pt)
			}
		}
		return m, nil

	case tea.PasteMsg:
		// Bracketed paste (e.g. Cmd+V / right-click paste): the terminal delivers
		// the whole clipboard payload as one message. A multi-line paste is
		// collapsed to a compact placeholder (expanded at submit); a single-line
		// paste is inserted verbatim. See handlePaste. Allowed mid-run too: the
		// composer stays live while a run streams (only submit is gated).
		return m.handlePaste(msg.Content)

	case tea.ClipboardMsg:
		// OSC52 clipboard read reply (from tea.ReadClipboard on Ctrl+V / Cmd+V).
		// Route through the same collapse-or-insert path as bracketed paste.
		return m.handlePaste(msg.Content)

	case clipboardImageMsg:
		// Reply to a Ctrl+V / Cmd+V image-read attempt. With an image, drop an
		// "[Image #N]" placeholder (expanded to an @image reference at submit); with
		// none, fall back to a normal OSC52 text read so plain-text paste still works.
		if msg.ok {
			return m.handleImagePaste(msg.path)
		}
		return m, tea.ReadClipboard

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case askUserMsg:
		// An ask_user tool in the running turn handed its questionnaire over
		// (T4.2). A stale arrival after the run ended (a cancel raced the
		// request send) is dropped; either way keep exactly one waitAsk in
		// flight so the next questionnaire is never lost.
		if m.running {
			m.ask = newAskPanel(msg.q)
			m.relayout()
		}
		return m, m.waitAsk()

	case approvalReqMsg:
		// A pending approval ask arrived from the permission engine (T7.6
		// D-C1). A stale arrival after the run ended (a cancel raced the
		// request send) is dropped; either way keep exactly one
		// waitApproval in flight so the next ask is never lost.
		if m.running {
			m.approval = newApprovalPanel(msg.req)
			m.relayout()
		}
		return m, m.waitApproval()

	case spinnerTickMsg:
		// Advance the working animation and schedule the next frame, but only while
		// a run is in flight; once idle the tick is not re-issued so the spinner
		// stops without a lingering goroutine.
		if !m.running {
			return m, nil
		}
		m.spinner.advance()
		return m, m.tickSpinner()

	case textDeltaMsg:
		m.spinner.addTokens(msg.delta)
		m.spinner.setActivity("Responding")
		m.statusBar.usage.markFirstDelta(time.Now())
		m.statusBar.usage.addChars(msg.delta, false)
		m.transcript.appendDelta(msg.delta)
		m.remoteEcho(msg.delta)
		return m, m.pumpNext()

	case thinkingDeltaMsg:
		// Reasoning-model thinking stream (T1.3): append to the dimmed thinking
		// block. Spinner token stats intentionally stay reply-only, and the
		// remote echo intentionally skips thinking — the paired view shows the
		// reply, not the reasoning. The usage row counts thinking characters
		// toward its think-share segment.
		m.spinner.setActivity("Thinking")
		m.statusBar.usage.addChars(msg.delta, true)
		m.transcript.appendThinking(msg.delta)
		return m, m.pumpNext()

	case turnEndMsg:
		m.transcript.finalizeTurn(msg.msg)
		// Usage row (O1/T7.3c): the loop recorded this turn into the session
		// ledger while it settled (timing, retries and sub-agent attribution
		// included), so the row re-aggregates the ledger and reports the
		// session-cumulative numbers /usage shows. A session-less model (tests,
		// pure construction) has no ledger, so the row just shows the model.
		u := msg.msg.Usage
		if m.session != nil && m.session.usage != nil {
			m.statusBar.usage.seed(m.session.usage.Stats())
		}
		m.statusBar.usage.endTurn()
		if u != nil {
			// Refresh the header readout per turn (S1/S2): input + cache buckets
			// + output approximates the context the next request will see, so the
			// figure tracks a long agentic run instead of jumping only at run end.
			// anthropic splits cache from input (sum all three); openai folds the
			// cached share into input with the cache buckets at zero — both add up.
			m.header.setTelemetry(u.InputTokens+u.CacheReadTokens+u.CacheWriteTokens+u.OutputTokens, m.live.ContextWindow)
		}
		m.turnStart = time.Now()
		// Surface a failed or empty turn so a provider/API error is never silent.
		// The loop delivers request failures (e.g. a 4xx from the endpoint) as a
		// terminal assistant message with stopReason error/aborted via TurnEndEvent
		// — not as the run's result error (runEndMsg.err) — so without this check
		// the TUI would finalize an empty turn and return to the prompt with no
		// output at all. Mirrors the headless driver and the line-based REPL.
		switch msg.msg.StopReason {
		case agentcore.StopReasonError:
			reason := strings.TrimSpace(msg.msg.ErrorMessage)
			if reason == "" {
				reason = "the provider returned an error with no message"
			}
			m.transcript.addSystem("error: " + reason)
		case agentcore.StopReasonAborted:
			m.transcript.addSystem("error: aborted")
		default:
			// A turn that ends cleanly (end_turn) but produced no text, no thinking,
			// and no tool calls means the endpoint accepted the request but sent back
			// nothing usable (e.g. a 200 whose body was not in the wire format this
			// protocol expects). Note it instead of showing nothing.
			if len(msg.msg.Content) == 0 && len(msg.results) == 0 {
				m.transcript.addSystem("note: empty response from the model (no content). " +
					"Check that --model, --base-url and --protocol match the same provider.")
			}
		}
		return m, m.pumpNext()

	case toolStartMsg:
		// Create a rich tool-call card, index it by id for the later end event, and
		// append it as an ordered transcript block so it renders inline (#389).
		card := &toolCard{id: msg.id, name: msg.name, input: msg.input, state: cardRunning}
		m.toolCards[msg.id] = card
		m.transcript.addToolCard(card)
		m.spinner.setActivity("Running " + toolVerb(msg.name).verb)
		m.remoteEcho("\n· " + msg.name + "\n")
		// A `task` tool call dispatches a sub-agent: open a status-panel row keyed by
		// the tool-call id (matching the later progress/end events) and record its
		// start so elapsed can be shown live (SPEC 4.4).
		if msg.name == "task" {
			m.subagents.add(msg.id, taskDescription(msg.input), time.Now())
			m.relayout() // the new panel row shrinks the transcript to fit
		}
		return m, m.pumpNext()

	case toolUpdateMsg:
		// A `task` sub-agent forwards its text as incremental tool-update deltas;
		// accumulate them onto the matching panel row so the expanded view can show
		// the running output. appendOutput is a no-op for non-task ids (nothing to
		// attach to), so ordinary tool updates are unaffected. Relayout only when the
		// delta lands on the currently expanded row, whose growing output changes the
		// panel height; other rows' output is buffered without touching the layout.
		m.subagents.appendOutput(msg.id, msg.partial)
		if m.subagents.expandedID() == msg.id {
			m.relayout()
		}
		return m, m.pumpNext()

	case subagentProgressMsg:
		// A running sub-agent reported structured progress: refresh its panel row's
		// activity/tokens. update adds the row if it is missing so a late/out-of-order
		// progress (arriving before the task's start) is still shown (SPEC 5.4).
		m.subagents.update(msg.id, msg.desc, msg.activity, msg.tokens, time.Now())
		m.relayout() // a first-seen id adds a row; keep the transcript sized to it
		return m, m.pumpNext()

	case toolEndMsg:
		// Flip the card's state and attach the parsed response tree. The card is
		// held by pointer in the transcript, so a reflow re-renders it in place.
		if card, ok := m.toolCards[msg.id]; ok {
			if msg.ok {
				card.state = cardSuccess
			} else {
				card.state = cardWarn
			}
			// A tool that reported a diff (edit) gets a colored Diff section; the
			// diff is also embedded in the result text, so strip it there to keep
			// the card from showing the change twice (#560).
			result := msg.result
			if diff, ok := ui.DiffFromDetails(msg.details); ok {
				card.diff = diff
				result = stripDiffTail(result)
			}
			// Raw tool output can carry 16-color SGR codes and the cursor /
			// screen-control sequences of progress bars; normalize both
			// before the card parses it into the response tree (T2.3).
			result = RemapANSI16(StripCursorControl(result), m.theme.ANSI)
			card.response = parseToolResult(result)
			m.transcript.reflow()
		}
		// Settle the sub-agent's status-panel row (a no-op for non-task tools whose
		// id was never added): a completed sub-agent is removed as before, while a
		// failed one (T5.1 envelope) stays visible with its stop_reason until run
		// end clears the panel.
		if _, wasSub := m.subagents.byID[msg.id]; wasSub {
			m.subagents.finish(msg.id, failedReasonFromDetails(msg.details, msg.ok))
			m.relayout()
		}
		return m, m.pumpNext()

	case telemetryMsg:
		// Feed the page header's context readout (S1/S2) and retain the event on
		// the session's telemetry holder so /status can render the cumulative +
		// last-run telemetry report (US-002, #292). Then keep the pump running.
		m.header.setTelemetry(msg.ev.ContextTokens, msg.ev.ContextWindow)
		if m.session != nil && m.session.telemetry != nil {
			m.session.telemetry.Fold(msg.ev)
		}
		return m, m.pumpNext()

	case compactionStartMsg:
		m.spinner.pin("Compacting conversation")
		return m, m.pumpNext()

	case usageQuotaMsg:
		// The /usage tab's provider probe landed (or failed): keep the
		// section for the renderer — an error renders as one dim line, never
		// as a zeroed plan.
		m.usagePanel = msg.sec
		m.usageWaiting = false
		m.relayout()
		return m, nil

	case compactionMsg:
		// T3.3 marker model: compaction only inserts a marker into the live
		// list, which the next persist()'s tail append carries into the tree —
		// no flatten bookkeeping needed here.
		m.spinner.unpin()
		m.transcript.addSystem("(context compacted)")
		return m, m.pumpNext()

	case rebuildDoneMsg:
		// A manual /rebuild finished: clear the pinned "Preparing conversation
		// context…" spinner (no run is pumping, so stop it and drop out of the
		// running state) and report the outcome. rebuild() already applied the
		// rebuilt messages and set session.compacted on success.
		m.spinner.unpin()
		m.spinner.stop()
		m.running = false
		if msg.err != nil {
			m.transcript.addSystem("rebuild failed: " + msg.err.Error() + " (context left unchanged)")
		} else {
			m.transcript.addSystem(msg.summary)
		}
		m.relayout()
		return m, nil

	case compactDoneMsg:
		// A manual /compact finished: clear the pinned "Compacting conversation"
		// spinner and report the outcome. compactCmd already applied the marker
		// insert + persist (T7.7: the async projection of the shared
		// cli.RunManualCompact core).
		m.spinner.unpin()
		m.spinner.stop()
		m.running = false
		m.transcript.addSystem(msg.summary)
		m.relayout()
		return m, nil

	case runEndMsg:
		m.running = false
		m.runCh = nil
		m.spinner.stop()
		// The run is over: any still-open sub-agent rows are stale (their tasks ended
		// with the run), so clear the panel to reclaim its height. A still-open
		// question panel is likewise stale (the tool returned via ctx cancellation),
		// and so is a pending approval (the ask returned via ctx cancellation and
		// the port already denied).
		m.subagents = subagentPanel{}
		m.ask = nil
		m.approval = nil
		m.relayout()
		if msg.err != nil {
			m.transcript.addSystem("Run ended: " + msg.err.Error())
		}
		// Persist the turn's new messages as a branch so the conversation survives
		// exit and can be resumed (FR-16). This is race-free: the pump goroutine
		// owns agentCtx.Messages during the run and only sends runEndMsg after
		// DrainStream returns (loop done), so no goroutine is still writing the
		// context when persist reads it here on the tea goroutine. A save failure
		// is surfaced but non-fatal.
		if m.session != nil {
			if err := m.session.persist(); err != nil {
				m.transcript.addSystem("Session save failed: " + err.Error())
			}
		}
		// The composer stays focused across the whole run (a blur/focus cycle
		// resets the Windows IME to English mid-session), so no re-focus here.
		// Re-probe git since a run may have changed the working tree. Then drain
		// the queue — T8.3 semantics: after an interrupt the queue is held
		// (prompts stay queued until a manual action resumes them), otherwise
		// the next prompt starts now, with slash-command entries executing
		// inline (they start no run) so the queue never stalls behind one.
		if m.queueHeld {
			if n := len(m.queueRows()); n > 0 {
				m.transcript.addSystem(fmt.Sprintf("(queue held — %d waiting; Enter runs the next)", n))
				m.relayout()
			}
			return m, fetchGitCmd(m.cwd)
		}
		var cmds []tea.Cmd
		for {
			next, ok := m.popQueueFront()
			if !ok {
				break
			}
			if strings.HasPrefix(next, "/") {
				var cmd tea.Cmd
				var mod tea.Model
				mod, cmd = m.runSlash(next)
				m = mod.(Model)
				cmds = append(cmds, cmd)
				if m.running {
					// The command started a run; its own runEndMsg drains the rest.
					return m, tea.Batch(cmds...)
				}
				continue
			}
			m.transcript.addUser(next)
			m.remoteEcho("\n> " + next + "\n")
			mod, cmd := m.startPrompt(next)
			cmds = append(cmds, cmd, fetchGitCmd(m.cwd))
			return mod, tea.Batch(cmds...)
		}
		if len(cmds) > 0 {
			return m, tea.Batch(cmds...)
		}
		return m, fetchGitCmd(m.cwd)

	case remoteInputMsg:
		// A prompt arrived from the paired browser (remote-control). Always re-issue
		// the listener so successive remote prompts keep arriving. While a run is in
		// flight the prompt is refused with a note (mirroring the local single-run
		// gate); when idle it is echoed as a user block and run — as a slash command
		// if it starts with "/", else a normal prompt.
		text := strings.TrimSpace(msg.text)
		if m.running || text == "" {
			if m.running && text != "" {
				m.transcript.addSystem("(remote input ignored: a run is in progress)")
				m.relayout()
			}
			return m, m.waitRemoteInput()
		}
		var cmd tea.Cmd
		var next tea.Model = m
		if strings.HasPrefix(text, "/") {
			next, cmd = m.runSlash(text)
		} else {
			m.transcript.addUser(text)
			m.remoteEcho("\n> " + text + "\n")
			m.relayout()
			next, cmd = m.startPrompt(text)
		}
		m = next.(Model)
		return m, tea.Batch(cmd, m.waitRemoteInput())
	}
	return m, nil
}

// handleKey processes a key press. It resolves the keys the shell owns —
// two-stage interrupt/quit, prompt submit, transcript scrolling — and delegates
// everything else (character entry, in-buffer cursor movement, Shift+Enter
// newline) to the input editor, which stays live while idle and while a run
// streams (the buffer is only read at submit). Keys are matched via
// KeyPressMsg.String() so the mapping is terminal-independent.
func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// While an approval panel is open (T7.6 D-C1), it owns the keys ahead of
	// every other surface: the pending ask must be answered before the
	// composer, panels, or the interrupt can react, and Esc inside the panel
	// denies the call instead of reaching the two-stage interrupt.
	if m.running && m.session != nil && m.approval.active() {
		consumed, reply := m.approval.handleKey(msg)
		if consumed {
			if reply != nil {
				if reply.always && m.session.trust != nil {
					m.session.trust.SetSessionTrust(m.session.cwd)
				}
				if m.session.approvalCh != nil {
					m.session.approvalCh.respond(*reply)
				}
				m.approval = nil
				m.relayout()
			}
			return m, nil
		}
	}

	// The context panel (TUI context-usage overlay) is modal while open: it
	// consumes every key. Tab/up/down/esc it handles itself; c copies the
	// session id (the model owns the clipboard Cmd).
	if m.ctxPanel.open {
		key := msg.String()
		if key == "c" && m.session != nil {
			return m, tea.SetClipboard(m.session.header.ID)
		}
		m.ctxPanel.handleKey(key)
		return m, nil
	}

	// The /sessions picker (T7.3 S8) is modal while open, exactly like the
	// context panel: it owns every key — navigation, filter typing, Enter
	// resume, the armed d/y delete — and never leaks them to the composer.
	if m.sessionsP.open {
		return m.handleSessionsKey(msg)
	}

	// The /skills and /mcp panels (T7.3 interactive redesign) are modal the
	// same way: navigation, filter typing, Enter toggle.
	if m.skillsP.open {
		return m.handleListPanelKey("skills", msg)
	}
	if m.mcpP.open {
		return m.handleListPanelKey("mcp", msg)
	}
	if m.lspP.open {
		return m.handleListPanelKey("lsp", msg)
	}
	if m.shellP.open {
		return m.handleListPanelKey("shell", msg)
	}
	if m.modeP.open {
		return m.handleListPanelKey("mode", msg)
	}

	// While idle with the /model (or /think) argument popup open, it owns the
	// arrow / Tab / Esc / Enter keys before the command menu gets them (T7.3
	// S1 + chained redesign): Enter on the model list chains into the effort
	// sub-list (grok), Enter on the effort list dispatches the composed line.
	if !m.running && m.modelMenu.active {
		switch msg.String() {
		case "up":
			m.modelMenu.moveUp()
			return m, nil
		case "down":
			m.modelMenu.moveDown()
			return m, nil
		case "tab":
			if it, ok := m.modelMenu.current(); ok {
				if m.modelMenu.phase == modelPhaseEffort {
					m.input.SetValue(m.modelMenu.chainCmd + it.id)
				} else {
					m.input.SetValue("/model " + it.id)
				}
				m.syncMenus()
			}
			m.relayout()
			return m, nil
		case "esc":
			m.modelMenu.close()
			m.relayout()
			return m, nil
		case "enter":
			if it, ok := m.modelMenu.current(); ok {
				if m.modelMenu.phase == modelPhaseList {
					// grok chain: the selection moves the buffer to
					// "/model <id> " and the same popup re-lists effort
					// levels; nothing is applied until the second Enter.
					m.modelMenu.enterEffort(it.id, m.live)
					m.input.SetValue("/model " + it.id + " ")
					m.relayout()
					return m, nil
				}
				line := m.modelMenu.chainCmd + it.id
				m.modelMenu.close()
				m.recordHistory(line)
				return m.runSlash(line)
			}
		}
	}

	// While idle with the autocomplete popup open, the arrow / Tab / Esc keys
	// drive the menu instead of the transcript or textarea (FR-15). Enter is left
	// to the main switch below, which routes through submit → runSlash so the
	// selected/typed command runs. These are matched via KeyPressMsg.String() so
	// the mapping is terminal-independent.
	if !m.running && m.menu.active {
		switch msg.String() {
		case "up":
			m.menu.moveUp()
			return m, nil
		case "down":
			m.menu.moveDown()
			return m, nil
		case "tab":
			m = m.completeSlash()
			m.relayout()
			return m, nil
		case "esc":
			m.closeMenus()
			m.relayout()
			return m, nil
		case "enter":
			return m.submitSlashSelected()
		}
	}

	// While an approval panel is open (T7.6 D-C1), the composer is disabled
	// and any keys it does not consume fall through to the running composer
	// below — the panel itself answers only y/n/a/s/Esc/Enter.

	// While a question panel is open (T4.2), the composer is disabled and the
	// panel owns the keys: option numbers, o/s shortcuts, or free-text entry in
	// other mode. Submitting the last step sends the reply back over the port,
	// which unblocks the ask_user tool and closes the panel.
	if m.running && m.ask.active() {
		consumed, done := m.ask.handleKey(msg)
		if consumed {
			if done != nil {
				if m.askPort != nil {
					m.askPort.replies <- *done
				}
				m.ask = nil
				m.relayout()
			}
			return m, nil
		}
	}

	// While a sub-agent run is streaming, the composer is disabled (no typing until
	// the run ends), so ↑/↓ drive a selection cursor over the live sub-agent status
	// rows and Enter expands the selected row to show its accumulated output inline.
	// Esc is the one-key escape back to the composer: with a row selected it drops
	// the selection AND re-focuses the input box in a single press, so arrowing into
	// the panel is never a trap. With no selection Esc falls through to its
	// two-stage interrupt role below. The Value()=="" guard is a safety net for the
	// rare case where text reached the buffer (e.g. a paste): then arrows edit the
	// buffer rather than the panel.
	if m.running && m.subagents.active() > 0 && m.input.Value() == "" {
		switch msg.String() {
		case "up":
			m.subagents.selectUp()
			m.relayout()
			return m, nil
		case "down":
			m.subagents.selectDown()
			m.relayout()
			return m, nil
		case "enter":
			m.subagents.toggleExpand()
			m.relayout()
			return m, nil
		case "esc":
			if m.subagents.hasSelection() {
				m.subagents.clearSelection()
				focus := m.input.Focus()
				m.relayout()
				return m, focus
			}
		}
	}

	// Queue pane keys (T8.3): with an empty composer and rows visible, ↑/↓ arm
	// and move the row selection and Del removes the selected row — both while
	// a run streams and while a held queue waits above the input. The
	// sub-agent panel keeps priority when both are live (its branch above
	// already returned); Esc here only disarms the selection — without one it
	// falls through to the interrupt-or-quit handling below.
	if m.input.Value() == "" && len(m.queueRows()) > 0 {
		switch msg.String() {
		case "up":
			m.queueSelectUp()
			m.relayout()
			return m, nil
		case "down":
			m.queueSelectDown()
			m.relayout()
			return m, nil
		case "delete":
			if removed, ok := m.queueDeleteSelected(); ok {
				first, _ := queueRowText(removed)
				m.transcript.addSystem(fmt.Sprintf("(removed from queue: %q — %d waiting)", first, len(m.queueRows())))
				m.relayout()
				return m, nil
			}
		case "esc":
			if m.qpane.selecting {
				m.qpane.selecting = false
				m.relayout()
				return m, nil
			}
		}
	}

	switch msg.String() {
	case "ctrl+c":
		// Ctrl+C copies the current mouse selection when there is one (over OSC52),
		// clearing it afterward; with no selection it keeps its interrupt-or-quit
		// role. Copying works even mid-run, so grabbing streamed output never
		// interrupts the run.
		if !m.sel.empty() {
			text := m.selectedText()
			m.sel = selection{}
			if text != "" {
				return m, tea.SetClipboard(text)
			}
			return m, nil
		}
		return m.interruptOrQuit()
	case "super+c":
		// Cmd+C on macOS is the platform-standard copy: copy the mouse selection
		// when there is one (clearing it), else the whole input buffer. Unlike
		// Ctrl+C it never interrupts/quits — Cmd+C means "copy" on macOS. Most
		// terminals intercept Cmd+C for their own native copy and never deliver it
		// here; this branch serves terminals that forward the Super modifier.
		if !m.sel.empty() {
			text := m.selectedText()
			m.sel = selection{}
			if text != "" {
				return m, tea.SetClipboard(text)
			}
			return m, nil
		}
		if !m.running {
			if v := m.input.Value(); v != "" {
				return m, tea.SetClipboard(v)
			}
		}
		return m, nil
	case "esc":
		return m.interruptOrQuit()
	case "ctrl+o":
		// Cycle the most-recent tool call through its fold states (S5/C1):
		// collapsed diamond row → card with capped response → card with the
		// full response tree → back to the row. Routed through the transcript
		// so the block's display state and the card's cache stay in sync.
		m.transcript.toggleTool()
		return m, nil
	case "ctrl+t":
		// Toggle the most-recent thinking block between its collapsed view (first
		// few lines + hidden-lines hint) and the full reasoning text (T1.3),
		// mirroring Ctrl+O for tool cards.
		m.transcript.toggleThinking()
		return m, nil
	case "ctrl+d":
		// Ctrl+D quits only when idle; mid-run it is ignored so a run is never
		// dropped by a stray EOF key.
		if !m.running {
			m.queued = nil
			m.sendNow = nil
			m.shutdownRemote()
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil
	case "enter":
		// Enter submits the composed buffer (FR-13). Shift+Enter inserts a newline
		// (rebound in newInput) so the editor is a true multi-line composer; when
		// the slash menu is open, Enter runs the highlighted command (handled
		// above), so this branch is only reached with the menu closed.
		if !m.running {
			// Queued rows while idle (held after an interrupt, or left behind
			// by a slash-command drain): a bare Enter is the manual trigger —
			// it promotes the front prompt instead of no-oping on the empty
			// buffer (T8.3).
			if len(m.queueRows()) > 0 && strings.TrimSpace(m.input.Value()) == "" {
				return m.startQueuedFront()
			}
			return m.submit()
		}
		// While a run streams, Enter enqueues the composed buffer (grok-style
		// queue) instead of dropping it; runEndMsg drains the queue.
		if v := strings.TrimSpace(m.input.Value()); v != "" {
			// Expand paste/image placeholders now: the buffer is consumed
			// here (it is cleared below), and the queued prompt must carry
			// the real body — runEndMsg starts it verbatim.
			if prompt := strings.TrimSpace(m.expandImages(m.expandPastes(m.input.Value()))); prompt != "" {
				m.recordHistory(v)
				m.queued = append(m.queued, prompt)
				m.pastes = make(map[int]string)
				m.images = make(map[int]string)
				m.input.Clear()
				m.closeMenus()
				m.transcript.addSystem(fmt.Sprintf("(queued — %d waiting, starts when the run ends)", len(m.queued)))
				m.relayout()
			}
		}
		return m, nil
	case "alt+enter":
		// Send-now (T8.3, grok interjection): idle it submits like Enter;
		// while a run streams the buffer jumps the queue — it starts when the
		// current run ends, ahead of previously queued prompts (which keep
		// their order behind it).
		if !m.running {
			return m.submit()
		}
		if v := strings.TrimSpace(m.input.Value()); v != "" {
			if prompt := strings.TrimSpace(m.expandImages(m.expandPastes(m.input.Value()))); prompt != "" {
				m.recordHistory(v)
				m.sendNow = append(m.sendNow, prompt)
				m.pastes = make(map[int]string)
				m.images = make(map[int]string)
				m.input.Clear()
				m.closeMenus()
				if n := len(m.queued); n > 0 {
					m.transcript.addSystem(fmt.Sprintf("(send-now — starts when the run ends, ahead of %d queued)", n))
				} else {
					m.transcript.addSystem("(send-now — starts when the run ends)")
				}
				m.relayout()
			}
		}
		return m, nil
	case "shift+tab":
		// Approval-mode cycle (T7.6, grok/claude Shift+Tab convention): ask →
		// plan → always-approve → ask, mid-run allowed (grok toggles plan
		// mode while the model is thinking). Panels and the approval panel
		// above consume their own keys first, so this only fires on the
		// free composer.
		m.cycleApprovalMode()
		m.relayout()
		return m, nil
	case "pgup", "pgdown":
		// Page scrolling reaches the transcript viewport whether idle or running,
		// so history stays readable while a run streams. Scrolling shifts the
		// content under a screen-anchored selection, so drop the selection to avoid
		// a stale highlight. Line-oriented keys (up / down / home / end) belong to
		// the multi-line editor and are delegated below.
		m.sel = selection{}
		cmd := m.transcript.update(msg)
		return m, cmd
	case "ctrl+v":
		// Explicit paste key: first try to pull an image off the clipboard (Claude
		// Code-style image paste); the reply arrives as clipboardImageMsg and, when
		// no image is present, falls back to an OSC52 text read (tea.ClipboardMsg).
		// This is intercepted before textarea so its own Ctrl+V binding — which reads
		// via an external process and returns an unexported message the model can't
		// route — is bypassed. The common Cmd+V path does not reach here; it arrives
		// as a bracketed tea.PasteMsg handled in Update. Allowed mid-run: the
		// composer stays live while a run streams.
		return m, readClipboardImage
	case "super+v":
		// Cmd+V on macOS is the platform-standard paste. Most terminals turn it
		// into a bracketed paste (tea.PasteMsg, handled in Update); this branch
		// covers terminals that instead forward the Super modifier as a key. Try an
		// image read first, falling back to an OSC52 text read when none is present.
		return m, readClipboardImage
	case "ctrl+y":
		// Copy: the editor has no text selection, so this copies the whole buffer
		// to the system clipboard over OSC52. A no-op on an empty buffer.
		if v := m.input.Value(); v != "" {
			return m, tea.SetClipboard(v)
		}
		return m, nil
	}

	// Everything else is editing input, allowed while a run streams too: the
	// buffer is only read at submit, so keystrokes cannot corrupt an in-flight
	// prompt, and a live composer keeps the Windows IME composition intact.
	// textarea handles CJK / emoji by rune and Shift+Enter as a newline. After
	// the buffer changes, refresh the autocomplete popup so it opens/filters/
	// closes as the user types a "/name" prefix.
	// ↑/↓ walk the submitted-prompt history only while idle; mid-run they move
	// the caret within the multi-line draft (handled by the textarea below).
	if !m.running {
		switch msg.String() {
		case "up":
			return m.historyPrev(msg)
		case "down":
			return m.historyNext(msg)
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.syncMenus()
	m.relayout()
	return m, cmd
}

// submit starts a run for the current buffer: it appends the user block, clears
// the editor (which stays focused — blur/focus cycles reset the Windows IME),
// flips to running, and — when a run starter is wired —
// returns the first pump Cmd. With no starter (pre-#392) it records the prompt
// and a system note without launching anything, and leaves the editor ready for
// the next line.
func (m Model) submit() (tea.Model, tea.Cmd) {
	raw := strings.TrimSpace(m.input.Value())
	prompt := strings.TrimSpace(m.expandImages(m.expandPastes(m.input.Value())))
	if prompt == "" {
		return m, nil
	}
	// Record the input (as typed) into the browse history, then exit browse mode.
	m.recordHistory(raw)
	// The placeholders have been expanded into the prompt, so the stored paste
	// bodies and image paths are consumed; drop them (the id counters keep climbing).
	m.pastes = make(map[int]string)
	m.images = make(map[int]string)
	// A "/name ..." line is a slash-command invocation, not a prompt: resolve it
	// against the shared registry (same as the REPL) rather than sending it to the
	// agent verbatim.
	if strings.HasPrefix(prompt, "/") {
		return m.runSlash(prompt)
	}
	m.transcript.addUser(prompt)
	m.remoteEcho("\n> " + prompt + "\n")
	m.input.Clear()
	m.closeMenus()
	m.relayout()
	return m.startPrompt(prompt)
}

// syncMenus refreshes all autocomplete popups from the current buffer. The
// stages are mutually exclusive: slashMenu owns the "/name" typing stage,
// modelMenu the argument stages — "/model <arg>" (list, then the chained
// effort sub-list) and "/think <arg>" (effort only). Once a space ends the
// name the command popup closes and the argument dropdown takes over (T7.3).
func (m *Model) syncMenus() {
	buffer := m.input.Value()
	m.menu.refresh(buffer, m.slash)
	if m.menu.active {
		m.modelMenu.close()
		return
	}
	// The effort stage keeps its own popup alive: the text after the chain
	// prefix filters the level list; deleting back past the prefix pops the
	// model list back open (grok chain re-entry) or closes (/think stage).
	if m.modelMenu.active && m.modelMenu.phase == modelPhaseEffort {
		trimmed := strings.TrimLeft(buffer, " \t")
		if rest, ok := strings.CutPrefix(trimmed, m.modelMenu.chainCmd); ok {
			m.modelMenu.refreshEffort(rest, m.modelMenu.chainCmd, m.live)
		} else if partial, ok := modelArgToken(buffer); ok {
			m.modelMenu.refresh(partial, m.live)
		} else if partial, prefix, ok := thinkArgToken(buffer); ok {
			m.modelMenu.refreshEffort(partial, prefix, m.live)
		} else {
			m.modelMenu.close()
		}
		return
	}
	if partial, ok := modelArgToken(buffer); ok {
		m.modelMenu.refresh(partial, m.live)
		return
	}
	if partial, prefix, ok := thinkArgToken(buffer); ok {
		m.modelMenu.refreshEffort(partial, prefix, m.live)
		return
	}
	m.modelMenu.close()
}

// closeMenus shuts both autocomplete popups (command stage and /model
// argument stage) — every input-clearing path goes through it so neither
// overlay can outlive the buffer it was driven by.
func (m *Model) closeMenus() {
	m.menu.close()
	m.modelMenu.close()
}

// completeSlash fills the buffer with the highlighted candidate's "/name " so the
// user can go on to type arguments; the trailing space ends name-completion, so
// the refresh closes the popup. It is the Tab action while the menu is open.
func (m Model) completeSlash() Model {
	if c, ok := m.menu.current(); ok {
		m.input.SetValue("/" + c.Name + " ")
		m.syncMenus()
	}
	return m
}

// submitSlashSelected runs the command the popup highlights (Enter while the
// menu is open). Navigating with the arrows then pressing Enter runs the
// selected command even if the typed prefix is shorter; with no selection it
// falls back to the raw buffer so a fully-typed "/name" still runs.
func (m Model) submitSlashSelected() (tea.Model, tea.Cmd) {
	line := strings.TrimSpace(m.input.Value())
	if c, ok := m.menu.current(); ok {
		line = "/" + c.Name
	}
	m.recordHistory(line)
	return m.runSlash(line)
}

// executor builds the slash Executor (T7.7) against the model's current
// bindings: the surface deps + creds the registry was assembled with (the
// session-less constructor keeps its own; withSession rebinds to the
// session's) and the status/session/memory renderers over the live session.
// The loop-owned projections (/compact, /rebuild) are declared Projection
// faces the TUI keeps off the tea loop (slice 2), so no Compact/Rebuild hook
// is wired — the REPL's blocking hooks cover the registry path there.
func (m Model) executor() *prompts.Executor {
	ex := &prompts.Executor{
		Live:    m.live,
		Creds:   m.slashCreds,
		Surface: m.slashDeps,
	}
	// /dump names its directory after the session in flight; the model is
	// copied per update, so read it from the session bound to this copy.
	ex.DumpSession = func() string {
		if m.session == nil {
			return ""
		}
		return m.session.header.ID
	}
	// /memory renders from live memory state the way the former intercept did:
	// the report degrades to an empty store without a session (the hook is
	// unconditional, so the TUI face never reports the executor unavailable).
	ex.Memory = func() string {
		var buf bytes.Buffer
		var store *memory.Store
		var memoryRoot, sessionID string
		var msgs agentcore.MessageList
		window := 0
		if m.live != nil {
			window = m.live.ContextWindow
		}
		if s := m.session; s != nil {
			store = s.memstore
			memoryRoot = s.memoryRoot
			sessionID = s.header.ID
			msgs = s.agentCtx.Messages
		}
		memstatus.RunMemory(&buf, store, memoryRoot, sessionID, msgs, window)
		return strings.TrimRight(buf.String(), "\n")
	}
	if s := m.session; s != nil {
		ex.Status = func() string {
			var b bytes.Buffer
			status.RunStatus(&b, s)
			return strings.TrimRight(b.String(), "\n")
		}
		ex.Session = func() string {
			var b bytes.Buffer
			s.renderSession(&b)
			return strings.TrimRight(b.String(), "\n")
		}
		// /usage opens the overlay at its plan-quota tab (ProjUsagePanel); the
		// tab body is rendered here from local state plus the last quota
		// lookup. /stats reads the session usage ledger through the shared
		// renderer and folds the returned text into a system block.
		ex.Stats = func(window string) string {
			w, _ := runtime.ParseUsageWindow(window)
			var b bytes.Buffer
			cli.WriteStatsReport(&b, cli.UsageLedger(s.store.Dir()), w, time.Now())
			return strings.TrimRight(b.String(), "\n")
		}
	}
	return ex
}

// runSlash resolves a slash-command line against the shared registry and folds
// its outcome into the transcript. The command's declared Projection face
// drives the dispatch (T7.7 slice 2): each face the TUI projects has exactly
// one projection site in the switch below — there is no per-name intercept
// list. Argument forms and faces the TUI does not project fall through to
// registry resolution: a Parse command runs through the intent Executor, an
// action command's status renders as a system block, a prompt/skill command's
// expanded text starts a run, a hybrid (plugin) command shows its notifications
// then runs its prompt, and a declared command with no executable face on this
// path surfaces the explicit unavailability notice.
func (m Model) runSlash(line string) (tea.Model, tea.Cmd) {
	if name, ok := slashCommandName(line); ok {
		if cmd, found := m.slash.Lookup(name); found {
			if cmd.Projection.REPLOnly() {
				// The loop face lives in the REPL (--no-tui) — the TUI
				// rejects explicitly (T7.7 §6: never a silent no-op).
				m.beginSlashInput(line)
				m.transcript.addSystem(cmd.Projection.UnavailableNotice(name))
				m.relayout()
				return m, nil
			}
			switch cmd.Projection {
			case runtime.ProjQuit:
				m.shutdownRemote()
				m.quitting = true
				return m, tea.Quit
			case runtime.ProjModelMenu, runtime.ProjThinkMenu:
				// The bare form opens the dropdown; the argument forms are
				// the same command's non-interactive projection and resolve
				// through the registry. Typing the trailing space opens the
				// same popup while composing.
				if strings.TrimSpace(line) != "/"+name {
					break
				}
				buffer := "/model "
				if cmd.Projection == runtime.ProjThinkMenu {
					buffer = "/think " // /effect is an alias; one popup grammar
				}
				m.input.SetValue(buffer)
				m.syncMenus()
				m.relayout()
				return m, nil
			case runtime.ProjSkillsPanel:
				if strings.TrimSpace(line) != "/"+name {
					break // the parameter form is the text projection (Parse)
				}
				return m.openSkillsPanel(line)
			case runtime.ProjMCPPanel:
				if strings.TrimSpace(line) != "/"+name {
					break // the parameter form is the text projection (Parse)
				}
				return m.openMCPPanel(line)
			case runtime.ProjLSPPanel:
				if strings.TrimSpace(line) != "/"+name {
					break // the parameter form is the text projection (Parse)
				}
				return m.openLSPPanel(line)
			case runtime.ProjShellPanel:
				if strings.TrimSpace(line) != "/"+name {
					break // the parameter form is the text projection (Parse)
				}
				return m.openShellPanel(line)
			case runtime.ProjModePanel:
				if strings.TrimSpace(line) != "/"+name {
					break // the parameter form is the text projection (Parse)
				}
				return m.openModePanel(line)
			case runtime.ProjSessionsPicker:
				return m.openSessionsPicker(line)
			case runtime.ProjRename:
				return m.renameSession(line)
			case runtime.ProjRebuild:
				if strings.TrimSpace(line) != "/"+name {
					break // the argument form resolves (and is refused) on Parse
				}
				return m.rebuildContext(line)
			case runtime.ProjCompact:
				if strings.TrimSpace(line) != "/"+name {
					break // the argument form resolves (and is refused) on Parse
				}
				return m.compactNow(line)
			case runtime.ProjContextPanel:
				return m.toggleContextPanel(line)
			case runtime.ProjUsagePanel:
				return m.openUsagePanel(line)
			case runtime.ProjRemoteControl:
				return m.runRemoteControl(line)
			case runtime.ProjRewind:
				return m.rewindConversation(line)
			}
		}
	}
	m.transcript.addUser(line)
	m.input.Clear()
	m.closeMenus()
	m.relayout()
	if m.slash == nil {
		m.transcript.addSystem("Slash commands unavailable")
		return m, nil
	}
	outcome, err := m.slash.ResolveOutcome(line)
	if err != nil {
		m.transcript.addSystem(err.Error())
		return m, nil
	}
	if outcome.Kind == runtime.SlashIntent {
		// T7.7: the registry parsed the invocation into a typed intent; the
		// executor runs it against the model's bindings and answers with the
		// same action-outcome shape projected below.
		outcome = m.executor().Execute(outcome.Intent)
	}
	if outcome.Message != "" {
		m.transcript.addSystem(outcome.Message)
	}
	// A live-state command (/model, /think) may have mutated m.live; sync the
	// usage row's model segment so the switch shows immediately.
	if m.live != nil {
		m.statusBar.SetModel(m.live.Model)
	}
	// An action command is complete once its status is shown; a hybrid with no
	// prompt (notifications only) likewise starts no run.
	if outcome.Kind == runtime.SlashAction || outcome.Prompt == "" {
		return m, nil
	}
	return m.startPrompt(outcome.Prompt)
}

// slashCommandName extracts the command name (without the leading "/") from a
// slash-command line: "/name" or "/name args…". ok is false for non-slash
// input. It shares runtime.SplitInvocation with the REPL and the headless
// guard, so all three surfaces agree on what "/name" means.
func slashCommandName(line string) (string, bool) {
	name, _, ok := runtime.SplitInvocation(line)
	return name, ok
}

// beginSlashInput is the shared preamble of every projection that owns the
// line: echo it into the transcript, clear the composer, shut the popups.
func (m *Model) beginSlashInput(line string) {
	m.transcript.addUser(line)
	m.input.Clear()
	m.closeMenus()
}

// openSessionsPicker projects the ProjSessionsPicker face (T7.3 S8, grok
// picker alignment): the overlay owns panel state and the resume/delete
// actions, so the projection opens it from the declaration. Degraded to an
// explicit notice without a session or mid-run.
func (m Model) openSessionsPicker(line string) (tea.Model, tea.Cmd) {
	m.beginSlashInput(line)
	switch {
	case m.session == nil:
		m.transcript.addSystem("(sessions unavailable: no active session)")
	case m.running:
		m.transcript.addSystem("(sessions: a run is in progress — open the picker once it finishes)")
	default:
		entries, note := gatherSessions(m.session.store, m.session.header.ID)
		m.sessionsP = sessionsPanel{
			open:    true,
			entries: entries,
			note:    note,
		}
	}
	m.relayout()
	return m, nil
}

// renameSession projects the ProjRename face (T7.3 S2): it renames the live
// session's display title and persists the header through Store.SetTitle —
// header mutation the intent executor cannot reach. The terminal title follows
// on the next frame: View composes tea.View.WindowTitle from the session
// header (see termtitle.go).
func (m Model) renameSession(line string) (tea.Model, tea.Cmd) {
	m.beginSlashInput(line)
	if m.session == nil {
		m.transcript.addSystem("(rename unavailable: no active session)")
		m.relayout()
		return m, nil
	}
	m.transcript.addSystem(renameMessage(m.session, strings.TrimPrefix(line, "/rename")))
	m.relayout()
	return m, nil
}

// rebuildContext projects the ProjRebuild face: it reconstructs the shared
// context from a persisted checkpoint (or falls back to compaction) and
// replaces the message list in place — loop-owned work the intent executor
// cannot do synchronously. It reuses the compacting-indicator: the spinner is
// armed and pinned to "Preparing conversation context…" while the rebuild runs
// off the tea loop, and rebuildDoneMsg clears it and reports the result.
func (m Model) rebuildContext(line string) (tea.Model, tea.Cmd) {
	m.beginSlashInput(line)
	if m.session == nil {
		m.transcript.addSystem("(rebuild unavailable: no active session)")
		m.relayout()
		return m, nil
	}
	m.spinner.begin(time.Now())
	m.spinner.pin("Preparing conversation context")
	m.running = true
	m.relayout()
	return m, tea.Batch(m.session.rebuildCmd(), m.tickSpinner())
}

// compactNow projects the ProjCompact face (T7.7 slice 1's async projection,
// now declaration-driven): the summarization stream runs off the tea loop and
// inserts the marker into the live context — work the intent executor cannot
// do synchronously; compactDoneMsg folds the result into the transcript.
func (m Model) compactNow(line string) (tea.Model, tea.Cmd) {
	m.beginSlashInput(line)
	switch {
	case m.session == nil:
		m.transcript.addSystem("(compact unavailable: no active session)")
	case m.running:
		m.transcript.addSystem("(compact: a run is in progress — compact once it finishes)")
	default:
		m.spinner.begin(time.Now())
		m.spinner.pin("Compacting conversation")
		m.running = true
		m.relayout()
		return m, tea.Batch(m.session.compactCmd(), m.tickSpinner())
	}
	m.relayout()
	return m, nil
}

// toggleContextPanel projects the ProjContextPanel face (grok context panel
// alignment): it toggles the context-usage overlay, which reads the live
// session's telemetry/tool/skill state a text projection cannot carry.
func (m Model) toggleContextPanel(line string) (tea.Model, tea.Cmd) {
	m.beginSlashInput(line)
	m.ctxPanel.toggle()
	m.relayout()
	return m, nil
}

// openUsagePanel projects the ProjUsagePanel face (grok /usage alignment): it
// opens the overlay at the plan-quota tab, showing the last lookup it has and
// refreshing it off the tea loop — the provider probe is a network round trip,
// so it must never block a render (or a keystroke).
func (m Model) openUsagePanel(line string) (tea.Model, tea.Cmd) {
	m.beginSlashInput(line)
	if m.session == nil {
		m.transcript.addSystem("(usage unavailable: no active session)")
		m.relayout()
		return m, nil
	}
	m.ctxPanel.openAt(tabUsage)
	m.relayout()
	if m.usageWaiting {
		return m, nil
	}
	probe := cli.QuotaProbeFor(m.session.live, m.session.creds)
	if !cli.QuotaSupported(probe) {
		return m, nil
	}
	m.usageWaiting = true
	return m, m.quotaProbeCmd(probe)
}

// quotaProbeCmd runs the plan-quota lookup away from the tea loop and reports
// it back as usageQuotaMsg.
func (m Model) quotaProbeCmd(probe cli.QuotaProbe) tea.Cmd {
	return func() tea.Msg {
		snap, err := cli.ProbeQuota(probe)
		return usageQuotaMsg{sec: cli.QuotaSectionFrom(probe, snap, err)}
	}
}

// openSkillsPanel projects the ProjSkillsPanel face (T7.3 interactive
// redesign, grok ExtensionsModal Skills-tab alignment): the panel is modal
// state, so the TUI opens it instead of echoing the arg-action text.
func (m Model) openSkillsPanel(line string) (tea.Model, tea.Cmd) {
	m.beginSlashInput(line)
	switch {
	case m.session == nil:
		m.transcript.addSystem("(skills unavailable: no active session)")
	case m.running:
		m.transcript.addSystem("(skills: a run is in progress — open the panel once it finishes)")
	default:
		rows, note := gatherSkillRows(m.session)
		m.skillsP = listPanel{open: true, title: "技能", hint: "↑↓ 选择 · Enter 启用/禁用 · Esc 关闭", rows: rows, note: note}
	}
	m.relayout()
	return m, nil
}

// openMCPPanel projects the ProjMCPPanel face (T7.3, grok ExtensionsModal
// MCP-tab alignment): the two-level server/tool panel is modal state.
func (m Model) openMCPPanel(line string) (tea.Model, tea.Cmd) {
	m.beginSlashInput(line)
	switch {
	case m.session == nil:
		m.transcript.addSystem("(mcp unavailable: no active session)")
	case m.running:
		m.transcript.addSystem("(mcp: a run is in progress — open the panel once it finishes)")
	default:
		rows, note := gatherMCPRows(m.session)
		m.mcpP = listPanel{open: true, title: "MCP 服务器", hint: "↑↓ 选择 · Enter 展开/收起 · Space 启停 · Esc 关闭", rows: rows, note: note}
	}
	m.relayout()
	return m, nil
}

// openLSPPanel projects the ProjLSPPanel face (T8.2, /mcp panel alignment):
// the two-level server/tool panel is modal state.
func (m Model) openLSPPanel(line string) (tea.Model, tea.Cmd) {
	m.beginSlashInput(line)
	switch {
	case m.session == nil:
		m.transcript.addSystem("(lsp unavailable: no active session)")
	case m.running:
		m.transcript.addSystem("(lsp: a run is in progress — open the panel once it finishes)")
	default:
		rows, note := gatherLSPRows(m.session)
		m.lspP = listPanel{open: true, title: "LSP 服务器", hint: "↑↓ 选择 · Enter 展开/收起 · Space 启停 · Esc 关闭", rows: rows, note: note}
	}
	m.relayout()
	return m, nil
}

// openModePanel projects the ProjModePanel face (T7.6): the three approval
// postures with the live one marked; Space switches (session-scoped, no
// config write — so the panel may open mid-run, matching shift+tab).
func (m Model) openModePanel(line string) (tea.Model, tea.Cmd) {
	m.beginSlashInput(line)
	if m.session == nil {
		m.transcript.addSystem("(mode unavailable: no active session)")
	} else {
		rows, note := gatherModeRows(m.session)
		m.modeP = listPanel{open: true, title: "审批模式", hint: "↑↓ 选择 · Space 切换 · Esc 关闭", rows: rows, note: note}
	}
	m.relayout()
	return m, nil
}

// openShellPanel projects the ProjShellPanel face (T8.4): the flat backend
// list with the live one marked; Space switches (config write + live swap).
func (m Model) openShellPanel(line string) (tea.Model, tea.Cmd) {
	m.beginSlashInput(line)
	switch {
	case m.session == nil:
		m.transcript.addSystem("(shell unavailable: no active session)")
	case m.running:
		m.transcript.addSystem("(shell: a run is in progress — open the panel once it finishes)")
	default:
		rows, note := gatherShellRows(m.session)
		m.shellP = listPanel{open: true, title: "Shell 后端", hint: "↑↓ 选择 · Space 切换 · Esc 关闭", rows: rows, note: note}
	}
	m.relayout()
	return m, nil
}

// rewindConversation projects the ProjRewind face (T3.1 G1/G2/G4): with no
// argument it lists the tree-derived restore points; with "/rewind <n>" it
// moves the active conversation leaf back before the selected turn and refills
// the input with that turn's prompt. The TUI keeps no file-snapshot journal,
// so rewind here is conversation-only.
func (m Model) rewindConversation(line string) (tea.Model, tea.Cmd) {
	m.beginSlashInput(line)
	if m.session == nil {
		m.transcript.addSystem("(rewind unavailable: no active session)")
		m.relayout()
		return m, nil
	}
	points, err := cli.DeriveRewindPoints(m.session.store, m.session.header.ID, m.session.curLeaf, nil)
	if err != nil {
		m.transcript.addSystem(fmt.Sprintf("pigo: cannot read session tree: %v", err))
		m.relayout()
		return m, nil
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		var buf bytes.Buffer
		cli.PrintRewindPoints(&buf, points)
		m.transcript.addSystem(strings.TrimRight(buf.String(), "\n"))
		m.relayout()
		return m, nil
	}
	n, convErr := strconv.Atoi(fields[1])
	if convErr != nil || n < 1 || n > len(points) {
		m.transcript.addSystem(fmt.Sprintf("invalid selection %q — run /rewind to list points (1..%d)", fields[1], len(points)))
		m.relayout()
		return m, nil
	}
	p := points[n-1]
	var msgs agentcore.MessageList
	if p.LeafID != "" {
		loaded, found, loadErr := cli.LoadLeafPath(m.session.store, m.session.header.ID, p.LeafID)
		if loadErr != nil {
			m.transcript.addSystem(fmt.Sprintf("pigo: cannot read session tree: %v", loadErr))
			m.relayout()
			return m, nil
		}
		if !found {
			m.transcript.addSystem("pigo: restore point's conversation node is no longer in the tree; conversation left unchanged")
			m.relayout()
			return m, nil
		}
		msgs = loaded
	}
	m.session.agentCtx.Messages = msgs
	m.session.curLeaf = p.LeafID
	m.session.persisted = len(msgs)
	note := fmt.Sprintf("rewound to before point %d — the prompt is back in the input line", n)
	if p.Lossy {
		note += "\nnote: this point predates a compaction; context was rebuilt from the summary"
	}
	m.transcript.addSystem(note)
	if p.Prompt != "" {
		m.input.SetValue(p.Prompt)
	}
	m.relayout()
	return m, nil
}

// recordHistory appends an submitted input to the browse history (skipping a
// consecutive duplicate, like a shell) and resets the browse cursor to the live
// draft, so the next ↑ starts from the most recent entry and any stashed draft is
// dropped. A blank entry is never stored.
func (m *Model) recordHistory(entry string) {
	entry = strings.TrimSpace(entry)
	if entry != "" && (len(m.history) == 0 || m.history[len(m.history)-1] != entry) {
		m.history = append(m.history, entry)
	}
	m.histIdx = len(m.history)
	m.histDraft = ""
}

// historyPrev recalls the previous submitted input into the composer, but only
// when the caret is on the first line — otherwise ↑ moves the caret within a
// multi-line draft. The first recall stashes the live draft so historyNext can
// restore it, and the cursor lands past the newest entry (len(history)) initially.
func (m Model) historyPrev(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if len(m.history) == 0 || m.input.Line() != 0 {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.syncMenus()
		m.relayout()
		return m, cmd
	}
	if m.histIdx == len(m.history) {
		m.histDraft = m.input.Value()
	}
	if m.histIdx > 0 {
		m.histIdx--
	}
	m.input.SetValue(m.history[m.histIdx])
	m.syncMenus()
	m.relayout()
	return m, nil
}

// historyNext walks forward toward more recent inputs — restoring the stashed
// draft once it steps past the newest entry — but only while browsing and with
// the caret on the last line; otherwise ↓ moves the caret within a multi-line
// draft.
func (m Model) historyNext(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.histIdx >= len(m.history) || m.input.Line() != m.input.LineCount()-1 {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.syncMenus()
		m.relayout()
		return m, cmd
	}
	m.histIdx++
	if m.histIdx == len(m.history) {
		m.input.SetValue(m.histDraft)
	} else {
		m.input.SetValue(m.history[m.histIdx])
	}
	m.syncMenus()
	m.relayout()
	return m, nil
}

// startPrompt launches an agent run for prompt and flips to running when a run
// starter is wired. The composer is NOT blurred: it stays focused and editable
// for the whole run so the next prompt can be drafted while output streams, and
// so the blur/focus cycle does not reset the Windows IME to English mid-session.
// With no starter (pre-session model / tests) it records the pre-#392 system
// note and stays idle. It is shared by a plain submit and by a slash prompt/
// skill command.
func (m Model) startPrompt(prompt string) (tea.Model, tea.Cmd) {
	if m.startRunFn == nil {
		m.transcript.addSystem("(run not wired up: see session assembly in #392)")
		return m, nil
	}
	ch, cmd := m.startRunFn(prompt)
	m.runCh = ch
	m.running = true
	// A run started — the queue pump is live again (T8.3: the held state ends
	// at the next manual action; the drain's own starts are always unheld).
	m.queueHeld = false
	now := time.Now()
	m.spinner.begin(now)
	m.statusBar.usage.beginRun(now)
	m.turnStart = now
	m.relayout()
	return m, tea.Batch(cmd, m.tickSpinner())
}

// taskDescription pulls the human-readable "description" out of a `task` tool
// call's decoded arguments for the sub-agent panel's row label. It returns ""
// when absent or non-string (the description field is optional in the schema),
// in which case the panel row leads with the activity instead.
func taskDescription(input map[string]any) string {
	if s, ok := input["description"].(string); ok {
		return s
	}
	return ""
}

// tickSpinner schedules the next spinner animation frame. The model re-issues it
// on each spinnerTickMsg while running, so the animation self-sustains until the
// run ends (the tick is simply not re-issued once idle).
func (m Model) tickSpinner() tea.Cmd {
	return tea.Tick(spinnerInterval, func(t time.Time) tea.Msg {
		return spinnerTickMsg(t)
	})
}

// interruptOrQuit is the shared Esc / bare-Ctrl+C action: a two-stage interrupt
// (FR-14) that stops an in-flight run on the first press and stays in the
// program, or quits when idle.
func (m Model) interruptOrQuit() (tea.Model, tea.Cmd) {
	if m.running {
		if m.interruptFn != nil {
			m.interruptFn()
		}
		// T8.3: the interrupt freezes the queue — runEndMsg keeps the queued
		// prompts instead of auto-starting the next one, so the user's
		// interrupt is never steamrolled by the queue.
		m.queueHeld = true
		m.transcript.addSystem("(interrupting the current run…)")
		return m, nil
	}
	// Quitting drops the whole queue (T8.3 ruling), held or not.
	m.queued = nil
	m.sendNow = nil
	m.shutdownRemote()
	m.quitting = true
	return m, tea.Quit
}

// shutdownRemote stops the remote-control server on quit so the listener and
// WebSocket are released cleanly. A no-op when remote control is off or no
// session is bound.
func (m Model) shutdownRemote() {
	if m.session != nil {
		m.session.stopRemote()
	}
}

// feedInput forwards a message (a paste payload) to the editor, then refreshes
// the slash menu and re-lays out because inserted text can add lines (growing
// the editor) or begin a "/name". It is the shared tail of the paste handlers.
func (m Model) feedInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.syncMenus()
	m.relayout()
	return m, cmd
}

// pastePlaceholderRe matches the "[Pasted text #N +M lines]" tokens handlePaste
// leaves in the composer, capturing the id so expandPastes can swap the stored
// body back in at submit.
var pastePlaceholderRe = regexp.MustCompile(`\[Pasted text #(\d+) \+\d+ lines\]`)

// handlePaste inserts a pasted payload into the editor. A multi-line paste is
// collapsed to a compact "[Pasted text #N +M lines]" placeholder (the full body
// stashed in m.pastes for expansion at submit), so a large paste does not flood
// the composer — mirroring Claude Code. A single-line paste is inserted verbatim.
func (m Model) handlePaste(content string) (tea.Model, tea.Cmd) {
	if content == "" {
		return m, nil
	}
	if strings.Contains(content, "\n") {
		if m.pastes == nil {
			m.pastes = make(map[int]string)
		}
		m.pasteSeq++
		id := m.pasteSeq
		m.pastes[id] = content
		lines := strings.Count(content, "\n") + 1
		placeholder := fmt.Sprintf("[Pasted text #%d +%d lines]", id, lines)
		return m.feedInput(tea.PasteMsg{Content: placeholder})
	}
	return m.feedInput(tea.PasteMsg{Content: content})
}

// expandPastes replaces every paste placeholder in s with its stored body, so
// the submitted prompt carries the real pasted text rather than the compact
// token the user saw in the composer. An unknown id (e.g. the user edited the
// token) is left as-is. It returns s unchanged when no pastes are stashed.
func (m Model) expandPastes(s string) string {
	if len(m.pastes) == 0 {
		return s
	}
	return pastePlaceholderRe.ReplaceAllStringFunc(s, func(tok string) string {
		sm := pastePlaceholderRe.FindStringSubmatch(tok)
		id, err := strconv.Atoi(sm[1])
		if err != nil {
			return tok
		}
		if body, ok := m.pastes[id]; ok {
			return body
		}
		return tok
	})
}

// handleImagePaste stashes a pasted image (already saved to a temp PNG at path)
// and drops a compact "[Image #N]" placeholder into the composer, mirroring the
// text-paste placeholder. submit expands it into an "@image:<path>" reference so
// BuildUserContent attaches the image as multimodal content. An empty path falls
// back to a plain text read.
func (m Model) handleImagePaste(path string) (tea.Model, tea.Cmd) {
	if path == "" {
		return m, tea.ReadClipboard
	}
	if m.images == nil {
		m.images = make(map[int]string)
	}
	m.imageSeq++
	id := m.imageSeq
	m.images[id] = path
	placeholder := fmt.Sprintf("[Image #%d]", id)
	return m.feedInput(tea.PasteMsg{Content: placeholder})
}

// imagePlaceholderRe matches the "[Image #N]" tokens handleImagePaste leaves in
// the composer, capturing the id so expandImages can swap the stored temp path
// back in as an "@image:<path>" reference at submit.
var imagePlaceholderRe = regexp.MustCompile(`\[Image #(\d+)\]`)

// expandImages replaces every image placeholder in s with an "@image:<path>"
// reference so BuildUserContent reads and attaches the pasted image. An unknown id
// (e.g. the user edited the token) is left as-is. It returns s unchanged when no
// images are stashed.
func (m Model) expandImages(s string) string {
	if len(m.images) == 0 {
		return s
	}
	return imagePlaceholderRe.ReplaceAllStringFunc(s, func(tok string) string {
		sm := imagePlaceholderRe.FindStringSubmatch(tok)
		id, err := strconv.Atoi(sm[1])
		if err != nil {
			return tok
		}
		if p, ok := m.images[id]; ok {
			return "@image:" + p
		}
		return tok
	})
}

// pumpNext re-issues waitForEvent for the in-flight run so the next bridged msg
// is pulled. It returns nil once the run has ended (runCh cleared), stopping the
// pump.
func (m Model) pumpNext() tea.Cmd {
	if m.running && m.runCh != nil {
		return waitForEvent(m.runCh)
	}
	return nil
}

// View implements tea.Model. It renders the shell on the alt-screen: the
// scrolling transcript filling the top rows, then the autocomplete popup (when
// open) and the multi-line input editor, and finally the persistent status bar
// (#386) on the very bottom row — below the input, per the layout fix. Setting
// AltScreen on the returned View is how Bubble Tea v2 enters/leaves the alternate
// screen buffer, so the user's scrollback is restored on quit.
func (m Model) View() tea.View {
	// First rendered frame closes startup.ui_init (T1.1); the probe is
	// once-guarded, so repeat View calls are free.
	m.uiProbe.FirstFrame()

	if m.quitting {
		return tea.View{AltScreen: true}
	}

	raw, cur := m.renderContent()
	content := m.applySelection(raw)

	// MouseModeCellMotion enables click/release/wheel events. Without it the
	// alt-screen swallows the wheel (no native scrollback), so history could only
	// be reached via PgUp/PgDn; enabling it lets the wheel scroll the transcript
	// and drives both scrollbar drag and mouse text selection. WindowTitle feeds
	// the terminal tab title (T7.3 S2): the renderer writes it on change and
	// clears it on close.
	v := tea.View{Content: content, AltScreen: true, MouseMode: tea.MouseModeCellMotion, WindowTitle: m.terminalWindowTitle()}
	if cur != nil {
		v.Cursor = cur
	}
	return v
}

// renderContent builds the full-screen shell string without any selection
// overlay (tui-render-semantics.md C4 page-region order): header line,
// transcript, running zone (sub-agent / ask panels, spinner line), autocomplete
// overlay, input editor, usage row, keys row. It also returns the composer's
// caret as an absolute-frame cursor for tea.View.Cursor (nil when the editor is
// blurred): driving the terminal's real cursor keeps the Windows IME
// composition anchored to the input — a hidden/virtual cursor knocks the IME
// back to English mid-session. View wraps the content with applySelection for
// display, and selectedText reuses it to extract the copied text from the
// exact rows the user sees.
// handleSessionsKey owns the keyboard while the /sessions picker is open
// (T7.3 S8): up/down move, printable keys type the filter, Enter resumes the
// highlighted session, d arms a delete that y confirms and anything else
// disarms, Esc closes. It reports the updated model and (rarely) a Cmd.
func (m Model) handleSessionsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	p := &m.sessionsP
	switch key {
	case "up":
		p.moveUp()
	case "down":
		p.moveDown()
	case "esc", "q":
		if p.armed != "" {
			p.armed = "" // disarm first: Esc never discards the picker mid-confirm
		} else {
			p.close()
		}
	case "enter":
		if e, ok := p.selectedEntry(); ok && p.armed == "" {
			p.close()
			return m.resumeSession(e.id), nil
		}
	case "d":
		if e, ok := p.selectedEntry(); ok && p.armed == "" {
			if e.current {
				p.note = "(当前会话不能在运行中删除)"
			} else {
				p.armed = e.id
			}
		}
	case "y":
		if p.armed != "" {
			if err := m.session.store.Delete(p.armed); err != nil {
				p.note = fmt.Sprintf("(删除失败: %v)", err)
			} else {
				p.note = "会话已删除"
			}
			p.armed = ""
			entries, note := gatherSessions(m.session.store, m.session.header.ID)
			p.entries = entries
			if note != "" {
				p.note = note
			}
			if p.selected >= len(p.visible()) {
				p.selected = max(len(p.visible())-1, 0)
			}
		}
	default:
		switch {
		case key == "backspace" || key == "delete":
			if p.filter != "" {
				r := []rune(p.filter)
				p.filter = string(r[:len(r)-1])
				p.selected = 0
			}
		case len([]rune(key)) == 1 && key != " " || key == " ":
			p.filter += key
			p.selected = 0
		}
	}
	return m, nil
}

// handleListPanelKey owns the keyboard while a /skills or /mcp panel is open
// (T7.3 interactive redesign): up/down navigate, printable keys filter, the
// highlighted row toggles through the session's surface deps, Esc closes.
// The skills panel toggles on Enter. The MCP panel is two-level (T7.3 实测
// 反馈: server 和 tool 级别都可看可切，grok /mcps 对齐): Enter expands/
// collapses a server row into its tools (a tool row's Enter toggles the
// tool), and Space toggles whichever row is highlighted — server or tool.
func (m Model) handleListPanelKey(kind string, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// bubbletea names the space bar "space" whatever its Text; normalize so
	// the MCP toggle case matches and the skills filter can actually contain
	// a space (the old `key == " "` comparison never fired).
	key := msg.String()
	if key == "space" {
		key = " "
	}
	var p *listPanel
	var apply func(listRow) string
	var regather func() ([]listRow, string)
	var expand func(listRow) // nil on the skills panel (one-level)
	// spaceToggles: panels where Space flips the row (skills' Space types a
	// space into the filter instead — backend names need no space).
	spaceToggles := false
	if kind == "skills" {
		p = &m.skillsP
		apply = func(r listRow) string { return m.session.surface.ToggleSkill(r.title, !r.off) }
		regather = func() ([]listRow, string) { return gatherSkillRows(m.session) }
	} else if kind == "lsp" {
		// The LSP panel (T8.2): Space on the server row flips the project
		// switch (trust-gated write + live apply); Space on a tool row flips
		// its slot in the global [lsp.gopls] tools allow-list (batch 2; the
		// deferred plan bakes the filter at startup, so it lands next session).
		p = &m.lspP
		apply = func(r listRow) string {
			if r.tool != "" {
				return m.session.surface.LSPToolToggle(r.tool, r.off)
			}
			return m.session.surface.LSPServerToggle(!r.off)
		}
		regather = func() ([]listRow, string) { return gatherLSPRows(m.session) }
		expand = func(r listRow) { p.toggleExpand(r) }
		spaceToggles = true
	} else if kind == "shell" {
		// The shell panel (T8.4): Space on a backend row switches the live
		// backend (config write + hot swap); rows are flat, no expansion.
		p = &m.shellP
		apply = func(r listRow) string { return m.session.surface.ShellSwitch(r.title) }
		regather = func() ([]listRow, string) { return gatherShellRows(m.session) }
		spaceToggles = true
	} else if kind == "mode" {
		// The mode panel (T7.6): Space on a posture row switches the
		// session's approval posture (session-scoped, no config write).
		p = &m.modeP
		apply = func(r listRow) string { return m.session.surface.ModeSet(r.title) }
		regather = func() ([]listRow, string) { return gatherModeRows(m.session) }
		spaceToggles = true
	} else {
		p = &m.mcpP
		apply = func(r listRow) string {
			if r.tool != "" {
				return m.session.surface.ToggleMCPTool(r.server, r.tool, !r.off)
			}
			return m.session.surface.ToggleMCPServer(r.title, !r.off)
		}
		regather = func() ([]listRow, string) { return gatherMCPRows(m.session) }
		expand = func(r listRow) { p.toggleExpand(r) }
		spaceToggles = true
	}
	toggleSelected := func() {
		if row, ok := p.selectedRow(); ok {
			p.note = firstLine(apply(row))
			rows, note := regather()
			p.setRows(rows)
			if note != "" {
				p.note = note
			}
			if p.selected >= len(p.visible()) {
				p.selected = max(len(p.visible())-1, 0)
			}
		}
	}
	switch key {
	case "up":
		p.moveUp()
	case "down":
		p.moveDown()
	case "esc", "q":
		p.close()
	case "enter":
		if row, ok := p.selectedRow(); ok && expand != nil && row.server != "" && row.tool == "" {
			expand(row)
		} else {
			toggleSelected()
		}
	case " ":
		if spaceToggles {
			toggleSelected() // Space on MCP/LSP/shell toggles the row
		} else {
			p.filter += key
			p.selected = 0
		}
	default:
		switch {
		case key == "backspace" || key == "delete":
			if p.filter != "" {
				r := []rune(p.filter)
				p.filter = string(r[:len(r)-1])
				p.selected = 0
			}
		case len([]rune(key)) == 1:
			p.filter += key
			p.selected = 0
		}
	}
	m.relayout()
	return m, nil
}

// firstLine takes the first line of a multi-line status message (the panel
// note row is one line tall).
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// resumeSession swaps the live session for a persisted one (T7.3 S8): the
// selected session's entries become the shared context, the run session's
// header/leaf rebind to that file (so the next turn's append continues the
// resumed session's tree), and the transcript is rebuilt around the replayed
// history — the in-TUI equivalent of launching with --resume. The launch-time
// live config (model/provider) is kept: the resumed session's model is shown
// in the transcript seed, not forced onto the wire.
func (m Model) resumeSession(id string) Model {
	if m.session == nil || m.session.store == nil {
		m.transcript.addSystem("(sessions unavailable: no active session)")
		return m
	}
	header, entries, err := m.session.store.LoadEntries(id)
	if err != nil {
		m.transcript.addSystem(fmt.Sprintf("pigo: cannot load session %s: %v", shortID(id), err))
		m.relayout()
		return m
	}
	msgs := make(agentcore.MessageList, len(entries))
	for i, e := range entries {
		msgs[i] = e.Message
	}
	curLeaf := ""
	if len(entries) > 0 {
		curLeaf = entries[len(entries)-1].ID
	}
	s := m.session
	s.header = header
	s.agentCtx.Messages = msgs
	if header.SystemPrompt != "" {
		s.agentCtx.SystemPrompt = header.SystemPrompt
	}
	s.curLeaf = curLeaf
	s.persisted = len(entries)

	// Rebuild the transcript around the resumed history: a fresh transcript
	// drops the old tool cards, fold state and streaming caches wholesale.
	m.transcript = newTranscript(m.theme)
	m.toolCards = make(map[string]*toolCard)
	if m.width > 0 && m.height > 0 {
		m.relayout()
	}
	m.transcript.addBanner(renderBanner(m.theme, m.opts, m.cwd))
	seedTranscript(&m.transcript, msgs)
	if header.Model != "" && header.Model != m.live.Model {
		m.transcript.addSystem(fmt.Sprintf("note: this session last ran on %s; the current model %s stays active", header.Model, m.live.Model))
	}
	m.transcript.addSystem(fmt.Sprintf("resumed session %s (%d messages) — new turns continue this session", shortID(header.ID), len(entries)))
	m.header.setTelemetry(estimateTokens(s.agentCtx.SystemPrompt)+messageTokens(s.agentCtx.Messages), m.live.ContextWindow)
	m.relayout()
	return m
}

func (m Model) renderContent() (string, *tea.Cursor) {
	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height
	if height <= 0 {
		height = 24
	}

	var b strings.Builder
	// Page header (S1): branch + cwd left, context tokens/window right. The
	// right readout yields one column to the transcript scrollbar whenever the
	// transcript overflows, so the two never cross at the right edge.
	rightInset := 0
	if m.transcript.overflowing() {
		rightInset = 1
	}
	b.WriteString(m.header.render(m.theme, width, rightInset))
	b.WriteByte('\n')

	// When an overlay panel is open it replaces the transcript region (it is
	// modal, so nothing underneath needs to stay visible).
	if m.sessionsP.open {
		b.WriteString(m.sessionsP.view(m.theme, width, max(height-7, 1)))
		b.WriteByte('\n')
	} else if m.skillsP.open {
		b.WriteString(m.skillsP.view(m.theme, width, max(height-7, 1)))
		b.WriteByte('\n')
	} else if m.mcpP.open {
		b.WriteString(m.mcpP.view(m.theme, width, max(height-7, 1)))
		b.WriteByte('\n')
	} else if m.lspP.open {
		b.WriteString(m.lspP.view(m.theme, width, max(height-7, 1)))
		b.WriteByte('\n')
	} else if m.shellP.open {
		b.WriteString(m.shellP.view(m.theme, width, max(height-7, 1)))
		b.WriteByte('\n')
	} else if m.ctxPanel.open {
		b.WriteString(m.ctxPanel.render(m.theme, m.contextData(), width, max(height-7, 1)))
		b.WriteByte('\n')
	} else if sized := m.width > 0 && m.height > 0; sized {
		// The viewport pads its content to exactly the rows relayout reserved.
		b.WriteString(m.transcript.view())
		b.WriteByte('\n')
	} else {
		for i := 0; i < transcriptHeight(height); i++ {
			b.WriteByte('\n')
		}
	}

	// The running status line sits just above the input while a run is in
	// flight (relayout reserves the row so the transcript shrinks to fit). The
	// sub-agent status panel, when any `task` sub-agents are live, renders on
	// the rows just ABOVE the running line: one line each, refreshed per tick.
	// The ask_user question panel (T4.2) renders in the same slot while the
	// user is answering a questionnaire.
	if m.running {
		if panel := m.subagents.view(m.theme, width, time.Now()); panel != "" {
			b.WriteString(panel)
			b.WriteByte('\n')
		}
		if panel := m.approval.view(m.theme, width); panel != "" {
			b.WriteString(panel)
			b.WriteByte('\n')
		}
		if panel := m.ask.view(m.theme, width); panel != "" {
			b.WriteString(panel)
			b.WriteByte('\n')
		}
		// The queue pane (T8.3) renders directly above the running line:
		// "#N" rows for the prompts waiting to start (send-now first).
		if rows := m.queueView(m.theme, width); rows != "" {
			b.WriteString(rows)
			b.WriteByte('\n')
		}
		if line := m.spinner.view(width, m.turnElapsed()); line != "" {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	} else if rows := m.queueView(m.theme, width); rows != "" {
		// Held queue (T8.3): after an interrupt the rows stay visible above
		// the input until a manual action resumes or clears them.
		b.WriteString(rows)
		b.WriteByte('\n')
	}
	// The autocomplete popup, when open, renders just above the input line as an
	// overlay (it contributes no rows while idle, so the empty-shell layout is
	// unchanged).
	if menu := m.menu.view(width); menu != "" {
		b.WriteString(menu)
		b.WriteByte('\n')
	}
	if menu := m.modelMenu.view(width); menu != "" {
		b.WriteString(menu)
		b.WriteByte('\n')
	}
	// Input editor with the bottom-border "model · approval" tag (S14)…
	// Mark the byte offset where the box starts: the caret's absolute row is
	// the newline count above it (+1 for the box's top border row).
	mark := b.Len()
	b.WriteString(m.input.View(m.inputLabel(), m.theme))
	b.WriteByte('\n')
	// …then the usage row (S12) and the keys line (S13) pin the bottom.
	b.WriteString(m.statusBar.Render(width, time.Now()))
	b.WriteByte('\n')
	b.WriteString(renderKeysLine(m.theme, width, m.keyBinds()))

	out := b.String()
	var cur *tea.Cursor
	if tc := m.input.Cursor(); tc != nil {
		// X+1 / Y+1: the rounded border draws one column left of and one row
		// above the textarea's own view.
		cur = tea.NewCursor(tc.X+1, tc.Y+strings.Count(out[:mark], "\n")+1)
	}
	return out, cur
}

// usageReport renders the /usage overlay tab's body: this session's
// cumulative accounting plus the provider plan quota from the last lookup
// (nil when none has run). It is built at render time from local state only —
// the network probe runs as a command when the panel opens, never here.
func (m Model) usageReport() string {
	if m.session == nil {
		return ""
	}
	model := ""
	if m.session.live != nil {
		model = m.session.live.Model
	}
	var b bytes.Buffer
	cli.WriteUsageReport(&b, m.session.usage.Stats(), cli.UsageReportOptions{
		SessionID: m.session.header.ID,
		Model:     model,
		Quota:     m.usagePanel,
	})
	return strings.TrimRight(b.String(), "\n")
}

// contextData snapshots the live state the context panel renders (tab data is
// gathered on the tea goroutine at render time — no I/O, no locking needed).
func (m Model) contextData() contextData {
	d := contextData{
		modelName: m.modelLabel(),
		sessionReport: func() string {
			if m.session == nil {
				return ""
			}
			var buf bytes.Buffer
			m.session.renderSession(&buf)
			return strings.TrimRight(buf.String(), "\n")
		}(),
		usageReport:  m.usageReport(),
		usageWaiting: m.usageWaiting,
	}
	if m.session != nil {
		d.sessionID = m.session.header.ID
	}
	// Context budget: telemetry when it has arrived, else a naive estimate from
	// the live messages so the panel is never empty before the first event.
	d.tokens, d.window = m.header.tokens, m.header.window
	if d.tokens == 0 && m.session != nil {
		d.tokens = estimateTokens(m.session.agentCtx.SystemPrompt) +
			messageTokens(m.session.agentCtx.Messages)
	}
	d.sysPromptTokens = estimateTokens(m.sessionSystemPrompt())
	if m.opts.ToolPlan != nil {
		d.toolCount = len(m.opts.ToolPlan.Deferred) + len(m.opts.ToolPlan.Hidden)
	}
	// Declaration-face cost estimate: name + description + schema ≈ tokens/4.
	for _, t := range m.opts.Tools {
		d.toolTokens += estimateTokens(t.Name() + t.Description() + string(t.Schema()))
	}
	if d.toolCount == 0 {
		d.toolCount = len(m.opts.Tools)
	}
	d.skillCount = len(m.opts.Skills)
	for _, s := range m.opts.Skills {
		d.skillTokens += estimateTokens(s.Frontmatter.Name + s.Frontmatter.Description)
	}
	return d
}

// sessionSystemPrompt returns the active system prompt text (live context
// first, launch options as fallback), or "".
func (m Model) sessionSystemPrompt() string {
	if m.session != nil && m.session.agentCtx != nil {
		return m.session.agentCtx.SystemPrompt
	}
	return m.opts.SysPrompt
}

// estimateTokens applies the same ≈4 chars/token heuristic the spinner's
// output readout uses — an estimate, never billing.
func estimateTokens(s string) int { return len([]rune(s)) / 4 }

// messageTokens sums a message list's text content through the same estimate;
// assistant messages with a usage payload use the reported token counts.
func messageTokens(msgs agentcore.MessageList) int {
	n := 0
	for _, msg := range msgs {
		if am, ok := msg.(agentcore.AssistantMessage); ok && am.Usage != nil {
			n += am.Usage.InputTokens + am.Usage.OutputTokens
			continue
		}
		n += estimateTokens(agentcore.ContentToText(contentOf(msg)))
	}
	return n
}

// contentOf extracts a message's content list through the concrete types the
// Message interface discriminates (ContentToText needs the list, not the role).
func contentOf(msg agentcore.Message) agentcore.ContentList {
	switch m := msg.(type) {
	case agentcore.UserMessage:
		return m.Content
	case agentcore.AssistantMessage:
		return m.Content
	case agentcore.ToolResultMessage:
		return m.Content
	}
	return nil
}

// modelLabel renders "model (provider)" the way the usage row and the input
// tag show it.
func (m Model) modelLabel() string {
	name := ""
	if m.live != nil {
		name = m.live.Model
	}
	if name == "" {
		name = m.opts.Model
	}
	if prov := m.providerName(); prov != "" {
		name += " (" + prov + ")"
	}
	return name
}

// providerName resolves the display provider name from the live config,
// falling back to the launch options.
func (m Model) providerName() string {
	if m.live != nil && m.live.ProviderName != "" {
		return m.live.ProviderName
	}
	return m.opts.ProviderName
}

// cycleApprovalMode moves the session's approval posture one step along the
// T7.6 ring and folds the switch feedback line into the transcript (grok's
// mode-switch banner as a pigo system line). A session-less model has no
// posture to cycle.
func (m *Model) cycleApprovalMode() {
	if m.session == nil || m.session.approval == nil {
		return
	}
	next := m.session.approval.Mode().Next()
	m.session.approval.Set(next)
	m.transcript.addSystem(prompts.ApprovalModeNote(next))
}

// approvalLabel is the S14 tag's approval face (T7.6): the live session
// posture — plan / ask / always-approve — with the durable-trust suffix in
// ask mode (a standing grant fast-paths effect calls even while the posture
// asks). Session-less models keep the launch flag's two-state face.
func (m Model) approvalLabel() string {
	if m.session == nil || m.session.approval == nil {
		if m.opts.Approve {
			return "always-approve"
		}
		return "restricted"
	}
	switch mode := m.session.approval.Mode(); mode {
	case toolrules.ModePlan, toolrules.ModeAll:
		return mode.String()
	default:
		if m.session.trust != nil && m.session.trust.IsTrusted(m.session.cwd) {
			return "ask·trusted"
		}
		return "ask"
	}
}

// inputLabel is the tag embedded in the input editor's bottom border (S14):
// the current shell mode as "model · [think X ·] approval face". pigo's
// approval face is the T7.6 posture (plan / ask / always-approve), cycled
// with shift+tab or /mode; --approve seeds always-approve. The thinking
// level joins the mode readout when it is on, so the reasoning posture is
// visible where the model types. Later modes (code mode, prompt-constraint
// styles) append their own segment here rather than growing a second tag
// slot.
func (m Model) inputLabel() string {
	parts := []string{m.modelLabel()}
	if t := m.opts.ThinkingLevel; t != "" && t != agentcore.ThinkingOff {
		parts = append(parts, "think "+string(t))
	}
	parts = append(parts, m.approvalLabel())
	return strings.Join(parts, " · ")
}

// keyBinds selects the keys-line content for the current shell mode (S13).
func (m Model) keyBinds() []keyBind {
	switch {
	case m.running && m.approval.active():
		// Pending approval (T7.6 D-C1): the panel owns the keyboard.
		binds := []keyBind{{"y", "允许"}, {"n/Esc", "拒绝"}, {"a", "会话信任"}}
		if m.approval.req.hint.Pattern != "" {
			binds = append(binds, keyBind{"s", "存规则"})
		}
		return binds
	case m.running && m.subagents.active() > 0 && m.input.Value() == "":
		return []keyBind{
			{"↑/↓", "选择"},
			{"Enter", "展开"},
			{"Esc", "返回"},
			{"Ctrl+C", "停止"},
		}
	case m.qpane.selecting && m.input.Value() == "" && len(m.queueRows()) > 0:
		// Queue row selection armed (T8.3): the pane consumes the arrows.
		stop := "停止"
		if !m.running {
			stop = "退出"
		}
		return []keyBind{
			{"↑/↓", "选择"},
			{"Del", "移除"},
			{"Esc", "返回"},
			{"Ctrl+C", stop},
		}
	case m.running:
		binds := []keyBind{{"Enter", "排队"}, {"Alt+Enter", "插队"}, {"Shift+Tab", "模式"}}
		if len(m.queueRows()) > 0 {
			binds = append(binds, keyBind{"↑/↓", "队列"}, keyBind{"Del", "移除"})
		}
		return append(binds, keyBind{"Ctrl+C", "停止"})
	case len(m.queueRows()) > 0:
		// Held queue (T8.3): a bare Enter runs the next queued prompt.
		return []keyBind{
			{"Enter", "运行下一条"},
			{"↑/↓", "队列"},
			{"Del", "移除"},
			{"Ctrl+C", "退出"},
		}
	default:
		return []keyBind{
			{"Enter", "发送"},
			{"Shift+Tab", "模式"},
			{"Ctrl+O", "工具"},
			{"Ctrl+T", "思考"},
			{"Ctrl+C", "退出"},
		}
	}
}

// turnElapsed is the current turn's wall time for the running line (zero when
// idle or before the first anchor).
func (m Model) turnElapsed() time.Duration {
	if !m.running || m.turnStart.IsZero() {
		return 0
	}
	return time.Since(m.turnStart)
}

// applySelection overlays the mouse selection highlight onto the rendered
// content, inverting the selected cells like a terminal's own selection. Only
// rows the selection intersects are rewritten (as plain text with the span
// inverted); untouched rows keep their original coloring. It is a no-op when the
// selection is empty.
func (m Model) applySelection(content string) string {
	if m.sel.empty() {
		return content
	}
	start, end := m.sel.ordered()
	hi := lipgloss.NewStyle().Reverse(true)
	rows := strings.Split(content, "\n")
	for y := start.y; y <= end.y && y < len(rows); y++ {
		if y < 0 {
			continue
		}
		c0, c1, ok := rowRange(start, end, y)
		if !ok {
			continue
		}
		rows[y], _ = selectRow(rows[y], c0, c1, hi)
	}
	return strings.Join(rows, "\n")
}

// selectedText extracts the plain text under the current selection from the rows
// the user sees, joining rows with newlines and trimming each row's trailing
// padding so copied text has no ragged whitespace tail. It returns "" when the
// selection is empty.
func (m Model) selectedText() string {
	if m.sel.empty() {
		return ""
	}
	start, end := m.sel.ordered()
	content, _ := m.renderContent()
	rows := strings.Split(content, "\n")
	var b strings.Builder
	wrote := false
	for y := start.y; y <= end.y && y < len(rows); y++ {
		if y < 0 {
			continue
		}
		c0, c1, ok := rowRange(start, end, y)
		if !ok {
			continue
		}
		_, text := selectRow(rows[y], c0, c1, lipgloss.Style{})
		if wrote {
			b.WriteByte('\n')
		}
		b.WriteString(strings.TrimRight(text, " "))
		wrote = true
	}
	return b.String()
}

// relayout re-sizes the transcript to the rows left after reserving the chrome
// rows (C4 page regions: header 1 + usage 1 + keys 1), the current input editor
// height, and any open autocomplete popup. It hands the transcript the full
// width; the transcript itself spends one column on the scrollbar only while
// its content overflows (see transcript.reflow), so a short conversation uses
// the whole width and shows no bar, while a scrolling one reserves the gutter —
// and that decision re-runs on every streamed line, not just on resize. It is
// called on every resize and after any edit that changes the input height or
// menu row count.
func (m *Model) relayout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	// Width first: the editor's DynamicHeight re-wrap depends on the width, and
	// the row accounting below must see the settled height. The textarea gets
	// the border's inner width (the rounded box costs one column per side).
	m.input.SetWidth(m.width - 2)
	rows := m.height - 3 - m.input.Height() - m.menu.rows() - m.modelMenu.rows()
	// Queue rows (T8.3) reserve their height in both states: above the running
	// line mid-run, above the input when the queue is held after an interrupt.
	rows -= m.queueLineCount()
	if m.running {
		rows-- // the running status line occupies the row just above the input
		// The sub-agent panel reserves one status row per live sub-agent, plus the
		// wrapped output lines of the expanded row (if any); an empty panel reserves
		// nothing so the single-run layout is unchanged.
		rows -= m.subagents.lineCount(m.width)
		// The approval panel (T7.6 D-C1) and the question panel (T4.2)
		// reserve their rendered rows the same way.
		rows -= m.approval.lineCount()
		rows -= m.ask.lineCount()
	}
	if rows < 0 {
		rows = 0
	}
	m.transcript.setSize(m.width, rows)
}

// onScrollbar reports whether the terminal cell (x, y) is the transcript's
// scrollbar: the rightmost column (relayout reserves m.width-1 for content, so
// the bar sits at column m.width-1) within the transcript's visible rows, which
// start at the top of the screen (row 0). It gates click-to-drag so presses in
// the body or on other chrome are left alone. When the content fits there is no
// bar (relayout reclaims the column), so it always returns false.
func (m Model) onScrollbar(x, y int) bool {
	if m.width <= 0 || !m.transcript.overflowing() {
		return false
	}
	h := m.transcript.viewportHeight()
	return x == m.width-1 && y >= 0 && y < h
}

// confirmClick resolves a release-confirmed click (grok two-phase parity): a
// second confirmed click on the same cell within mouseMultiClickWindow is a
// double click and toggles the fold of the block under the cursor — any row
// inside the block's span counts, X never participates — while the first is a
// bare click that merely leaves the (empty) selection so the highlight clears.
func (m *Model) confirmClick(pt point) {
	now := time.Now()
	if m.lastClick.ok && m.lastClick.pt == pt && now.Sub(m.lastClick.at) < mouseMultiClickWindow {
		m.lastClick.ok = false
		m.sel = selection{}
		if !m.ctxPanel.open && !m.sessionsP.open && m.width > 0 && m.height > 0 {
			if line, ok := m.transcript.lineAt(pt.y); ok {
				m.transcript.toggleInBlock(line)
			}
		}
		return
	}
	m.lastClick = lastMouseClick{ok: true, pt: pt, at: now}
}

// transcriptHeight returns the fallback number of rows for the transcript before
// the first size message arrives: the total minus the status bar and a single
// input row, floored at zero so tiny terminals never produce a negative extent.
func transcriptHeight(total int) int {
	h := total - 2
	if h < 0 {
		h = 0
	}
	return h
}
