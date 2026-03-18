# Initial Design

## 1. Goals

The first version should solve one real problem well:

- a sender can persist a message immediately
- the receiver can discover and consume it later
- the message lifecycle is durable and inspectable
- the design does not depend on the sender or receiver current working directory
- the design does not depend on a single transport such as `agent-deck`

The first version should not try to solve everything:

- no distributed multi-host protocol
- no mandatory daemon
- no workflow-specific control message contract
- no complicated broadcast or load-balancing semantics in the MVP

## 2. Core Model

Separate the system into four layers:

1. Mailbox core
2. Consumer CLI / API
3. Transport adapters
4. Workflow protocols

The mailbox core owns persistence and message lifecycle.
The consumer CLI exposes local operations such as send and receive.
Transport adapters may notify or deliver to other runtimes.
Workflow protocols sit above the mailbox and should treat it as infrastructure.

## 3. Storage

Use a centralized state directory:

- default: `$XDG_STATE_HOME/ai-agent/mailbox`
- fallback: `~/.local/state/ai-agent/mailbox`

Use SQLite for mutable metadata:

- messages
- deliveries
- endpoints
- aliases
- events

Use a blob store for larger payloads:

- message bodies too large for inline storage
- attachments
- optional snapshots copied from project-local artifacts when needed

The store must be independent of current working directory.
Any external file reference recorded in a message should be absolute.

## 4. Endpoint Identity

Human-friendly names are useful, but they should not be the primary key.

Use two identity forms:

- internal stable endpoint id, for example `ep_01hv...`
- optional aliases, for example `workflow/reviewer/task-123`

Rules:

- deliveries target endpoint ids
- humans and workflows may use aliases
- aliases are resolved through a registry
- aliases should be namespaced
- alias uniqueness is enforced per mailbox instance

Recommended alias prefixes:

- `user/...`
- `agent/...`
- `workflow/...`
- `topic/...`

This avoids relying on "the agent will notice the message is not for it".
That fallback may remain possible for ad hoc usage, but it should not be the
main correctness mechanism.

## 5. Message and Delivery Semantics

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
- `body_inline` or `body_blob_ref`
- `reply_to_message_id`
- `metadata_json`

### Delivery

Suggested fields:

- `delivery_id`
- `message_id`
- `recipient_endpoint_id`
- `state`
- `lease_owner`
- `lease_expires_at`
- `defer_until`
- `acked_at`
- `attempt_count`

### Delivery States

- `queued`: available to be claimed
- `leased`: temporarily claimed by one consumer, not yet completed
- `acked`: completed successfully
- `released`: previously leased, returned to queue
- `deferred`: intentionally hidden until a later time
- `dead_letter`: terminal failure or manual quarantine

The key state to understand is `leased`.
It means a receiver has claimed the delivery, but the system is still waiting
for explicit completion.
Without `leased`, two consumers can race on the same message or one crash can
leave work permanently stuck.

## 6. CLI Contract

The first CLI should be explicit and boring.

### Send

```text
mailbox send --to workflow/reviewer/task-123 --subject "review request" --body-file /abs/path/request.md
```

Behavior:

- resolve the recipient alias
- persist the immutable message
- create a delivery in `queued`
- return immediately

### Wait

```text
mailbox wait --for workflow/reviewer/task-123
```

Behavior:

- block until at least one visible delivery exists
- do not claim the delivery
- exit with enough information for the caller to decide whether to receive now

This command should be cancelable with `Ctrl-C` and should have no side effects
if canceled before a receive operation.

### Receive

```text
mailbox recv --for workflow/reviewer/task-123 --wait
```

Behavior:

- wait if necessary
- atomically select the oldest visible delivery
- move it to `leased`
- assign a lease owner and lease timeout
- return the message content and delivery identifiers

### Ack

```text
mailbox ack --delivery <delivery_id>
```

Behavior:

- mark the leased delivery as complete

### Release

```text
mailbox release --delivery <delivery_id>
```

Behavior:

- return a leased delivery to `queued`

### Defer

```text
mailbox defer --delivery <delivery_id> --until 2026-03-18T12:00:00Z
```

Behavior:

- move a leased or queued delivery to `deferred`

### List

```text
mailbox list --for workflow/reviewer/task-123
```

Behavior:

- summarize visible deliveries without changing state

## 7. Pull First, Adapter Second

The core system should work even if there are no adapters.

That means:

- `wait` and `recv` are first-class operations
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

## 8. Direct Mailbox vs Topic vs Work Queue

These are different routing semantics and should not be conflated.

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

## 9. Concurrency and Recovery

Requirements:

- message creation must be atomic
- `queued -> leased` must be atomic
- lease expiry must return abandoned work to `queued`
- transport notification must not imply mailbox completion

Recovery principles:

- if a consumer dies before ack, lease expiry recovers the delivery
- if an adapter fails, the delivery remains in the mailbox
- state transitions should be auditable through an event log

## 10. Recommended First Iteration

Build the smallest complete slice:

1. SQLite schema for endpoints, messages, deliveries, events
2. alias registration and lookup
3. `send`
4. `wait`
5. `recv`
6. `ack`
7. `release`
8. `list`

Skip for now:

- topics
- consumer groups
- adapter daemons
- remote networking

## 11. Open Questions

- Should there be an explicit "peek next" command separate from `wait` and `recv`?
- Should the first adapter be `agent-deck` notification or no adapter at all?
- Should the first implementation be Go, Python, or another language?
- Should blob storage deduplicate by content hash in v1 or wait until later?
