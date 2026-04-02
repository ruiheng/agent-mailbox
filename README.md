# agent-mailbox

`agent-mailbox` is a local-first mailbox for AI agents and workflows. It keeps
message durability, delivery state, and audit history in one mailbox store so a
sender can persist work immediately and a receiver can claim it later.

The current MVP is intentionally narrow:

- one local Unix user on one machine
- direct mailbox delivery by endpoint address
- SQLite metadata plus blob-backed message bodies
- explicit `send`, `recv`, `wait`, `watch`, `list`, `stale`, `ack`, `release`, `defer`, and `fail`
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
Structured-output commands (`send`, `list`, `recv`, `wait`, `watch`, and `stale`) accept
either `--json` or `--yaml`, but not both together.
`send`, `recv`, and `wait` also accept `--full` when you need the full legacy
payload instead of the default compact view.

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

By default, `send` prints only `delivery_id=...`. Add `--full` when you also
need the legacy `message_id` and `blob_id`, or add `--json` / `--yaml` for the
same compact or full payloads in structured form.

Group delivery is explicit. Create a group address first, then send with
`--group`:

```bash
printf 'group update\n' | \
agent-mailbox --state-dir /tmp/mailbox-demo \
  send --to group/ops --group --from agent/sender --body-file - --json
```

Group send keeps personal send semantics unchanged: plain `send --to <address>`
still targets the personal queue path and fails if `<address>` is already a
known group address.

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

Ask for the full receive payload only when you need internal metadata such as
lease expiry or blob references:

```bash
agent-mailbox --state-dir /tmp/mailbox-demo \
  recv --for workflow/reviewer/task-123 --json --full
```

Observe matching queued deliveries without claiming them:

```bash
agent-mailbox --state-dir /tmp/mailbox-demo \
  wait --for workflow/reviewer/task-123 --timeout 30s --json
```

The default `recv`/`wait` JSON and YAML payloads are intentionally compact.
Use `--full` to include the full legacy metadata shape.

Stream matching queued deliveries without claiming them:

```bash
agent-mailbox --state-dir /tmp/mailbox-demo \
  watch --for workflow/reviewer/task-123 --timeout 30s --json
```

`--timeout` uses Go duration syntax such as `30s`, `5m`, `120ms`, or `1m30s`.

Use `--yaml` when you want the same `list`, `recv`, `wait`, `watch`, or `stale`
payloads in YAML. `wait` returns one YAML mapping. `watch` returns a YAML
document stream with one delivery per `---` document.

Find personal inboxes with receivable mail older than a threshold:

```bash
agent-mailbox --state-dir /tmp/mailbox-demo \
  stale --for workflow/reviewer/task-123 --older-than 10m --json
```

`stale` is structured-output-only in v1: use `--json` or `--yaml`.

Ack the leased delivery using the returned `delivery_id` and `lease_token`:

```bash
agent-mailbox --state-dir /tmp/mailbox-demo \
  ack --delivery <delivery_id> --lease-token <lease_token>
```

List previously acked deliveries for one inbox:

```bash
agent-mailbox --state-dir /tmp/mailbox-demo \
  list --for workflow/reviewer/task-123 --state acked --json
```

Read one persisted delivery body later by `delivery_id`:

```bash
agent-mailbox --state-dir /tmp/mailbox-demo \
  read --delivery <delivery_id> --json
```

Read the same body directly by `message_id`:

```bash
agent-mailbox --state-dir /tmp/mailbox-demo \
  read --message <message_id> --json
```

`read --message` is a raw body read in the trusted local environment. It does
not update group read tracking; group `recv` remains the operation that records
reads and advances group unread/read state.

For the common "read the previous message from this inbox" case, skip `list`
and read the latest acked delivery in one step:

```bash
agent-mailbox --state-dir /tmp/mailbox-demo \
  read --latest --for workflow/reviewer/task-123 --json
```

For the full command reference, see [`docs/cli.md`](docs/cli.md).

`recv` v1 contract for multiple `--for` flags:

- repeated `--for` searches the union of the requested inboxes
- `--max` limits how many deliveries one command will claim, up to `10`
- default output returns a compact leased message view
- `--full` returns the full legacy leased-message payload
- when `--max` is provided, structured output returns `messages` plus `has_more`
- when `has_more=true`, additional claimable deliveries still remain after this batch
- unseen addresses behave like empty inboxes
- selection is deterministic global oldest-first by `visible_at`, then
  `message_created_at`, then `delivery_id`

Use `list` for a one-shot snapshot, `wait` for a one-shot observe-only block,
`watch` for observe-only streaming metadata, and `recv` when the consumer is
ready to claim work and receive a lease token.

## Cron Wake Helper

For cron-style wakeups of live `agent-deck` sessions, use:

```bash
scripts/wake-stale-agent-deck-sessions.sh \
  --older-than 10m \
  --confirm-delay 2 \
  --state-dir /tmp/mailbox-demo
```

The script:

- lists `waiting` and `idle` sessions
- runs `agent-mailbox stale` for their `agent-deck/<session-id>` inboxes
- waits briefly, rechecks both session status and staleness, then sends
  `agent-deck session send --no-wait` only if the session is still idle/waiting

Install it into a bin directory:

```bash
scripts/wake-stale-agent-deck-sessions.sh install --prefix "$HOME/.local"
```

`--all-mail-states` tells the script to pass `--all` through to `agent-deck list`,
so it includes sessions that the default list view may hide.

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

`send` now makes the blob durable before it starts the SQLite write transaction:
it writes a temp file in `blobs/`, fsyncs that file, renames it into place, and
fsyncs the `blobs/` directory. That narrows the success window so committed
metadata does not rely on an unflushed blob filename or payload.

The remaining v1 trade-off is explicit: there is still no cross-store atomic
commit between the filesystem blob and the SQLite transaction. A crash after the
blob is durable but before the SQLite transaction commit can still leave an
orphaned blob. That is acceptable for the MVP and should be handled by a later
GC command rather than hidden behind implicit cleanup.
