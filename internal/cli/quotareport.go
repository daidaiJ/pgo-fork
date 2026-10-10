// This file holds the provider plan-quota face of /usage (T6.3): the probe
// that resolves a snapshot for the session's active provider, and the shared
// renderer both front-ends use. The renderer returns plain text, exactly as
// WriteUsageReport / WriteStatsReport do, so the REPL prints it and the TUI
// folds it into a system block.
//
// Spec: wiki/port/provider-usage.md (module shape) and wiki/port/usage-ledger.md
// deviation D-6 (the quota section joins the /usage command that already
// reports session usage).
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/smallnest/pigo/internal/cli/ui"
	"github.com/smallnest/pigo/internal/usage"
)

// quotaBarWidth mirrors grok's /usage allowance bar: 30 cells filled by the
// used share, with the floored percentage beside it.
const quotaBarWidth = 30

// QuotaSection is the plan-quota part of a /usage report: the snapshot, or the
// error that kept it out. Label names the provider for a header the snapshot
// itself cannot label.
type QuotaSection struct {
	Label    string
	Snapshot *usage.Snapshot
	Err      error
}

// WriteQuotaReport renders the provider's plan quota. A provider with no quota
// source prints nothing at all — the module's "unsupported providers stay
// quiet" rule — while a source that exists but could not be read prints a dim
// notice (never a 0%), so an offline lookup cannot masquerade as an empty
// plan. now anchors the reset countdown.
func WriteQuotaReport(out io.Writer, sec QuotaSection, now time.Time) {
	color := ui.Enabled()
	if sec.Err != nil {
		if errors.Is(sec.Err, usage.ErrUnsupported) {
			return
		}
		detail := sec.Err.Error()
		switch {
		case errors.Is(sec.Err, context.DeadlineExceeded):
			// The raw net error ("Get \"https://…\": context deadline
			// exceeded") is noisy and does not say what the bound was.
			detail = fmt.Sprintf("timed out after %s", QuotaTimeout)
		case errors.Is(sec.Err, usage.ErrNoCredential):
			detail = "no API key configured"
		}
		fmt.Fprintf(out, "  %s\n", ui.Colorize(color, ui.Dim,
			fmt.Sprintf("%s · plan quota unavailable (%s)", quotaName(sec), detail)))
		return
	}
	if sec.Snapshot == nil {
		return
	}
	snap := sec.Snapshot
	fmt.Fprintln(out, ui.Colorize(color, ui.Bold, quotaTitle(sec)))
	if snap.Note != "" {
		fmt.Fprintf(out, "  %s\n", ui.Colorize(color, ui.Dim, snap.Note))
	}
	if len(snap.Windows) == 0 {
		fmt.Fprintf(out, "  %s\n", ui.Colorize(color, ui.Dim, "no quota windows reported"))
	}
	for _, w := range snap.Windows {
		title := w.Title()
		if w.Note != "" {
			title += " · " + w.Note
		}
		fmt.Fprintf(out, "  %s\n", title)
		if w.Percent != nil {
			fmt.Fprintf(out, "  %s\n", quotaBar(*w.Percent))
		} else if w.Remaining == nil {
			fmt.Fprintf(out, "  %s\n", ui.Colorize(color, ui.Dim, quotaBarWithUnknown()))
		}
		if w.Remaining != nil {
			fmt.Fprintf(out, "  %s\n", quotaWithUnit(*w.Remaining, w.Unit)+" left")
		} else if w.Total != nil && *w.Total > 0 && w.Used != nil {
			fmt.Fprintf(out, "  %s\n", ui.Colorize(color, ui.Dim,
				quotaWithUnit(*w.Used, w.Unit)+" of "+quotaWithUnit(*w.Total, w.Unit)+" used"))
		}
		if w.ResetsAt != nil {
			fmt.Fprintf(out, "  %s\n", ui.Colorize(color, ui.Dim, "Resets "+quotaReset(*w.ResetsAt, now)))
		}
	}
	for _, warn := range snap.Warnings {
		fmt.Fprintf(out, "  %s\n", ui.Colorize(color, ui.Dim, "note: "+warn))
	}
}

// quotaName is the section's provider identity: the source that resolved the
// snapshot, else the caller's label, else a placeholder.
func quotaName(sec QuotaSection) string {
	name := ""
	if sec.Snapshot != nil {
		name = strings.TrimSpace(sec.Snapshot.Source)
	}
	if name == "" {
		name = strings.TrimSpace(sec.Label)
	}
	if name == "" {
		name = "provider"
	}
	return name
}

// quotaTitle is the resolved section's header: the name, the section role, and
// the plan in parentheses when one is known (grok's "Weekly limit (SuperGrok)"
// shape).
func quotaTitle(sec QuotaSection) string {
	title := quotaName(sec) + " · plan quota"
	if sec.Snapshot != nil && sec.Snapshot.Plan != "" {
		title += " (" + sec.Snapshot.Plan + ")"
	}
	return title
}

// quotaBar renders one allowance bar: filled cells for the used share, empty
// cells for the rest, then the percentage floored to match the provider's own
// truncation (grok's shape and arithmetic).
func quotaBar(percent float64) string {
	pct := usage.ClampPercent(percent)
	filled := int(math.Round(pct / 100 * float64(quotaBarWidth)))
	if filled > quotaBarWidth {
		filled = quotaBarWidth
	}
	return strings.Repeat("\u2588", filled) +
		strings.Repeat("\u2591", quotaBarWidth-filled) +
		fmt.Sprintf("  %d%%", int(math.Floor(pct)))
}

// quotaBarWithUnknown is the neutral state for a window whose percentage could
// not be read: an empty track and "--%", never a confident 0%.
func quotaBarWithUnknown() string {
	return strings.Repeat("\u2591", quotaBarWidth) + "  --%"
}

// quotaWithUnit formats a quota amount with two decimals, prefixed or suffixed
// with its unit ("$52.43", "12.50 requests"). Two decimals is how both the
// surveyed providers and their panels present credits and dollar caps.
func quotaWithUnit(v float64, unit string) string {
	amount := fmt.Sprintf("%.2f", v)
	switch {
	case strings.EqualFold(unit, "USD"):
		return "$" + amount
	case unit != "":
		return amount + " " + unit
	default:
		return amount
	}
}

// quotaReset renders a reset time in local time, with a relative hint when it
// is far enough away to be worth reading.
func quotaReset(reset, now time.Time) string {
	when := reset.Local().Format("01-02 15:04")
	if d := reset.Sub(now); d > time.Minute {
		return when + " (in " + quotaDuration(d) + ")"
	}
	return when
}

// quotaDuration renders a coarse countdown: days and hours, hours and minutes,
// or minutes.
func quotaDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	d -= hours * time.Hour
	minutes := d / time.Minute
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}
