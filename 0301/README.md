# Chapter 0301: Lexical Analysis (Zero-Copy Tokenization)

## Systems Engineering Overview
A database engine is fundamentally a specialized compiler. Before executing a query, the engine must translate a human-readable SQL string into an Abstract Syntax Tree (AST). This chapter introduces the **Tokenizer** (or Lexer)—the first stage of this pipeline.

In high-throughput database systems, lexical analysis is a notoriously hot path. If the tokenizer allocates memory for every word it reads, the Garbage Collector (GC) will choke the engine's throughput. Therefore, this implementation relies on **Deterministic Finite Automata (DFA) principles** and **Zero-Copy semantics** to parse strings with absolute minimal overhead.

---

## 1. Architecture: The Zero-Allocation Cursor & Pointer Receivers

```go
type Parser struct {
	buf string
	pos int
}

-ex: select a,b from t where c=1;
Will be represented as:
StmtSelect{
  table: "t",
  cols: []string{"a", "b"},
  keys: []NamedCell{{column: "c", value: Cell{Type: TypeI64, I64: 1}}},
}
```

This struct is deceptively simple, but it represents a massive performance decision. In Go, a `string` under the hood is a `StringHeader` containing a data pointer (`uintptr`) and a length (`int`). 

### The Zero-Allocation Philosophy
A database parses millions of queries a second. If your tokenizer uses standard string manipulation like `strings.Split()`, Go has to allocate new backing arrays in memory for every single word in the query. Eventually, memory fills up, and the Garbage Collector has to pause your entire database program to clean up the mess, causing severe latency spikes.

By using a single raw string (`buf`) and an integer (`pos`), we perform lexical analysis strictly in the CPU registers. We just slide a window over existing memory. Creating a token simply means returning two integers (a start index and an end index), slicing the original string `buf[start:pos]`. In Go, slicing an existing string does not allocate new memory; it merely creates a lightweight pointer to the original memory block. The GC is completely bypassed.

### The Anatomy of the Cursor: Pointer Receivers
Implementing this cursor correctly requires a deep understanding of Go's memory model. When you implement the `try...` methods, they must use a pointer receiver, not a value receiver.

```go
// CORRECT: Mutates the underlying state
func (p *Parser) tryKeyword(keyword string) bool { ... }

// INCORRECT: Operates on a copy, state is lost
func (p Parser) tryKeyword(keyword string) bool { ... }
```
**The Reasoning:** In Go, everything is pass-by-value. If you use a value receiver, the parser copies the entire `Parser` struct when calling the method. The method might advance its local copy of `pos`, but the original `Parser` state remains entirely unchanged. By passing a pointer (`*Parser`), every tokenizing function shares the exact same memory address, allowing the cursor to march forward consistently.

---

## 2. The Power of "Do No Harm": Backtracking & Lookahead

The operational boundary of these `try...` functions is absolute:
* **Commit on Success:** Advance `pos` only if the entire token is validated.
* **Rollback on Failure:** On failure, `pos` is strictly left untouched. 

This is the cornerstone of Recursive Descent Parsing, specifically dealing with **Lookahead**. SQL grammars often feature ambiguities that cannot be resolved by looking at a single word. Imagine the parser encounters the sequence `ORDER BY`.

1. The parser might first try: `tryKeyword("ORDERING")`. It reads O, R, D, E, R, and then sees a space instead of an I. It fails.
2. Because the cursor was completely reset to its original position upon failure, the parser can seamlessly attempt the next rule: `tryKeyword("ORDER")`, which succeeds.

This "try, fail, reset" mechanism means our lexer doesn't need to preemptively "know" what token is coming next. It simply attempts paths down the decision tree and safely backtracks if a path proves invalid, eliminating the need for complex state machines.

---

## 3. CPU Mechanics: Demystifying the Bitwise Magic (`ch | 32`)

The helper functions dictate the grammar rules, but they are engineered for mechanical sympathy with the CPU. SQL is case-insensitive, meaning `SELECT`, `select`, and `SeLeCt` must all resolve to the same keyword token. 

