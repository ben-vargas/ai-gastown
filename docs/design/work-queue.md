# Work Queue Architecture

> Capacity-controlled polecat dispatch for batched work.

## Quick Start

Enable the queue and dispatch some work:

```bash
# 1. Enable daemon auto-dispatch
gt config set queue.enabled true

# 2. Queue beads for deferred dispatch
gt sling gt-abc gastown --queue              # Single bead
gt sling gt-abc gt-def gt-ghi gastown --queue  # Batch

# 3. Check what's queued
gt queue status
gt queue list

# 4. Dispatch manually (or let the daemon do it)
gt queue run
gt queue run --dry-run    # Preview first
```

### Common CLI Flags

| Command | Flags | Description |
|---------|-------|-------------|
| `gt sling <bead> <rig> --queue` | `--force`, `--merge`, `--args`, `--no-convoy`, `--formula`, `--hook-raw-bead` | Queue single bead |
| `gt convoy queue <id>` | `--dry-run`, `--force`, `--formula`, `--hook-raw-bead` | Queue all open issues in convoy (auto-resolve rigs) |
| `gt queue epic <id>` | `--dry-run`, `--force`, `--formula`, `--hook-raw-bead` | Queue all children of epic (auto-resolve rigs) |
| `gt queue run` | `--batch N`, `--max-polecats N`, `--dry-run` | Trigger dispatch manually |
| `gt queue status` | `--json` | Show queue state and capacity |
| `gt queue list` | `--json` | List all queued beads by rig |
| `gt queue pause` | | Pause all dispatch town-wide |
| `gt queue resume` | | Resume dispatch |
| `gt queue clear` | `--bead <id>` | Remove beads from queue |

### Minimal Example

```bash
gt sling gt-abc gastown --queue   # Enqueue (adds gt:queued label + metadata)
gt queue status                   # "Queued: 1 total, 1 ready"
gt queue run                      # Dispatches → spawns polecat → strips metadata
```

---

## Overview

The work queue solves **back-pressure** and **capacity control** for batched polecat dispatch.

Without the queue, slinging N beads spawns N polecats simultaneously, exhausting API rate limits, memory, and CPU. The queue introduces a governor: beads enter a waiting state and the daemon dispatches them incrementally, respecting a configurable concurrency cap.

The queue integrates into the daemon heartbeat as **step 14** — after all agent health checks, lifecycle processing, and branch pruning. This ensures the system is healthy before spawning new work.

```
Daemon heartbeat (every 3 min)
    │
    ├─ Steps 0-13: Health checks, agent recovery, cleanup
    │
    └─ Step 14: gt queue run (capacity-controlled dispatch)
         │
         ├─ flock (exclusive)
         ├─ Check paused state
         ├─ Load config (max_polecats, batch_size)
         ├─ Count active polecats (tmux)
         ├─ Query ready queued beads (bd ready --label gt:queued)
         ├─ Dispatch loop (up to min(capacity, batch, ready))
         │    └─ dispatchSingleBead → executeSling
         ├─ Wake rig agents (witness, refinery)
         └─ Save dispatch state
```

---

## Bead State Machine

A queued bead transitions through these states, tracked by labels and metadata:

```
                ┌──────────────────────────────────────────────┐
                │                                              │
                ▼                                              │
          ┌──────────┐    dispatch ok     ┌──────────────┐    │
 enqueue  │          │ ────────────────►  │              │    │
────────► │  QUEUED   │                    │  DISPATCHED  │    │
          │          │                    │              │    │
          └──────────┘                    └──────────────┘    │
                │                                              │
                ├── 3 failures ──► ┌────────────────┐          │
                │                  │ CIRCUIT-BROKEN │          │
                │                  └────────────────┘          │
                │                                              │
                ├── no metadata ─► ┌──────────────┐            │
                │                  │  QUARANTINED  │           │
                │                  └──────────────┘            │
                │                                              │
                └── gt queue clear ► ┌───────────┐             │
                                     │ UNQUEUED  │ ───────────┘
                                     └───────────┘  (re-queueable)
```

