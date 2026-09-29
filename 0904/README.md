# Chapter 0904: Multithread & Channels

Chapter 0903 used mutexes to protect transaction state and to order commits. Chapter
0904 adds **automatic background compaction**. A successful commit tells a worker
goroutine that there may be storage maintenance to do. The worker sleeps when there
is no signal, runs `KV.Compact()` when signaled, and exits during shutdown.

The central problem is coordinating three activities that can overlap:

```text
transaction goroutines: commit new data to the WAL and MemTable
compaction goroutine:   turn the MemTable into an SSTable, merge SSTables
closing goroutine:      stop new work and wait before closing files
```

This chapter is about *in-process concurrency*. A Go channel here is an in-memory
communication mechanism between goroutines. DDIA also discusses message queues
between processes; the comparison is useful, but the durability and delivery
guarantees are different.

## Reading map

| Source | Where | Why it matters here |
|---|---|---|
| *Build Your Own Database From Scratch in Go* | 0904, printed pp. 99–103 | Auto-compaction, channels, `select`, shutdown, and `WaitGroup` |
| Martin Kleppmann, *Designing Data-Intensive Applications* (DDIA) | Ch. 3, “Data Structures That Power Your Database,” printed pp. 71–76 | Immutable segments, MemTable flush, SSTable merge, background compaction, WAL |
| DDIA | Ch. 4, “Message passing data flow,” pp. 132–134 | Why a queue separates a sender from a receiver; distinction between messages and calls |
| DDIA | Ch. 11, “Transmitting Event Streams,” p. 427 | Buffering versus backpressure when producers outrun a consumer |

Local source files: [0904 reference `kv.go`](../db_solution/0904/kv.go),
[0904 reference tests](../db_solution/0904/kv_test.go), and the
[0903 notes](../0903/README.md). The book PDFs are
[`db_in_45_steps_go.pdf`](/home/morgankim/Documents/ebooks/db_in_45_steps_go.pdf)
and [DDIA](/home/morgankim/Documents/ebooks/Martin-Kleppmann---Designing-Data-Intensive-Applications_-O’Reilly-Media-(2017).pdf).

## Why compaction belongs in a worker

The engine accepts writes into a MemTable and records them in a write-ahead log
(WAL). The MemTable cannot grow forever. Once it is large enough, `compactLog`
writes its sorted contents to an SSTable and truncates the now-obsolete log. Older
SSTables must occasionally be merged so reads do not have to search an ever-growing
list of files.

DDIA's LSM-tree description follows the same cycle: append a durable log entry,
update an in-memory sorted structure, flush that structure to an immutable SSTable,
and merge SSTables in the background. Readers can keep using old immutable files
while a new merged file is built; the engine then switches to the new file. Chapter
0904 turns the earlier explicit `KV.Compact()` call into work triggered after commits.

```text
commit:     WAL commit → publish MemTable → signal updated → return
worker:                                             wake → Compact()
                                                      ├─ flush MemTable if large
                                                      └─ merge an eligible SSTable pair
```

The signal does **not** contain a key, value, or snapshot. Compaction inspects the
current database state when it runs. The WAL already holds the durable updates;
the channel is only a request to check whether maintenance is due. If a signal is
discarded during shutdown, that does not discard the committed data: `Open()` can
recover it from the WAL. It may leave compaction work for a later session.

## Goroutines, threads, locks, and channels

The book often says “thread.” The code starts a Go **goroutine** with `go func()`;
the Go runtime schedules goroutines onto operating-system threads. You should
reason about possible interleavings, regardless of how many cores execute them.

The two synchronization tools have different jobs:

| Tool | What it coordinates in 0904 |
|---|---|
| `kv.commit` mutex | Gives WAL commits and log compaction an exclusive order |
| `kv.mu` mutex | Protects short in-memory publication of the MemTable, SSTable list, and transaction bookkeeping |
| `kv.updated` channel | Sends a wake-up signal from commits to the compaction worker |
| `kv.closing` channel | Broadcasts that the database is closing |
| `kv.threads` `WaitGroup` | Waits for tracked transactions and the worker to finish |

A channel send does not grant exclusive access to the database. `Compact()` still
needs appropriate locks around shared state. Conversely, mutexes alone do not tell
a sleeping worker *when* to run.

## A channel, one operation at a time

Create a channel with `make(chan T, capacity)`. Send with `c <- value`; receive
with `value := <-c`.

```go
work := make(chan string, 2)
work <- "A"
work <- "B"
item := <-work // "A"; the receive frees one buffer slot
```

A buffered channel holds at most `capacity` pending values. A send to a full
buffer waits until a receiver removes a value. A receive from an empty channel
waits until a sender provides one. Waiting parks the goroutine; it does not require
a CPU-burning polling loop. With capacity zero, a send and receive rendezvous:
both sides must be ready at the same time.

For a signal with no payload, `chan struct{}` expresses the intent:

