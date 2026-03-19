# Initial Design v0.6

## 1. Goals

The first version should solve one real problem well:

- a sender can persist a message immediately
- a receiver can discover and consume it later
- the message lifecycle is durable and inspectable
- correctness does not depend on the sender or receiver current working directory
- correctness does not depend on any single transport such as `agent-deck`
- the system works for one local Unix user without requiring a daemon

The first version should not try to solve everything:

- no distributed multi-host protocol
- no mandatory daemon
- no workflow-specific control message contract
- no topic pub/sub or consumer-group routing in the MVP
- no transport-specific full-body push as a correctness requirement

## 2. Core Model

Separate the system into four layers:

1. mailbox core
2. consumer CLI / local API
3. transport adapters
4. workflow protocols

The mailbox core owns persistence, routing, leasing, and recovery.
The consumer CLI exposes explicit local operations such as `send`, `recv`, and
`ack`.
Transport adapters may notify other runtimes, but they are not the source of
truth.
Workflow protocols sit above the mailbox and should treat it as infrastructure.

The system is pull-first:

- the authoritative state is in the mailbox store
- receivers explicitly claim work when ready
- transport failures do not redefine mailbox correctness

## 3. Storage

Use a centralized state directory:

- default: `$XDG_STATE_HOME/ai-agent/mailbox`
- fallback: `~/.local/state/ai-agent/mailbox`

The directory should be created with user-only permissions, for example `0700`.
The v1 trust boundary is one local Unix user on one machine.

Use SQLite for authoritative mutable state:

- `endpoints`
- `endpoint_aliases`
- `messages`
- `deliveries`
- `events`

Use SQLite in WAL mode.
Current state tables are authoritative.
The event log is append-only audit history, not a second source of truth.

Use a blob store for message payloads and larger artifacts:

- all message bodies in v1
- attachments
- snapshots copied from external project-local artifacts when durable processing
  depends on the content

Rules:

- the store must be independent of current working directory
- any external file reference recorded in a message must be absolute
- all message bodies are persisted through the blob store in v1
- if correctness depends on file content, ingest or snapshot it into the blob
  store
- external references may be recorded for convenience, but they are not a
  durability mechanism

Deduplication by content hash is not required in v1.

Blob storage format in v1:

- store blobs as regular files under `blobs/`
- use mailbox-generated ids as blob filenames
- do not use content-hash naming in v1
- directory sharding may be added later if blob count makes it necessary

## 4. Identity Model

Human-friendly names are useful, but they should not be the primary key.

Use two identity forms:

- endpoint id: stable logical identity, for example `ep_01hv...`
- alias: optional human-facing name, for example `workflow/reviewer/task-123`

Rules:

- deliveries target endpoint ids
- humans and workflows may address aliases
- aliases resolve through a local registry
- aliases are namespaced
- alias uniqueness is enforced per mailbox instance

Recommended alias prefixes:

- `user/...`
- `agent/...`
- `workflow/...`

The `topic/...` namespace may be reserved for future use, but it is not part of
the v1 routing model.

### Endpoint

Suggested fields:

- `endpoint_id`
- `created_at`
- `metadata_json`

### Alias

Suggested fields:

- `alias`
- `endpoint_id`
- `created_at`

Observability-only consumer session tracking may be added later, but it is not
part of lease correctness in v1.

## 5. Message Model

Treat message content and delivery state as separate concerns.

A message is immutable after creation.
A delivery is mutable and tracks whether a particular endpoint still needs to
process that message.

### Message

Suggested fields:

- `message_id`
- `created_at`
- `sender_endpoint_id`
- `subject`
- `content_type`
- `schema_version`
- `idempotency_key`
- `body_blob_ref`
- `body_size`
- `body_sha256`
- `reply_to_message_id`
- `metadata_json`

Rules:

- `sender_endpoint_id` is nullable in v1
- `--from` is optional for `send`
- `body_blob_ref` is the canonical body storage handle in v1
- message bodies are always read through blob lookup, not through dual inline/blob
  branches
- `idempotency_key` is an optional opaque sender field in v1
- v1 does not enforce mailbox-level deduplication for `idempotency_key`
- `schema_version` describes the sender-defined schema version of the message
  body for the given `content_type`
- the mailbox stores `schema_version` but does not interpret it in v1
- `body_sha256` is computed and stored by `send`
- `recv` does not verify `body_sha256` in v1; it is stored for audit and optional
  consumer-side verification
- `metadata_json` is for small extensible metadata, not an unbounded dumping
  ground

## 6. Delivery Model

