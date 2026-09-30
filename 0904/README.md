# Multithreading and channels in a database engine

Chapter 0904 uses background compaction to introduce concurrency. The ideas are
broader than this chapter: whenever a database accepts writes while doing
maintenance, several activities must share resources without corrupting state
or waiting forever. This guide explains the concepts, using our WAL, MemTable,
and SSTables as examples.

“Thread” in the book often means a **goroutine** in Go. The Go runtime schedules
goroutines onto operating-system threads. Whether they run on different cores
or take turns on one core, their operations can overlap in unpredictable ways.

## The database situation

A write follows this path:

```text
client write → transaction commit → durable WAL → current MemTable
```

The WAL lets the database recover committed writes after a crash. The MemTable
lets it find them quickly. Eventually the MemTable is written to an SSTable,
and older SSTables are merged. This maintenance is **compaction**.

If every client write also performed all necessary compaction, a slow merge
could make that request very slow. A background worker separates the two jobs:

```text
client goroutine:  commit write ── notify worker ── return
worker goroutine:                    wake ── inspect state ── compact if needed
```

Background does not mean unimportant. Without compaction, the WAL and SSTable
collection can grow, and reads may inspect more files. The challenge is to let
the worker overlap with transactions safely.

Martin Kleppmann describes this storage pattern in *Designing Data-Intensive
Applications* (DDIA), Chapter 3, printed pp. 71–76. Old, immutable segments
can keep serving readers while a new merged segment is built. The database
then publishes the replacement. The distinction between **building** a new
version and **making it visible** is central to concurrent database work.

## What “concurrent” means here

Imagine a transaction committing while compaction reads the MemTable. We
cannot assume a neat execution order. If compaction truncates a WAL entry it
did not include in the SSTable, recovery may lose a write. If a transaction
sees a partially changed SSTable list, it may read an incoherent version.

Two separate questions arise:

| Question | Database example | Coordination |
|---|---|---|
| Can two operations use shared state at once? | Commit and compaction both use the WAL. | Mutex or ownership rule |
| When should another activity run? | A commit makes maintenance worth checking. | Channel notification |

A channel does not automatically protect the MemTable. A mutex does not
automatically wake a sleeping worker. We use both because they answer
different questions.

### Data races and logical mistakes

A **data race** occurs when goroutines access the same memory concurrently,
at least one access writes it, and nothing synchronizes them. For example,
one goroutine could replace the `main` SSTable slice while another reads its
length. Go's race detector helps find data races that a test actually runs.

There is also a higher-level problem: individual memory accesses might be
synchronized, yet the overall storage operation may still be wrong. Suppose
compaction reads a MemTable snapshot, a new write commits, and compaction
truncates the WAL as though its snapshot included that write. The issue is
the **ordering of actions**, including durability and publication. Reason
about invariants such as: every committed WAL entry is either still in the
WAL or represented by a published SSTable.

## A channel communicates an event

A Go channel lets one goroutine send a value and another receive it. If no
value is ready, the receiver waits without repeatedly checking in a busy
loop. This lets a worker sleep until there may be work.

```go
updated := make(chan struct{}, 2)
updated <- struct{}{} // commit: "check whether compaction is needed"
<-updated             // worker wakes
```

`struct{}` has no payload. The sender communicates an **event**, not the
committed rows. The worker reads current storage state and decides what to
do. If ten commits occur before it runs, it may observe all ten changes in
one check. One notification does not mean one SSTable.

The durable record of a write is the WAL, not the channel value. A Go channel
lives in process memory and disappears on crash. After restart, the database
replays the WAL; it does not replay channel notifications. Losing a maintenance
hint may delay compaction, but must not lose committed user data.

This is a general distinction: a message can mean “perform this specific
job” or “something changed; inspect the source of truth.” Our notification
means the latter.

### Sending, receiving, and capacity

| Channel state | Send | Receive |
|---|---|---|
| Unbuffered, no partner ready | Waits | Waits |
| Buffered, room available | Enqueues and continues | Takes a value if present |
| Buffered, full | Waits for room | Takes a value and frees room |
| Buffered, empty | Enqueues if sent | Waits for a value |
| Closed and drained | Panics | Returns zero value with `ok == false` |
| Nil | Waits forever | Waits forever |

With an **unbuffered** channel, sender and receiver meet at the same time.
With a **buffered** channel, the sender can get ahead by at most the buffer
capacity. Receiving frees a slot; it does **not** mean the worker finished
compaction. A closed buffered channel yields its remaining values before
receives return `ok == false`. Closing it twice panics.

