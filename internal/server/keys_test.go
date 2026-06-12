package server

import (
	"bytes"
	"testing"
)

func TestParseKeySequence(t *testing.T) {
	tests := []struct {
		name     string
		sequence string
		want     []byte
	}{
		{
			name:     "ctrl-c appends enter",
			sequence: "ctrl-c",
			want:     []byte{3, '\r'},
		},
		{
			name:     "ctrl-o then plain key appends enter",
			sequence: "ctrl-o d",
			want:     []byte{15, 'd', '\r'},
		},
		{
			name:     "named keys append enter",
			sequence: "esc tab backspace enter",
			want:     []byte{0x1b, '\t', 0x7f, '\r', '\r'},
		},
		{
			name:     "escape alias",
			sequence: "escape",
			want:     []byte{0x1b, '\r'},
		},
		{
			name:     "space named key",
			sequence: "space",
			want:     []byte{' ', '\r'},
		},
		{
			name:     "plain key preserves byte",
			sequence: "A",
			want:     []byte{'A', '\r'},
		},
		{
			name:     "dash plain key",
			sequence: "-",
			want:     []byte{'-', '\r'},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseKeySequence(tt.sequence)
			if err != nil {
				t.Fatalf("parseKeySequence(%q) returned error: %v", tt.sequence, err)
			}
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("parseKeySequence(%q) = %v, want %v", tt.sequence, got, tt.want)
			}
		})
	}
}

func TestParseKeySequenceRejectsUnsupportedModifier(t *testing.T) {
	_, err := parseKeySequence("alt-x")
	if err == nil {
		t.Fatal("parseKeySequence(\"alt-x\") returned nil error")
	}
}

func TestParseKeySequenceRejectsEmptySequence(t *testing.T) {
	_, err := parseKeySequence("   ")
	if err == nil {
		t.Fatal("parseKeySequence returned nil error, want empty sequence error")
	}
}

func TestParseKeySequenceRejectsUnsupportedKey(t *testing.T) {
	_, err := parseKeySequence("delete")
	if err == nil {
		t.Fatal("parseKeySequence returned nil error, want unsupported key error")
	}
}
