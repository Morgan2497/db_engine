# Chapter 0702: Store Metadata

## Overview

Chapter 0702 connects the atomic metadata store from Chapter 0701 to the
database's SSTable lifecycle. The database now owns a directory rather than a
few paths supplied independently by its caller, and each compacted SSTable is
given a unique, versioned name.

The important change is how the active SSTable is selected:

```text
before 0702: fixed path + rename(new, old)

after 0702:  create uniquely named SSTable
                         |
                         v
             atomically record its name in metadata
                         |
                         v
             delete the previously active SSTable
```

Metadata is the durable source of truth. An SSTable becomes current only when
its filename is recorded in the newest valid metadata slot.

---

## 1. Directory-Owned Storage

The user supplies one database directory:

```go
type KVOptions struct {
	Dirpath string
}
```

`KV` derives every internal path from that directory:

```go
type KV struct {
	Options KVOptions
	meta    KVMetaStore
	log     Log
	main    SortedFile
	// ...
}
```

The files with stable roles keep fixed names:

```text
<Dirpath>/kv_log   write-ahead log
<Dirpath>/meta0    metadata slot 0
<Dirpath>/meta1    metadata slot 1
```

In code, build these names with `path.Join` as shown in the chapter:

```go
kv.log.FileName = path.Join(kv.Options.Dirpath, "kv_log")
kv.meta.slots[0].FileName = path.Join(kv.Options.Dirpath, "meta0")
kv.meta.slots[1].FileName = path.Join(kv.Options.Dirpath, "meta1")
```

SSTable names are different: they are generated dynamically and the active
one is stored in `KVMetaData`.

```text
database/
|-- kv_log
|-- meta0
|-- meta1
`-- sstable_42     name selected by the newest valid metadata record
```

### Metadata files versus metadata contents

The chapter's wording can be confusing here. The intended distinction is:

- `kv_log`, `meta0`, and `meta1` are fixed physical filenames. The database
  must know these names in advance so it can find the WAL and metadata when it
  starts.
- `KVMetaData` is not another fixed-name file. It is the value serialized
  inside `meta0` and `meta1`.
- `KVMetaData.SSTable` contains the dynamically generated name of the active
  SSTable, such as `sstable_42`.

For example, one of the fixed metadata files might contain the equivalent of:

```json
{
  "Version": 42,
  "SSTable": "sstable_42"
}
```

Startup therefore works like this:

```text
open the known files meta0 and meta1
                  |
                  v
choose the newest valid KVMetaData value
                  |
                  v
read SSTable = "sstable_42"
                  |
                  v
open <Dirpath>/sstable_42
```

In other words, the metadata files have stable names, while the SSTable name
stored inside their contents changes after successful compaction. This lets
the database switch from `sstable_41` to `sstable_42` by atomically updating
small metadata instead of renaming or overwriting the SSTable itself.

---

## 2. Why a Fixed SSTable Name Is No Longer Enough

Chapter 0605 could compact into a temporary file and rename it over one fixed
SSTable. That works while the engine owns exactly one disk level with exactly
one known filename.

An LSM tree will eventually contain several files and levels. A fixed name
cannot describe which collection of files makes up the current database
state. The engine therefore separates:

- immutable data files, whose names never change; and
- small mutable metadata, which records which data files are active.

Chapter 0701 makes that small metadata update atomic with two checksummed,
versioned slots. Chapter 0702 uses it to select the current SSTable.

---

## 3. Versioned SSTable Names

`KV` keeps a monotonic counter:

```go
type KV struct {
	version uint64
	// ...
}
```

Every compaction attempt reserves a new filename:

```go
kv.version++
sstable := fmt.Sprintf("sstable_%d", kv.version)
file := SortedFile{
	FileName: path.Join(kv.Options.Dirpath, sstable),
}
```

The same number is stored in the metadata record:

```go
type KVMetaData struct {
	Version uint64
	SSTable string
}
```

On open, the database resumes the counter from durable metadata:

```go
kv.version = kv.meta.Get().Version
```

This prevents a normal restart from reusing an active SSTable name. More
subtly, the in-memory counter must advance even when compaction fails. If the
process remains alive and retries, that retry must not overwrite a file left
behind by an earlier attempt whose outcome is uncertain.

---

## 4. Compaction Is a Metadata Switch

A successful compaction follows this order:

```text
1. Increment the version and choose a unique SSTable name.
2. Write the merged database state into that new file.
3. Flush the new SSTable so it is durable.
4. Store {Version, SSTable} through KVMetaStore.Set().
5. Start using the new SSTable.
6. Delete the old SSTable.
```

The ordering protects both sides of the transition:

- The new file must be complete before metadata can point to it.
- The old file must remain until the metadata switch has succeeded.
- The old file is garbage only after the new metadata state is known to be
  durable.

The state transition can be pictured as:

```text
metadata -> sstable_41

write sstable_42
        |
        v
metadata.Set({Version: 42, SSTable: "sstable_42"})
        |
        v