### Label Transitions

| State | Label(s) | Metadata | Trigger |
|-------|----------|----------|---------|
| **QUEUED** | `gt:queued` | Present (delimiter block) | `enqueueBead()` |
| **DISPATCHED** | `gt:queue-dispatched` | Stripped | `dispatchSingleBead()` success |
| **CIRCUIT-BROKEN** | `gt:dispatch-failed` | Retained (failure count) | `dispatch_failures >= 3` |
| **QUARANTINED** | `gt:dispatch-failed` | Missing | Missing metadata at dispatch |
| **UNQUEUED** | (label removed) | Stripped | `gt queue clear` |

Key invariant: `gt:queued` is always removed on terminal transitions. Dispatched beads get `gt:queue-dispatched` as an audit trail so reopened beads aren't mistaken for actively queued ones.

---

## Entry Points

### CLI Entry Points

| Command | Use Case | Formula |
|---------|----------|---------|
| `gt sling <bead> <rig> --queue` | Queue single bead | `mol-polecat-work` (auto, override with `--formula`) |
| `gt sling <bead>... <rig> --queue` | Queue batch | `mol-polecat-work` (auto, override with `--formula`) |
| `gt sling <formula> --on <bead> <rig> --queue` | Queue with explicit formula | User-specified |
| `gt convoy queue <convoy-id>` | Queue all open convoy issues (auto-resolve rigs from prefix) | `mol-polecat-work` (override with `--formula`) |
| `gt queue epic <epic-id>` | Queue all children of epic (auto-resolve rigs from prefix) | `mol-polecat-work` (override with `--formula`) |
| `gt queue run` | Manual dispatch trigger | N/A (dispatches) |

All enqueue paths go through `enqueueBead()` in `internal/cmd/sling_queue.go`. All dispatch goes through `dispatchQueuedWork()` in `internal/cmd/queue_dispatch.go`.

### Daemon Entry Point

The daemon calls `gt queue run` as a subprocess on each heartbeat (step 14):

```go
// internal/daemon/daemon.go:1617-1631
func (d *Daemon) dispatchQueuedWork() {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
    defer cancel()
    cmd := exec.CommandContext(ctx, "gt", "queue", "run")
    cmd.Env = append(os.Environ(), "GT_DAEMON=1", "BD_DOLT_AUTO_COMMIT=off")
    // ...
}
```

| Property | Value |
|----------|-------|
| Timeout | 5 minutes |
| Environment | `GT_DAEMON=1` (identifies daemon dispatch) |
| Gating | `queue.enabled` must be `true` for daemon dispatch |
| Manual override | `gt queue run` always works, even with `queue.enabled=false` |

---

## Enqueue Path

`enqueueBead()` performs these steps in order:

1. **Validate** bead exists, rig exists
2. **Cross-rig guard** — reject if bead prefix doesn't match target rig (unless `--force`)
3. **Idempotency** — skip if bead is already open with `gt:queued` label
4. **Status guard** — reject if bead is hooked/in_progress (unless `--force`)
5. **Validate formula** — verify formula exists (lightweight, no side effects)
6. **Cook formula** — `bd cook` to catch bad protos before daemon dispatch
7. **Build metadata** — `NewQueueMetadata(rigName)` with all sling params
8. **Strip existing metadata** — ensure idempotent re-enqueue (no duplicates)
9. **Write metadata** — `bd update --description=...` (inert without label)
10. **Add label** — `bd update --add-label=gt:queued` (atomic activation)
11. **Auto-convoy** — create convoy if not already tracked (unless `--no-convoy`)
12. **Log event** — feed event for dashboard visibility

**Metadata-before-label ordering** is critical: metadata without the label is inert (dispatch queries `bd ready --label gt:queued`, so unlabeled beads are invisible). The label is the atomic "commit." This prevents a race where dispatch fires between label-add and metadata-write, sees `meta==nil`, and irreversibly quarantines the bead.

