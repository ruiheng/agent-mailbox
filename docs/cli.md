# Mailbox CLI Guide

This document is for people who want to use the `mailbox` command-line tool,
not work on the implementation.

## What The CLI Does

`mailbox` is a local-first queue-like mailbox for agents and workflows on a
single machine and under a single Unix user.

The CLI supports these operations:

- register a named endpoint
- send a message to an endpoint alias
- receive the next claimable message
- acknowledge, release, defer, or fail a leased delivery
- inspect queued or terminal deliveries

## Installation

Build the local executable:

```bash
make build
```

This produces:

```text
./bin/mailbox
```

Install it into your `PATH`:

```bash
make install PREFIX="$HOME/.local"
```

If you install into `"$HOME/.local"`, make sure `"$HOME/.local/bin"` is in
your `PATH`.

## State Directory

The mailbox stores all state locally in one directory.

The default state directory is:

- `$MAILBOX_STATE_DIR` when set
- otherwise `$XDG_STATE_HOME/ai-agent/mailbox`
- otherwise `~/.local/state/ai-agent/mailbox`

For testing or demos, use an isolated directory:

```bash
export MAILBOX_STATE_DIR=/tmp/mailbox-demo
```

You can also override it per command:

```bash
./bin/mailbox --state-dir /tmp/mailbox-demo list --for workflow/reviewer/task-123
```

## Command Summary

Global option:

- `--state-dir <path>`: override the mailbox state directory

Commands:

- `endpoint register`
- `send`
- `recv`
- `ack`
- `release`
- `defer`
- `fail`
- `list`

## Quick Start

Register a recipient endpoint:

```bash
./bin/mailbox endpoint register \
  --alias workflow/reviewer/task-123 \
  --kind workflow
```

Optionally register a sender endpoint:

```bash
./bin/mailbox endpoint register \
  --alias agent/sender \
  --kind agent
```

Send a message from stdin:

```bash
printf 'review request body\n' | \
./bin/mailbox send \
  --to workflow/reviewer/task-123 \
  --from agent/sender \
  --subject "review request" \
  --body-file -
```

Receive the next claimable message:

```bash
./bin/mailbox recv \
  --for workflow/reviewer/task-123 \
  --json
```

The JSON output includes the `delivery_id` and `lease_token` needed for follow
up actions.

Ack the delivery:

```bash
./bin/mailbox ack \
  --delivery <delivery_id> \
  --lease-token <lease_token>
```

## Blocking Wait

To block until a message becomes claimable:

```bash
./bin/mailbox recv \
  --for workflow/reviewer/task-123 \
  --wait \
  --json
```

To stop waiting after a fixed duration:

```bash
./bin/mailbox recv \
  --for workflow/reviewer/task-123 \
  --wait \
  --timeout 30s \
  --json
```

## Command Reference

### `endpoint register`

Create an endpoint alias, or safely retry the same registration.

Required flags:

- `--alias <alias>`
- `--kind <kind>`

Example:

```bash
./bin/mailbox endpoint register \
  --alias workflow/reviewer/task-123 \
  --kind workflow
```

Behavior:

- if the alias does not exist, the endpoint is created
- if the alias already exists with the same kind, the command succeeds
- if the alias already exists with a different kind, the command fails

### `send`

Persist a message and queue one delivery for the recipient alias.

Required flags:

- `--to <recipient-alias>`
- `--body-file <path-or->`

Optional flags:

- `--from <sender-alias>`
- `--subject <text>`
- `--content-type <mime-type>`
- `--schema-version <version>`

Examples:

Send from a file:

```bash
./bin/mailbox send \
  --to workflow/reviewer/task-123 \
  --subject "review request" \
  --body-file ./message.txt
```

Send from stdin:

```bash
printf 'review request body\n' | \
./bin/mailbox send \
  --to workflow/reviewer/task-123 \
  --body-file -
```

Notes:

- `--from` is optional
- sending to an unknown alias fails
- `--body-file -` means read the body from stdin

### `recv`

Claim the next delivery that is ready for the recipient alias.

Required flags:

- `--for <recipient-alias>`

Optional flags:

- `--wait`
- `--timeout <duration>`
- `--json`

Examples:

Immediate receive:

```bash
./bin/mailbox recv --for workflow/reviewer/task-123 --json
```

Blocking receive:

```bash
./bin/mailbox recv --for workflow/reviewer/task-123 --wait --json
```

Timed blocking receive:

```bash
./bin/mailbox recv --for workflow/reviewer/task-123 --wait --timeout 30s --json
```

Notes:

- without `--wait`, the command returns immediately
- `--timeout` requires `--wait`
- `recv --json` is the stable machine-readable form
- a successful receive returns a fresh `lease_token`

### `ack`

Mark a leased delivery as complete.

Required flags:

- `--delivery <delivery-id>`
- `--lease-token <lease-token>`

Example:

```bash
./bin/mailbox ack \
  --delivery <delivery_id> \
  --lease-token <lease_token>
```

### `release`

Return a leased delivery to the queue immediately.

Required flags:

- `--delivery <delivery-id>`
- `--lease-token <lease-token>`

Example:

```bash
./bin/mailbox release \
  --delivery <delivery_id> \
  --lease-token <lease_token>
```

### `defer`

Return a leased delivery to the queue, but keep it invisible until a future
time.

Required flags:

- `--delivery <delivery-id>`
- `--lease-token <lease-token>`
- `--until <rfc3339-timestamp>`

Example:

```bash
./bin/mailbox defer \
  --delivery <delivery_id> \
  --lease-token <lease_token> \
  --until 2026-03-18T12:00:00Z
```

### `fail`

Record a processing failure for a leased delivery.

Required flags:

- `--delivery <delivery-id>`
- `--lease-token <lease-token>`
- `--reason <text>`

Example:

```bash
./bin/mailbox fail \
  --delivery <delivery_id> \
  --lease-token <lease_token> \
  --reason "tool crashed"
```

Retry behavior in v1:

- attempts 1 and 2: requeue immediately
- attempt 3: move to `dead_letter`

### `list`

Inspect deliveries for one recipient alias.

Required flags:

- `--for <recipient-alias>`

Optional flags:

- `--state <state>`
- `--json`

Examples:

List currently claimable queued deliveries:

```bash
./bin/mailbox list --for workflow/reviewer/task-123 --json
```

List dead-letter deliveries:

```bash
./bin/mailbox list \
  --for workflow/reviewer/task-123 \
  --state dead_letter \
  --json
```

Notes:

- without `--state`, `list` shows only currently claimable queued deliveries
- `list --json` is the stable machine-readable form

## Exit Codes

- `0`: command succeeded
- `2`: `recv` found no message, or `recv --wait --timeout ...` timed out
- any other non-zero code: usage error or operational failure

## Output And Automation Notes

Use these forms for scripts and agents:

- `recv --json`
- `list --json`

Plain-text output is meant for people and may change more freely.

When you receive a delivery, keep the returned `delivery_id` and `lease_token`.
Those two values are required for `ack`, `release`, `defer`, and `fail`.

## Operational Notes

- the CLI is local-only in v1
- one mailbox store is intended for one Unix user on one machine
- the lease timeout defaults to 5 minutes
- message bodies are stored as local blob files under the mailbox state
- `body_sha256` is recorded when sending, but v1 does not re-verify it on
  receive