```go
func isAlpha(ch byte) bool {
	return 'a' <= (ch|32) && (ch|32) <= 'z'
}
```

The `isAlpha` bitwise trick is a textbook example of high-performance systems programming. The ASCII table was intentionally designed in the 1960s to make this exact operation mathematically trivial. 

* `'A'` is 65 in decimal, which is `01000001` in binary.
* `'a'` is 97 in decimal, which is `01100001` in binary.

The only difference between the uppercase and lowercase version of any ASCII letter is the 6th bit (the 32s column). 32 in binary is `00100000`. By forcing the 6th bit to 1 via a bitwise OR (`ch | 32`), you are mathematically dragging any uppercase letter into its lowercase equivalent in a single CPU cycle.

**Why not use `strings.ToLower()`?**
Standard library string functions have to account for Unicode, locales, and memory allocation. They are vastly heavier. Since SQL keywords are strictly ASCII, this bitwise trick bypasses function call overhead, stack frame allocation, and complex Unicode branching, keeping the CPU instruction pipeline fully saturated.

# It is Heavy: The Cost of Unicode and Memory
It seems like strings.ToLower() should be simple, but it is doing vastly more work than you might realize.

- The Unicode Penalty
Go’s strings are UTF-8 encoded by default. The standard library cannot assume you are just passing it standard English ASCII letters (A-Z). When you call strings.ToLower(), the engine must: Decode the string into runes (handling characters that might be 1, 2, 3, or 4 bytes long).

Look up every single character in a massive, pre-compiled Unicode translation table to see if a lowercase version exists (e.g., converting 'É' to 'é', or handling Greek, Cyrillic, etc.). Account for characters that change byte-size when lowercased.

- Our tokenizer only cares about SQL syntax, which is strictly ASCII. By doing ch | 32, we bypass all Unicode decoding and table lookups, completing the operation in a single hardware CPU cycle.

The Memory Allocation Penalty (The Garbage Collector)
In Go, strings are immutable. You cannot change a string once it is created.
If you have a 1,000-character SQL query and you run strings.ToLower(query), Go cannot just change the letters in place. It must:

1. Ask the operating system for a brand new block of RAM.

2. Copy the newly lowercased characters into that new memory.

3. Leave the old string sitting in memory.

-> If your database processes 10,000 queries a second, you are constantly forcing the Go Garbage Collector (GC) to wake up, scan the RAM, and clean up millions of discarded strings. This causes your program to stutter and pause. Our tokenizer never allocates new memory; it just slides a cursor (the pos integer) over the original string.

# Why Regular Expressions (Regex) are Heavy: The Cost of Generalization
- Regex is incredibly powerful, but it is essentially a mini-programming language running inside your programming language.

The State Machine Overhead When you write a regex like [a-zA-Z_][a-zA-Z0-9_]* to find a SQL identifier, the CPU doesn't understand that. The Regex engine must:
1. Parse your regex string.

2. Compile it into a complex mathematical construct called a Non-deterministic Finite Automaton (NFA).

3. Execute a generalized interpreter that steps through the NFA state-by-state to check your string.

The Tracking Penalty
Regex is designed to do everything: finding matches, capturing groups, and backtracking through complex patterns. To do this, regex engines allocate arrays in memory to keep track of where potential matches started and ended.

---

## 4. System Constraints: The "Maximal Munch" Principle & Lexical Boundaries

A critical constraint introduced here is the concept of **Separators** and boundary enforcement. 

```go
func isSeparator(ch byte) bool {
	return ch < 128 && !isNameContinue(ch)
}
```

This enforces what is known in compiler theory as the **Maximal Munch Principle**. A tokenizer should always try to consume as much of the string as possible to form a valid token. If it reads `s-e-l-e-c-t-i-o-n-s`, it shouldn't stop at `t` just because `select` is a valid keyword. It must keep consuming alphanumeric characters until it hits a defined boundary (whitespace, a comma, an operator, or EOF).

* If the chunk ends up being `select`, it is classified as a Keyword. 
* If the chunk ends up being `selections`, the tokenizer classifies it as an Identifier (Name). 

