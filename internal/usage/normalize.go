package usage

import (
	"math"
	"time"
)

// Ptr returns a pointer to f, for sources building a Window field.
func Ptr(f float64) *float64 { return &f }

// Percent clamps a used percentage to [0,100] and returns it as a Window
// field (rule 2). A NaN input is treated as unknown rather than clamped.
func Percent(p float64) *float64 {
	if math.IsNaN(p) {
		return nil
	}
	return Ptr(ClampPercent(p))
}

// ClampPercent bounds a percentage to [0,100]: a provider reporting 104% or a
// small negative (an overshoot or a rounding artifact) still renders a bar
// that cannot run off its track.
func ClampPercent(p float64) float64 {
	return math.Max(0, math.Min(100, p))
}

// UsedPercent normalizes a used/limit pair into a used percentage. An unknown
// or non-positive limit yields nil — the window stays "unknown", never 0
// (rules 1 and 2).
func UsedPercent(used, limit float64) *float64 {
	if math.IsNaN(used) || math.IsNaN(limit) || limit <= 0 {
		return nil
	}
	return Percent(used / limit * 100)
}

// UsedFromRemainingPercent inverts a response that reports the REMAINING
// share (rule 2: MiniMax-style "current_*_remaining_percent").
func UsedFromRemainingPercent(remaining float64) *float64 {
	if math.IsNaN(remaining) {
		return nil
	}
	return Percent(100 - remaining)
}

// ResetSkew tolerates clock drift between the provider and this machine when
// judging whether a reset time is legitimate (rule 3).
const ResetSkew = time.Minute

// CheckReset validates a window's reset time against the window's own length
// (rule 3). A time beyond the window plus one minute of skew — the signature
// of a field the provider filled with something other than the window's reset
// — or one already in the past is dropped rather than rendered as a confident
// countdown; the percentage still shows. The second return reports whether
// the value is kept, the third carries the warning text (empty when kept or
// when no judgement was possible).
func CheckReset(reset time.Time, kind WindowKind, now time.Time) (*time.Time, string) {
	if reset.IsZero() {
		return nil, ""
	}
	if max := MaxDuration(kind); max > 0 {
		if reset.After(now.Add(max + ResetSkew)) {
			return nil, "reset time is beyond the " + string(kind) + " window; dropped"
		}
	}
	if reset.Before(now.Add(-ResetSkew)) {
		return nil, "reset time is already in the past; dropped"
	}
	return &reset, ""
}
