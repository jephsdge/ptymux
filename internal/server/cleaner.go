package server

import "strings"

type cleanerState int

const (
	cleanerNormal cleanerState = iota
	cleanerEscape
	cleanerCSI
	cleanerOSC
	cleanerOSCEscape
	cleanerSkipOne
)

type TerminalCleaner struct {
	state     cleanerState
	line      []rune
	col       int
	pendingCR bool
}

func NewTerminalCleaner() *TerminalCleaner {
	return &TerminalCleaner{}
}

func CleanTerminalString(input string) string {
	cleaner := NewTerminalCleaner()
	return strings.TrimRight(cleaner.WriteString(input)+cleaner.Flush(), "\n")
}

func (c *TerminalCleaner) WriteString(input string) string {
	var out strings.Builder
	for _, r := range input {
		switch c.state {
		case cleanerNormal:
			switch r {
			case '\x1b':
				c.applyPendingCR()
				c.state = cleanerEscape
			case '\x03':
				c.applyPendingCR()
				out.WriteString("^C")
			case '\a':
				continue
			case '\n':
				if c.pendingCR {
					c.pendingCR = false
				}
				c.flushLine(&out)
				out.WriteByte('\n')
			case '\r':
				c.pendingCR = true
			case '\b':
				c.applyPendingCR()
				if c.col > 0 {
					c.col--
				}
			default:
				if isAllowedTextRune(r) {
					c.applyPendingCR()
					c.writeRune(r)
				}
			}
		case cleanerEscape:
			switch r {
			case '[':
				c.state = cleanerCSI
			case ']':
				c.state = cleanerOSC
			case '(', ')', '*', '+', '-', '.', '/':
				c.state = cleanerSkipOne
			default:
				c.state = cleanerNormal
			}
		case cleanerCSI:
			if r >= 0x40 && r <= 0x7e {
				c.state = cleanerNormal
			}
		case cleanerOSC:
			switch r {
			case '\a':
				c.state = cleanerNormal
			case '\x1b':
				c.state = cleanerOSCEscape
			}
		case cleanerOSCEscape:
			if r == '\\' {
				c.state = cleanerNormal
			} else {
				c.state = cleanerOSC
			}
		case cleanerSkipOne:
			c.state = cleanerNormal
		}
	}
	return out.String()
}

func (c *TerminalCleaner) Flush() string {
	var out strings.Builder
	c.pendingCR = false
	c.flushLine(&out)
	return out.String()
}

func (c *TerminalCleaner) Pending() bool {
	return len(c.line) > 0 || c.pendingCR
}

func isAllowedTextRune(r rune) bool {
	if r == '\t' {
		return true
	}
	return r >= 0x20 && r != 0x7f
}

func (c *TerminalCleaner) applyPendingCR() {
	if c.pendingCR {
		c.col = 0
		c.pendingCR = false
	}
}

func (c *TerminalCleaner) writeRune(r rune) {
	for len(c.line) < c.col {
		c.line = append(c.line, ' ')
	}
	if c.col < len(c.line) {
		c.line[c.col] = r
	} else {
		c.line = append(c.line, r)
	}
	c.col++
}

func (c *TerminalCleaner) flushLine(out *strings.Builder) {
	if c.col < len(c.line) {
		c.line = c.line[:c.col]
	}
	out.WriteString(string(c.line))
	c.line = c.line[:0]
	c.col = 0
}
