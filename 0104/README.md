# Step 0104: Fsync & Data Durability

> **Overview:** Writing data to a file does not mean the data is physically on the hard drive. Operating Systems use volatile memory caches to delay writes for performance. In this step, we implement `fsync` (File Sync) to forcefully bypass these caches, guaranteeing absolute **Durability** so our database survives sudden power loss.

## Core Architectural Concepts

### 1. The Illusion of Disk I/O (The Page Cache)
When an application calls a standard file `Write()` operation, the Operating System does **not** immediately spin up the physical hard drive. Physical disk I/O is incredibly slow. Instead, the OS writes your data into a hidden layer of volatile RAM called the **Page Cache**. 

* **The Benefit:** By holding writes in RAM, the OS can merge hundreds of tiny writes into one large, efficient disk write later. It massively improves system throughput.
* **The Danger:** If the server loses power while your data is sitting in the Page Cache, the RAM is wiped. Your file might disappear entirely, or become corrupted (filled with `0x00` null bytes).

The Page Cache is a core component of the operating system's kernel that uses unused volatile RAM to store copies of disk data pages that purposely minimizes direct interaction with physical storage. 
It acts as a transparent buffer between applications and the physical disk. When an application requests data (read) or saves data (write), the OS handles the operation in RAM first. 

* Reads: If read requested, that's good because data is already in the cache, the OS returns it immediately from RAM, avoiding a slow disk seek. 

* Writes: The OS copies the data into the cache and marks these memory pages as "dirty". The data is considered written from the application's perspective, but it has not yet reached the physical disk. 

### 2. The Database Durability Contract
A fundamental rule of database engineering is **Durability**. The database must guarantee that once it tells a user "Success, your data is saved," that data will absolutely survive a pulled power plug. 

Because the OS Page Cache and the Hard Drive's own internal hardware caches are volatile, standard `Write()` calls are not enough to fulfill this contract. 

### 3. The `fsync` System Call
To guarantee durability, an application must issue a strict command to the OS: *"Take this specific file, flush it out of your RAM caches, force the physical disk to write it, and do not let my program continue until the disk hardware confirms it is done."*

In Linux/Unix, this system call is `fsync`. In Go, it is exposed as the `Sync()` method on an `os.File`.

---

## The Hidden Trap: Directory Fsyncing

Flushing the data inside the file is only half the battle. We also have to ensure the file *exists*. 

### How Unix Filesystems Work
On Unix/Linux systems, a file is actually split into two parts:
1. **The Data:** The actual bytes inside the file.
2. **The Metadata (Directory Entry):** The file's name and its pointer, which are recorded inside the parent directory folder. 

If you create a brand new log file and sync its *data* to disk, but the system crashes before the parent directory updates its *metadata*, the file will become an invisible "orphan." The data is physically on the disk, but the OS doesn't know the file exists because the directory folder wasn't saved!

**The Rule:** Creating, renaming, or deleting a file requires you to `fsync` the **parent directory folder** itself. (Note: Windows handles this automatically, but Unix requires explicit instruction).

---

## Technical Implementation

### 1. File Descriptors (Unix Basics)
To interact with anything at a low level in Unix, the OS gives you a number called a **File Descriptor (fd)**. It acts as an ID badge for open files, directories, or network connections. We must use raw system calls (`syscall`) to get a file descriptor for our parent directory so we can sync it.

- File Descriptor: It is a unique identifier for a file or other input/output resource, such as a pipe or network socket. It typically has non-negative integer values, with negative values being reserved to indicate "no value" or error conditions.


### 2. Implementing the Directory Sync
Because Go's standard library does not have a built-in method for syncing directories, we must build a custom function wrapping raw OS system calls:

```go
import (
    "os"
    "path"
    "syscall"
)

func syncDir(file string) error {
    // Open the parent directory specifically as a read-only directory
    flags := os.O_RDONLY | syscall.O_DIRECTORY
    dirfd, err := syscall.Open(path.Dir(file), flags, 0o644)
    if err != nil {
        return err
    }
    defer syscall.Close(dirfd) // Always close file descriptors to prevent memory leaks

    // Force the physical disk to save the directory metadata
    return syscall.Fsync(dirfd)
}
```

### 3. Safe File Creation
We wrap the standard `os.OpenFile` in a custom function that automatically handles the directory sync every time a new log file is generated:

```go
func createFileSync(file string) (*os.File, error) {
    fp, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE, 0o644)
    if err != nil {
        return nil, err
    }
    
    // Ensure the new file's existence is permanently recorded in the parent folder
    if err = syncDir(file); err != nil {
        _ = fp.Close()
        return nil, err
    }
    
    return fp, err
}
```

### 4. Updating the Log Interface
Finally, we update our database `Write` and `Open` methods to utilize these new safety mechanisms:

```go
func (log *Log) Open() (err error) {
    // Replace standard os.OpenFile with our durable custom function
    log.fp, err = createFileSync(log.FileName)
    return err
}

func (log *Log) Write(ent *Entry) error {
    // 1. Write to the OS Page Cache
    if _, err := log.fp.Write(ent.Encode()); err != nil {
        return err
    }
    
    // 2. Force the OS to flush the cache to the physical disk hardware
    return log.fp.Sync() 
}
```

---

## ⚠️ Looking Ahead: Atomicity
While `fsync` guarantees our data survives a power loss, it introduces a new problem. What if the server loses power *while* the physical disk is in the middle of writing our `fsync` command? The file will end up half-written and corrupted. Protecting the database from partially written records (Atomicity) is the next hurdle to overcome.
