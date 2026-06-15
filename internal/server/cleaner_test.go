package server

import "testing"

func TestCleanTerminalStringRemovesCSI(t *testing.T) {
	input := "\x1b[01;32mhost $\x1b[00m\x1b[K"

	got := CleanTerminalString(input)
	want := "host $"
	if got != want {
		t.Fatalf("CleanTerminalString = %q, want %q", got, want)
	}
}

func TestCleanTerminalStringRemovesOSCBEL(t *testing.T) {
	input := "\x1b]0;user@host:/path\x07host $"

	got := CleanTerminalString(input)
	want := "host $"
	if got != want {
		t.Fatalf("CleanTerminalString = %q, want %q", got, want)
	}
}

func TestCleanTerminalStringRemovesOSCST(t *testing.T) {
	input := "\x1b]2;user@host:/path\x1b\\host $"

	got := CleanTerminalString(input)
	want := "host $"
	if got != want {
		t.Fatalf("CleanTerminalString = %q, want %q", got, want)
	}
}

func TestTerminalCleanerHandlesSplitOSC(t *testing.T) {
	cleaner := NewTerminalCleaner()

	first := cleaner.WriteString("\x1b]0;user@")
	second := cleaner.WriteString("host:/path\x07host $")

	if first != "" {
		t.Fatalf("first chunk = %q, want empty", first)
	}
	if second != "" {
		t.Fatalf("second chunk = %q, want empty before flush", second)
	}
	if flushed := cleaner.Flush(); flushed != "host $" {
		t.Fatalf("flushed = %q, want host prompt", flushed)
	}
}

func TestCleanTerminalStringAppliesCarriageReturnOverwrite(t *testing.T) {
	input := "abc\rxyz"

	got := CleanTerminalString(input)
	want := "xyz"
	if got != want {
		t.Fatalf("CleanTerminalString = %q, want %q", got, want)
	}
}

func TestCleanTerminalStringAppliesBackspace(t *testing.T) {
	input := "abc\b \b"

	got := CleanTerminalString(input)
	want := "ab"
	if got != want {
		t.Fatalf("CleanTerminalString = %q, want %q", got, want)
	}
}

func TestCleanTerminalStringKeepsTabsAndNewlines(t *testing.T) {
	input := "a\tb\r\nc"

	got := CleanTerminalString(input)
	want := "a\tb\nc"
	if got != want {
		t.Fatalf("CleanTerminalString = %q, want %q", got, want)
	}
}

func TestCleanTerminalStringKeepsCtrlCAsCaretText(t *testing.T) {
	input := "\x03\r\nsh$ "

	got := CleanTerminalString(input)
	want := "^C\nsh$ "
	if got != want {
		t.Fatalf("CleanTerminalString = %q, want %q", got, want)
	}
}

func TestCleanTerminalStringCleansMixedPrompt(t *testing.T) {
	input := "\x1b]0;tianyijie@host:/home/work/appmixer\x07\x1b[01;32m2.opera-ps-appmixer-000-cm.IM.bjhw appmixer $\x1b[00m\x1b[K"

	got := CleanTerminalString(input)
	want := "2.opera-ps-appmixer-000-cm.IM.bjhw appmixer $"
	if got != want {
		t.Fatalf("CleanTerminalString = %q, want %q", got, want)
	}
}