Keep the state machine small.
Do not model historical actions as separate long-lived states unless they change
current behavior.

### Delivery

Suggested fields:

- `delivery_id`
- `message_id`
- `recipient_endpoint_id`
- `state`
- `visible_at`
- `lease_token`
- `lease_expires_at`
- `acked_at`
- `attempt_count`
- `last_error_code`
- `last_error_text`

### Delivery States

- `queued`: eligible to be claimed when `visible_at <= now`
- `leased`: claimed by one consumer, awaiting terminal action
- `acked`: completed successfully
- `dead_letter`: terminal failure or manual quarantine

Do not keep `released` or `deferred` as separate current states.

- `release` means transition `leased -> queued` and set `visible_at = now`
- `defer` means set `visible_at` to a future time and leave the delivery in
  `queued`

The key state is `leased`.
It prevents concurrent consumers from processing the same delivery and allows
crash recovery through lease expiry.

On claim paths, a delivery with `state = leased` and `lease_expires_at <= now`
is treated as eligible for re-claim, equivalent to `queued`.
This rule is required for lazy lease expiry recovery to work without a daemon.

### Lease Ownership

`delivery_id` alone is not sufficient authorization for `ack`, `release`,
`defer`, or `fail`.

`recv` must return:

- `delivery_id`
- `lease_token`
- `lease_expires_at`

Any later state transition on that lease must prove ownership by presenting the
current `lease_token`.
If the lease expired and another consumer claimed the delivery, old tokens must
be rejected.

## 7. CLI Contract

The first CLI should be explicit and boring.

All read-style commands should support `--json`.
Machine consumers should use `--json` instead of parsing human text output.

Recommended exit codes:

- `0`: success
- `2`: no message available or wait timed out
- other non-zero: error

### Send

```text
agent-mailbox send --to workflow/reviewer/task-123 --subject "review request" --body-file /abs/path/request.md
```

Behavior:

- resolve the recipient alias
- fail if the recipient alias does not exist
- do not implicitly create endpoints or aliases during `send`
- if `--from` is provided, resolve it to `sender_endpoint_id`
- if `--from` is omitted, store `sender_endpoint_id = NULL`
- accept body input from either `--body-file <path>` or `--body-file -` for
  stdin
- persist the message body into the blob store
- persist the immutable message
- create one delivery in `queued`
- set `visible_at = now`
- return immediately with identifiers

The blob write may happen before the SQLite transaction and can leave an orphaned
blob if the process crashes at the wrong time.
That is acceptable in v1 and should be handled by later garbage collection.
Message row insertion and delivery row insertion must happen atomically in one
SQLite transaction.

### Endpoint Registration

```text
agent-mailbox endpoint register --alias workflow/reviewer/task-123
```

Behavior:

- create a new endpoint and bind the alias if the alias does not exist
- if the alias already exists, return the existing endpoint id and exit success
- make alias creation explicit before first receive

Alias prefixes such as `workflow/...` and `agent/...` remain useful naming
conventions for humans and tooling, but they are not stored as a separate
endpoint type field in the mailbox state.

### Receive

```text
agent-mailbox recv --for workflow/reviewer/task-123 --for workflow/reviewer/task-456 --wait --timeout 30s --json
```

Behavior:

- if `--wait` is not provided, attempt one immediate claim
- if `--wait` is provided, block until a message becomes claimable or until an
  optional timeout expires
- require at least one `--for` alias; repeated `--for` flags search the union
  of the requested inboxes
- if any requested alias does not resolve, fail the whole command
- atomically select the oldest visible queued delivery across the eligible union
- selection order is `visible_at`, then `message_created_at`, then `delivery_id`
- transition it to `leased`
- assign a fresh lease token and lease timeout
- return the message envelope and identifiers

If no claimable message exists:

- without `--wait`, return exit code `2` immediately
- with `--wait` and `--timeout <duration>`, return exit code `2` when the
  timeout expires
- with `--wait` and no timeout, block until success or cancellation

The claim operation must be atomic.
Two receivers must not be able to lease the same delivery.

The default lease timeout in v1 is `5m`.
`recv` may later accept an optional override such as `--lease-timeout`, but the
default timeout is part of the base contract and must be stable.

Blocking wait in v1 is implemented by polling SQLite with adaptive backoff.
The recommended schedule starts around `50ms`, grows up to about `1s`, and
resets after a successful claim.
This avoids a daemon dependency while keeping latency acceptable for local use.
No fairness or alias rotation guarantee is made in v1 while waiting across
multiple aliases.

### Ack

```text
agent-mailbox ack --delivery <delivery_id> --lease-token <lease_token>
```

