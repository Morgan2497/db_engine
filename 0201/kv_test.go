package kv

import (
	"bytes"
	"testing"
)

// 1. Testing the Integer logic (Ensuring negative numbers survive the binary round-trip)
func TestCellEncodeDecode_I64(t *testing.T) {
	original := &Cell{Type: TypeI64, I64: -999}

	// Encode it. Passing 'nil' tells append to just create a new slice.
	buffer := original.Encode(nil)

	decoded := &Cell{Type: TypeI64}
	rest, err := decoded.Decode(buffer)

	if err != nil {
		t.Fatalf("Failed to decode I64: %v", err)
	}
	// The rest slice should be completely empty if we bit off exactly 8 bytes
	if len(rest) != 0 {
		t.Fatalf("Expected empty rest buffer, got %d bytes left over", len(rest))
	}
	// Did -999 survive?
	if decoded.I64 != original.I64 {
		t.Fatalf("Expected %d, got %d", original.I64, decoded.I64)
	}
}

// 2. Testing the String logic (Ensuring the 4-byte header works)
func TestCellEncodeDecode_Str(t *testing.T) {
	original := &Cell{Type: TypeStr, Str: []byte("Database Engineering")}
	
	buffer := original.Encode(nil)

	decoded := &Cell{Type: TypeStr}
	rest, err := decoded.Decode(buffer)

	if err != nil {
		t.Fatalf("Failed to decode Str: %v", err)
	}
	if len(rest) != 0 {
		t.Fatalf("Expected empty rest buffer, got %d bytes left over", len(rest))
	}
	// bytes.Equal safely compares two byte slices
	if !bytes.Equal(decoded.Str, original.Str) {
		t.Fatalf("Expected %s, got %s", string(original.Str), string(decoded.Str))
	}
}

// 3. Testing the actual Database Row scenario (Chaining them together)
func TestRowBuffer(t *testing.T) {
	// Simulate a database row: Column 1 is an Int, Column 2 is a String
	col1 := &Cell{Type: TypeI64, I64: 42}
	col2 := &Cell{Type: TypeStr, Str: []byte("Cat")}

	// ENCODE PHASE: Pack them into a single, continuous tape
	rowBuffer := make([]byte, 0)
	rowBuffer = col1.Encode(rowBuffer) // Ribbon is now 8 bytes
	rowBuffer = col2.Encode(rowBuffer) // Ribbon is now 15 bytes (8 + 4 + 3)

	// DECODE PHASE: Unpack them sequentially using the "rest" leftovers
	readCol1 := &Cell{Type: TypeI64}
	leftovers, err1 := readCol1.Decode(rowBuffer)

	readCol2 := &Cell{Type: TypeStr}
	finalRest, err2 := readCol2.Decode(leftovers)

	// Verify nothing crashed
	if err1 != nil || err2 != nil {
		t.Fatalf("Crashed during sequential decoding")
	}
	
	// Verify the data matches exactly
	if readCol1.I64 != 42 || string(readCol2.Str) != "Cat" {
		t.Fatalf("Data corruption during sequential decode!")
	}
	
	// Verify the entire tape was consumed
	if len(finalRest) != 0 {
		t.Fatalf("Expected tape to be empty, but bytes were left behind!")
	}
}
