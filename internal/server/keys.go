package server

import (
	"fmt"
	"strings"
)

func parseKeySequence(sequence string) ([]byte, error) {
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
		out = append(out, key)
	}
	out = append(out, '\r')
	return out, nil
}

func parseKeyPress(press string) (byte, error) {
	if len(press) == 1 {
		return parsePlainKey(press)
	}
	pieces := strings.Split(press, "-")
	if len(pieces) > 1 {
		key := strings.ToLower(pieces[len(pieces)-1])
		for _, modifier := range pieces[:len(pieces)-1] {
			if strings.ToLower(modifier) != "ctrl" {
				return 0, fmt.Errorf("unsupported key modifier %q in %q", modifier, press)
			}
		}
		return parseCtrlKey(key, press)
	}

	return parsePlainKey(press)
}

func parseCtrlKey(key string, press string) (byte, error) {
	if len(key) != 1 || key[0] < 'a' || key[0] > 'z' {
		return 0, fmt.Errorf("unsupported ctrl key %q in %q", key, press)
	}
	return key[0] & 0x1f, nil
}

func parsePlainKey(press string) (byte, error) {
	switch strings.ToLower(press) {
	case "enter":
		return '\r', nil
	case "esc", "escape":
		return 0x1b, nil
	case "tab":
		return '\t', nil
	case "backspace":
		return 0x7f, nil
	case "space":
		return ' ', nil
	}

	if len(press) == 1 {
		return press[0], nil
	}
	return 0, fmt.Errorf("unsupported key %q", press)
}