## Backpressure: when writes outrun maintenance

Suppose commits produce signals faster than the worker receives them. A
finite buffer eventually fills. The next send waits, delaying that commit
call. This is **backpressure**: slow downstream work pushes waiting time
toward producers.

DDIA discusses the same choice for event streams in Chapter 11, printed
p. 427: buffer messages, drop them, or block producers. A bounded channel
buffers a burst and then blocks senders.

| Choice | Benefit | Cost |
|---|---|---|
| Larger buffer | More bursts finish without waiting. | More queued signals before pressure reaches writers. |
| Smaller buffer | Pressure reaches writers sooner. | More commits wait during ordinary bursts. |
| Drop or coalesce signals | Avoids a growing notification queue. | Requires another reliable way to notice outstanding maintenance. |

In the reference engine, the buffer capacity is `LogShreshold` (the book's
spelling), and each successful top-level commit sends a signal, even if it
was read-only. Buffer occupancy is **not** the number of MemTable keys or a
hard upper bound on WAL size. Repeated updates to one key can make many
signals, and the worker removes a signal before compaction finishes.

Backpressure is observable as write latency. The key questions are whether
waiting preserves progress and whether the chosen limit matches the resource
being protected.

## How a channel and a lock can deadlock

A send may block; acquiring a mutex may also block. These waits can form a
cycle:

```text
commit holds WAL lock
    ↓ waits to send because signal buffer is full
worker needs to receive more signals to free space
    ↓ waits for WAL lock to compact
commit still holds WAL lock
```

Neither side can do what the other needs. That is a **deadlock**. The general
method is to trace the entire wait chain whenever a goroutine can block
while holding a resource another goroutine needs.

In 0904, a commit finishes its protected WAL and publication work before
sending the signal. The send can still wait on a full buffer, but it no
longer holds the WAL lock needed by the worker. That keeps backpressure
without this particular deadlock.

Healthy backpressure means the worker can eventually run and free a slot.
Deadlock means the worker cannot run because the waiting sender holds a
resource it needs.

## `select`: respond to work or shutdown

A worker must respond to “new work” and “the database is closing.” Go's
`select` waits for a channel operation that can proceed:

```go
select {
case <-updated:
    // Examine current storage state.
case <-closing:
    // Stop waiting for new work.
}
```

Closing `closing` makes every receive from it ready, so it broadcasts a
shutdown signal. If both cases are ready, `select` chooses one ready case;
source order gives no priority. Shutdown therefore does not promise that
every queued notification is processed. Closing a channel also does not
interrupt compaction already in progress.

The commit side can likewise select between sending a notification and
observing shutdown. A commit then need not wait forever on a full channel
after the worker has been told to stop. The signal channel stays open:
closing it while goroutines may send would make those sends panic.

## Telling work to stop versus waiting for it

These are separate events:

```text
announce shutdown → worker notices → current action finishes → worker exits
```

The database cannot close files at the first arrow if the worker or a
transaction still uses them. A `sync.WaitGroup` counts active work:
starting a tracked activity increments the count, finishing decrements it,
and `Wait()` blocks until it reaches zero. The sample tracks the worker and
top-level transaction lifetimes.

```text
RUNNING → STOPPING → CLOSED
            │           ▲
            └─ wait for admitted work to finish
```

A complete lifecycle also stops **admitting** new work when shutdown begins.
The 0904 sample does not enforce that boundary: `NewTX` can add work while
`Close` waits. Its `Close()` also cannot safely be called twice because
closing the same channel twice panics. A transaction that never commits or
aborts can make shutdown wait forever. These are lifecycle limits of the
sample, not general properties of channels.

## How this relates to DDIA's message passing

DDIA Chapter 4, printed pp. 132–134, describes message passing between
processes through brokers. A sender and receiver can run at different
speeds, with a queue between them. The differences matter:

| Go channel in this engine | Message broker in DDIA |
|---|---|
| Connects goroutines in one process. | Typically connects processes or nodes. |
| Values vanish when the process exits. | May store and redeliver messages, depending on design. |
| A receive takes a queued value. | Delivery and acknowledgement rules vary. |
| Carries a maintenance hint. | May carry durable events or commands. |

DDIA also describes actors that own state and process messages one at a
time. This engine is not entirely organized as actors: transactions and
compaction share storage structures. They still need mutexes and careful
publication rules. Sending on a channel does not automatically make shared
memory safe.

## Walk through one timeline

