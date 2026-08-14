# Chapter 0600: Atomic Updates

## Overview

The database built so far keeps two representations of its state:

```text
append-only log on disk  +  sorted array in memory
        durability                 queryability
```

The log survives a restart, and replay rebuilds the sorted array. This is a
durable in-memory database, but it is still limited by RAM. Chapter 0600 asks
the next architectural question:

> How can a database update an on-disk data structure without a crash exposing
> a half-old, half-new state?

The difficulty is not merely writing bytes to disk. A database update often
changes many bytes, pages, or files. A power loss may occur between any two
writes. `fsync` makes completed writes durable, but it does not automatically
turn a multi-write change into one indivisible operation.

This chapter presents a family of solutions:

```text
copy-on-write ─┐
root pointers  ├─ preserve old state, prepare new state, switch atomically
double buffer  ┘

double write ─── describe enough physical data to repair an in-place update

LSM ──────────── avoid modifying structures; add and merge immutable ones
```

The chapter is conceptual. It does not add Go code yet. It establishes the
crash-safety designs used by the disk-based storage chapters that follow.

---

## 1. From Memory to Disk

The current sorted array is a useful teaching structure:

- point lookup can use binary search;
- iteration is naturally ordered;
- its contents can be serialized to disk simply.

It is not a practical final write structure. Inserting or deleting an element
requires shifting later elements:

```text
before insert: [a, c, d, e]
insert b:      [a, _, c, d, e]
                       └──── elements must move
after insert:  [a, b, c, d, e]
```

The work is `O(N)`. More importantly for this chapter, changing the array in
place destroys portions of the old state while constructing the new state. A
crash during the movement can leave neither a valid old array nor a valid new
one.

The book identifies two practical families toward which the simple array can
evolve:

| Family | Basic write idea |
|---|---|
| B+Tree | Update tree pages, commonly using a log or copy-on-write |
| LSM-Tree | Write new immutable structures and merge them later |

Both can be introduced incrementally from sorted arrays. Before choosing the
eventual structure, the engine needs a general model for atomic disk updates.

---

## 2. Atomicity and Durability Are Different Guarantees

Atomicity and durability are the **A** and **D** in ACID.

### Atomicity

An update is indivisible from recovery's point of view:

```text
after a crash, observe either:
    complete old state
or:
    complete new state

never:
    a mixture that is not a valid state
```

### Durability

Once the database reports success, the committed state survives a crash.

### Why both matter

Suppose changing a structure requires writes `A`, `B`, and `C`:

```text
write A → write B → write C
```

Without an atomic-update protocol, power can fail here:

```text
new A → new B → ⚡ → old C
```

Calling `fsync` after each piece could make this mixed state *durable*. It
would not make the three-piece change atomic. `fsync` is a persistence barrier,
not a multi-write transaction primitive.

The protocol must therefore answer two separate questions:

1. **What state is recoverable if power fails at this exact point?**
2. **What must be synced before success can be returned?**

---

## 3. The Unifying Invariant

The append-only log already demonstrates the central invariant:

> Do not destroy the last valid state until another complete, durable state is
> available.

The log has two useful properties:

- appending a record does not overwrite the valid log prefix;
- a checksum distinguishes a complete new record from a torn one.

If a crash tears the tail record, recovery ignores it and retains the earlier
valid prefix. The checksum acts as an implicit commit boundary between usable
and unusable records.

The same idea appears in every method in this chapter:

```text
1. Preserve a recoverable state.
2. Write enough information to obtain the new state.
3. Make that information durable.
4. Establish which state is current.
5. Reclaim the obsolete state only when it is safe.
```

Different methods mainly disagree about:

- what is copied or logged;
- where the old and new versions live;
- how the current version is selected;
- how many `fsync` barriers are required;
- whether recovery rolls backward or forward.

---

## 4. Copy-on-Write (CoW)

An array insert normally overwrites the array being read. Copy-on-write instead
constructs a separate version:

```text
old file: [a, c, d]
              │ copy and modify
              ▼
new file: [a, b, c, d]
```

The old version remains intact while the new version is incomplete. Only after
the new file is complete and durable does the database replace the name that
identifies the active file.