metadata -> sstable_42       sstable_41 may now be removed
```

Unlike the earlier design, the SSTable itself is not installed by renaming it
over a fixed destination. The atomic operation is the much smaller metadata
update.

---

## 5. A Failed Metadata Write Has an Unknown Outcome

The chapter's most important error-handling rule is:

> If a state-changing durable write returns an error, do not assume that the
> on-disk state stayed unchanged.

For example, `kv.meta.Set(...)` may report an I/O or `fsync` error even though
some or all of the new metadata reached storage. After such an error, either
of these may be recovered after a restart:

```text
old metadata -> old SSTable
new metadata -> new SSTable
```

Therefore, if the metadata switch fails:

- the running process may keep using its old in-memory SSTable;
- it must keep the old SSTable because metadata may still point to it;
- it must also keep the new SSTable because metadata may point to that one;
- a retry must use another, larger version and a new filename.

Deleting the new SSTable on the error path is unsafe. The metadata write may
have taken effect despite returning an error, which would leave durable
metadata pointing to a missing file.

This conservative policy can leave orphan SSTables after an I/O error or
power loss. They should be reclaimed only when the engine can determine the
durable metadata state with confidence.

---

## 6. Opening the Database

At startup, `KV.Open()` has to reconstruct both filenames and ownership:

```text
KV.Open
|-- derive kv_log, meta0, and meta1 from Options.Dirpath
|-- open the WAL
|-- open both metadata slots
|-- choose the newest valid metadata record
|-- restore kv.version from that record
`-- open the SSTable named by that record, if one exists
```

Only the filename stored in the selected metadata record identifies the
active SSTable. Other `sstable_<version>` files may be remnants of interrupted
or failed compactions and must not silently become active merely because they
exist in the directory.

---

## 7. Close Partially Opened State

`KV.Open()` initializes several resources. If a later open step fails, every
resource opened earlier in the sequence must still be closed.

The chapter uses a collection of `io.Closer` values:

```go
type MultiClosers []io.Closer

type KV struct {
	// ...
	MultiClosers
}
```

Each component is appended after it opens successfully. `MultiClosers.Close`
then closes all recorded resources and clears the slice:

```go
func (mc *MultiClosers) Close() (reterr error) {
	for _, item := range *mc {
		if err := item.Close(); err != nil {
			reterr = err
		}
	}
	*mc = nil
	return reterr
}
```

This gives `KV` a reusable `Close()` implementation and makes cleanup on an
`Open()` error straightforward. Clearing the slice also makes repeated calls
safe with respect to the already recorded handles.

One implementation choice worth testing explicitly is close order. When
components depend on one another, closing in reverse acquisition order is
usually safer, even though the compact example in the chapter iterates
forward.

---

## 8. Invariants

The design depends on a small set of durable invariants:

1. A metadata record never names an SSTable that has not been fully written
   and flushed.
2. A successful metadata update selects exactly one current SSTable.
3. The previously active SSTable is deleted only after the switch succeeds.
4. A file involved in an uncertain metadata update is not deleted.
5. Every compaction attempt uses a filename not used by an earlier attempt in
   the same process.
6. `Open()` trusts the newest valid metadata slot, not directory contents or
   filename ordering.
7. A failed `Open()` closes all components it opened successfully.

These invariants are more useful than reasoning from individual system calls:
they describe what must remain true across errors, crashes, and restarts.

---

## 9. Suggested Tests

### Directory layout

- Opening a database creates or opens `kv_log`, `meta0`, and `meta1` beneath
  `KVOptions.Dirpath`.
- No internal database file is created outside that directory.

### Successful compaction

- The first compaction creates a versioned SSTable.
- Metadata contains the same version and basename.
- A restart opens the SSTable named by metadata.
- A later successful compaction records a larger version and removes the old
  file only after the metadata switch.

### Failure handling

- Failure while writing the new SSTable leaves the old state readable.
- Failure from `KVMetaStore.Set` does not delete either the old or new
  SSTable.
- A retry after metadata failure uses a fresh, larger filename.
- Recovery works whether the old or new metadata slot is the newest valid
  copy.

### Resource cleanup

- If opening metadata fails after the WAL opens, the WAL is closed.
- If opening the SSTable fails, both the WAL and metadata store are closed.
- Calling `Close` after partial initialization does not close a resource
  twice.

---

## 10. Relationship to Adjacent Chapters

```text
0701  Atomic Store
      two checksummed metadata slots; newest valid version wins
        |
        v
0702  Store Metadata
      directory layout + versioned SSTable names + safe file lifecycle
        |
        v
0703  Multiple Levels
      metadata expands from one SSTable name to a list of level files
```

Chapter 0702 is the bridge between an atomic metadata primitive and a real
multi-file LSM-tree layout. It still has one main SSTable, but that file is no
longer identified by a hard-coded path. The next chapter can extend
`KVMetaData` from one `SSTable` string to multiple `SSTables` without changing
the central rule: immutable files hold data, while atomically updated metadata
decides which files belong to the database.

## Source

Based on Chapter 0702, “Store Metadata,” pages 77–78 of
`/home/morgankim/Documents/ebooks/db_in_45_steps_go.pdf`.

The chapter also references the USENIX ATC 2020 paper *Can Applications
Recover from fsync Failures?* for the limitations of assuming perfectly
reported filesystem I/O errors.
