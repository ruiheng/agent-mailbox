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

## Requirements

To build the CLI from source you need:

- Go 1.24 or newer
- a working C toolchain, because the current SQLite driver uses CGO

## Build

Build a local executable:

```bash
make build
```

This produces:

```text
./bin/mailbox
```

Run the full test suite:

```bash
make test
```

Show the available build targets:

```bash
make help
```

## Install

Install into `/usr/local/bin`:

```bash
make install
```

Install into a user-local prefix without needing root:

```bash
make install PREFIX="$HOME/.local"
```

If you use a user-local prefix, make sure `"$HOME/.local/bin"` is in your
`PATH`.

## Quick Start

Use `--state-dir` for demos and tests so mailbox state stays isolated:

```bash
MAILBOX_STATE_DIR=/tmp/mailbox-demo ./bin/mailbox endpoint register \
  --alias workflow/reviewer/task-123 --kind workflow

MAILBOX_STATE_DIR=/tmp/mailbox-demo ./bin/mailbox endpoint register \
  --alias agent/sender --kind agent
```

The default state directory is:

- `$MAILBOX_STATE_DIR` when set
- otherwise `$XDG_STATE_HOME/ai-agent/mailbox`
- otherwise `~/.local/state/ai-agent/mailbox`

You can set `MAILBOX_STATE_DIR` once, or pass `--state-dir` per command.

## Basic Usage

Register the recipient and optional sender aliases before sending:

Send a message from stdin:

```bash
printf 'review request body\n' | \
./bin/mailbox --state-dir /tmp/mailbox-demo \
  send --to workflow/reviewer/task-123 --from agent/sender \
  --subject "review request" --body-file -
```

Receive the next claimable message:

```bash
./bin/mailbox --state-dir /tmp/mailbox-demo \
  recv --for workflow/reviewer/task-123 --json
```

Ack the leased delivery using the returned `delivery_id` and `lease_token`:

```bash
./bin/mailbox --state-dir /tmp/mailbox-demo \
  ack --delivery <delivery_id> --lease-token <lease_token>
```

Release a delivery back to the queue:

```bash
./bin/mailbox --state-dir /tmp/mailbox-demo \
  release --delivery <delivery_id> --lease-token <lease_token>
```

Defer a delivery until a future time:

```bash
./bin/mailbox --state-dir /tmp/mailbox-demo \
  defer --delivery <delivery_id> --lease-token <lease_token> \
  --until 2026-03-18T12:00:00Z
```

Record a processing failure:

```bash
./bin/mailbox --state-dir /tmp/mailbox-demo \
  fail --delivery <delivery_id> --lease-token <lease_token> \
  --reason "tool crashed"
```

Inspect queued or terminal deliveries:

```bash
./bin/mailbox --state-dir /tmp/mailbox-demo \
  list --for workflow/reviewer/task-123 --json

./bin/mailbox --state-dir /tmp/mailbox-demo \
  list --for workflow/reviewer/task-123 --state dead_letter --json
```

## CLI Notes

- most commands exit `0` on success
- `recv` exits `2` when no message is available or a `--wait --timeout` call
  times out
- `recv --timeout` requires `--wait`.
- `recv --json` and `list --json` are the stable machine-readable paths.
- `send --body-file -` reads the message body from stdin.
- `--from` on `send` is optional; when omitted, the sender endpoint id is stored
  as `NULL`.
- `fail` requeues on attempts 1 and 2, and moves the delivery to `dead_letter`
  on attempt 3.

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
