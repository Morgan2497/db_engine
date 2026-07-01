package kv

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestCellI64Roundtrip(t *testing.T) {
	cell := Cell{Type: TypeI64, I64: -42}
	buf := cell.Encode(nil)
	if len(buf) != 8 {
		t.Fatalf("i64 encode length: got %d want 8", len(buf))
	}

	decoded := Cell{Type: TypeI64}
	rest, err := decoded.Decode(buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 0 {
		t.Fatalf("expected no rest bytes, got %d", len(rest))
	}
	if decoded.I64 != -42 {
		t.Fatalf("I64: got %d want -42", decoded.I64)
	}
}

func TestCellStrRoundtrip(t *testing.T) {
	cell := Cell{Type: TypeStr, Str: []byte("hello")}
	buf := cell.Encode(nil)
	if binary.LittleEndian.Uint32(buf[0:4]) != 5 {
		t.Fatalf("bad length prefix")
	}

	decoded := Cell{Type: TypeStr}
	rest, err := decoded.Decode(buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 0 {
		t.Fatalf("expected no rest bytes, got %d", len(rest))
	}
	if !bytes.Equal(decoded.Str, cell.Str) {
		t.Fatalf("Str: got %q want %q", decoded.Str, cell.Str)
	}
}

func TestCellChainedDecode(t *testing.T) {
	buf := (&Cell{Type: TypeI64, I64: 1}).Encode(nil)
	buf = (&Cell{Type: TypeStr, Str: []byte("x")}).Encode(buf)

	c1 := Cell{Type: TypeI64}
	rest, err := c1.Decode(buf)
	if err != nil {
		t.Fatal(err)
	}
	if c1.I64 != 1 {
		t.Fatalf("first cell I64: got %d want 1", c1.I64)
	}

	c2 := Cell{Type: TypeStr}
	rest, err = c2.Decode(rest)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 0 {
		t.Fatalf("expected no rest bytes, got %d", len(rest))
	}
	if string(c2.Str) != "x" {
		t.Fatalf("second cell Str: got %q want x", c2.Str)
	}
}

func TestCellEncodeAppend(t *testing.T) {
	base := []byte("prefix")
	buf := (&Cell{Type: TypeI64, I64: 7}).Encode(base)
	if !bytes.HasPrefix(buf, []byte("prefix")) {
		t.Fatal("Encode should append to existing buffer")
	}
	if len(buf) != len(base)+8 {
		t.Fatalf("append length: got %d want %d", len(buf), len(base)+8)
	}
}
