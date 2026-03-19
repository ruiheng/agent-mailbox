# Mailbox CLI

`agent-mailbox` is a local mailbox CLI for one Unix user on one machine.

This guide is intentionally short. It covers what a user needs to run the CLI:

- where state lives
- the normal send/receive flow
- how blocking receive works
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

Register the recipient:

```bash
agent-mailbox endpoint register \
  --alias workflow/reviewer/task-123 \
  --kind workflow
```

Optionally register the sender:

```bash
agent-mailbox endpoint register \
  --alias agent/sender \
  --kind agent
```

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
- v1 does not guarantee fairness or alias rotation while waiting
- if any requested alias is unknown, `recv` fails instead of partially
  succeeding

## Commands

### `endpoint register`

Create an endpoint alias.

```bash
agent-mailbox endpoint register --alias <alias> --kind <kind>
```

Notes:

- same alias + same kind is a safe retry
- same alias + different kind is an error

### `send`

Queue one message for a recipient alias.

```bash
agent-mailbox send --to <alias> --body-file <path-or->
```

Common options:

- `--from <alias>`
- `--subject <text>`
- `--content-type <mime-type>`
- `--schema-version <version>`

Notes:

- `--body-file -` reads from stdin
- sending to an unknown alias fails
- `--from` is optional

### `recv`

Claim the next delivery for one or more recipient aliases.

```bash
agent-mailbox recv --for <alias> [--for <alias> ...] [--wait] [--timeout 30s] [--json]
```

Use `--json` for scripts and agents.

Notes:

- repeat `--for` to search multiple inboxes with one claim attempt
- duplicate `--for` values are ignored after the first occurrence
- plain-text output includes `recipient_alias=...` so the matched inbox is clear
- `--json` keeps the existing schema and still includes `recipient_alias`

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

Inspect deliveries for one recipient alias.

```bash
agent-mailbox list --for <alias> [--state dead_letter] [--json]
```

Notes:

- default output shows currently claimable queued deliveries
- `--state dead_letter` shows dead-lettered deliveries
- use `--json` for scripts and agents

## Exit Codes

- `0`: success
- `2`: `recv` found no message, or `recv --wait --timeout ...` timed out
- other non-zero: usage error or operational failure
