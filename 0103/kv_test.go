package kv

import (
	"bytes"
	"os"
	"testing"
)

// ============================================================================
// TEST 1: The 9-Byte Binary Serialization
// ============================================================================
func TestEncodeDecode(t *testing.T) {
	ent := &Entry{
		key:     []byte("test_key"),
		val:     []byte("test_value"),
		deleted: true, // Let's test the tombstone flag
	}

	// 1. Encode into raw bytes
	encodedBytes := ent.Encode()

	// 2. Simulate reading from a hard drive using a byte buffer
	buf := bytes.NewBuffer(encodedBytes)
	
	// 3. Decode the bytes back into a new struct
	decodedEnt := &Entry{}
	err := decodedEnt.Decode(buf)

	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	// 4. Verify everything perfectly matches
	if string(decodedEnt.key) != "test_key" {
		t.Errorf("Expected key 'test_key', got '%s'", string(decodedEnt.key))
	}
	if string(decodedEnt.val) != "test_value" {
		t.Errorf("Expected val 'test_value', got '%s'", string(decodedEnt.val))
	}
	if decodedEnt.deleted != true {
		t.Errorf("Expected deleted to be true, got false")
	}
}

// ============================================================================
// TEST 2: Disk Logging and Crash Recovery
// ============================================================================
func TestKVRecovery(t *testing.T) {
	// Setup: Create a temporary log file for testing
	fileName := "test_recovery.log"
	defer os.Remove(fileName) // This ensures the file is deleted when the test finishes

	// --- PHASE 1: Normal Database Operation ---
	db1 := &KV{log: Log{FileName: fileName}}
	if err := db1.Open(); err != nil {
		t.Fatalf("Failed to open db1: %v", err)
	}

	// Write some history to the log
	db1.Set([]byte("user1"), []byte("Morgan"))
	db1.Set([]byte("user2"), []byte("Alice"))
	db1.Set([]byte("user1"), []byte("Morgan Kim")) // Overwrite user1
	db1.Del([]byte("user2"))                       // Delete user2

	db1.Close() // Simulating a server shutdown or crash! RAM is now cleared.

	// --- PHASE 2: Reboot and Recover ---
	db2 := &KV{log: Log{FileName: fileName}}
	
	// Open() will trigger the `for` loop to read the file until io.EOF
	if err := db2.Open(); err != nil {
		t.Fatalf("Failed to open db2: %v", err)
	}

	// 1. Check if user1 was properly overwritten by the later entry
	if val, ok := db2.mem["user1"]; !ok || string(val) != "Morgan Kim" {
		t.Errorf("Recovery failed for user1. Expected 'Morgan Kim', got '%s'", string(val))
	}

	// 2. Check if the tombstone correctly wiped user2 from the RAM map
	if _, ok := db2.mem["user2"]; ok {
		t.Errorf("Recovery failed for user2. It should have been deleted, but was found in RAM.")
	}

	db2.Close()
}
