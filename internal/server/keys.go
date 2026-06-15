package server

import (
	"fmt"
	"strings"
)

func parseKeySequence(sequence string) ([]byte, error) {
	return parseKeySequenceWithOptions(sequence, true)
}

func parseKeySequenceNoEnter(sequence string) ([]byte, error) {
	return parseKeySequenceWithOptions(sequence, false)
}

func parseKeySequenceWithOptions(sequence string, appendEnter bool) ([]byte, error) {
	parts := strings.Fields(sequence)
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty key sequence")
	}

	out := make([]byte, 0, len(parts)+1)
	for _, part := range parts {
		key, err := parseKeyPress(part)
		if err != nil {
			return nil, err
		}
		out = append(out, key...)
	}
	if appendEnter {
		out = append(out, '\r')
	}
	return out, nil
}

func parseKeyPress(press string) ([]byte, error) {
	if len(press) == 1 {
		return parsePlainKey(press)
	}
	pieces := strings.Split(press, "-")
	if len(pieces) > 1 {
		key := strings.ToLower(pieces[len(pieces)-1])
		for _, modifier := range pieces[:len(pieces)-1] {
			if strings.ToLower(modifier) != "ctrl" {
				return nil, fmt.Errorf("unsupported key modifier %q in %q", modifier, press)
			}
		}
		return parseCtrlKey(key, press)
	}

	return parsePlainKey(press)
}

func parseCtrlKey(key string, press string) ([]byte, error) {
	if len(key) != 1 || key[0] < 'a' || key[0] > 'z' {
		return nil, fmt.Errorf("unsupported ctrl key %q in %q", key, press)
	}
	return []byte{key[0] & 0x1f}, nil
}

func parsePlainKey(press string) ([]byte, error) {
	switch strings.ToLower(press) {
	case "enter":
		return []byte{'\r'}, nil
	case "esc", "escape":
		return []byte{0x1b}, nil
	case "tab":
		return []byte{'\t'}, nil
	case "backspace":
		return []byte{0x7f}, nil
	case "space":
		return []byte{' '}, nil
	case "up":
		return []byte("\x1b[A"), nil
	case "down":
		return []byte("\x1b[B"), nil
	case "right":
		return []byte("\x1b[C"), nil
	case "left":
		return []byte("\x1b[D"), nil
	case "home":
		return []byte("\x1b[H"), nil
	case "end":
		return []byte("\x1b[F"), nil
	case "delete":
		return []byte("\x1b[3~"), nil
	case "pageup":
		return []byte("\x1b[5~"), nil
	case "pagedown":
		return []byte("\x1b[6~"), nil
	}

	if len(press) == 1 {
		return []byte{press[0]}, nil
	}
	return nil, fmt.Errorf("unsupported key %q", press)
}
