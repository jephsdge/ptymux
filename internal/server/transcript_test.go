package server

import (
	"strconv"
	"strings"
	"testing"

	"github.com/hinshun/vt10x"
)

func TestANSITranscriptCSI3JClearsCommittedHistory(t *testing.T) {
	var transcript ansiTranscript
	transcript.Write([]byte("old-one\r\nold-two\r\ncurrent\x1b[3J"))

	if got, want := transcript.RecentLines(10), "current"; got != want {
		t.Fatalf("RecentLines = %q, want %q", got, want)
	}
}

func TestANSITranscriptCSI3JInAlternateScreenPreservesMainHistory(t *testing.T) {
	var transcript ansiTranscript
	transcript.Write([]byte("main-one\r\nmain-\x1b[?1049halt\r\n\x1b[3J\x1b[?1049ltwo"))

	if got, want := transcript.RecentLines(10), "main-one\nmain-two"; got != want {
		t.Fatalf("RecentLines = %q, want %q", got, want)
	}
}

func TestANSITranscriptAlternateScreenPreservesOversizedPendingLine(t *testing.T) {
	var transcript ansiTranscript
	transcript.Write([]byte(strings.Repeat("x", maxTranscriptBytes+1)))
	transcript.Write([]byte("\x1b[?1049halt\x1b[?1049lignored\r\nok"))

	if got, want := transcript.RecentLines(10), "ok"; got != want {
		t.Fatalf("RecentLines = %q, want %q", got, want)
	}
}

func TestANSITranscriptHandlesSplitControls(t *testing.T) {
	var transcript ansiTranscript
	for _, chunk := range []string{
		"\x1b", "[31", "mred", "\x1b]0;ti", "tle\x1b", "\\",
		"\x1b[0", "m\r", "\nplain",
	} {
		transcript.Write([]byte(chunk))
	}

	if got, want := transcript.RecentLines(2), "\x1b[31mred\x1b[0m\nplain"; got != want {
		t.Fatalf("RecentLines = %q, want %q", got, want)
	}
}

func TestANSITranscriptStyleCarriesIntoSelectedLine(t *testing.T) {
	var transcript ansiTranscript
	transcript.Write([]byte("\x1b[31mone\r\ntwo"))

	if got, want := transcript.RecentLines(1), "\x1b[38;5;1mtwo\x1b[0m"; got != want {
		t.Fatalf("RecentLines = %q, want %q", got, want)
	}
}

func TestANSITranscriptProgressControlsRemainOneLogicalLine(t *testing.T) {
	var transcript ansiTranscript
	transcript.Write([]byte("progress 10%\rprogress 20%\b!\r\nready"))

	if got, want := transcript.RecentLines(1), "ready"; got != want {
		t.Fatalf("RecentLines(1) = %q, want %q", got, want)
	}
	if got, want := transcript.RecentLines(2), "progress 10%\rprogress 20%\b!\nready"; got != want {
		t.Fatalf("RecentLines(2) = %q, want %q", got, want)
	}
}

func TestANSITranscriptEvictsOldestLinesAtLineLimit(t *testing.T) {
	var transcript ansiTranscript
	for i := 0; i <= maxTranscriptLines; i++ {
		transcript.Write([]byte("line-" + strconv.Itoa(i) + "\n"))
	}

	got := strings.Split(transcript.RecentLines(maxTranscriptLines+1), "\n")
	if len(got) != maxTranscriptLines {
		t.Fatalf("line count = %d, want %d", len(got), maxTranscriptLines)
	}
	if got[0] != "line-1" || got[len(got)-1] != "line-4096" {
		t.Fatalf("retained range = %q..%q", got[0], got[len(got)-1])
	}
}

func TestANSITranscriptRecentOutputRespectsByteLimit(t *testing.T) {
	var transcript ansiTranscript
	first := strings.Repeat("a", maxTranscriptBytes/2)
	second := strings.Repeat("b", maxTranscriptBytes/2)
	transcript.Write([]byte(first + "\n" + second + "\n"))

	got := transcript.RecentLines(2)
	if len(got) > maxTranscriptBytes {
		t.Fatalf("RecentLines returned %d bytes, limit %d", len(got), maxTranscriptBytes)
	}
	if got != second {
		t.Fatalf("RecentLines retained a partial or older line: length %d", len(got))
	}
}

func TestANSITranscriptDropsOversizedLineAndRecovers(t *testing.T) {
	var transcript ansiTranscript
	transcript.Write([]byte(strings.Repeat("x", maxTranscriptBytes+1) + "\naccepted"))

	if got, want := transcript.RecentLines(10), "accepted"; got != want {
		t.Fatalf("RecentLines = %q, want %q", got, want)
	}
}

func TestPTYRunnerReadHandlesSplitUTF8(t *testing.T) {
	runner := &PTYRunner{term: vt10x.New(vt10x.WithSize(20, 4))}
	input := []byte("中文🙂")
	for _, b := range input {
		runner.observe([]byte{b})
	}

	for _, count := range []int{0, 1} {
		result, err := runner.Read(count)
		if err != nil {
			t.Fatalf("Read(%d) returned error: %v", count, err)
		}
		if result.Output != string(input) {
			t.Fatalf("Read(%d) = %q, want %q", count, result.Output, input)
		}
	}
}

func TestPTYRunnerReadCurrentScreenUsesCursorState(t *testing.T) {
	runner := &PTYRunner{term: vt10x.New(vt10x.WithSize(10, 3))}
	runner.observe([]byte("abcdef\x1b[3DXYZ"))

	result, err := runner.Read(0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := result.Output, "abcXYZ"; got != want {
		t.Fatalf("Read = %q, want %q", got, want)
	}
}

func TestPTYRunnerReadCurrentScreenPreservesStyledBlankRow(t *testing.T) {
	runner := &PTYRunner{term: vt10x.New(vt10x.WithSize(5, 3))}
	runner.observe([]byte("\x1b[2;1H\x1b[44m     \x1b[0m"))

	result, err := runner.Read(0)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := result.Output, "\n\x1b[48;5;4m     \x1b[0m"; got != want {
		t.Fatalf("Read = %q, want %q", got, want)
	}
}

func TestPTYRunnerReadNUsesAlternateScreenRowsWithoutChangingHistory(t *testing.T) {
	runner := &PTYRunner{term: vt10x.New(vt10x.WithSize(20, 5))}
	runner.observe([]byte("main-one\r\nmain-\x1b[?1049halt-one\r\nalt-two\r\nalt-three"))

	alternate, err := runner.Read(2)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := alternate.Output, "alt-two\nalt-three"; got != want {
		t.Fatalf("alternate Read = %q, want %q", got, want)
	}

	runner.observe([]byte("\x1b[?1049ltwo"))
	history, err := runner.Read(10)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := history.Output, "main-one\nmain-two"; got != want {
		t.Fatalf("history Read = %q, want %q", got, want)
	}
}