### Protocol

```text
0. Initial

   pointer ───────────────► [old data]

1. Write a new version

   pointer ───────────────► [old data]
                             [new data, possibly incomplete]

2. fsync the new version

   pointer ───────────────► [old data]
                             [complete durable new data]

3. Atomically switch the pointer

                             [old data]
   pointer ───────────────► [new data]

4. Delete the old version

   pointer ───────────────► [new data]
```

The linearization point—the instant at which the update becomes logically
current—is step 3, not the earlier data writes.

### Using `rename()` as the switch

On Linux, a rename that replaces the target name can serve as the atomic
switch. After a crash, that target name resolves to the old file or the new
file, rather than a partially updated directory entry.

The ordering is essential:

```text
write temporary/new file
        ↓
fsync new file contents
        ↓
rename over active target       ← atomic visibility switch
        ↓
fsync parent directory          ← make the naming change durable
```

Renaming before the new contents are durable can atomically point to data that
does not survive the crash. Atomicity of the name and durability of the file
contents are separate requirements.

### The source-name subtlety

Atomic replacement concerns the target name. There may be an intermediate
state in which both the source name and target name refer to the same new data.
That is harmless if the source name is treated as temporary and is not reused
prematurely.

Deleting the obsolete source is cleanup, not the commit point. A crash during
cleanup may leak an extra file, but it must not corrupt the chosen state.

### Cost

For a flat array, every update copies `O(N)` data. CoW is easy to reason about,
but a whole-file rewrite per logical update is too expensive for large data.

---

## 5. Root Pointers: Reduce a Large Update to a Small Switch

The CoW pattern becomes more powerful when a data structure has a root.

In a B+Tree, changing one leaf does not require copying every node. Copy the
changed leaf and each node on the path back to the root; unchanged subtrees are
shared:

```text
old root
├── shared subtree A
└── old internal node
    ├── shared subtree B
    └── old leaf

new root
├── shared subtree A
└── new internal node
    ├── shared subtree B
    └── new leaf
```

Once every new node is durable, atomically replacing the root pointer selects
the complete new tree. The large logical update has been reduced to a tiny
atomic metadata update.

```text
atomic data update  →  atomic root-pointer update
```

For a whole-file representation, the active file name plays the role of a root
pointer. For a tree, the root may be a page identifier or disk offset. If that
identifier fits inside the hardware's atomic write unit, a single-sector write
can perform the switch. If it cannot safely rely on such a write, the next
method protects the pointer in software.

### The common pattern in three forms

| Representation | Preserve old state by | Select new state by |
|---|---|---|
| Append-only log | Keeping the valid prefix | Accepting a complete checksummed record |
| Replaceable file | Writing a separate file | Atomic rename of the active name |
| Copy-on-write tree | Sharing old nodes and copying changed path | Atomic root-pointer change |

---

## 6. Double Buffering

Double buffering stores two complete slots and updates them alternately. Each
slot contains:

```text
[monotonic version | data | checksum]
```

Example:

```text
slot 0 → [version 124 | data yyy | crc32]   ← current
slot 1 → [version 123 | data zzz | crc32]
```

Recovery validates both checksums and chooses the valid slot with the greatest
version number.

### Update protocol

Suppose slot 0 at version 124 is current:

```text
1. Leave slot 0 unchanged.
2. Write version 125 and the new data into slot 1.
3. Write/checksum the complete slot as one recoverable record.
4. fsync slot 1.
5. Version 125 is now the newest valid slot.
```

No separate pointer switch is required. The `(checksum, version)` pair is the
selector:

- checksum answers **“is this slot complete?”**;
- version answers **“which complete slot is newer?”**.

### Crash matrix

| Crash point | Slot 124 | Slot 125 | Recovery result |
|---|---|---|---|
| Before overwriting inactive slot | valid | absent/old | choose 124 |
| During write of slot 125 | valid | checksum invalid | choose 124 |
| After complete sync of slot 125 | valid | valid and newer | choose 125 |

This method does not need filesystem rename atomicity or a hardware-atomic
pointer. It is effectively a two-record cyclic log—a ring buffer of size two.

