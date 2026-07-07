# 0302: Parse Values — Predictive Parsing & Value Extraction

## Systems Engineering Overview
Transitioning from basic lexical tokenization (identifying names and keywords) to value extraction requires parsing raw bytes into typed scalar data. This module introduces the engine's capability to parse literal values (`int64` and `string`) into a normalized `Cell` structure. 

The core design philosophy here is **Predictive Parsing (LL(1) strategy)**. The engine determines the entire subsequent parsing path—and which specialized state machine to invoke—based on a single byte of lookahead. This avoids complex backtracking loops when identifying data types, keeping the parser tightly bound to $O(N)$ time complexity for literal extraction.

## Architecture / Core Implementation
The `parseValue` function acts as a high-speed router. It examines the current byte at the cursor (`p.buf[p.pos]`) to dispatch execution to either `parseString` or `parseInt`.

```go
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
```

* **Pointer Semantics (`out *Cell`):** By passing a pointer to the target `Cell`, the parser avoids allocating new structs on the heap and returning them by value. It mutates the state by writing the parsed integer or string directly into pre-allocated memory.
* **Integer Parsing (`parseInt`):** Triggered by an initial digit, `+`, or `-`. It scans contiguous numeric ASCII bytes and mathematically converts them into a base-10 `int64`.
* **String Parsing (`parseString`):** Triggered by single or double quotes. It requires a more complex state machine to handle escape sequences (`\'`, `\"`, `\\`), distinguishing between the literal quote character and the string-terminating quote.

## CPU Mechanics & Memory Layout
***The Zero-Allocation Dilemma:** In Chapter 0301, identifiers were parsed with zero allocations by simply returning a slice of the original string (`p.buf[start:p.pos]`). However, **escaped strings break the zero-copy assumption**. Because `"is\'nt"` (7 bytes raw) translates to `isn't` (5 bytes actual), the physical memory footprint changes. The parser is forced to allocate a new, separate buffer (or Go string) to accumulate the unescaped characters.

- Dilemma: 
Because computer memory is contiguous, you can't just slice the backslash ('\') out of the middle, magically squeezing s and the ' together. 

To give the database the correct word (isn't), the parser forced to: 
Escapes = Allocation: Because the physical shape of the data changes (7 bytes raw becomes 5 bytes actual), we are forced to allocate brand new memory and manually copy the characters over one by one to create the final, clean string.


* **Branch Prediction:** The initial byte-check (`ch == '"' || ch == '\''`) translates to highly efficient machine code. Because SQL values are overwhelmingly likely to be either strings or numbers, the CPU's branch predictor can highly optimize this conditional routing pipeline.
* **Cache Locality:** The parser continues to operate as a sliding window over `p.buf`. Because the string data is contiguous in memory, fetching the next byte during `parseInt` or `parseString` results in near-zero L1 cache misses.

## System Constraints & Boundaries
* **Strict Scope Constraints:** The engine intentionally limits string escaping to structural necessities (quotes and slashes). Deferring complex escapes (like `\n`, `\xFF`, or Unicode `\u3412`) pushes peripheral complexity out of the critical path, keeping the current focus strictly on engine architecture rather than standard library replication.
* **Type Exclusivity:** The system currently only acknowledges two scalar primitives (`int64`, `string`). There is no floating-point (`float64`) or boolean fallback yet. Any value starting with a character outside of the defined sets (quotes, digits, signs) will instantly trigger an `expect value` boundary error, preventing malformed data from propagating further down the stack.

## Some notes for myself.
* out.Str(The payload)
- By the end of the function, out.Str holds [a, ', b]. Because we avoided the string() cast, out.Str is a raw byte slice.
When the database needs to save this row to disk, it passes this exact Cell to cell.go's Encode() method. Encode() will immediately take out.Str and write those exact bytes to the disk buffer. 

