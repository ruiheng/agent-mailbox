# agent-mailbox

`agent-mailbox` is a local-first mailbox for AI agents and workflows. It keeps
message durability, delivery state, and audit history in one mailbox store so a
sender can persist work immediately and a receiver can claim it later.

The current MVP is intentionally narrow:

- one local Unix user on one machine
- direct mailbox delivery by endpoint alias
- SQLite metadata plus blob-backed message bodies
- explicit `send`, `recv`, `ack`, `release`, `defer`, `fail`, and `list`
- no daemon, no network transport, no adapter-specific correctness dependency

## Status

The Go CLI now implements the MVP described in
[`docs/initial-design.md`](docs/initial-design.md). The focus is correctness and
operational clarity, not transport integrations or extra routing models.

## Build And Verify

Run the full test suite:

```bash
go test ./...
```

The default state directory is:

- `$MAILBOX_STATE_DIR` when set
- otherwise `$XDG_STATE_HOME/ai-agent/mailbox`
- otherwise `~/.local/state/ai-agent/mailbox`

For ad hoc testing, use `--state-dir` to keep state isolated in a temp
directory.

## Basic Workflow

Register the recipient and optional sender aliases before sending:

```bash
go run ./cmd/mailbox --state-dir /tmp/mailbox-demo \
  endpoint register --alias workflow/reviewer/task-123 --kind workflow

go run ./cmd/mailbox --state-dir /tmp/mailbox-demo \
  endpoint register --alias agent/sender --kind agent
```

Send a message from stdin:

```bash
printf 'review request body\n' | \
go run ./cmd/mailbox --state-dir /tmp/mailbox-demo \
  send --to workflow/reviewer/task-123 --from agent/sender \
  --subject "review request" --body-file -
```

Receive the next claimable message:

```bash
go run ./cmd/mailbox --state-dir /tmp/mailbox-demo \
  recv --for workflow/reviewer/task-123 --json
```

Ack the leased delivery using the returned `delivery_id` and `lease_token`:

```bash
go run ./cmd/mailbox --state-dir /tmp/mailbox-demo \
  ack --delivery <delivery_id> --lease-token <lease_token>
```

Inspect queued or terminal deliveries:

```bash
go run ./cmd/mailbox --state-dir /tmp/mailbox-demo \
  list --for workflow/reviewer/task-123 --json

go run ./cmd/mailbox --state-dir /tmp/mailbox-demo \
  list --for workflow/reviewer/task-123 --state dead_letter --json
```

## CLI Notes

- `recv` exits `0` on success.
- `recv` exits `2` when no message is available or a `--wait --timeout` call
  times out.
- `recv --timeout` requires `--wait`.
- `recv --json` and `list --json` are the stable machine-readable paths.
- `send --body-file -` reads the message body from stdin.
- `--from` on `send` is optional; when omitted, the sender endpoint id is stored
  as `NULL`.

## Local State Layout

The mailbox state directory contains:

- `mailbox.db`: authoritative SQLite state for endpoints, messages, deliveries,
  and events
- `blobs/`: immutable message body files referenced by `messages.body_blob_ref`

The event log is append-only audit history. Current-state tables remain the
source of truth for delivery behavior.

## MVP Boundaries

What this repository does today:

- durable direct mailbox delivery
- lazy recovery of expired leases without a daemon
- fixed retry behavior for `fail`
- local auditability through the `events` table

What it does not do yet:

- topic routing or consumer groups
- remote networking
- transport adapters such as `agent-deck`
- background garbage collection

One v1 trade-off is explicit: a crash after the blob write but before the SQLite
transaction commit can leave an orphaned blob. That is acceptable for the MVP
and should be handled by a later GC command rather than hidden behind implicit
cleanup.