Behavior:

- mark the currently leased delivery as complete
- require a valid unexpired lease token

### Release

```text
agent-mailbox release --delivery <delivery_id> --lease-token <lease_token>
```

Behavior:

- return a leased delivery to `queued`
- set `visible_at = now`
- require a valid unexpired lease token

### Defer

```text
agent-mailbox defer --delivery <delivery_id> --lease-token <lease_token> --until 2026-03-18T12:00:00Z
```

Behavior:

- move a leased delivery back to `queued`
- set `visible_at` to the requested future time
- require a valid unexpired lease token

### Fail

```text
agent-mailbox fail --delivery <delivery_id> --lease-token <lease_token> --reason "tool crashed"
```

Behavior:

- record an explicit processing failure
- require a valid unexpired lease token
- increment `attempt_count`
- if `attempt_count >= 3`, move the delivery to `dead_letter`
- otherwise return it to `queued` with `visible_at = now`

Failure handling is fixed in v1 so behavior is testable and predictable.

### List

```text
agent-mailbox list --for workflow/reviewer/task-123 --json
```

Behavior:

- summarize deliveries without changing state
- default to visible deliveries
- optionally allow filters by state for inspection

## 8. Pull First, Adapter Second

The core system must work even if there are no adapters.

That means:

- `recv --wait` is a first-class operation
- no receiver is forced to accept pushed full-body content
- transport failures do not redefine mailbox correctness

Adapters may still be useful:

- desktop notifications
- `agent-deck`
- chat bridge integrations

But they should operate as optional delivery helpers.

The recommended default adapter behavior is notification, not forced full-body
injection.
For example, an `agent-deck` adapter would ideally send:

- a short hint that new mail exists
- the endpoint alias
- the command needed to fetch the next message

The actual message remains in the mailbox store until a receiver explicitly
claims it.

The first implementation does not require any adapter.

## 9. Routing Boundaries

Different routing semantics should not be conflated.

### Direct Mailbox

- one delivery targets one endpoint
- one receiver eventually acks it

This is the MVP.

### Topic Pub/Sub

- a sender targets a topic
- the system fans the message out into one delivery per subscriber
- each subscriber acks its own delivery

This is a future capability.
It should be implemented as explicit fan-out, not as many consumers racing on
one shared delivery row.

### Consumer Group / Work Queue

- many workers share one queue
- one delivery is claimed by one worker

This is also a future capability.

## 10. Concurrency and Recovery

Requirements:

- message creation must be atomic
- `queued -> leased` must be atomic
- lease expiry must make abandoned work claimable again
- transport notification must not imply mailbox completion

Recovery principles:

- if a consumer dies before `ack`, lease expiry recovers the delivery
- if an adapter fails, the delivery remains in the mailbox
- state transitions must be auditable through an event log

Recovery does not require a daemon in v1.
Lease expiry may be enforced lazily on read/claim paths.
An optional background sweeper can be added later if needed.

### Event Log

Suggested fields:

- `event_id`
- `created_at`
- `event_type`
- `endpoint_id`
- `message_id`
- `delivery_id`
- `detail_json`

The event log should capture lifecycle transitions such as:

- endpoint registered
- message created
- delivery queued
- delivery leased
- delivery acked
- delivery released
- delivery deferred
- delivery failed
- delivery dead-lettered

## 11. Retention and Garbage Collection

Durable does not mean infinite retention by default.

The design should support later garbage collection for:

- old `acked` deliveries
- orphaned blobs no longer referenced by any message
- old events beyond an operator-defined retention horizon

The first implementation may leave GC as a manual or later command, but the data
model should not assume permanent unbounded growth.

## 12. Recommended First Iteration

Build the smallest complete slice:

1. SQLite schema for `endpoints`, `endpoint_aliases`, `messages`,
   `deliveries`, and `events`
2. `endpoint register`
3. alias lookup
4. `send`
5. `recv --wait`
6. `ack`
7. `release`
8. `defer`
9. `fail`
10. `list`

Skip for now:

- topics
- consumer groups
- adapter daemons
- remote networking
- blob deduplication
 
## 13. Implementation Choice

The first implementation should be Go.

Reasons:

- single static-friendly binary is easy to distribute
- SQLite support is mature without requiring a daemon
- concurrency and cancellation fit blocking CLI operations well
- deployment is simpler than a Python toolchain-based CLI

## 14. Open Questions

- Should `fail` in a future version support scheduled retry backoff in addition
  to immediate requeue?
- Should observability-only consumer session tracking be added in v1.1, or wait
  until adapter work begins?