**Rollback on failure**: if the label-add fails, the metadata write is rolled back to the original description.

---

## Dispatch Engine

`dispatchQueuedWork()` is the main dispatch loop:

```
flock(queue-dispatch.lock)
    │
    ├─ Load QueueState → check paused?
    │
    ├─ Load WorkQueueConfig (or defaults)
    │
    ├─ Check queue.enabled (daemon only; manual always proceeds)
    │
    ├─ Determine limits:
    │    maxPolecats = config (or override)
    │    batchSize   = config (or override)
    │    spawnDelay  = config
    │
    ├─ Count active polecats (tmux session scan)
    │
    ├─ Compute capacity = maxPolecats - activePolecats
    │    (0 = unlimited if maxPolecats = 0)
    │
    ├─ Query ready beads:
    │    bd ready --label gt:queued --json --limit=0
    │    (scans all rig DBs, deduplicates, skips circuit-broken)
    │
    ├─ toDispatch = min(capacity, batchSize, readyCount)
    │
    ├─ Dispatch loop:
    │    for i := 0; i < toDispatch; i++ {
    │        dispatchSingleBead(bead, townRoot, actor)
    │        sleep(spawnDelay)   // between spawns
    │    }
    │
    ├─ Wake rig agents (witness, refinery) for each rig with dispatches
    │
    └─ Save dispatch state (fresh read to avoid clobbering concurrent pause)
```

### dispatchSingleBead

Each bead dispatch:

1. **Parse metadata** from bead description
2. **Validate metadata** — quarantine immediately if missing (no circuit breaker waste)
3. **Reconstruct SlingParams** from metadata fields:
   - `FormulaName`, `Args`, `Vars`, `Merge`, `BaseBranch`, `Account`, `Agent`, etc.
   - `FormulaFailFatal=true` (rollback + requeue on failure)
   - `NoConvoy=true` (convoy already created at enqueue)
   - `NoBoot=true` (avoid lock contention in daemon dispatch loop)
   - `CallerContext="queue-dispatch"`
4. **Call `executeSling(params)`** — unified sling path (same as batch sling)
5. **On failure**: record failure in metadata, increment `dispatch_failures` counter
6. **On success**: strip queue metadata, swap `gt:queued` → `gt:queue-dispatched`
7. **Log event** — feed event with polecat name

---

## Queue Metadata Format

Queue parameters are stored in the bead's description, delimited by a namespaced marker to avoid collision with user content.

### Delimiter

```
---gt:queue:v1---
```

Everything after the delimiter until the next delimiter (or end of description) is parsed as `key: value` lines.

### Field Reference

| Field | Type | Description |
|-------|------|-------------|
| `target_rig` | string | Destination rig name |
| `formula` | string | Formula to apply at dispatch (e.g., `mol-polecat-work`) |
| `args` | string | Natural language instructions for executor |
| `var` | repeated | Formula variables, one `var: key=value` per line |
| `enqueued_at` | RFC3339 | Timestamp of enqueue |
| `merge` | string | Merge strategy: `direct`, `mr`, `local` |
| `convoy` | string | Convoy bead ID (set after auto-convoy creation) |
| `base_branch` | string | Override base branch for polecat worktree |
| `no_merge` | bool | Skip merge queue on completion |
| `account` | string | Claude Code account handle |
| `agent` | string | Agent/runtime override |
| `hook_raw_bead` | bool | Hook without default formula |
| `owned` | bool | Caller-managed convoy lifecycle |
| `mode` | string | Execution mode: `ralph` (fresh context per step) |
| `dispatch_failures` | int | Consecutive failure count (circuit breaker) |
| `last_failure` | string | Most recent dispatch error message |

### Lifecycle

1. **Write** at enqueue — `FormatQueueMetadata()` appends block to description
2. **Read** at dispatch — `ParseQueueMetadata()` extracts fields for `SlingParams`
3. **Strip** after dispatch — `StripQueueMetadata()` removes the block on success

---

## Capacity Management