Assume the MemTable is nearing its flush threshold:

1. Transaction A commits. Its WAL record is durable and its new MemTable
   version becomes visible. A sends a maintenance signal.
2. The worker receives the signal, sees enough data to flush, and begins
   writing a new SSTable. Old storage remains available while it builds.
3. Transaction B starts during the file write. It must capture one coherent
   committed view. It may see the old MemTable and SSTable list.
4. The worker publishes the new SSTable as a coordinated state change.
   Future transactions can see it; B keeps its earlier snapshot.
5. A later signal may cause no compaction at all. Signals prompt a state
   check rather than dictate a particular merge.

At each step, ask: **What is durable? What is visible to new transactions?
What could block, and what would unblock it?** These questions reveal most
of the important concurrency issues.

## The limits of the reference implementation

The example illustrates a pattern; it does not prove every concurrent call
is safe. A caller can invoke `Compact()` directly, and reads of shared
`kv.mem` and `kv.main` need a full synchronization review under arbitrary
concurrent use. The previous chapter's conflict-history read can also race
with abort cleanup; see the [0903 notes](../0903/README.md).

The reusable reasoning method is:

1. Identify shared state and the invariant it must preserve.
2. Decide which operations need exclusive access and which only need a signal.
3. Draw the wait chain for every send, receive, lock, and shutdown wait.
4. Separate durability of user data from delivery of background hints.
5. Define when new work stops entering and when existing work has ended.

## Questions and answers

### Why can two goroutines interfere even on a single CPU core?

Only one goroutine executes on that core at an instant, but the scheduler can
pause it between steps and run another. Suppose a transaction reads the old
SSTable list, then compaction replaces the list before the transaction uses
what it read. The operations overlap in *time*, even if their instructions
never run simultaneously. A second core permits true simultaneous execution,
but is not required for an unsafe interleaving.

### Why is the notification channel unnecessary for WAL recovery?

The notification contains no user data; it only says “check whether
compaction is needed.” Committed writes are recorded in the WAL. After a
crash, the in-memory channel and MemTable disappear, but opening the database
replays the WAL to rebuild recent state. If a notification is lost, a flush
or merge may be delayed. It must not be needed to reconstruct a committed
write. Once a flush is safely represented by an SSTable, the corresponding
WAL records can be retired through the storage engine's normal procedure.

### What is the difference between “received a signal” and “finished compaction”?

Receiving removes one value from the channel and wakes the worker. The
worker has not yet inspected the MemTable, written an SSTable, merged files,
or published a new view. It may discover that no work is needed, encounter
an error, or still be busy after the sender continues. Thus a free channel
slot means **the signal was taken**, not **the storage work is complete**.

### When does a full channel create backpressure, and when can it help form a deadlock?

With a full buffered channel, another send waits until the worker receives
a value. That is backpressure if the worker can keep running: it eventually
frees a slot, and the sender continues. A deadlock can occur if the sender
waits **while holding a lock the worker needs**. For example, a commit holds
the WAL lock and waits to send, while the worker is trying to compact and
waits for that same lock. Each needs the other to move first. Sending after
releasing the WAL lock breaks this particular cycle.

### Why can a channel wake the worker without protecting the SSTable list?

Receiving a notification synchronizes communication of that value, but it
does not give the worker exclusive ownership of every `KV` field. Other
goroutines may still read or publish the SSTable list. The worker needs a
separate rule, such as a mutex around publication and snapshot capture, so
readers see one coherent list. A wake-up answers **when to check**; a lock
answers **who may access or change shared state at the same time**.

### Why must shutdown stop admitting work before waiting for active work to end?

`Wait()` can safely tell us that all *already admitted* work has finished
only if no new transaction can enter as the count approaches zero. Otherwise
`Close()` may observe no active work and close files while a new transaction
starts using them. A sound lifecycle first marks the database as stopping
and rejects new starts, then waits for admitted transactions and the worker,
then closes storage. The 0904 sample's `NewTX` does not enforce this admission
boundary, so its shutdown sequence is not a complete concurrent-close design.

### Which message-broker guarantees should you avoid assuming about a Go channel?

Do not assume persistence across process crashes, delivery to another
process, acknowledgement after work completes, automatic retry or redelivery,
or exactly-once processing. A receive only takes a value from an in-memory
channel. Real brokers differ in which guarantees they provide, too; those
properties require explicit storage and delivery protocols. In this engine,
the WAL provides durable user data and the channel provides a temporary
maintenance hint.
