# Mailbox CLI

`agent-mailbox` is a local mailbox CLI for one Unix user on one machine.

This guide is intentionally short. It covers what a user needs to run the CLI:

- where state lives
- the normal send/receive flow
- how blocking receive and observe-only watch work
- what each command is for

## State Directory

The mailbox keeps all local state in one directory.

Resolution order:

- `$MAILBOX_STATE_DIR`
- `$XDG_STATE_HOME/ai-agent/mailbox`
- `~/.local/state/ai-agent/mailbox`

For demos or tests, use an isolated directory:

```bash
export MAILBOX_STATE_DIR=/tmp/mailbox-demo
```

You can also override it per command:

```bash
agent-mailbox --state-dir /tmp/mailbox-demo list --for workflow/reviewer/task-123
```

## Typical Flow

Send a message:

```bash
printf 'review request body\n' | \
agent-mailbox send \
  --to workflow/reviewer/task-123 \
  --from agent/sender \
  --subject "review request" \
  --body-file -
```

Receive a message:

```bash
agent-mailbox recv \
  --for workflow/reviewer/task-123 \
  --json
```

Search multiple inboxes with one receive:

```bash
agent-mailbox recv \
  --for workflow/reviewer/task-123 \
  --for workflow/reviewer/task-456 \
  --json
```

The receive result includes `delivery_id` and `lease_token`. Keep both. You
need them for follow-up actions.

Observe deliveries without claiming them:

```bash
agent-mailbox watch \
  --for workflow/reviewer/task-123 \
  --timeout 30s \
  --json
```

`watch` emits delivery metadata only. It never returns message bodies or lease
tokens.

Ack when processing succeeds:

```bash
agent-mailbox ack \
  --delivery <delivery_id> \
  --lease-token <lease_token>
```

## Blocking Receive

Wait until a message becomes claimable:

```bash
agent-mailbox recv \
  --for workflow/reviewer/task-123 \
  --wait \
  --json
```

Wait with a timeout:

```bash
agent-mailbox recv \
  --for workflow/reviewer/task-123 \
  --wait \
  --timeout 30s \
  --json
```

Rules:

- without `--wait`, `recv` returns immediately
- `--timeout` requires `--wait`
- timeout or no-message returns exit code `2`
- repeated `--for` flags search the union of the requested inboxes
- selection is global oldest-first by `visible_at`, then `message_created_at`,
  then `delivery_id`
- v1 does not guarantee fairness or address rotation while waiting
- unseen addresses behave like empty inboxes
- `recv --wait` can start before the first message is ever sent to an address

## Watch

`watch` is the observe-only companion to `recv`.

```bash
agent-mailbox watch --for <address> [--for <address> ...] [--state dead_letter] [--timeout 30s] [--json]
```

Rules:

- `watch` always stays observe-only; it does not claim deliveries or create
  lease tokens
- repeated `--for` flags search the union of the requested inboxes
- duplicate `--for` values are ignored after the first occurrence
- default output watches currently visible queued deliveries
- `--state <state>` watches that delivery state instead
- `--json` emits one JSON object per line (NDJSON)
- without `--timeout`, `watch` runs until interrupted
- `--timeout` is an idle timeout; if no newly matching delivery appears during
  that interval, `watch` exits successfully
- duplicate polling cycles do not reprint the same unchanged delivery snapshot
- unseen addresses behave like empty inboxes until matching deliveries exist

## Commands

### `send`

Queue one message for a recipient address.

```bash
agent-mailbox send --to <address> --body-file <path-or->
```

Common options:

- `--from <address>`
- `--subject <text>`
- `--content-type <mime-type>`
- `--schema-version <version>`

Notes:

- `--body-file -` reads from stdin
- the message body must not be empty
- `send` creates the recipient address automatically on first use
- `send` also creates the optional `--from` address automatically on first use
- `--from` is optional

### `recv`

Claim the next delivery for one or more recipient addresses.

```bash
agent-mailbox recv --for <address> [--for <address> ...] [--wait] [--timeout 30s] [--json]
```

Use `--json` for scripts and agents.

Notes:

- repeat `--for` to search multiple inboxes with one claim attempt
- duplicate `--for` values are ignored after the first occurrence
- plain-text output includes `recipient_address=...` so the matched inbox is clear
- `--json` keeps the existing schema and still includes `recipient_address`
- unseen addresses are ignored until a matching delivery exists

### `watch`

Observe matching deliveries without claiming them.

```bash
agent-mailbox watch --for <address> [--for <address> ...] [--state dead_letter] [--timeout 30s] [--json]
```

Use `--json` for streaming consumers. Each output line is one delivery metadata
object, not a JSON array.

Notes:

- default watch scope is visible queued deliveries
- `--state` lets you watch another delivery state with the same metadata schema
- plain-text output includes `recipient_address=...`
- `watch` is for observation only; use `recv` to claim work

### `ack`

Mark a leased delivery as complete.

```bash
agent-mailbox ack --delivery <delivery_id> --lease-token <lease_token>
```

### `release`

Return a leased delivery to the queue immediately.

```bash
agent-mailbox release --delivery <delivery_id> --lease-token <lease_token>
```

### `defer`

Return a leased delivery to the queue, but hide it until a future time.

```bash
agent-mailbox defer \
  --delivery <delivery_id> \
  --lease-token <lease_token> \
  --until 2026-03-18T12:00:00Z
```

### `fail`

Record a processing failure.

```bash
agent-mailbox fail \
  --delivery <delivery_id> \
  --lease-token <lease_token> \
  --reason "tool crashed"
```

Retry behavior in v1:

- attempts 1 and 2 requeue immediately
- attempt 3 moves the delivery to `dead_letter`

### `list`

Inspect deliveries for one recipient address.

```bash
agent-mailbox list --for <address> [--state dead_letter] [--json]
```

Notes:

- default output shows currently claimable queued deliveries
- `--state dead_letter` shows dead-lettered deliveries
- `list` is a snapshot; use `watch` for a stream
- use `--json` for scripts and agents
- unseen addresses return an empty result

## Exit Codes

- `0`: success
- `2`: `recv` found no message, or `recv --wait --timeout ...` timed out
- other non-zero: usage error or operational failure
