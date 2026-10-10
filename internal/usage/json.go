package usage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// FlexTime unmarshals a timestamp a provider may send either as an ISO-8601
// string or as an epoch number (seconds, millis, micros or nanos). The
// surveyed implementations all had to defend against this exact variance, and
// for one provider the format is not documented at all.
type FlexTime struct {
	Time time.Time
	OK   bool
	// Note records a value that had to be read as UTC because it carried no
	// zone — a provider quirk worth surfacing rather than guessing at.
	Note string
	// Raw keeps the literal for fixtures and debugging.
	Raw string
}

// Value returns the parsed time and whether it was present.
func (f FlexTime) Value() (time.Time, bool) {
	if !f.OK || f.Time.IsZero() {
		return time.Time{}, false
	}
	return f.Time, true
}

// UnmarshalJSON accepts a JSON string, a JSON number, or null.
func (f *FlexTime) UnmarshalJSON(b []byte) error {
	*f = FlexTime{}
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		return f.parse(s)
	}
	var n float64
	if err := json.Unmarshal(trimmed, &n); err != nil {
		return err
	}
	f.Raw = string(trimmed)
	f.Time, f.OK = timeFromEpoch(n)
	return nil
}

func (f *FlexTime) parse(s string) error {
	f.Raw = s
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if t, ok := parseISOTime(s); ok {
		f.Time, f.OK = t, true
		return nil
	}
	if t, ok := parseNaiveISO(s); ok {
		f.Time, f.OK = t, true
		f.Note = "timestamp carried no timezone; read as UTC"
		return nil
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		f.Time, f.OK = timeFromEpoch(n)
		return nil
	}
	return fmt.Errorf("unrecognized timestamp %q", s)
}

// isoZoneAware are the layouts that carry a zone; the rest are read as UTC
// with a Note, because guessing the provider's timezone would be worse than
// saying so.
var (
	isoZoneAware = []string{time.RFC3339Nano, time.RFC3339}
	isoNaive     = []string{
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
)

func parseISOTime(s string) (time.Time, bool) {
	for _, layout := range isoZoneAware {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ParseNaiveISO parses a zone-less timestamp as UTC and reports that it did.
// Sources call it through FlexTime; it exists separately so the assumption is
// testable.
func parseNaiveISO(s string) (time.Time, bool) {
	for _, layout := range isoNaive {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// timeFromEpoch converts an epoch number to a time by magnitude: seconds up to
// 1e11, millis to 1e14, micros to 1e17, nanos beyond.
func timeFromEpoch(v float64) (time.Time, bool) {
	if v <= 0 {
		return time.Time{}, false
	}
	n := int64(v)
	switch {
	case v < 1e11:
		return time.Unix(n, 0).UTC(), true
	case v < 1e14:
		return time.Unix(n/1000, (n%1000)*int64(time.Millisecond)).UTC(), true
	case v < 1e17:
		return time.Unix(n/1_000_000, (n%1_000_000)*int64(time.Microsecond)).UTC(), true
	default:
		return time.Unix(n/1_000_000_000, n%1_000_000_000).UTC(), true
	}
}

// FlexFloat unmarshals a number a provider may send either as a JSON number or
// as a numeric string (a documented defense in the surveyed implementations).
type FlexFloat struct {
	Value float64
	OK    bool
	Raw   string
}

// Float returns the value and whether it was present.
func (f FlexFloat) Float() (float64, bool) {
	if !f.OK {
		return 0, false
	}
	return f.Value, true
}

// UnmarshalJSON accepts a JSON number, a numeric string, or null.
func (f *FlexFloat) UnmarshalJSON(b []byte) error {
	*f = FlexFloat{}
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	raw := strings.Trim(string(trimmed), `"`)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return errors.New("not a number: " + string(trimmed))
	}
	f.Value, f.OK, f.Raw = v, true, raw
	return nil
}
