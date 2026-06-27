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
		deleted: true, 
	}

	encodedBytes := ent.Encode()
	buf := bytes.NewBuffer(encodedBytes)
	
	decodedEnt := &Entry{}
	err := decodedEnt.Decode(buf)

	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

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
// TEST 2: Disk Logging and Crash Recovery (Now with Fsync!)
// ============================================================================
func TestKVRecovery(t *testing.T) {
	fileName := "test_recovery.log"
	defer os.Remove(fileName) 

	// --- PHASE 1: Normal Database Operation ---
	db1 := &KV{log: Log{FileName: fileName}}
	if err := db1.Open(); err != nil {
		t.Fatalf("Failed to open db1: %v", err)
	}

	db1.Set([]byte("user1"), []byte("Morgan"))
	db1.Set([]byte("user2"), []byte("Alice"))
	db1.Set([]byte("user1"), []byte("Morgan Kim")) 
	db1.Del([]byte("user2"))                       

	db1.Close() 

	// --- PHASE 2: Reboot and Recover ---
	db2 := &KV{log: Log{FileName: fileName}}
	
	if err := db2.Open(); err != nil {
		t.Fatalf("Failed to open db2: %v", err)
	}

	if val, ok := db2.mem["user1"]; !ok || string(val) != "Morgan Kim" {
		t.Errorf("Recovery failed for user1. Expected 'Morgan Kim', got '%s'", string(val))
	}

	if _, ok := db2.mem["user2"]; ok {
		t.Errorf("Recovery failed for user2. It should have been deleted, but was found in RAM.")
	}

	db2.Close()
}
