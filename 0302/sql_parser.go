package kv

import (
	"errors"
	"strconv"
	"strings"
)

// Parser represents our zero-allocation string cursor.
type Parser struct {
	buf string
	pos int
}

// NewParser initializes the lexer at the beginning of the SQL string.
func NewParser(s string) Parser {
	return Parser{buf: s, pos: 0}
}

/*
	1. Skip leading spaces.
	2. First char is a letter or _, following chars are letters, digits, or _.
	3. On success, return true and advance pos.
	4. On failure, return false and keep pos.
*/
// Parse table, column names.
func (p *Parser) tryName() (string, bool) {
	//  0   1  2  3  4
	// ' '  u  s  e  r
	p.skipSpaces()
	start, cur := p.pos, p.pos
	// First character validation.
	if !(cur < len(p.buf) && isNameStart(p.buf[cur])) {
		return "", false
	}
	cur++
	for cur < len(p.buf) && isNameContinue(p.buf[cur]) {
		cur++
	}
	p.pos = cur
	return p.buf[start:cur], true
}

func (p *Parser) parseValue(out *Cell) error {
	p.skipSpaces()

	if p.pos >= len(p.buf) {
		return errors.New("expect value")
	}

	ch := p.buf[p.pos]

	if ch == '"' || ch == '\'' {
		return p.parseString(out)
	} else if isDigit(ch) || ch == '-' || ch == '+' {
		return p.parseInt(out)
	} else {
		return errors.New("expect value")
	}
}

/*
The rules:
• Enclosed by single or double quotes.
• Quotes inside the string must be escaped with a slash.
• A slash must also be escaped with a slash.
For example, "is\'nt" or 'is\'nt' both mean isn't.
*/
func (p *Parser) parseString(out *Cell) error {
	// 1. Identify the quote type (' or ") and advance past it.
	quote := p.buf[p.pos]
	cur := p.pos + 1

	// 2. Allocate a new buffer to hold the clean string.
	for cur < len(p.buf) {
		ch := p.buf[cur]

		if ch == '\\' {
			cur++
			// escape boudary check: only \' or \" allowed.
			if cur < len(p.buf) && (p.buf[cur] == '"' || p.buf[cur] == '\'') {
				out.Str = append(out.Str, p.buf[cur])
				cur++
			} else {
				return errors.New("bad escape")
			}
		} else if ch == quote {
			// Found the unescaped closing quote.
			out.Type = TypeStr
			p.pos = cur + 1 // commit the state.
			return nil
		} else {
			// Normal character, just append it.
			out.Str = append(out.Str, p.buf[cur])
			cur++
		}
	}
	return errors.New("string is not terminated")
}

func (p *Parser) parseInt(out *Cell) (err error) {
	start, cur := p.pos, p.pos

	if p.buf[cur] == '-' || p.buf[cur] == '+' {
		cur++
	}

	for cur < len(p.buf) && isDigit(p.buf[cur]) {
		cur++
	}

	// strconv.ParseInt safely catches overflow vulnerabilities (ErrRange)
	if out.I64, err = strconv.ParseInt(p.buf[start:cur], 10, 64); err != nil {
		return err
	}

	out.Type = TypeI64
	p.pos = cur
	return nil
}

func (p *Parser) skipSpaces() {
	for p.pos < len(p.buf) && isSpace(p.buf[p.pos]) {
		p.pos++
	}
}

func (p *Parser) tryKeyword(kw string) bool {
	p.skipSpaces()
	if !(p.pos+len(kw) <= len(p.buf) && strings.EqualFold(p.buf[p.pos:p.pos+len(kw)], kw)) {
		return false
	}
	if p.pos+len(kw) < len(p.buf) && !isSeparator(p.buf[p.pos+len(kw)]) {
		return false
	}
	p.pos += len(kw)
	return true
}

func (p *Parser) isEnd() bool {
	p.skipSpaces()
	return p.pos >= len(p.buf)
}

// isSpace detects standard whitespace characters.
func isSpace(ch byte) bool {
	switch ch {
	case '\t', '\n', '\v', '\f', '\r', ' ':
		return true
	}
	return false
}

// isAlpha uses bitwise arithmetic (ch|32) to check for letters [a-zA-Z] in a single cycle.
func isAlpha(ch byte) bool {
	return 'a' <= (ch|32) && (ch|32) <= 'z'
}

// isDigit checks if the byte is an ASCII number [0-9].
func isDigit(ch byte) bool {
	return '0' <= ch && ch <= '9'
}

// isNameStart enforces that identifiers must start with a letter or underscore.
func isNameStart(ch byte) bool {
	return isAlpha(ch) || ch == '_'
}

// isNameContinue allows letters, digits, and underscores for the rest of the identifier.
func isNameContinue(ch byte) bool {
	return isAlpha(ch) || isDigit(ch) || ch == '_'
}

// isSeparator enforces the "Maximal Munch" boundary (e.g., spaces or punctuation).
func isSeparator(ch byte) bool {
	return ch < 128 && !isNameContinue(ch)
}
