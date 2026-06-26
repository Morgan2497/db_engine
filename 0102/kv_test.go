package kv 

import (
	"bytes"
	"testing"
)

func TestEntrySerialization(t *testing.T) {
	original := &Entry {
		key: []byte("a"),
		val: []byte("bb"),
	}
	
	// encode key and val
	encoded := original.Encode()
	
	expectedBytes := []byte{1,0,0,0,2,0,0,0,'a','b','b'}

	if !bytes.Equal(encoded, expectedBytes) {
		t.Fatalf("Encode failed!/nExpected: %v\nGot:		%v", expectedBytes, encoded)
	}
	
	// decode testing.
	// bytes.NewReader takes our raw byte array and turns it into an io.Reader stream.
	// Like an open file on a hard drive, waiting to be read.
	reader := bytes.NewReader(encoded)

	// an empty array to get the decoded output.
	decoded := &Entry{}
	
	decodedError := decoded.Decode(reader)

	if decodedError != nil {
		t.Fatalf("Decode crahsed with an unexpected error: %v", decodedError)
	}

	if !bytes.Equal(decoded.key, original.key) {
		t.Errorf("Key mismatch! Expected %s, but got %s", original.key, decoded.key)
	}

	if !bytes.Equal(decoded.val, original.val) {
		t.Errorf("Value mismatch! Expected %s, but got %s", original.val, decoded.val)
	}
}
