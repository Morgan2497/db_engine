# Chapter 0301: Lexical Analysis (Zero-Copy Tokenization)

## Overview: SQL Is a Tiny Compiler Front-End
A database engine is a specialized compiler. Before any `Get`/`Set`, human SQL text must become structured work. Stage 1 is the **lexer (tokenizer)**: slide a cursor over the string and recognize identifiers.

```text
SQL text                 Lexer                    Later stages
────────                 ─────                    ────────────
"  SELECT id FROM t"  →  tokens / names    →  values → AST → execute
```

This chapter: **identifiers only**. No keywords yet, no literals, no statements.

---

## 1. The Zero-Copy Cursor

```go
type Parser struct {
    buf string
    pos int
}
```

| Field | Role |
| :--- | :--- |
| `buf` | Entire SQL string (immutable view) |
| `pos` | Current index into `buf` |

**Zero-copy idea:** a token is `buf[start:pos]` — a slice into the original string, not a newly allocated copy.

```text
buf:  [ ][ ][S][E][L][E][C][T][ ][i][d]
idx:   0  1  2  3  4  5  6  7  8  9 10
pos starts at 0
```

---

## 2. Character Classes (Skeleton Table)

| Predicate | Matches | Example |
| :--- | :--- | :--- |
| `isSpace` | space, tab, newline | `"  "` |
| `isAlpha` | A–Z / a–z via `ch\|32` | `'S'`, `'s'` |
| `isDigit` | `0`–`9` | `'1'` |
| `isNameStart` | alpha or `_` | `'a'`, `'_'` |
| `isNameContinue` | start or digit | `'b0'` |
| `isSeparator` | space / punctuation / end | ends a maximal name |

### Case folding without allocation

```text
'S' | 32 → 's'     (ASCII trick)
Avoid strings.ToLower() per character (alloc / slow path)
```

---

## 3. Maximal Munch + Separators

Identifier grammar:

```text
identifier ::= [A-Za-z_] [A-Za-z0-9_]*
```

**Maximal munch:** consume as many continue-chars as possible.

```text
Input: "selections"
Token: "selections"   ← NOT "select" then leftover "ions"

Why separators matter:
  "select("  → name "select" then punct "("
  "selectx"  → name "selectx" (keyword match must fail later)
```

| Text | Next char | Valid keyword boundary? |
| :--- | :--- | :---: |
| `select` | space / `,` / `(` / EOF | yes |
| `select` | `i` (as in `selections`) | no |

---

## 4. Step-by-Step Trace: `"  SELECT id"`

```text
Initial:
  buf = "  SELECT id"
  pos = 0
  ┌─┬─┬─┬─┬─┬─┬─┬─┬─┬─┬─┐
  │ │ │S│E│L│E│C│T│ │i│d│
  └─┴─┴─┴─┴─┴─┴─┴─┴─┴─┴─┘
    ^pos
```

| Step | Action | `pos` | Token |
| :--- | :--- | :---: | :--- |
| 1 | Skip spaces (if `skipSpaces` present) | 2 | — |
| 2 | `isNameStart('S')` ✓ | 2 | start=2 |
| 3 | Consume `SELECT` | 8 | — |
| 4 | Hit space → stop | 8 | `"SELECT"` = `buf[2:8]` |
| 5 | Later call gets `"id"` | 11 | `"id"` |

```text
After reading SELECT:
  ┌─┬─┬─┬─┬─┬─┬─┬─┬─┬─┬─┐
  │ │ │S│E│L│E│C│T│ │i│d│
  └─┴─┴─┴─┴─┴─┴─┴─┴─┴─┴─┘
                    ^pos=8
  token view ────────┘
```

---

## 5. Commit / Rollback Pattern (Lookahead)

Parsers often **try** a production, then undo on failure:

```text
saved := p.pos
if !matchSomething() {
    p.pos = saved   // rollback
    return false
}
// else: commit by leaving pos advanced
```

That pattern becomes critical for keywords and multi-word phrases in later chapters (`CREATE TABLE`).

---

## 6. What This Chapter Does Not Parse

```text
✓  identifiers / names
✗  keywords as reserved words
✗  integers / strings
✗  SELECT … AST
```

Foreshadow (0303):

```sql
select a,b from t where c=1;
→ StmtSelect{table:"t", cols:["a","b"], keys:[{c,1}]}
```

---

## Crucial Takeaways

* Lexer = cursor (`buf` + `pos`) emitting slices, not copies.
* Maximal munch + separators keep `select` ≠ `selections`.
* ASCII `| 32` folds case without allocations.
* Next: turn literals into `Cell`s (0302).
