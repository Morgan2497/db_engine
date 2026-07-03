 package kv 
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
	// 1. Get the pos.
	oldPos := p.pos
	
	// 0  1  2  3  4 
	//    u  s  e  r
	// 2. Iterate until there is a space.
	for p.pos < len(p.buf) && isSpace(p.buf[p.pos]) {
		p.pos++
	}
	
	// 3. First character validation.
	if p.pos >= len(buf) || !isNameStart(p.buf[p.pos]) {
		p.pos = oldPos 
		return "", false
	}

	start := p.pos 
	p.pos++ 

	for p.pos < len(p.buf) && isNameContinue(p.buf[p.pos]) {
		p.pos++
	}

	return p.buf[start:p.pos], true
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

