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

func TestTornWriteRecovery(t *testing.T) {
	// 1. Setup a temporary test file
	os.Remove("test_torn_write.db")
	kv := &KV{log: Log{FileName: "test_torn_write.db"}}
	
	err := kv.Open()
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}

	// 2. Write a perfectly valid record
	kv.Set([]byte("key1"), []byte("value1"))
	kv.Close()

	// 3. THE SABOTAGE: Simulate a torn write by appending 5 garbage bytes directly to the file
	file, err := os.OpenFile("test_torn_write.db", os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("Failed to open file for sabotage: %v", err)
	}
	// Writing 5 bytes. A real header needs 13. This will trigger io.ErrUnexpectedEOF!
	file.Write([]byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00}) 
	file.Close()

	// 4. THE RECOVERY: Reboot the database
	kv2 := &KV{log: Log{FileName: "test_torn_write.db"}}
	err = kv2.Open()
	
	// If our Log.Read() didn't catch the error, err would not be nil and the DB would crash!
	if err != nil {
		t.Fatalf("Database crashed during recovery of a torn write! Error: %v", err)
	}

	// 5. THE VERIFICATION: Did it successfully load the valid record and drop the garbage?
	val, exists := kv2.mem["key1"]
	if !exists || string(val) != "value1" {
		t.Fatalf("Failed to recover valid record after a torn write.")
	}

	kv2.Close()
	os.Remove("test_torn_write.db")
}
