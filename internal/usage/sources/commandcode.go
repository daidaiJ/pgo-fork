package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/usage"
)

// Endpoint and response shape come from grok-build-proxy
// docs-local/usage/quota-endpoints-and-credentials.md §3 "Command Code
// (GOAT)": read-source verified, then verified end to end with a real key
// (2026-09-27), and re-probed with a real key on 2026-10-10 while this source
// was written — the probe is what pinned resetAt to epoch MILLISECONDS and
// showed windowLimits.exceeded arriving as null.
const (
	commandCodeCreditsURL       = "https://api.commandcode.ai/alpha/billing/credits"
	commandCodeSubscriptionsURL = "https://api.commandcode.ai/alpha/billing/subscriptions"
)

func init() { usage.Register(newCommandCode()) }

// commandCode serves the Command Code plans (GOAT et al). The inference API
// key doubles as the quota credential; a config-defined provider section
// resolves to the generic "custom" driver (T8.1), so the endpoint host is what
// identifies this source.
type commandCode struct {
	creditsURL       string
	subscriptionsURL string
}

func newCommandCode() *commandCode {
	return &commandCode{
		creditsURL:       commandCodeCreditsURL,
		subscriptionsURL: commandCodeSubscriptionsURL,
	}
}

func (c *commandCode) Name() string { return "commandcode" }

func (c *commandCode) MatchProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "commandcode", "command-code":
		return true
	default:
		return false
	}
}

func (c *commandCode) MatchBase(baseURL string) bool {
	host, _ := usage.SplitBase(baseURL)
	return host == "api.commandcode.ai"
}

// Fetch issues the two documented requests. The credits call carries every
// window and its failure is the query's failure; subscriptions carries the
// plan id and the scheduled-cancellation fields the credits body lacks
// (documented: "planId 在 subscriptions 里，credits 里没有"), so its failure
// only costs the plan label and rides the bundle as a warning.
func (c *commandCode) Fetch(ctx context.Context, cred usage.Credential) ([]byte, error) {
	credits, err := usage.GetJSON(ctx, c.creditsURL, cred.Key)
	if err != nil {
		return nil, err
	}
	bundle := commandCodeBundle{Credits: credits}
	if subs, serr := usage.GetJSON(ctx, c.subscriptionsURL, cred.Key); serr == nil {
		bundle.Subscriptions = subs
	} else {
		bundle.SubscriptionsError = serr.Error()
	}
	return json.Marshal(bundle)
}

// commandCodeBundle is Fetch's output: the two raw responses plus the
// subscriptions failure text when there was one. Keeping it as the Parse input
// is what lets Parse stay pure and fixture-driven for a multi-request source.
type commandCodeBundle struct {
	Credits            json.RawMessage `json:"credits"`
	Subscriptions      json.RawMessage `json:"subscriptions,omitempty"`
	SubscriptionsError string          `json:"subscriptions_error,omitempty"`
}

// commandCodeCredits is the credits body. The alternate key spellings
// (fiveHour / five_hour / rolling5h, cap / limit, resetAt / resetsAt) are the
// defenses the community implementations carried; accepting them costs a
// field each and removes a whole class of breakage.
type commandCodeCredits struct {
	Credits struct {
		MonthlyCredits usage.FlexFloat `json:"monthlyCredits"`
	} `json:"credits"`
	WindowLimits struct {
		FiveHour    *commandCodeWindow `json:"fiveHour"`
		FiveHourAlt *commandCodeWindow `json:"five_hour"`
		Rolling5h   *commandCodeWindow `json:"rolling5h"`
		Weekly      *commandCodeWindow `json:"weekly"`
	} `json:"windowLimits"`
}

type commandCodeWindow struct {
	Used     usage.FlexFloat `json:"used"`
	Cap      usage.FlexFloat `json:"cap"`
	Limit    usage.FlexFloat `json:"limit"`
	Exceeded *bool           `json:"exceeded"`
	ResetAt  usage.FlexTime  `json:"resetAt"`
	ResetsAt usage.FlexTime  `json:"resetsAt"`
}

func (w *commandCodeWindow) capValue() (float64, bool) {
	if w == nil {
		return 0, false
	}
	if v, ok := w.Cap.Float(); ok {
		return v, true
	}
	return w.Limit.Float()
}

func (w *commandCodeWindow) resetValue() (time.Time, bool) {
	if w == nil {
		return time.Time{}, false
	}
	if t, ok := w.ResetAt.Value(); ok {
		return t, true
	}
	return w.ResetsAt.Value()
}

// present reports whether the window carried any readable field.
func (w *commandCodeWindow) present() bool {
	if w == nil {
		return false
	}
	_, used := w.Used.Float()
	_, cap := w.capValue()
	_, reset := w.resetValue()
	return used || cap || reset || w.Exceeded != nil
}

// commandCodeSubscription is the subscriptions body's data object. The nested
// type is spelled out rather than inlined because the alias decoding above
// reads it through a named field.
type commandCodeSubscription struct {
	Data struct {
		Status            string         `json:"status"`
		PlanID            string         `json:"planId"`
		CurrentPeriodEnd  usage.FlexTime `json:"currentPeriodEnd"`
		CancelAt          usage.FlexTime `json:"cancelAt"`
		CanceledAt        usage.FlexTime `json:"canceledAt"`
		CancelAtPeriodEnd bool           `json:"cancelAtPeriodEnd"`
	} `json:"data"`
}

