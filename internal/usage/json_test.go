package usage

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFlexTimeAcceptsDocumentedForms(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    time.Time
		wantOK  bool
		wantNot string
	}{
		{
			name:   "iso with zone",
			raw:    `"2026-10-11T00:15:43.000Z"`,
			want:   time.Date(2026, 10, 11, 0, 15, 43, 0, time.UTC),
			wantOK: true,
		},
		{
			name:   "iso offset",
			raw:    `"2026-10-11T08:15:43+08:00"`,
			want:   time.Date(2026, 10, 11, 0, 15, 43, 0, time.UTC),
			wantOK: true,
		},
		{
			name:    "naive iso read as utc with a note",
			raw:     `"2026-10-11T00:15:43"`,
			want:    time.Date(2026, 10, 11, 0, 15, 43, 0, time.UTC),
			wantOK:  true,
			wantNot: "",
		},
		{
			name:   "epoch millis",
			raw:    `1791648943527`,
			want:   time.UnixMilli(1791648943527).UTC(),
			wantOK: true,
		},
		{
			name:   "epoch seconds",
			raw:    `1791648943`,
			want:   time.Unix(1791648943, 0).UTC(),
			wantOK: true,
		},
		{
			name:   "epoch micros",
			raw:    `1791648943527000`,
			want:   time.UnixMilli(1791648943527).UTC(),
			wantOK: true,
		},
		{
			name:   "numeric string",
			raw:    `"1791648943527"`,
			want:   time.UnixMilli(1791648943527).UTC(),
			wantOK: true,
		},
		{name: "null", raw: `null`, wantOK: false},
		{name: "empty string", raw: `""`, wantOK: false},
	}
	for _, tc := range cases {
		var ft FlexTime
		if err := json.Unmarshal([]byte(tc.raw), &ft); err != nil {
			t.Errorf("%s: Unmarshal(%s): %v", tc.name, tc.raw, err)
			continue
		}
		got, ok := ft.Value()
		if ok != tc.wantOK {
			t.Errorf("%s: Value() ok = %v, want %v", tc.name, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if !got.Equal(tc.want) {
			t.Errorf("%s: Value() = %s, want %s", tc.name, got, tc.want)
		}
	}
	// A zone-less value is announced, not silently assumed.
	var ft FlexTime
	if err := json.Unmarshal([]byte(`"2026-10-11T00:15:43"`), &ft); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if ft.Note == "" {
		t.Error("zone-less timestamp did not record a note")
	}
}

func TestFlexTimeRejectsGarbage(t *testing.T) {
	var ft FlexTime
	if err := json.Unmarshal([]byte(`"not a timestamp"`), &ft); err == nil {
		t.Error("Unmarshal of a non-timestamp string returned no error")
	}
}

func TestFlexFloatAcceptsNumberAndNumericString(t *testing.T) {
	for _, raw := range []string{`17.4`, `"17.4"`, `0`, `"0"`} {
		var ff FlexFloat
		if err := json.Unmarshal([]byte(raw), &ff); err != nil {
			t.Fatalf("Unmarshal(%s): %v", raw, err)
		}
		if _, ok := ff.Float(); !ok {
			t.Errorf("Unmarshal(%s) left the value unset", raw)
		}
	}
	for _, raw := range []string{`null`, `""`} {
		var ff FlexFloat
		if err := json.Unmarshal([]byte(raw), &ff); err != nil {
			t.Fatalf("Unmarshal(%s): %v", raw, err)
		}
		if _, ok := ff.Float(); ok {
			t.Errorf("Unmarshal(%s) should be unknown, got %v", raw, ff.Value)
		}
	}
	var ff FlexFloat
	if err := json.Unmarshal([]byte(`"abc"`), &ff); err == nil {
		t.Error("Unmarshal of a non-numeric string returned no error")
	}
}
