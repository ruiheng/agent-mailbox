# agent-mailbox

`agent-mailbox` is a local-first mailbox for AI agents and workflows. It keeps
message durability, delivery state, and audit history in one mailbox store so a
sender can persist work immediately and a receiver can claim it later.

The current MVP is intentionally narrow:

- one local Unix user on one machine
- direct mailbox delivery by endpoint address
- SQLite metadata plus blob-backed message bodies
- explicit `send`, `recv`, `wait`, `watch`, `ack`, `release`, `defer`, `fail`, and `list`
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
./bin/agent-mailbox
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

## User Guide

For day-to-day CLI usage, examples, receive semantics, exit codes, and command
reference, see [`docs/cli.md`](docs/cli.md).

## Quick Start

Use `--state-dir` for demos and tests so mailbox state stays isolated:

```bash
export MAILBOX_STATE_DIR=/tmp/mailbox-demo
```

Address prefixes such as `workflow/...` or `agent/...` are naming conventions for
humans and tooling.

The default state directory is:

- `$MAILBOX_STATE_DIR` when set
- otherwise `$XDG_STATE_HOME/ai-agent/mailbox`
- otherwise `~/.local/state/ai-agent/mailbox`

You can set `MAILBOX_STATE_DIR` once, or pass `--state-dir` per command.
Read-style commands (`list`, `recv`, `wait`, and `watch`) accept either
`--json` or `--yaml`, but not both together.

## Minimal Example

Send a message from stdin:

```bash
printf 'review request body\n' | \
agent-mailbox --state-dir /tmp/mailbox-demo \
  send --to workflow/reviewer/task-123 --from agent/sender \
  --subject "review request" --body-file -
```

`send` requires a non-empty message body. Empty stdin and empty files are
rejected.

Receive the next claimable message:

```bash
agent-mailbox --state-dir /tmp/mailbox-demo \
  recv --for workflow/reviewer/task-123 --json
```

Receive across multiple inboxes with one command:

```bash
agent-mailbox --state-dir /tmp/mailbox-demo \
  recv --for workflow/reviewer/task-123 --for workflow/reviewer/task-456 --json
```

Claim up to 10 messages in one call:

```bash
agent-mailbox --state-dir /tmp/mailbox-demo \
  recv --for workflow/reviewer/task-123 --max 10 --json
```

Observe matching queued deliveries without claiming them:

```bash
agent-mailbox --state-dir /tmp/mailbox-demo \
  wait --for workflow/reviewer/task-123 --timeout 30s --json
```

Stream matching queued deliveries without claiming them:

```bash
agent-mailbox --state-dir /tmp/mailbox-demo \
  watch --for workflow/reviewer/task-123 --timeout 30s --json
```

Use `--yaml` when you want the same `list`, `recv`, `wait`, or `watch`
payloads in YAML. `wait` returns one YAML mapping. `watch` returns a YAML
document stream with one delivery per `---` document.

Ack the leased delivery using the returned `delivery_id` and `lease_token`:

```bash
agent-mailbox --state-dir /tmp/mailbox-demo \
  ack --delivery <delivery_id> --lease-token <lease_token>
```

For the full command reference, see [`docs/cli.md`](docs/cli.md).

`recv` v1 contract for multiple `--for` flags:

- repeated `--for` searches the union of the requested inboxes
- `--max` limits how many deliveries one command will claim, up to `10`
- default output still returns one leased message
- when `--max` is provided, structured output returns `messages` plus `has_more`
- when `has_more=true`, additional claimable deliveries still remain after this batch
- unseen addresses behave like empty inboxes
- selection is deterministic global oldest-first by `visible_at`, then
  `message_created_at`, then `delivery_id`

Use `list` for a one-shot snapshot, `wait` for a one-shot observe-only block,
`watch` for observe-only streaming metadata, and `recv` when the consumer is
ready to claim work and receive a lease token.

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