### Where it fits

Double buffering works especially well for small, fixed-size state such as a
root pointer or metadata header. It needs only one `fsync` for an update because
there is no second switch record to persist.

It is less attractive for a large structure because each slot is a full copy.

---

## 7. Double Write and Physical Logging

Copy-on-write avoids in-place modification. Some structures, such as a hash
table array, naturally overwrite bytes at fixed offsets and may have no single
root pointer to switch.

Double write protects such an update by recording the affected physical bytes
before touching the main structure. The same information is deliberately
written twice:

1. once in a repair record;
2. once at its final location in the data structure.

The record describes low-level facts such as:

```text
file offset + byte count + bytes + checksum
```

It does not merely say a logical operation such as `SET account = 10`. This is
why the technique is called **physical logging**: recovery can repair raw bytes
even if the structure is too damaged to interpret logically.

### 7.1 Undo form: record the old bytes

Suppose offset `P` currently stores `xxx` and the update wants `yyy`:

```text
1. Write repair record: [P | length | old bytes xxx | checksum]
2. fsync repair record
3. Overwrite xxx with yyy in the main data
4. fsync main data
```

If the main write tears, recovery restores `xxx` from the record. This is an
undo design: recovery returns to the old state.

#### Undo crash matrix

| Crash point | Repair record | Main data | Recovery action |
|---|---|---|---|
| During steps 1–2 | invalid or valid | still old `xxx` | ignore bad record, or safely restore `xxx` |
| During step 3 | valid and durable | unknown mixture | restore `xxx` |
| During step 4 | valid and durable | possibly durable `yyy` | restoring `xxx` is still safe |
| After step 4 returns | record available, new data durable | `yyy` | installation of the new bytes is complete |

The critical ordering is:

```text
durable repair information happens-before destructive overwrite
```

Without the first `fsync`, the crash could tear both the repair record and the
main data, leaving no trustworthy version.

The chapter concentrates on the ordering that makes one overwrite repairable.
A complete implementation must also manage when a repair record is considered
pending and when it may be retired or reused; that lifecycle must not erase the
only repair information before the main bytes are known to be durable.

### 7.2 Redo form: record the new bytes

The repair record can instead store `yyy`:

```text
1. Write repair record: [P | length | new bytes yyy | checksum]
2. fsync repair record
3. Overwrite xxx with yyy in the main data
4. fsync main data
```

If the main write tears, recovery reapplies `yyy`. This is a redo design:
recovery completes the new state.

Because the durable record already contains everything needed to finish the
update, the database can acknowledge the update after the first `fsync`; the
copy to its final location may be completed asynchronously. The acknowledged
state is durable through the record even though the main structure has not yet
caught up.

### Undo versus redo

| Question | Undo physical log | Redo physical log |
|---|---|---|
| Record contains | bytes about to be overwritten | bytes that should replace them |
| Recovery direction | restore old state | finish new state |
| Safe state after main-write crash | old | new |
| Can final write be deferred after acknowledgement? | not by this chapter's protocol | yes, after durable redo record |

### Cost

Double write supports incremental changes to essentially any data structure,
but normally requires two persistence barriers:

```text
fsync repair record → modify main data → fsync main data
```

It trades write amplification for a general recovery mechanism.

---

## 8. LSM: Update Without Modifying Existing Structures

The previous methods focus on how to modify data safely. A log-structured merge
design changes the question:

> What if on-disk structures are immutable, and an update only adds another
> structure?

An LSM-Tree is not one concrete tree layout. In this chapter it is best
understood as a **set of mergeable data structures** supporting three actions:

1. On update, add a new structure to the set.
2. Merge existing structures so the set cannot grow without bound.
3. On query, search relevant structures and merge their results.

The structures can be sorted arrays, hash tables, or trees. “LSM-Tree” names
the organization and update strategy, not a requirement that each component be
a tree.

### Immutability changes the safety problem

```text
old components:  [C2] [C1] [C0]

new update:      [N]  [C2] [C1] [C0]
                  ▲
                  add; do not overwrite old components
```

