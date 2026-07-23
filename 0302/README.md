# Chapter 0302: Parse Values — Predictive Parsing

## Overview: From Names to Typed Literals
0301 recognized identifiers. SQL also needs **values**: `123`, `-5`, `'hello'`, `"x\"y"`. This chapter routes on the **first byte** under the cursor (LL(1) predictive parsing) and fills a `Cell`.

```text
Cursor byte          Parser branch              Cell result
───────────          ─────────────              ───────────
digit / + / -   →    parseInt              →    TypeI64
' or "          →    parseString           →    TypeStr
letter / _      →    tryName (0301)        →    identifier (not a Cell)
```

---

## The Concept & Theory: Predictive (LL(1)) Parsing

### One Token of Lookahead

A grammar is **LL(1)**-friendly when the next symbol uniquely decides which production to use. For literals:

* If you see a digit or sign → you are parsing an integer.
* If you see a quote → you are parsing a string.

You do not need to guess and heavily backtrack. That keeps the parser simple, fast, and easy to debug — ideal for a teaching SQL subset.

### Values Are Already Storage-Ready

`parseValue` does not produce an abstract “literal AST node” that later needs conversion. It fills a **`Cell`** — the same typed unit Encode already understands. That collapses “parse time” and “storage type system” into one representation and avoids a second conversion pass.

### Escapes Break Zero-Copy (And That Is OK)

A string without escapes can be a slice of `buf`. The moment you see `\'`, the output bytes differ from the source bytes, so you must allocate and rebuild. Engines accept that cost because escaped strings are rarer than bare identifiers and integers. The design principle: **stay zero-copy until correctness forces a copy**.

### Keywords vs Identifiers

`tryKeyword` must ensure a keyword is not a prefix of a longer name (`select` vs `selections`). The separator check is lexical theory meeting SQL reality: reserved words are context-sensitive spellings of identifiers, not separate character classes.

---

## 1. New Lexer Helpers

| Helper | Role |
| :--- | :--- |
| `skipSpaces()` | Advance over whitespace |
| `tryKeyword(kw)` | Match ASCII keyword + separator boundary |
| `isEnd` | Cursor at EOF |
| `parseValue(out *Cell)` | Dispatch to int/string parsers |
| `parseInt` / `parseString` | Fill `out` |

Pointer `out *Cell` avoids returning a heap Cell on every call — the caller owns the slot.

---

## 2. Grammar Supported

```text
value   ::= int_literal | string_literal
int     ::= ['+'|'-'] digit+
string  ::= '"' char* '"' | "'" char* "'"
escape  ::= '\' ( '"' | "'" | '\' )
```

Still **no** full statements — only value extraction.

---

## 3. Integer Path (Skeleton)

```text
Input: " -123 "
         ^

1. skipSpaces → pos on '-'
2. parseValue sees '-' / digit → parseInt
3. Slice digits (zero-copy) → strconv.ParseInt
4. out.Type = TypeI64; out.I64 = -123
```

```text
┌─────────────────────────┐
│ Cell                    │
│ Type: TypeI64           │
│ I64:  -123              │
│ Str:  nil               │
└─────────────────────────┘
```

---

## 4. String Path + Escapes

Unescaped strings can stay zero-copy (`buf[start:end]`). Escapes force allocation into `out.Str`:

```text
Input:  'abc\'\"d'
Wire:   a b c \ ' \ " d

Escape map:
  \' → '
  \" → "
  \\ → \

Result Cell:
┌─────────────────────────┐
│ Type: TypeStr           │
│ Str:  abc'"d            │
└─────────────────────────┘
```

```text
State machine (simplified):
  NORMAL ──\──► ESCAPE ──(quote or \)──► NORMAL (emit unescaped)
     │
     └── closing quote ──► DONE
```

---

## 5. `parseValue` Decision Table

| First byte | Branch |
| :--- | :--- |
| `0`–`9`, `+`, `-` | `parseInt` |
| `'` or `"` | `parseString` |
| else | fail (not a value) |

```text
        parseValue
            │
     ┌──────┼──────────┐
     ▼      ▼          ▼
   int?   string?    error
```

---

## 6. Test-Shaped Mocks

```text
" -123 "     → Cell{TypeI64, I64:-123}

'abc\'\"d'   → Cell{TypeStr, Str: abc'"d}

tryName chain on " a b0 _0_ 123 ":
  "a" ✓  "b0" ✓  "_0_" ✓  "123" ✗ (digit cannot start a name)
```

---

## Crucial Takeaways

* LL(1): one lookahead byte chooses the production.
* Integers stay mostly zero-copy; escaped strings allocate.
* `out *Cell` feeds directly into later AST / `Encode`.
* Next: assemble values into a **SELECT AST** (0303).