```go
updated := make(chan struct{}, 1000)
updated <- struct{}{} // "check whether compaction is needed"
<-updated             // wake and check
```

`struct{}{}` carries no data. The buffer still contains **one slot per signal**.
The reference implementation makes its capacity `LogShreshold` (the book and code
spell it this way). That setting limits pending notifications; it is not a byte
limit on the WAL or a guarantee that the MemTable has exactly that many entries.
For example, repeated commits to one key may enqueue many signals while leaving
only one current key in the MemTable.

### Closing and nil are different states

Receiving from a closed, drained channel returns the element type's zero value
and `ok == false`:

```go
v, ok := <-work
if !ok {
    // No more values will arrive.
}
```

Buffered values are received first; `ok` becomes false after the buffer drains.
Sending to a closed channel panics. Closing a channel a second time also panics.
The party that controls the end of sending normally closes it.

A channel's zero value is `nil`. Both send and receive on a nil channel block
forever. There is no hidden queue to wake them later. In this chapter, `Open()`
initializes `closing`, and `startCompactThread()` initializes `updated` when
`AutoCompact` is enabled.

## Follow one successful write

The 0904 solution splits the commit into `applyTXSync` and `applyTX`:

```go
func (kv *KV) applyTX(tx *KVTX) error {
    if err := kv.applyTXSync(tx); err != nil {
        return err
    }
    if kv.Options.AutoCompact {
        select {
        case kv.updated <- struct{}{}:
        case <-kv.closing:
        }
    }
    return nil
}
```

`applyTXSync` takes `kv.commit`, validates the transaction, writes the WAL, and
publishes the new MemTable while holding `kv.mu`. It returns **after releasing
the locks**. Only then does `applyTX` try to send a notification. The worker
receives a notification and calls `KV.Compact()`; `Compact()` checks current
thresholds rather than assuming that every signal requires a flush.

An important detail: the shown `applyTX` signals after any successful top-level
`applyTXSync`, including a read-only transaction whose update set was empty.
That can cause an extra no-op check. A failed or conflicting commit returns
without signaling.

### Why the send must happen after unlocking

Imagine putting a blocking channel send inside the `kv.commit` critical section.
Suppose the channel buffer is full:

```text
writer: holds kv.commit → tries to send → waits for buffer space
worker: received earlier signal → enters Compact → waits for kv.commit
```

The worker must finish work to keep draining the queue, but it cannot progress
past the commit lock held by the writer. The writer cannot release that lock
because its send is waiting for the worker. This is a **deadlock**: each side
waits on something the other side controls. Moving the send after `applyTXSync`
returns removes this particular cycle.

The send may still block if compaction falls behind. That is **backpressure**:
the rate of successful commit calls is constrained by the worker's ability to
receive signals. DDIA describes the same broad choice for event streams: drop
messages, buffer them, or make producers wait when the buffer is full. Here the
bounded channel buffers some signals and then makes commit callers wait. This
protects against an unlimited in-memory notification queue, but it can increase
write latency. It does not impose an exact upper bound on WAL growth because
buffer occupancy is not the same as log size, and a worker removes a signal
*before* it completes the related compaction check.

## `select`: wait for work or shutdown

`select` lets one goroutine wait on several channel operations:

```go
select {
case kv.updated <- struct{}{}:
    // A notification was queued or received.
case <-kv.closing:
    // Shutdown has begun; do not wait forever to send.
}
```

If no case can proceed, `select` waits. If several cases are ready, Go chooses
one of the ready cases; source order does not give priority. Thus, once `closing`
is closed, a send can still win if `updated` also has room. Correctness cannot
depend on every pending signal being processed during shutdown.

The worker uses the same two channels:

```go
for {
    ok := false
    select {
    case _, ok = <-kv.updated:
    case <-kv.closing:
    }
    if !ok {
        break
    }
    if err := kv.Compact(); err != nil {
        log.Println("KV.Compact():", err)
    }
}
```

`updated` is never closed in this design, so receiving one of its signals sets
`ok` to true. The `closing` receive leaves `ok` false and breaks the loop.
Closing `closing` wakes *all* receivers waiting on it: the worker and commit
callers blocked in their `select`. Unlike closing `updated`, this does not make
senders panic, because nobody sends on `closing`.

The worker may already be inside `Compact()` when shutdown begins. Closing a
channel does not interrupt that function; it exits when it gets back to the
`select` loop. Pending `updated` signals need not be drained before exit.

## Shutdown: notification and waiting are separate

The solution tracks the worker and top-level transactions with a `sync.WaitGroup`:

```go
// NewTX, after registering the transaction:
kv.threads.Add(1)

// On top-level commit or abort, after untracking:
kv.threads.Add(-1)

// Before starting the worker:
kv.threads.Add(1)
go func() {
    defer kv.threads.Add(-1)
    // receive signals and compact
}()

func (kv *KV) Close() error {
    close(kv.closing)
    kv.threads.Wait()
    return kv.MultiClosers.Close()
}
```

