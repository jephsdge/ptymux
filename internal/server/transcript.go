package server

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/hinshun/vt10x"
)

const (
	maxTranscriptLines = 4096
	maxTranscriptBytes = 128 << 10
	maxControlBytes    = 4096
)

type ansiScanState uint8

const (
	ansiScanText ansiScanState = iota
	ansiScanEscape
	ansiScanCSI
	ansiScanOSC
	ansiScanOSCEscape
)

type ansiColor struct {
	kind  uint8
	value uint32
}

type ansiStyle struct {
	bold      bool
	faint     bool
	italic    bool
	underline bool
	blink     bool
	conceal   bool
	strike    bool
	fg        ansiColor
	bg        ansiColor
}

type ansiTranscript struct {
	lines             []string
	totalBytes        int
	pending           bytes.Buffer
	pendingStart      bool
	pendingDrop       bool
	savedPending      []byte
	savedPendingStart bool
	savedPendingDrop  bool
	state             ansiScanState
	control           []byte
	style             ansiStyle
	savedStyle        ansiStyle
	altScreen         bool
}

func (t *ansiTranscript) Write(data []byte) {
	for offset := 0; offset < len(data); {
		if t.state == ansiScanText && data[offset] >= 0x20 {
			end := offset + 1
			for end < len(data) && data[end] >= 0x20 {
				end++
			}
			if !t.altScreen {
				t.appendPending(data[offset:end])
			}
			offset = end
			continue
		}
		b := data[offset]
		offset++
		switch t.state {
		case ansiScanText:
			t.writeTextByte(b)
		case ansiScanEscape:
			t.appendControlByte(b)
			switch b {
			case '[':
				t.state = ansiScanCSI
			case ']':
				t.state = ansiScanOSC
			default:
				t.finishControl()
			}
		case ansiScanCSI:
			t.appendControlByte(b)
			if b >= 0x40 && b <= 0x7e {
				t.handleCSI()
				t.finishControl()
			}
		case ansiScanOSC:
			if b == 0x07 {
				t.finishControl()
				continue
			}
			if b == 0x1b {
				t.state = ansiScanOSCEscape
				continue
			}
			t.appendControlByte(b)
		case ansiScanOSCEscape:
			if b == '\\' {
				t.finishControl()
				continue
			}
			t.appendControlByte(0x1b)
			t.appendControlByte(b)
			t.state = ansiScanOSC
		}
	}
}

func (t *ansiTranscript) writeTextByte(b byte) {
	if b == 0x1b {
		t.state = ansiScanEscape
		t.control = append(t.control[:0], b)
		return
	}
	if t.altScreen {
		return
	}
	switch b {
	case '\n':
		t.commitLine()
	case '\r', '\b', '\t':
		t.appendPending([]byte{b})
	default:
		if b >= 0x20 {
			t.appendPending([]byte{b})
		}
	}
}

func (t *ansiTranscript) appendControlByte(b byte) {
	if len(t.control) < maxControlBytes {
		t.control = append(t.control, b)
	}
}

func (t *ansiTranscript) finishControl() {
	t.control = t.control[:0]
	t.state = ansiScanText
}

func (t *ansiTranscript) handleCSI() {
	if len(t.control) < 3 {
		return
	}
	final := t.control[len(t.control)-1]
	params := string(t.control[2 : len(t.control)-1])

	if (final == 'h' || final == 'l') && strings.HasPrefix(params, "?") && hasAlternateScreenMode(params[1:]) {
		if final == 'h' && !t.altScreen {
			t.savedStyle = t.style
			t.savedPending = append(t.savedPending[:0], t.pending.Bytes()...)
			t.savedPendingStart = t.pendingStart
			t.savedPendingDrop = t.pendingDrop
			t.altScreen = true
			t.resetPending()
		} else if final == 'l' && t.altScreen {
			t.altScreen = false
			t.style = t.savedStyle
			t.resetPending()
			_, _ = t.pending.Write(t.savedPending)
			t.pendingStart = t.savedPendingStart
			t.pendingDrop = t.savedPendingDrop
			t.savedPending = t.savedPending[:0]
			t.savedPendingStart = false
			t.savedPendingDrop = false
		}
		return
	}
	if final == 'J' && !t.altScreen && params == "3" {
		t.lines = nil
		t.totalBytes = 0
		return
	}
	if final != 'm' || t.altScreen {
		return
	}

	t.ensurePendingStart()
	t.appendPending(t.control)
	t.style.applySGR(params)
}

func hasAlternateScreenMode(params string) bool {
	return hasCSIParameter(params, 47) || hasCSIParameter(params, 1047) || hasCSIParameter(params, 1049)
}