Existing data structures are never modified. A merge creates a new component
from old components; only after the result is ready does the engine update the
set of component pointers and later remove obsolete inputs.

Thus LSM still needs an atomic method, but only for the much smaller metadata
set that says which immutable components are live. That pointer set can be
protected with copy-on-write, double buffering, a log, or another method from
this chapter.

### Multiple versions and recency

An update may produce a newer version of a key while an older version remains
in another component:

```text
newest                                            oldest
[a=0]  [f=6]  [e=5, d=4]  [c=3, a=2]
  │                               │
  └──────── query for a chooses this newer value
```

Queries must know component recency. When the same key appears in several
levels, the newest version wins. Merging eventually discards or supersedes old
versions.

### Why merge similarly sized structures?

The book's sequence can be viewed as carrying in a binary counter:

```text
one small component + another similar component
        ↓ merge
one component roughly twice as large
```

Repeated merges produce exponentially increasing component sizes:

```text
level 0: size about 1
level 1: size about 2
level 2: size about 4
level 3: size about 8
...
```

If each occupied level is progressively larger, only `O(log N)` levels are
needed to cover `N` elements. This bounds the number of component structures
that queries and maintenance must manage.

### Worked evolution

```text
operation    live structures, newest first
───────────  ──────────────────────────────────
set a=1      [a=1]
set a=2      [a=2] [a=1]
merge        [a=2]
set c=3      [c=3] [a=2]
merge        [c=3, a=2]
set d=4      [d=4] [c=3, a=2]
set e=5      [e=5] [d=4] [c=3, a=2]
merge        [e=5, d=4] [c=3, a=2]
merge        [e=5, d=4, c=3, a=2]
set f=6      [f=6] [e=5, d=4, c=3, a=2]
set a=0      [a=0] [f=6] [e=5, d=4, c=3, a=2]
merge        [a=0, f=6] [e=5, d=4, c=3, a=2]
```

The last line still contains old `a=2` in the older structure. A lookup for
`a` returns `0` because `[a=0, f=6]` is newer.

---

## 9. The Continuing Role of the Log

Moving data into disk structures solves unbounded log growth:

```text
updates
   ↓
append-only log ──mirrored by──► in-memory sorted structure
   │                                  (MemTable)
   └──────── flush/merge ─────────► disk structures
                                      (later: SSTables)
```

When the log reaches a chosen limit:

1. its in-memory mirror is persisted into disk structures;
2. the old log can be reset;
3. the in-memory mirror can be reset too.

The in-memory mirror is commonly called a **MemTable**. The log is a durable
history optimized for appends; the MemTable is the queryable, ordered view of
those same recent updates. Keeping both bounds recovery-log size and RAM use
once flushing is added.

### Why not update disk structures directly?

The log remains valuable because an append normally needs one `fsync`:

```text
write log record → fsync → acknowledge
```

Many direct atomic-update methods need at least two:

```text
write new/repair data → fsync → switch or overwrite → fsync
```

Persistence barriers are expensive latency points. A log can also batch many
updates behind one sync, pushing the average sync cost per update toward one
or even below one shared barrier per individual update. The disk structure is
updated later in larger, more efficient batches.

The log therefore has two jobs in the emerging design:

- make recent updates durable quickly;
- buffer and batch changes before building on-disk structures.

It is not itself used as the primary query structure; that is why it is
mirrored in the MemTable.

---

## 10. Comparison of Atomic Update Methods

| Method | What it preserves or records | Update granularity | How recovery chooses/repairs | Minimum sync cost stated in chapter | Main limitation |
|---|---|---|---|---:|---|
| Log + checksum | Old valid log prefix plus new record | Append-only records | Ignore incomplete checksummed tail | 1 | A log-specific technique, not a general mutable structure |
| Copy-on-write | Old object while new object is built | Whole small object, or changed tree path | Follow atomically switched file name/root | At least 2 | Needs an atomic root-pointer mechanism; full copies can be costly |
| Double buffering | Two complete copies | Whole small object | Highest-version slot with valid checksum | 1 | Full update; best for small state |
| Double write | Old or new physical bytes in repair record | Incremental byte ranges | Undo old bytes or redo new bytes | 2 | Extra writes and recovery machinery |
| LSM | Immutable component structures | New component plus later merges | Search newest-to-oldest and atomically maintain component set | Not a concrete standalone solution | Queries/merges must reconcile multiple components and versions |

