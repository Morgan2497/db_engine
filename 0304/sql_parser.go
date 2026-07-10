package kv

import (
	"errors"
	"strconv"
	"strings"
)

// NamedCell binds a parsed column name directly to its disk-ready primitive.
type NamedCell struct {
	column string
	value Cell
}

// StmtSelect represents a strictly formatted, single-row primary key point query.
type StmtSelect struct {
	table string
	cols []string
	keys []NamedCell
}
// Parser represents our zero-allocation string cursor.
type Parser struct {
	buf string
	pos int
}

type StmtCreateTable struct {
	table string
	cols []Column 
	pkey []string
}

type StmtInsert struct {
	table string 
	value []Cell
}

type StmtUpdate struct {
	table string
	keys []NamedCell
	value []NamedCell
}

type StmtDelete struct {
	table string
	keys []NamedCell
}

// NewParser initializes the lexer at the beginning of the SQL string.
func NewParser(s string) Parser {
	return Parser{buf: s, pos: 0}
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

func (p *Parser) skipSpaces() {
	for p.pos < len(p.buf) && isSpace(p.buf[p.pos]) {
		p.pos++
	}
}

// This is used when the compiler expects an exact, fixed mathemtical or structural symbol like (=), (,), or (*).
func (p *Parser) tryPunctuation(punct string) bool {
	p.skipSpaces()
	if p.pos+len(punct) <= len(p.buf) && p.buf[p.pos:p.pos+len(punct)] == punct {
		p.pos += len(punct)
		return true
	}
	return false
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

func (p *Parser) tryKeyword(kws ...string) bool {
	// Save the original cursor pos.
	// if a multi-word match fails in the way, we rewind to this state.
	savedPos := p.pos

	for _, kw := range kws {
		p.skipSpaces()
		
		// 1. Check bounds and perform case-insensitive match.
		if !(p.pos+len(kw) <= len(p.buf) && strings.EqualFold(p.buf[p.pos:p.pos+len(kw)], kw)) {
			// Rollback on failure.
			p.pos = savedPos
		return false
		}

		// 2. Longest match: check the separator boundary to prevent partial matches.
		if p.pos+len(kw) < len(p.buf) && !isSeparator(p.buf[p.pos+len(kw)]) {
			// Rollback on failure.
			p.pos = savedPos 
			return false
		}
		p.pos += len(kw)
	}
	return true
}

func (p *Parser) parseStmt() (out interface{}, err error) {
	if p.tryKeyword("SELECT") {
		stmt := &StmtSelect{}
		err = p.parseSelect(stmt)
		out = stmt
	} else if p.tryKeyword("CREATE", "TABLE") {
		stmt := &StmtCreateTable{}
		err = p.parseCreateTable(stmt)
		out = stmt
	} else if p.tryKeyword("INSERT", "INTO") {
		stmt := &StmtInsert{}
		err = p.parseInsert(stmt)
		out = stmt
	} else if p.tryKeyword("UPDATE") {
		stmt := &StmtUpdate{}
		err = p.parseUpdate(stmt)
		out = stmt
	} else if p.tryKeyword("DELETE", "FROM") {
		stmt := &StmtDelete{}
		err = p.parseDelete(stmt)
		out = stmt
	} else {
		err = errors.New("unknown statement")
	}

	if err != nil {
		return nil, err
	}
	return out, nil
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

// parseEqual extracts a strict "column_name = literal value" grammatical sequence.
func (p *Parser) parseEqual(out *NamedCell) error {
	var ok bool
	out.column, ok = p.tryName()

	if !ok {
		return errors.New("Expect column")
	}
	if !p.tryPunctuation("=") {
		return errors.New("expect =")
	}
	return p.parseValue(&out.value)
}

// Parse "SELECT" statement
func (p *Parser) parseSelect(out *StmtSelect) error {
	if !p.tryKeyword("SELECT"){
		return errors.New("expected keyword")
	}

	for !p.tryKeyword("FROM") {
		if len(out.cols) > 0 && !p.tryPunctuation(",") {
			return errors.New("expect comma")
		}
		if name, ok := p.tryName(); ok {
			out.cols = append(out.cols, name)
		} else {
			return errors.New("expect column")
		}
	}
	if len(out.cols) == 0 {
		return errors.New("expect column list")
	}

	var ok bool 
	if out.table, ok = p.tryName(); !ok {
		return errors.New("expect table name")
	}
	return p.parseWhere(&out.keys)
}

func (p *Parser) parseWhere(out *[]NamedCell) error {
	if !p.tryKeyword("WHERE") {
		return errors.New("expected keyword")
	}
	
	// If found WHERE clause.
	for !p.tryPunctuation(";") {
		expr := NamedCell{}
		
		if len(*out) > 0 && !p.tryKeyword("AND") {
			return errors.New("expect AND")
		}

		if err := p.parseEqual(&expr); err != nil {
			return err
		}
		*out = append(*out, expr)
	}
	if len(*out) == 0 {
		return errors.New("expect where clause")
	}
	return nil
}