### Configuration

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `queue.enabled` | bool | `false` | Enable daemon auto-dispatch (must opt in) |
| `queue.max_polecats` | *int | `10` | Max concurrent polecats across ALL rigs |
| `queue.batch_size` | *int | `3` | Beads dispatched per heartbeat tick |
| `queue.spawn_delay` | string | `"2s"` | Delay between spawns (Dolt lock contention) |

Set via `gt config set`:

```bash
gt config set queue.enabled true
gt config set queue.max_polecats 5
gt config set queue.batch_size 2
gt config set queue.spawn_delay 3s
```

### Dispatch Count Formula

```
toDispatch = min(capacity, batchSize, readyCount)

where:
  capacity   = maxPolecats - activePolecats   (0 if maxPolecats=0, meaning unlimited)
  batchSize  = queue.batch_size (default 3)
  readyCount = number of beads from bd ready --label gt:queued
```

### Active Polecat Counting

Active polecats are counted by scanning tmux sessions and matching role via `session.ParseSessionName()`. This counts **all** polecats (both queue-dispatched and directly-slung) because API rate limits, memory, and CPU are shared resources.

---

## Circuit Breaker

The circuit breaker prevents permanently-failing beads from causing infinite retry loops.

| Property | Value |
|----------|-------|
| Threshold | `maxDispatchFailures = 3` |
| Counter | `dispatch_failures` field in queue metadata |
| Break action | Add `gt:dispatch-failed` label, remove `gt:queued` |
| Reset | No automatic reset (manual intervention required) |

### Flow

```
Dispatch attempt fails
    │
    ├─ Increment dispatch_failures in metadata
    ├─ Store last_failure error message
    │
    └─ dispatch_failures >= 3?
         ├─ Yes → add gt:dispatch-failed, remove gt:queued
         │         (bead exits queue permanently)
         └─ No  → bead stays queued, retried next cycle
```

Beads without metadata are **quarantined immediately** (no circuit breaker retries) since they can never succeed:

```
dispatchSingleBead: meta == nil || meta.TargetRig == ""
    └─ add gt:dispatch-failed, remove gt:queued (instant quarantine)
```

---

## Queue Control

### Pause / Resume

Pausing stops all dispatch town-wide. The state is stored in `.runtime/queue-state.json`.

```bash
gt queue pause    # Sets paused=true, records actor and timestamp
gt queue resume   # Clears paused state
```

State file (`<townRoot>/.runtime/queue-state.json`):
```json
{
  "paused": true,
  "paused_by": "mayor/",
  "paused_at": "2026-02-19T10:00:00Z",
  "last_dispatch_at": "2026-02-19T09:57:00Z",
  "last_dispatch_count": 3
}
```

Write is atomic (temp file + rename) to prevent corruption from concurrent writers.

### Clear

Removes beads from the queue by stripping the `gt:queued` label:

```bash
gt queue clear              # Remove ALL beads from queue
gt queue clear --bead gt-abc  # Remove specific bead
```

### Status / List

```bash
gt queue status         # Summary: paused, queued count, active polecats
gt queue status --json  # JSON output

gt queue list           # Beads grouped by target rig, with blocked indicator
gt queue list --json    # JSON output
```

`list` reconciles `bd list --label=gt:queued` (all queued) with `bd ready --label=gt:queued` (unblocked) to mark blocked beads. Already-dispatched beads (hooked/closed) are filtered out since `gt:queued` is retained as audit trail.

---

## Queue and Convoy Integration

Convoys and the work queue are complementary but distinct mechanisms. Convoys track completion of related beads; the queue controls dispatch capacity. Two paths exist for dispatching convoy work:

### Dispatch Paths

| Path | Trigger | Capacity Control | Use Case |
|------|---------|-----------------|----------|
| **Deacon dogs** | `mol-convoy-feed` formula | None (fires immediately) | Autonomous — Deacon observes convoy events and slings directly |
| **Queue dispatch** | `gt convoy queue` / `gt queue epic` | Yes (daemon heartbeat, max_polecats, batch_size) | Operator-initiated — batched work with back-pressure |