Think of the counter as the number of tracked activities that have not finished.
`Wait()` blocks until it reaches zero. Thus the database files are closed only
after the registered transactions and the worker have finished. The `closing`
channel tells them to stop waiting for new work; the `WaitGroup` tells `Close`
when they actually stopped. A channel close alone would not provide that wait.

Example timeline:

```text
worker:    receive signal ── Compact() ── see closing ── Done
transaction:              finish commit ── select sees closing ── Done
Close:              close(closing) ─────────────── Wait ── close files
```

`Close()` can wait indefinitely if a caller begins a transaction and never
commits or aborts it. This is a consequence of counting transaction lifetimes,
not a channel malfunction.

## What the locks protect during compaction

The chapter revises the older compaction methods because they now overlap with
transaction calls:

1. `compactLog` takes `kv.commit` while it makes an SSTable from the MemTable,
   updates metadata, and truncates the WAL. A concurrent transaction commit
   cannot append to that WAL halfway through the flush. It briefly takes `kv.mu`
   to publish the new SSTable and clear the current MemTable.
2. `compactSSTable` builds a merged file, then briefly takes `kv.mu` to replace
   two entries in `kv.main` with that file.
3. `applyTXSync` also takes `kv.commit` for WAL commit order and `kv.mu` for
   publication. The intended lock order when both are needed is
   `kv.commit` → `kv.mu`.

The long file work does not need to hold `kv.mu` in the intended design. That
allows a transaction to capture a coherent snapshot while disk work continues.
As DDIA explains, immutable old segments can remain useful to readers while a
replacement is built. The moment the new SSTable list becomes visible needs
coordination; the entire build need not freeze readers.

## Where DDIA's message passing analogy ends

DDIA's message brokers can buffer messages between separate processes and may
offer persistence or redelivery, depending on the broker. A Go channel here has
none of those properties. Its values live only in this process, and the worker
does not acknowledge a notification after a durable action. The notification
is best read as **“check the current state”**, not **“execute this specific job
exactly once.”** The durable source of truth is the WAL and storage metadata.

Likewise, DDIA's actor model processes messages through actor-owned state. This
engine does not make all state private to the compaction worker: transactions and
compaction still share `KV` fields and use mutexes. Channels and locks are
complementary here.

## Limits of the chapter's reference code

These are useful boundaries when you later add more concurrency:

- `Close()` does not prevent a new `NewTX()` after it starts. A concurrent positive
  `WaitGroup.Add` while `Wait` is shutting down can violate the required lifecycle
  ordering. A production design needs an admission state protected by a mutex:
  reject new transactions once closing begins, then wait for those already
  admitted. Repeated `Close()` also panics because it closes `closing` twice.
- The worker may choose `closing` while `updated` still contains signals, so
  `Close()` does not promise to finish every queued compaction request. That is
  separate from waiting for a compaction already in progress.
- A caller can invoke `Compact()` directly. The background worker is not an
  exclusive gate for all compaction calls. Reads of `kv.mem` and `kv.main` in
  `Compact()` and `compactSSTable()` deserve a full lock review before claiming
  arbitrary concurrent manual compaction is race-free.
- As noted in [0903](../0903/README.md), the conflict check scans `kv.history`
  without `kv.mu` while abort cleanup may prune it. Adding channels does not
  repair that pre-existing race.

These limits do not obscure the main lesson: first identify the owner and
lifetime of each shared resource, then check every interleaving where a
goroutine can block, publish state, or stop.

## Review questions

1. Why is a `chan struct{}` enough to request compaction?
2. What is the difference between a goroutine waiting on an empty channel and
   repeatedly checking a boolean in a loop?
3. In the deadlock example, which side holds `kv.commit`, and what does each
   side need before it can continue?
4. What does a full `updated` buffer do to commit latency? Why is that called
   backpressure?
5. Why does the worker check `kv.mem.Size()` rather than assume one signal
   means one new SSTable?
6. Why is `closing` closed but `updated` left open?
7. Why does `Close()` need both `close(kv.closing)` and `kv.threads.Wait()`?
8. If both `select` cases are ready, which wins? What does that mean for
   queued compaction work?
9. Which data is durable after a committed transaction: the channel signal,
   the WAL entry, or both?
10. What extra state would stop new transactions from entering while `Close()`
    waits for the old ones?

## Final mental model

```text
             sync.Mutex                    chan struct{}
transactions ───────► committed state ───────► background worker
     │                    │                        │
     │                    │ WAL is durable         │ Compact reads current state
     │                    └────────────────────────┘
     │
     └── tracked by WaitGroup ◄── worker also tracked
                    ▲
Close: close(closing) → Wait() → close storage files
```

Use the mutexes to make shared state coherent, the `updated` channel to wake
the worker, the `closing` channel to announce shutdown, and the `WaitGroup` to
know when registered work has actually ended.
