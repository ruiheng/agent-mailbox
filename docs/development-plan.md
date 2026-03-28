# Development Plan

This plan implements `docs/initial-design.md` v0.6 in small, mergeable slices.
The primary goal is to reach a correct local-first mailbox MVP before adding
workflow adapters or extra ergonomics.

## Principles

- keep the state model simple and explicit
- prove correctness with focused tests around leases and recovery
- land a usable CLI early, then fill in behavior gaps
- prefer one complete vertical slice at a time over broad partial scaffolding

## Phase 1: Bootstrap Core Runtime

Objective:
Create a runnable Go CLI with persistent local state, schema creation, blob
storage, and enough commands to prove the store and identity model work.

Scope:

- initialize Go module and project layout
- create mailbox state directory layout and blob directory
- implement SQLite open/init path with WAL mode
- define schema for `endpoints`, `endpoint_aliases`, `messages`, `deliveries`,
  and `events`
- implement `endpoint register`
- implement `send`
- implement `list`
- add JSON output contract for read commands
- add tests for schema init, endpoint registration idempotency, and send/list

Exit criteria:

- `mailbox endpoint register` works with alias/kind conflict handling
- `mailbox send` persists blobs, message rows, delivery rows, and events
- `mailbox list --json` can inspect queued deliveries
- tests cover the happy path and basic conflict/error cases

## Phase 2: Lease and Consumption Semantics

Objective:
Implement the claim path and all lease-controlled delivery transitions with the
correct lazy-expiry behavior.

Scope:

- implement `recv` with immediate and `--wait` modes
- implement adaptive polling with optional `--timeout`
- implement lease issuance and `lease_token` validation
- implement lazy reclaim of expired leased deliveries on claim paths
- implement `ack`, `release`, `defer`, and `fail`
- enforce fixed v1 retry policy for `fail`
- add event logging for all state transitions
- add tests for concurrency-sensitive transitions and lease expiry recovery

Exit criteria:

- only one consumer can claim a delivery at a time
- expired leases become claimable again without a daemon
- stale lease tokens are rejected
- `fail` requeues until attempt 3 and then dead-letters

## Phase 3: CLI Hardening and End-to-End Verification

Objective:
Make the MVP operationally usable from scripts and agent workflows.

Scope:

- finish JSON output shapes and exit code handling
- support stdin body input ergonomics and error messages
- add end-to-end CLI tests covering register/send/recv/ack and timeout cases
- document command usage and local state layout in `README.md`
- verify orphan-blob handling assumptions and GC boundaries are documented

Exit criteria:

- CLI behavior matches the design doc for blocking, timeout, and no-message paths
- end-to-end tests exercise the main mailbox loop
- repository docs describe how to run and verify the MVP locally

## Deferred

- topic routing
- consumer groups
- remote networking
- transport adapters
- blob deduplication
- background sweeper

## Initial Queue

1. Phase 1 bootstrap core runtime
2. Phase 2 lease and consumption semantics
3. Phase 3 CLI hardening and end-to-end verification