**Deacon dogs** (direct sling): The Deacon's `mol-convoy-feed` formula watches convoy activity via `bd activity --follow`. When a new issue is tracked, the Deacon determines the rig from the bead's prefix (`beads.ExtractPrefix` + `beads.GetRigNameForPrefix`) and slings it immediately. No capacity control — all issues dispatch at once.

**Queue dispatch** (capacity-controlled): `gt convoy queue <convoy-id>` enqueues all open tracked issues. Each issue's rig is auto-resolved from its bead ID prefix. The daemon dispatches incrementally via `gt queue run`, respecting `max_polecats` and `batch_size`. Use this for large batches where simultaneous dispatch would exhaust resources.

### When to Use Which

- **Small convoys (< 5 issues)**: Deacon dogs are fine — simultaneous dispatch is manageable
- **Large batches (5+ issues)**: Use `gt convoy queue` for capacity-controlled dispatch
- **Epics**: Use `gt queue epic` — same auto-rig-resolution, scoped to epic children

### Rig Resolution

Both `gt convoy queue` and `gt queue epic` auto-resolve the target rig per-bead from its ID prefix using `beads.ExtractPrefix()` + `beads.GetRigNameForPrefix()`. This matches the pattern used by the Deacon's `mol-convoy-feed` and `redispatch`. Town-root beads (`hq-*`) are skipped with a warning since they are coordination artifacts, not dispatchable work.

### ConvoyWatcher Event-Driven Completion

The `ConvoyWatcher` (in `internal/convoy/observer.go`) streams `bd activity --follow` on the convoy bead. When a tracked issue closes, the watcher updates convoy progress and checks completion conditions. This works identically regardless of whether the issue was dispatched via Deacon dogs or the queue.

### Auto-Convoy at Enqueue

`enqueueBead()` automatically creates a convoy for each queued bead (unless `--no-convoy` is set or the bead is already tracked). When using `gt convoy queue`, auto-convoy is disabled (`NoConvoy: true`) since the bead is already tracked by the source convoy.

---

## Safety Properties

| Property | Mechanism |
|----------|-----------|
| **Enqueue idempotency** | Skip if bead is open with `gt:queued` label |
| **Cross-rig guard** | Reject if bead prefix doesn't match target rig (unless `--force`) |
| **Dispatch serialization** | `flock(queue-dispatch.lock)` prevents double-dispatch |
| **Metadata-before-label** | Metadata is inert without label; label is atomic activation |
| **Post-dispatch label swap** | `gt:queued` → `gt:queue-dispatched` prevents reopened beads from re-entering queue |
| **Formula pre-cooking** | `bd cook` at enqueue time catches bad protos before daemon dispatch loop |
| **Rollback on label failure** | Metadata stripped if label-add fails (no orphaned metadata) |
| **Fresh state on save** | Dispatch re-reads QueueState before saving to avoid clobbering concurrent pause |

---

## Known Limitations

1. **Stale dispatch snapshot** — `bd ready` is called once per dispatch cycle. A `gt queue clear` between the query and the dispatch loop can result in dispatching a bead that was just cleared. The window is narrow (seconds) and the bead would simply process normally.

2. **Delimiter substring match** — `StripQueueMetadata()` uses `strings.Index(description, "---gt:queue:v1---")`. If user content contains the exact delimiter string, the strip would truncate real content. The namespaced delimiter (`gt:queue:v1`) makes collision extremely unlikely.

3. **Single-sling path not yet unified** — Single bead sling to a rig (`gt sling gt-abc gastown`) still uses inline dispatch logic rather than the unified `executeSling()` path. Batch sling and queue dispatch already use the unified path.

---

## See Also

- [Watchdog Chain](watchdog-chain.md) — Daemon heartbeat, where queue dispatch runs as step 14
- [Convoys](../concepts/convoy.md) — Convoy tracking, auto-convoy on enqueue
- [Operational State](operational-state.md) — Labels-as-state pattern used by queue labels