### Choosing by structure

```text
small metadata or root pointer
    └─ double buffering is a natural fit

replaceable whole file
    └─ copy-on-write + rename

tree with a root
    └─ copy changed path + atomically switch root

arbitrary in-place structure
    └─ double write / physical logging

mergeable immutable structures
    └─ LSM organization + atomic component-set update
```

These are not always mutually exclusive. A storage engine can use a log for
recent writes, immutable SSTables for durable bulk data, copy-on-write during
merge, and double buffering for the metadata that names the current files.

---

## 11. Crash-Safety Reasoning Checklist

For any proposed disk update, walk through these questions in order:

### A. What is the last known-good state?

It might be:

- the valid log prefix;
- the old CoW file;
- the older checksummed slot;
- the main structure plus an undo record;
- an older set of immutable LSM components.

### B. What can a torn write corrupt?

Assume power can fail during every individual write. Do not assume that
`Write()` or a multi-page `fsync` makes the application-level operation
indivisible.

### C. How is incomplete data detected?

Typical answers in this chapter are checksums and validated versions.

### D. What persistence ordering is required?

Examples:

```text
CoW:          sync new data before switching pointer
double write: sync repair record before overwriting main data
rename:       sync file contents, rename, then sync directory metadata
```

### E. What is the commit or selection point?

Examples:

- a valid new log record;
- atomic rename;
- new root pointer;
- highest valid double-buffer version;
- durable redo record.

### F. What if cleanup is interrupted?

Deleting old files or components should be post-commit reclamation. A cleanup
crash may leave garbage, but must not make the chosen state unreachable.

### G. When may the database acknowledge success?

Only when the state promised to the client can be recovered after power loss.
For redo physical logging, that can be after the durable redo record even if
installation into the final location happens later.

---

## 12. Common Misunderstandings

### “`fsync` makes an update atomic.”

No. It makes earlier writes durable. A protocol is still needed to ensure a
multi-write update resolves to one complete state.

### “Atomic `rename()` means I can rename an unsynced new file safely.”

No. Rename protects the name switch. The new contents must be synced first,
and the directory must be synced to make its metadata change durable.

### “Copy-on-write always copies the whole database.”

No. A flat array may require a full copy, but a tree can copy only the changed
leaf-to-root path and share untouched subtrees.

### “Double buffering needs a separate current-slot pointer.”

Not necessarily. The valid checksum determines whether a slot is usable, and
the monotonic version determines which usable slot is current.

### “Double write and double buffering are the same.”

No. Double buffering alternates two complete versions. Double write first
persists repair bytes, then writes those bytes at the main structure's final
location.

### “LSM is one specific tree data structure.”

No. It is a set-and-merge organization. Its components may themselves be
arrays, hash tables, or trees.

### “An LSM has no atomic-update problem because its components are immutable.”

Immutability avoids in-place component corruption, but the live set of
component pointers still needs an atomic update method.

### “Once disk structures exist, the log is redundant.”

No. The log gives low-latency durability and batches small updates before they
are flushed or merged into disk structures.

---

## 13. How This Chapter Connects the Book

```text
0103  append-only log and replay
0104  fsync and directory durability
0105  checksum detection of torn log records
  │
  ├── establishes: valid log prefix + durable append
  ▼
0600  general atomic-update strategies
  │
  ├── CoW and atomic replacement
  ├── double-buffered root metadata
  ├── physical undo/redo writes
  └── immutable mergeable structures
  ▼
0601  build an SSTable from the sorted in-memory array
  │
  ▼
later  atomic store, bounded log/MemTable, LSM levels and merging
```

Chapter 0600 is the design vocabulary for everything that follows. Chapter
0601 will persist the current sorted array as an SSTable. Later chapters can
replace and merge those files safely because this chapter has already defined
how to reason about old versions, new versions, pointer switches, sync order,
and recovery.

---

## 14. Compact Mental Model

Remember the chapter with four verbs:

