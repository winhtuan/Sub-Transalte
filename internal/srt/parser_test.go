package srt

import (
	"strings"
	"testing"
)

func parse(t *testing.T, input string) []Subtitle {
	t.Helper()
	subs, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	return subs
}

/* ---------- Normal parsing ---------- */

func TestParseBasic(t *testing.T) {
	input := `1
00:00:01,000 --> 00:00:03,000
Hello world

2
00:00:04,000 --> 00:00:06,000
Goodbye
`
	subs := parse(t, input)
	if len(subs) != 2 {
		t.Fatalf("expected 2 subtitles, got %d", len(subs))
	}
	if subs[0].Lines[0] != "Hello world" {
		t.Errorf("unexpected first line: %q", subs[0].Lines[0])
	}
}

/* ---------- Malformed block recovery ---------- */

func TestMalformedIndexSkipped(t *testing.T) {
	// Block with a non-numeric index should be skipped; subsequent blocks still parsed.
	input := `1
00:00:01,000 --> 00:00:03,000
Good block

not_a_number
00:00:04,000 --> 00:00:06,000
Skipped

3
00:00:07,000 --> 00:00:09,000
Another good block
`
	subs := parse(t, input)
	if len(subs) != 2 {
		t.Errorf("expected 2 subtitles (skipping malformed), got %d", len(subs))
	}
	if subs[0].Lines[0] != "Good block" {
		t.Errorf("first subtitle wrong: %q", subs[0].Lines[0])
	}
	if subs[1].Lines[0] != "Another good block" {
		t.Errorf("third subtitle wrong: %q", subs[1].Lines[0])
	}
}

func TestMalformedTimestampSkipped(t *testing.T) {
	input := `1
00:00:01,000 --> 00:00:03,000
Good block

2
NOT A TIMESTAMP
Skipped block

3
00:00:07,000 --> 00:00:09,000
Good again
`
	subs := parse(t, input)
	if len(subs) != 2 {
		t.Errorf("expected 2 subtitles (skipping malformed timestamp), got %d", len(subs))
	}
	if subs[1].Lines[0] != "Good again" {
		t.Errorf("third subtitle wrong: %q", subs[1].Lines[0])
	}
}

func TestAllBlocksMalformed(t *testing.T) {
	input := `not_an_index
00:00:01,000 --> 00:00:03,000
Text

another_bad
00:00:04,000 --> 00:00:06,000
Text2
`
	subs := parse(t, input)
	if len(subs) != 0 {
		t.Errorf("expected 0 subtitles for all-malformed input, got %d", len(subs))
	}
}

/* ---------- Timestamp range validation ---------- */

func TestTimestampValidRanges(t *testing.T) {
	valid := []string{
		"00:00:01,000 --> 00:00:03,000",
		"99:59:59,999 --> 99:59:59,999",
		"10:30:00,500 --> 10:30:02,000",
	}
	for _, ts := range valid {
		if err := validateTimestamp(ts); err != nil {
			t.Errorf("validateTimestamp(%q) = %v, want nil", ts, err)
		}
	}
}

func TestTimestampInvalidMinutes(t *testing.T) {
	// MM >= 60 is invalid.
	ts := "00:60:00,000 --> 00:61:00,000"
	if err := validateTimestamp(ts); err == nil {
		t.Errorf("expected error for MM=60, got nil")
	}
}

func TestTimestampInvalidSeconds(t *testing.T) {
	ts := "00:00:60,000 --> 00:00:61,000"
	if err := validateTimestamp(ts); err == nil {
		t.Errorf("expected error for SS=60, got nil")
	}
}

func TestTimestampInvalidMilliseconds(t *testing.T) {
	ts := "00:00:01,1000 --> 00:00:02,9999"
	if err := validateTimestamp(ts); err == nil {
		t.Errorf("expected error for ms=1000, got nil")
	}
}

func TestTimestampOutOfRangeBlockSkipped(t *testing.T) {
	// Block with MM=60 should be skipped; subsequent valid block still parsed.
	input := `1
00:60:00,000 --> 00:61:00,000
This block has invalid minutes

2
00:00:01,000 --> 00:00:03,000
Valid block
`
	subs := parse(t, input)
	if len(subs) != 1 {
		t.Errorf("expected 1 subtitle (skipping invalid timestamp), got %d", len(subs))
	}
	if len(subs) > 0 && subs[0].Lines[0] != "Valid block" {
		t.Errorf("unexpected subtitle text: %q", subs[0].Lines[0])
	}
}

/* ---------- Edge cases ---------- */

func TestParseEmptyInput(t *testing.T) {
	subs := parse(t, "")
	if len(subs) != 0 {
		t.Errorf("expected 0 subtitles for empty input, got %d", len(subs))
	}
}

func TestParseNoTrailingNewline(t *testing.T) {
	input := "1\n00:00:01,000 --> 00:00:03,000\nHello"
	subs := parse(t, input)
	if len(subs) != 1 {
		t.Fatalf("expected 1 subtitle, got %d", len(subs))
	}
	if subs[0].Lines[0] != "Hello" {
		t.Errorf("unexpected line: %q", subs[0].Lines[0])
	}
}