func hasCSIParameter(params string, want int) bool {
	for _, raw := range strings.Split(params, ";") {
		value, err := strconv.Atoi(raw)
		if err == nil && value == want {
			return true
		}
	}
	return false
}

func (t *ansiTranscript) ensurePendingStart() {
	if t.pendingStart || t.pendingDrop {
		return
	}
	t.pendingStart = true
	if prefix := t.style.sequence(); prefix != "" {
		t.appendPending([]byte(prefix))
	}
}

func (t *ansiTranscript) appendPending(data []byte) {
	if t.pendingDrop || len(data) == 0 {
		return
	}
	if !t.pendingStart {
		t.ensurePendingStart()
	}
	if t.pending.Len()+len(data) > maxTranscriptBytes {
		t.pending.Reset()
		t.pendingDrop = true
		return
	}
	_, _ = t.pending.Write(data)
}

func (t *ansiTranscript) commitLine() {
	if t.pendingDrop {
		t.resetPending()
		return
	}
	line := strings.TrimSuffix(t.pending.String(), "\r")
	if line != "" && !t.style.isDefault() {
		line += "\x1b[0m"
	}
	t.addLine(line)
	t.resetPending()
}

func (t *ansiTranscript) resetPending() {
	t.pending.Reset()
	t.pendingStart = false
	t.pendingDrop = false
}

func (t *ansiTranscript) addLine(line string) {
	if isInternalMarkerLine(CleanTerminalString(line)) || len(line) > maxTranscriptBytes {
		return
	}
	t.lines = append(t.lines, line)
	t.totalBytes += len(line)
	for len(t.lines) > maxTranscriptLines || t.totalBytes > maxTranscriptBytes {
		t.totalBytes -= len(t.lines[0])
		t.lines[0] = ""
		t.lines = t.lines[1:]
	}
}

func (t *ansiTranscript) RecentLines(count int) string {
	if count <= 0 {
		return ""
	}
	lines := append([]string(nil), t.lines...)
	if !t.pendingDrop && t.pending.Len() > 0 {
		line := t.pending.String()
		if !t.style.isDefault() {
			line += "\x1b[0m"
		}
		if !isInternalMarkerLine(CleanTerminalString(line)) {
			lines = append(lines, line)
		}
	}
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	for transcriptSize(lines) > maxTranscriptBytes && len(lines) > 0 {
		lines = lines[1:]
	}
	return strings.Join(lines, "\n")
}

func transcriptSize(lines []string) int {
	total := 0
	for i, line := range lines {
		total += len(line)
		if i > 0 {
			total++
		}
	}
	return total
}

func (s *ansiStyle) applySGR(raw string) {
	params := parseSGRParameters(raw)
	for i := 0; i < len(params); i++ {
		switch params[i] {
		case 0:
			*s = ansiStyle{}
		case 1:
			s.bold = true
		case 2:
			s.faint = true
		case 3:
			s.italic = true
		case 4:
			s.underline = true
		case 5, 6:
			s.blink = true
		case 8:
			s.conceal = true
		case 9:
			s.strike = true
		case 22:
			s.bold = false
			s.faint = false
		case 23:
			s.italic = false
		case 24:
			s.underline = false
		case 25:
			s.blink = false
		case 28:
			s.conceal = false
		case 29:
			s.strike = false
		case 30, 31, 32, 33, 34, 35, 36, 37:
			s.fg = indexedColor(uint32(params[i] - 30))
		case 38:
			color, consumed := extendedColor(params[i+1:])
			if consumed > 0 {
				s.fg = color
				i += consumed
			}
		case 39:
			s.fg = ansiColor{}
		case 40, 41, 42, 43, 44, 45, 46, 47:
			s.bg = indexedColor(uint32(params[i] - 40))
		case 48:
			color, consumed := extendedColor(params[i+1:])
			if consumed > 0 {
				s.bg = color
				i += consumed
			}
		case 49:
			s.bg = ansiColor{}
		case 90, 91, 92, 93, 94, 95, 96, 97:
			s.fg = indexedColor(uint32(params[i] - 90 + 8))
		case 100, 101, 102, 103, 104, 105, 106, 107:
			s.bg = indexedColor(uint32(params[i] - 100 + 8))
		}
	}
}

func parseSGRParameters(raw string) []int {
	if raw == "" {
		return []int{0}
	}
	parts := strings.Split(raw, ";")
	params := make([]int, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			params = append(params, 0)
			continue
		}
		value, err := strconv.Atoi(part)
		if err != nil {
			return params
		}
		params = append(params, value)
	}
	return params
}

