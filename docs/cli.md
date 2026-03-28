# Mailbox CLI

`agent-mailbox` is a local mailbox CLI for one Unix user on one machine.

This guide is intentionally short. It covers what a user needs to run the CLI:

- where state lives
- the normal send/receive flow
- how immediate receive, observe-only wait, and observe-only watch work
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

Swap `--json` for `--yaml` when you want the same payload in YAML.
Add `--full` when you need the full legacy payload instead of the default
compact view.

Search multiple inboxes with one receive:

```bash
agent-mailbox recv \
  --for workflow/reviewer/task-123 \
  --for workflow/reviewer/task-456 \
  --json
```

Claim multiple messages in one call:

```bash
agent-mailbox recv \
  --for workflow/reviewer/task-123 \
  --max 10 \
  --json
```

Without `--max`, `recv` returns one leased message in the legacy single-message
shape only when `--full` is set. By default it returns a compact single-message
view with just the fields needed for normal processing. With `--max`, the
receive result becomes an object with `messages` and `has_more`. Each message
entry includes its own `delivery_id` and `lease_token`. Keep those for
follow-up actions.

Observe deliveries without claiming them:

```bash
agent-mailbox wait \
  --for workflow/reviewer/task-123 \
  --timeout 30s \
  --json
```

`wait` emits one compact delivery metadata object and exits. It never returns
message bodies or lease tokens, and it does not reserve the delivery. Add
`--full` when you need the full legacy metadata object.

Observe deliveries continuously without claiming them:

```bash
agent-mailbox watch \
  --for workflow/reviewer/task-123 \
  --timeout 30s \
  --json
```

`watch` emits delivery metadata only. It never returns message bodies or lease
tokens. Use `--yaml` to emit the same metadata as a YAML document stream.

Ack when processing succeeds:

```bash
agent-mailbox ack \
  --delivery <delivery_id> \
  --lease-token <lease_token>
```

## Receive

`recv` is always an immediate claim attempt.

Rules:

- `recv` returns immediately
- `--max` defaults to `1` and may not exceed `10`
- `--json` and `--yaml` are mutually exclusive
- no-message returns exit code `2`
- repeated `--for` flags search the union of the requested inboxes
- selection is global oldest-first by `visible_at`, then `message_created_at`,
  then `delivery_id`
- unseen addresses behave like empty inboxes
- `has_more=true` means the batch hit the requested max and more claimable mail remains

## Wait

`wait` is the one-shot observe-only companion to `recv`.

```bash
agent-mailbox wait --for <address> [--for <address> ...] [--timeout 30s] [--json | --yaml] [--full]
```

`--timeout` uses Go duration syntax such as `30s`, `5m`, `120ms`, or `1m30s`.

Rules:

- `wait` is observe-only; it does not claim deliveries, create lease tokens, or
  reserve the result
- repeated `--for` flags search the union of the requested inboxes
- duplicate `--for` values are ignored after the first occurrence
- default wait scope is currently visible queued deliveries
- `--json` emits one delivery metadata object
- `--yaml` emits the same metadata as one YAML mapping
- without `--timeout`, `wait` blocks until the first matching delivery exists
- `--timeout` is an absolute wait deadline; if no matching delivery appears
  before it expires, `wait` exits with code `2`
- selection is deterministic global oldest-first by `visible_at`, then
  `message_created_at`, then `delivery_id`
- unseen addresses behave like empty inboxes until a matching delivery exists

## Watch

`watch` is the observe-only companion to `recv`.

```bash
agent-mailbox watch --for <address> [--for <address> ...] [--state dead_letter] [--timeout 30s] [--json | --yaml]
```

`--timeout` uses Go duration syntax such as `30s`, `5m`, `120ms`, or `1m30s`.

Rules:

- `watch` always stays observe-only; it does not claim deliveries or create
  lease tokens
- repeated `--for` flags search the union of the requested inboxes
- duplicate `--for` values are ignored after the first occurrence
- default output watches currently visible queued deliveries
- `--state <state>` watches that delivery state instead
- `--json` emits one JSON object per line (NDJSON)
- `--yaml` emits one YAML document per matching delivery, each starting with `---`
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

Claim one or more deliveries for one or more recipient addresses.

```bash
agent-mailbox recv --for <address> [--for <address> ...] [--max 10] [--json | --yaml] [--full]
```

Use `--json` or `--yaml` for scripts and agents.

Notes:

- repeat `--for` to search multiple inboxes with one batch claim
- `--max <n>` limits how many deliveries one invocation can lease and may not exceed `10`
- duplicate `--for` values are ignored after the first occurrence
- without `--max`, default plain-text and structured output return a compact
  single-message view with `delivery_id`, `recipient_address`, `lease_token`,
  `subject`, `content_type`, and `body`
- add `--full` to return the full legacy single-message payload
- with `--max`, plain-text output prints each claimed message and appends
  `notice=more_messages_available` when additional claimable mail remains
- with `--max`, `--json` and `--yaml` emit a result object with `messages` and `has_more`
- with `--max --full`, each `messages[]` entry uses the full legacy payload
- unseen addresses are ignored until a matching delivery exists
- `recv` does not wait; use `wait` if you need to block until work appears

### `wait`

Observe until one matching queued delivery exists, then exit without claiming it.

```bash
agent-mailbox wait --for <address> [--for <address> ...] [--timeout 30s] [--json | --yaml] [--full]
```

Use `--json` or `--yaml` for scripts and agents.

Notes:

- `--timeout` uses Go duration syntax such as `30s`, `5m`, `120ms`, or `1m30s`
- repeat `--for` to search multiple inboxes with one wait
- duplicate `--for` values are ignored after the first occurrence
- plain-text output includes `recipient_address=...`
- default `wait` output is a compact metadata view with `delivery_id`,
  `recipient_address`, `subject`, and `content_type`
- add `--full` to return the full legacy delivery metadata schema used by `list`
  and `watch`
- `wait` does not claim or reserve the returned delivery; use `recv` to claim work

### `watch`

Observe matching deliveries without claiming them.

```bash
agent-mailbox watch --for <address> [--for <address> ...] [--state dead_letter] [--timeout 30s] [--json | --yaml]
```

Use `--json` or `--yaml` for streaming consumers.

- `--json` emits one delivery metadata object per line, not a JSON array
- `--yaml` emits one YAML document per delivery, separated by `---`

Notes:

- `--timeout` uses Go duration syntax such as `30s`, `5m`, `120ms`, or `1m30s`
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
agent-mailbox list --for <address> [--state dead_letter] [--json | --yaml]
```

Notes:

- default output shows currently claimable queued deliveries
- `--state dead_letter` shows dead-lettered deliveries
- `list` is a snapshot; use `wait` for one-shot blocking or `watch` for a stream
- use `--json` or `--yaml` for scripts and agents
- unseen addresses return an empty result

## Exit Codes

- `0`: success
- `2`: `recv` found no message, or `wait --timeout ...` found no matching
  delivery
- other non-zero: usage error or operational failure