```text
PRESERVE → PREPARE → PERSIST → PUBLISH
```

1. **Preserve** a valid old state or enough bytes to repair it.
2. **Prepare** the complete new state without relying on damaged data.
3. **Persist** the data needed for recovery in the correct order.
4. **Publish** the new state through one atomic selection mechanism.

Then reclaim the old state only after publication is safe.

Mapped to the chapter:

| Method | Preserve | Prepare/persist | Publish or recover |
|---|---|---|---|
| Log | Valid prefix | Append and sync checksummed record | Valid record extends prefix |
| CoW | Old file/tree nodes | Build and sync new version | Rename/switch root |
| Double buffer | Other valid slot | Write and sync inactive slot | Highest valid version wins |
| Undo double write | Old bytes in repair record | Sync record, then overwrite | Restore old bytes after failure |
| Redo double write | New bytes in repair record | Sync record, then install | Reapply new bytes after failure |
| LSM | Old immutable components | Build new component/merge output | Atomically update live pointer set |

---

## 15. Review Questions

### 1. Why is the current database still an in-memory database if it has a disk log?

The log provides durability and replay, but queries use the sorted array in
RAM. The complete queryable state must be rebuilt into memory, so database size
is still bounded by memory.

### 2. Why is a flat on-disk sorted array dangerous to update in place?

Insertions and deletions shift many elements. A crash during those overwrites
can destroy the old layout before the new layout is complete.

### 3. What is the central idea shared by logs and CoW?

Write new data without destroying the last valid old state, then use an atomic
or recoverable rule to select the new state.

### 4. Why must CoW sync the new file before rename?

The rename can safely publish only data that will survive a crash. Otherwise
the active name may point to a new file whose contents were never durable.

### 5. Why can a file name be considered a root pointer?

It is the small piece of metadata that identifies which complete data version
is active. Replacing that name redirects the database from the old version to
the new one.

### 6. How does double buffering select a version without trusting a pointer?

It ignores checksum-invalid slots and chooses the valid slot with the greatest
monotonic version number.

### 7. Why must the double-write repair record be synced first?

The main overwrite may corrupt the only installed copy. Recovery information
must already be durable before that destructive write begins.

### 8. What is the difference between logical and physical logging?

Logical logging records database operations such as setting a key. Physical
logging records offsets and raw byte images, so it can repair a structure that
is not safe to parse.

### 9. Why can redo double write acknowledge after its first sync?

The durable record contains the complete new bytes. Recovery can finish
installing them even if the main data write has not happened or tears later.

### 10. Why does an LSM query sometimes inspect multiple structures?

Updates add new immutable structures instead of modifying old ones. Different
versions of a key can coexist until merging, so the query must reconcile them
and choose the newest.

### 11. Why merge structures of similar size?

Their sizes then grow roughly exponentially by level, keeping the number of
live size classes or levels at `O(log N)`.

### 12. Why keep both a log and a MemTable?

The log is the fast durable append representation; the MemTable is the
queryable in-memory representation of the same recent updates.

### 13. Why keep the log after adding SSTables?

One append and sync can make an update durable immediately. Many updates can
later be flushed or merged into disk structures in a batch, avoiding multiple
expensive direct syncs per small operation.

---

## Crucial Takeaways

- Atomicity means recovery sees the complete old state or complete new state,
  never an invalid mixture.
- Durability barriers are necessary, but correct ordering and a recovery
  protocol create atomicity.
- The core invariant is to preserve a recoverable state until the replacement
  is complete and durable.
- Copy-on-write publishes a separate version through an atomic file-name or
  root-pointer switch.
- Double buffering uses two checksummed, versioned copies and needs no separate
  trusted pointer.
- Double write protects arbitrary in-place changes with physical undo or redo
  information.
- LSM designs avoid modifying component structures; they add immutable
  components, merge them, and atomically maintain the live component set.
- A log remains useful after disk structures arrive because it turns small
  writes into fast durable appends and enables batching.
- The next storage design combines these ideas: log + MemTable for recent
  updates, SSTables for disk-resident state, and atomic replacement/metadata
  protocols for flushes and merges.