The boundary enforcement is what physically separates these distinct grammatical categories. Without this strict boundary, the tokenizer would suffer from a vulnerability where a table legitimately named `selections` would trigger a syntax error because the lexer eagerly consumed the `select` prefix as a keyword.

# Execution Trace: The Cursor in Motion

## The Mock Scenario
Imagine our database receives the following query from a client:
`"  SELECT id"`

When we initialize our tokenizer, it creates the following state in memory:

```go
p := NewParser("  SELECT id")
// p.buf = "  SELECT id" (length: 11 bytes)
// p.pos = 0
```

Let's walk through exactly what happens at the CPU and memory level when we command the parser to find the first token.

### Step 1: Whitespace Pruning (The `isSpace` loop)
SQL engines ignore arbitrary whitespace. Before the parser tries to read a word, it must advance the cursor past any leading spaces.

* **Iteration 1:** * Cursor: `p.pos = 0`
  * Byte: `p.buf[0]` is `' '` (Space, ASCII 32).
  * Check: `isSpace(32)` returns `true`.
  * Action: The parser advances the cursor. `p.pos = 1`.

* **Iteration 2:**
  * Cursor: `p.pos = 1`
  * Byte: `p.buf[1]` is `' '` (Space, ASCII 32).
  * Check: `isSpace(32)` returns `true`.
  * Action: The parser advances the cursor. `p.pos = 2`.

* **Iteration 3:**
  * Cursor: `p.pos = 2`
  * Byte: `p.buf[2]` is `'S'` (Uppercase S, ASCII 83).
  * Check: `isSpace(83)` returns `false`.
  * Action: The loop breaks. The cursor stops at `p.pos = 2`. The parser is now looking at the start of a valid word.

### Step 2: The Bitwise Read (The `isAlpha` check)
Now the parser needs to figure out if it is looking at a keyword (like `SELECT`). It looks at the byte at `p.pos = 2`, which is `'S'`.

Here is exactly how the bitwise math executes in the CPU register to verify this is a letter:
1. `ch` is `83` (Binary: `01010011`).
2. The function applies the bitwise OR: `ch | 32`.
   ```text
     01010011  ('S', 83)
   | 00100000  (32)
   ----------
     01110011  ('s', 115)
   ```
3. The CPU checks if `115` falls between `'a'` (97) and `'z'` (122). It does.
4. `isAlpha` returns `true`.

### Step 3: The "Maximal Munch" Loop
Because the first letter was valid, the parser enters a fast loop to consume the rest of the word. It will keep advancing `pos` as long as `isNameContinue()` returns true.

* `pos=2` ('S') -> Valid. `pos++`
* `pos=3` ('E') -> Valid. `pos++`
* `pos=4` ('L') -> Valid. `pos++`
* `pos=5` ('E') -> Valid. `pos++`
* `pos=6` ('C') -> Valid. `pos++`
* `pos=7` ('T') -> Valid. `pos++`

### Step 4: The Boundary Enforcement
The cursor is now at `p.pos = 8`. 
The parser looks at `p.buf[8]`, which is `' '` (the space before "id").

1. The loop calls `isNameContinue(' ')`. 
2. A space is not a letter, not a digit, and not an underscore. It returns `false`.
3. The fast loop breaks. 

The parser now knows the exact boundaries of the word. It started at index `2` and ended at index `8`. 
It executes a zero-allocation slice: `token := p.buf[2:8]`, which yields `"SELECT"`.

### Step 5: The Final Safety Check (`isSeparator`)
Before the parser officially commits to this token, it verifies the boundary to prevent the "fromage/from" vulnerability. 

It checks the byte that broke the loop (`p.buf[8]`, the space) against `isSeparator(' ')`.
Since a space is less than ASCII 128 and is *not* a valid name character, it returns `true`.

**The Result:** The parser has successfully verified the word `"SELECT"`. The token is returned to the execution engine, and the cursor (`p.pos`) remains parked at `8`, perfectly positioned to parse the next token (`"id"`).`:> [!WARNING]
> `