func (c *commandCode) Parse(body []byte) (*usage.Snapshot, error) {
	var bundle commandCodeBundle
	if err := json.Unmarshal(body, &bundle); err != nil {
		return nil, fmt.Errorf("decode response bundle: %w", err)
	}
	var credits commandCodeCredits
	if err := json.Unmarshal(bundle.Credits, &credits); err != nil {
		return nil, fmt.Errorf("decode credits: %w", err)
	}
	now := nowFunc()
	snap := &usage.Snapshot{}
	if bundle.SubscriptionsError != "" {
		snap.Warnings = append(snap.Warnings, "subscription details unavailable: "+bundle.SubscriptionsError)
	}
	if len(bundle.Subscriptions) > 0 {
		c.applySubscription(snap, bundle.Subscriptions)
	}
	// windowLimits.limited is deliberately not read: the documented reading is
	// that only `exceeded` names a blocked window while `limited` is a flag
	// the service sets even at a few percent of use, so a panel that warned on
	// it would cry wolf on every render.
	c.addWindow(snap, usage.WindowFiveHour, firstWindow(
		credits.WindowLimits.FiveHour, credits.WindowLimits.FiveHourAlt, credits.WindowLimits.Rolling5h), now)
	c.addWindow(snap, usage.WindowWeekly, firstWindow(credits.WindowLimits.Weekly), now)
	if v, ok := credits.Credits.MonthlyCredits.Float(); ok {
		snap.Windows = append(snap.Windows, usage.Window{
			Kind:      usage.WindowBilling,
			Remaining: usage.Ptr(v),
			Unit:      "USD",
		})
	}
	if len(snap.Windows) == 0 {
		snap.Warnings = append(snap.Warnings, "no recognized quota windows in the response")
	}
	return snap, nil
}

// addWindow normalizes one window: used/cap into a used percentage and the
// reset through the rule-3 check. A window with nothing readable is left out
// rather than rendered as an empty row.
func (c *commandCode) addWindow(snap *usage.Snapshot, kind usage.WindowKind, w *commandCodeWindow, now time.Time) {
	if !w.present() {
		return
	}
	// The service denominates these caps in dollars (documented, and visible in
	// the recorded body: cap 14 and 35 for a GOAT plan).
	win := usage.Window{Kind: kind, Unit: "USD"}
	if used, ok := w.Used.Float(); ok {
		if limit, ok := w.capValue(); ok {
			win.Used = usage.Ptr(used)
			win.Total = usage.Ptr(limit)
			win.Percent = usage.UsedPercent(used, limit)
		}
	}
	if t, ok := w.resetValue(); ok {
		reset, warn := usage.CheckReset(t, kind, now)
		win.ResetsAt = reset
		if warn != "" {
			snap.Warnings = append(snap.Warnings, string(kind)+": "+warn)
		}
	}
	if w.Exceeded != nil && *w.Exceeded {
		win.Note = "blocked"
	}
	snap.Windows = append(snap.Windows, win)
}

func (c *commandCode) applySubscription(snap *usage.Snapshot, raw json.RawMessage) {
	var sub commandCodeSubscription
	if err := json.Unmarshal(raw, &sub); err != nil {
		snap.Warnings = append(snap.Warnings, "subscription details could not be decoded")
		return
	}
	snap.Plan = commandCodePlanName(sub.Data.PlanID)
	end, haveEnd := sub.Data.CurrentPeriodEnd.Value()
	if cancelAt, ok := sub.Data.CancelAt.Value(); ok {
		end, haveEnd = cancelAt, true
	}
	canceled := sub.Data.CancelAtPeriodEnd
	if _, ok := sub.Data.CanceledAt.Value(); ok {
		canceled = true
	}
	switch {
	case canceled && haveEnd:
		snap.Note = "renewal canceled; access ends " + end.Format("2006-01-02")
	case canceled:
		snap.Note = "renewal canceled"
	case haveEnd:
		snap.Note = "billing period ends " + end.Format("2006-01-02")
	}
}

// commandCodePlanName maps the documented planId values to display names (the
// plan's monthly allowance in USD: Go 10 / GOAT 70 / Pro 80 / Max10 150 /
// Max20 300). An unknown id is shown verbatim rather than guessed at.
func commandCodePlanName(planID string) string {
	switch strings.ToLower(strings.TrimSpace(planID)) {
	case "individual-goat", "goat":
		return "GOAT"
	case "go":
		return "Go"
	case "pro":
		return "Pro"
	case "max10":
		return "Max 10"
	case "max20":
		return "Max 20"
	default:
		return strings.TrimSpace(planID)
	}
}

// firstWindow returns the first window that carried a readable field, so a
// response using one of the documented alternate keys is read the same way.
func firstWindow(ws ...*commandCodeWindow) *commandCodeWindow {
	for _, w := range ws {
		if w.present() {
			return w
		}
	}
	return nil
}