func extendedColor(params []int) (ansiColor, int) {
	if len(params) >= 2 && params[0] == 5 && params[1] >= 0 && params[1] <= 255 {
		return indexedColor(uint32(params[1])), 2
	}
	if len(params) >= 4 && params[0] == 2 {
		for _, value := range params[1:4] {
			if value < 0 || value > 255 {
				return ansiColor{}, 0
			}
		}
		return rgbColor(uint32(params[1]), uint32(params[2]), uint32(params[3])), 4
	}
	return ansiColor{}, 0
}

func indexedColor(value uint32) ansiColor {
	return ansiColor{kind: 1, value: value}
}

func rgbColor(red, green, blue uint32) ansiColor {
	return ansiColor{kind: 2, value: red<<16 | green<<8 | blue}
}

func (s ansiStyle) isDefault() bool {
	return s == ansiStyle{}
}

func (s ansiStyle) sequence() string {
	params := make([]string, 0, 10)
	if s.bold {
		params = append(params, "1")
	}
	if s.faint {
		params = append(params, "2")
	}
	if s.italic {
		params = append(params, "3")
	}
	if s.underline {
		params = append(params, "4")
	}
	if s.blink {
		params = append(params, "5")
	}
	if s.conceal {
		params = append(params, "8")
	}
	if s.strike {
		params = append(params, "9")
	}
	params = appendColor(params, s.fg, true)
	params = appendColor(params, s.bg, false)
	if len(params) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(params, ";") + "m"
}

func appendColor(params []string, color ansiColor, foreground bool) []string {
	if color.kind == 0 {
		return params
	}
	base := "38"
	if !foreground {
		base = "48"
	}
	if color.kind == 1 {
		return append(params, base, "5", strconv.FormatUint(uint64(color.value), 10))
	}
	return append(params,
		base,
		"2",
		strconv.FormatUint(uint64(color.value>>16&0xff), 10),
		strconv.FormatUint(uint64(color.value>>8&0xff), 10),
		strconv.FormatUint(uint64(color.value&0xff), 10),
	)
}

const (
	vtAttrUnderline = 1 << 1
	vtAttrBold      = 1 << 2
	vtAttrItalic    = 1 << 4
	vtAttrBlink     = 1 << 5
)

func renderTerminalScreen(term vt10x.Terminal, count int) string {
	term.Lock()
	defer term.Unlock()

	cols, rows := term.Size()
	lines := make([]string, 0, rows)
	glyphs := make([]vt10x.Glyph, cols)
	plain := make([]rune, cols)
	for y := 0; y < rows; y++ {
		last := -1
		for x := 0; x < cols; x++ {
			glyphs[x] = term.Cell(x, y)
			if glyphMeaningful(glyphs[x]) {
				last = x
			}
		}
		if last < 0 {
			lines = append(lines, "")
			continue
		}
		for x, glyph := range glyphs[:last+1] {
			char := glyph.Char
			if char == 0 {
				char = ' '
			}
			plain[x] = char
		}
		if isInternalMarkerLine(string(plain[:last+1])) {
			continue
		}
		lines = append(lines, renderGlyphs(glyphs[:last+1]))
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if count > 0 && len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return strings.Join(lines, "\n")
}

func renderGlyphs(glyphs []vt10x.Glyph) string {
	var output strings.Builder
	current := ansiStyle{}
	for _, glyph := range glyphs {
		style := glyphStyle(glyph)
		if style != current {
			output.WriteString("\x1b[0m")
			output.WriteString(style.sequence())
			current = style
		}
		char := glyph.Char
		if char == 0 {
			char = ' '
		}
		output.WriteRune(char)
	}
	if !current.isDefault() {
		output.WriteString("\x1b[0m")
	}
	return strings.TrimPrefix(output.String(), "\x1b[0m")
}

func glyphMeaningful(glyph vt10x.Glyph) bool {
	return (glyph.Char != 0 && glyph.Char != ' ') || !glyphStyle(glyph).isDefault()
}

func glyphStyle(glyph vt10x.Glyph) ansiStyle {
	return ansiStyle{
		bold:      glyph.Mode&vtAttrBold != 0,
		italic:    glyph.Mode&vtAttrItalic != 0,
		underline: glyph.Mode&vtAttrUnderline != 0,
		blink:     glyph.Mode&vtAttrBlink != 0,
		fg:        terminalColor(glyph.FG, vt10x.DefaultFG),
		bg:        terminalColor(glyph.BG, vt10x.DefaultBG),
	}
}

func terminalColor(color, defaultColor vt10x.Color) ansiColor {
	if color == defaultColor {
		return ansiColor{}
	}
	value := uint32(color)
	if value <= 255 {
		return indexedColor(value)
	}
	if value < uint32(vt10x.DefaultFG) {
		return rgbColor(value>>16&0xff, value>>8&0xff, value&0xff)
	}
	return ansiColor{}
}
