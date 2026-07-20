# Waypost CLI

`waypost` is a local handoff CLI for one Unix user on one machine.

This guide is intentionally short. It covers what a user needs to run the CLI:

- where state lives
- the normal send/receive flow
- how immediate receive, observe-only wait, observe-only watch, and stale-address inspection work
- what each command is for

## State Directory

The waypost keeps all local state in one directory.

Resolution order:

- `$WAYPOST_STATE_DIR`
- `$XDG_STATE_HOME/ai-agent/waypost`
- `~/.local/state/ai-agent/waypost`

For demos or tests, use an isolated directory:

```bash
export WAYPOST_STATE_DIR=/tmp/waypost-demo
```

You can also override it per command:

```bash
waypost --state-dir /tmp/waypost-demo list --for workflow/reviewer/task-123
```

### Migrate previous local state

Stop all previous-version processes, then move the previous default state
directory to the new Waypost location:

```bash
waypost migrate
```

When the previous default state exists, normal commands require this migration
before they initialize the new default state. The destination must not already
exist for a new migration. If the migration is interrupted, rerun the same
command to finish it; normal Waypost commands will refuse to initialize the
target until then. Source and destination must not overlap. They may be on
different filesystems: on Unix-like systems, Waypost durably copies the
complete state and records a copy commit before removing the old directory. On
Windows cross-volume migrations it retains the old directory as a reported
recovery copy, because Windows does not expose durable directory syncing.

If an older interrupted migration has no durable copy-commit marker and its
source no longer exists, Waypost refuses to treat the destination as complete.

For a custom previous location, pass both paths explicitly:

```bash
waypost --state-dir /new/waypost-state migrate --from /old/legacy-state
```

## Web Transcript UI

Run a local read-only UI for group messages:

```bash
waypost --state-dir /tmp/waypost-demo group web --group group/ops
```

By default it listens on `127.0.0.1:0`, so the OS chooses a free local port and
the command prints the actual URL. The UI reads group message bodies and streams
updates without using `recv`, so it does not mark any participant's group
messages as read. In an interactive terminal it offers to open or copy the URL.

## Typical Flow

Send a message:

```bash
printf 'review request body\n' | \
waypost send \
  --to workflow/reviewer/task-123 \
  --from agent/sender \
  --subject "review request" \
  --body-file -
```

Receive a message:

```bash
waypost recv \
  --for workflow/reviewer/task-123 \
  --json
```

Swap `--json` for `--yaml` when you want the same payload in YAML.
Add `--full` when you need the full legacy payload instead of the default
compact view.

Receive from multiple addresses with one command:

```bash
waypost recv \
  --for workflow/reviewer/task-123 \
  --for workflow/reviewer/task-456 \
  --json
```

Claim multiple messages in one call:

```bash
waypost recv \
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
waypost wait \
  --for workflow/reviewer/task-123 \
  --timeout 30s \
  --json
```

`wait` emits one compact delivery metadata object and exits. It never returns
message bodies or lease tokens, and it does not reserve the delivery. Add
`--full` when you need the full legacy metadata object.

Observe deliveries continuously without claiming them:

```bash
waypost watch \
  --for workflow/reviewer/task-123 \
  --timeout 30s \
  --json
```

`watch` emits delivery metadata only. It never returns message bodies or lease
tokens. Use `--yaml` to emit the same metadata as a YAML document stream.

Find queues with receivable delivery older than a threshold:

```bash
waypost stale \
  --for workflow/reviewer/task-123 \
  --for workflow/reviewer/task-456 \
  --older-than 10m \
  --json
```

`stale` is personal-waypost-only and structured-output-only in v1. Use
`--json` or `--yaml`; plain-text mode is intentionally unsupported.

Ack when processing succeeds:

```bash
waypost ack \
  --delivery <delivery_id> \
  --lease-token <lease_token>
```

Renew an active lease when processing needs more time:

```bash
waypost renew \
  --delivery <delivery_id> \
  --lease-token <lease_token> \
  --for 10m
```

List already-acked deliveries later:

```bash
waypost list \
  --for workflow/reviewer/task-123 \
  --state acked \
  --json
```

Read one persisted delivery body later:

```bash
waypost read \
  --message <message_id> \
  --json
```

Read the latest delivery for one queue in one step:

```bash
waypost read \
  --latest \
  --for workflow/reviewer/task-123 \
  --json
```

Group waypost quick start:

```bash
waypost group create --group group/eng
waypost group add-member --group group/eng --person alice
printf 'team sync\n' | waypost send --to group/eng --group --body-file -
waypost list --for group/eng --as alice --json
waypost recv --for group/eng --as alice --json
```

## Group Waypost

Group waypost is explicit. It does not reuse lease/ack queue semantics.

Rules:

- create and reserve a group address with `group create`
- add or remove members with `group add-member` and `group remove-member`
- use `send --to <group-address> --group` for group messages
- use `list|wait|recv --for <group-address> --as <person>` for group reads
- `watch`, `ack`, `renew`, `release`, `defer`, `undefer`, and `fail` stay personal-waypost-only
- `--as` is caller-asserted identity in the trusted local workflow environment;
  it is not an authentication boundary

Group history semantics:

- new members can see older messages immediately
- older messages start unread for the new member
- leaving a group stops future eligibility but keeps historical visibility
- `eligible_count` is based on membership snapshot at message creation time
- later joins do not rewrite old messages' `eligible_count`

## Receive

`recv` is always an immediate claim attempt.

Rules:

- `recv` returns immediately
- `--max` defaults to `1` and may not exceed `10`
- `--json` and `--yaml` are mutually exclusive
- no-message returns exit code `2`
- repeated `--for` flags search the union of the requested queues
- selection is global oldest-first by `visible_at`, then `message_created_at`,
  then `delivery_id`
- unseen addresses behave like empty queues
- `has_more=true` means the batch hit the requested max and more claimable delivery remains

## Wait

`wait` is the one-shot observe-only companion to `recv`.

```bash
waypost wait --for <address> [--for <address> ...] [--timeout 30s] [--json | --yaml] [--full]
```

`--timeout` uses Go duration syntax such as `30s`, `5m`, `120ms`, or `1m30s`.

Rules:

- `wait` is observe-only; it does not claim deliveries, create lease tokens, or
  reserve the result
- repeated `--for` flags search the union of the requested queues
- duplicate `--for` values are ignored after the first occurrence
- default wait scope is currently visible queued deliveries
- `--json` emits one delivery metadata object
- `--yaml` emits the same metadata as one YAML mapping
- without `--timeout`, `wait` blocks until the first matching delivery exists
- `--timeout` is an absolute wait deadline; if no matching delivery appears
  before it expires, `wait` exits with code `2`
- selection is deterministic global oldest-first by `visible_at`, then
  `message_created_at`, then `delivery_id`
- unseen addresses behave like empty queues until a matching delivery exists

## Watch

`watch` is the observe-only companion to `recv`.

```bash
waypost watch --for <address> [--for <address> ...] [--state dead_letter] [--timeout 30s] [--json | --yaml]
```

`--timeout` uses Go duration syntax such as `30s`, `5m`, `120ms`, or `1m30s`.

Rules:

- `watch` always stays observe-only; it does not claim deliveries or create
  lease tokens
- repeated `--for` flags search the union of the requested queues
- duplicate `--for` values are ignored after the first occurrence
- default output watches currently visible queued deliveries
- `--state <state>` watches that delivery state instead
- `--json` emits one JSON object per line (NDJSON)
- `--yaml` emits one YAML document per matching delivery, each starting with `---`
- without `--timeout`, `watch` runs until interrupted
- `--timeout` is an idle timeout; if no newly matching delivery appears during
  that interval, `watch` exits successfully
- duplicate polling cycles do not reprint the same unchanged delivery snapshot
- unseen addresses behave like empty queues until matching deliveries exist

## Commands

### `mcp`

Run the built-in stdio MCP server from the main binary.

```bash
waypost mcp
```

Notes:

- this starts the waypost MCP server over stdio
- use the main `waypost` binary in MCP configs and pass `mcp` as the first argument
- `--state-dir` remains a global option on the main binary, but the MCP server manages waypost state through its own tool calls rather than per-command CLI flags

### `send`

Queue one message for a recipient address, or append one message to a known group
address when `--group` is set.

```bash
waypost send --to <address> --body-file <path-or-> [--group] [--json | --yaml] [--full]
```

Use `--json` or `--yaml` for scripts and agents.

Common options:

- `--from <address>`
- `--subject <text>`
- `--content-type <mime-type>`
- `--schema-version <version>`
- `--group`

Notes:

- `--body-file -` reads from stdin
- the message body must not be empty
- personal send default output is a compact acknowledgement with `delivery_id`
- personal `--full` returns the legacy identifier payload with `message_id`,
  `delivery_id`, and `blob_id`
- `send` creates the recipient address automatically on first use
- `send` also creates the optional `--from` address automatically on first use
- `--from` is optional
- `send --to <address>` always uses personal queue semantics
- personal send to a known group address fails with an explicit collision error
- `send --to <group-address> --group` requires an existing group address
- group send stores one durable message record, creates no `deliveries`, and
  returns structured output with `mode`, `message_id`, `group_id`,
  `group_address`, `eligible_count`, and `message_created_at`
- group send plain-text output is
  `message_id=<id> group=<address> eligible_count=<n>`

### `recv`

Claim one or more personal deliveries, or receive one unread group message for a
specific person.

```bash
waypost recv --for <address> [--for <address> ...] [--max 10] [--json | --yaml] [--full]
waypost recv --for <group-address> --as <person> [--json | --yaml] [--full]
```

Use `--json` or `--yaml` for scripts and agents.

Notes:

- repeat `--for` to search multiple queues with one batch claim
- `--max <n>` limits how many deliveries one invocation can lease and may not exceed `10`
- duplicate `--for` values are ignored after the first occurrence
- without `--max`, default plain-text and structured output return a compact
  single-message view with `delivery_id`, `recipient_address`, `lease_token`,
  `subject`, `content_type`, and `body`
- add `--full` to return the full legacy single-message payload
- with `--max`, plain-text output prints each claimed message and appends
  `notice=more_messages_available` when additional claimable delivery remains
- with `--max`, `--json` and `--yaml` emit a result object with `messages` and `has_more`
- with `--max --full`, each `messages[]` entry uses the full legacy payload
- unseen addresses are ignored until a matching delivery exists
- `recv` does not wait; use `wait` if you need to block until work appears
- group mode requires `--as <person>` and exactly one `--for`
- group mode does not support `--max`
- group mode returns the oldest unread visible group message for that person and
  marks it read immediately
- compact group `recv` output includes `message_id`, `group_id`,
  `group_address`, `person`, `message_created_at`, `subject`, `content_type`,
  `body`, `read_count`, `eligible_count`, and `first_read_at`
- group `recv --full` adds `sender_endpoint_id`, `schema_version`,
  `body_blob_ref`, `body_size`, and `body_sha256`

### `wait`

Observe until one matching queued delivery exists, or until one unread visible
group message exists for a specific person.

```bash
waypost wait --for <address> [--for <address> ...] [--timeout 30s] [--json | --yaml] [--full]
waypost wait --for <group-address> --as <person> [--timeout 30s] [--json | --yaml] [--full]
```

Use `--json` or `--yaml` for scripts and agents.

Notes:

- `--timeout` uses Go duration syntax such as `30s`, `5m`, `120ms`, or `1m30s`
- repeat `--for` to search multiple queues with one wait
- duplicate `--for` values are ignored after the first occurrence
- plain-text output includes `recipient_address=...`
- default `wait` output is a compact metadata view with `delivery_id`,
  `recipient_address`, `subject`, and `content_type`
- add `--full` to return the full legacy delivery metadata schema used by `list`
  and `watch`
- `wait` does not claim or reserve the returned delivery; use `recv` to claim work
- group mode requires `--as <person>` and exactly one `--for`
- group `wait` stays observe-only; it does not mark the message read
- compact group `wait` output includes `message_id`, `group_id`,
  `group_address`, `person`, `message_created_at`, `subject`, `content_type`,
  `read`, `first_read_at`, `read_count`, and `eligible_count`
- group `wait --full` adds `sender_endpoint_id` and `schema_version`

### `watch`

Observe matching deliveries without claiming them.

```bash
waypost watch --for <address> [--for <address> ...] [--state dead_letter] [--timeout 30s] [--json | --yaml]
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

### `read`

Read one or more persisted messages, one or more deliveries by id, or the
latest deliveries for one or more queues.

```bash
waypost read --message <id> [--message <id> ...] [--json | --yaml]
waypost read --delivery <id> [--delivery <id> ...] [--json | --yaml]
waypost read --latest --for <address> [--for <address> ...] [--state <state>] [--limit <n>] [--json | --yaml]
```

Use `--json` or `--yaml` for scripts and agents.

Notes:

- `--message` reads by message identity, which matches the body-bearing object
- `--message` is a raw trusted-environment body read and does not update group
  read tracking
- `--delivery` reads by one or more delivery records, regardless of whether they are
  `queued`, `leased`, `acked`, or `dead_letter`
- `--latest` requires at least one `--for`
- `--latest` defaults to no state filter (`any`) and `--limit 1`
- `--latest` searches the union of the requested queues and returns newest-first
- structured output always returns an object with `items` and `has_more`
- returns the persisted body after verifying the blob size and sha256
- plain-text output prints one item after another, separated by `---`
- `--message` items return message metadata plus `body`
- `--delivery` and `--latest` items also include delivery metadata such as
  `state`, recipient, and `acked_at` when present

### `ack`

Mark a leased delivery as complete.

```bash
waypost ack --delivery <delivery_id> --lease-token <lease_token>
```

### `renew`

Extend a current lease without changing the lease token.

```bash
waypost renew --delivery <delivery_id> --lease-token <lease_token> --for 10m
```

Notes:

- `--for` uses Go duration syntax such as `30s`, `5m`, `10m`, or `1h`
- renewal requires the current lease token; expiry only allows another receiver
  to reclaim and replace that token
- renewal keeps the same `lease_token` and only updates `lease_expires_at`

### `release`

Return a leased delivery to the queue immediately.

```bash
waypost release --delivery <delivery_id> --lease-token <lease_token>
```

### `defer`

Return a leased delivery to the queue, but hide it until a future time.

```bash
waypost defer \
  --delivery <delivery_id> \
  --lease-token <lease_token> \
  --until 2026-03-18T12:00:00Z
```

### `undefer`

Make a deferred queued delivery visible immediately.

```bash
waypost undefer --delivery <delivery_id>
```

Notes:

- `undefer` does not restore the old lease or acknowledge the delivery
- after `undefer`, call `recv` again and use the new lease token for `ack`
- if the delivery is already visible, call `recv` instead of `undefer`

### `fail`

Record a processing failure.

```bash
waypost fail \
  --delivery <delivery_id> \
  --lease-token <lease_token> \
  --reason "tool crashed"
```

Retry behavior in v1:

- attempts 1 and 2 requeue immediately
- attempt 3 moves the delivery to `dead_letter`

### `list`

Inspect queued personal deliveries for one recipient address, or inspect group
message metadata visible to one person.

```bash
waypost list --for <address> [--state queued|leased|acked|dead_letter] [--json | --yaml]
waypost list --for <group-address> --as <person> [--json | --yaml]
```

Notes:

- default output shows currently claimable queued deliveries
- `--state queued|leased|acked|dead_letter` filters to that personal delivery state
- `acked` results include `acked_at` in structured output and plain text
- `list` is a snapshot; use `wait` for one-shot blocking or `watch` for a stream
- use `--json` or `--yaml` for scripts and agents
- unseen addresses return an empty result
- group mode requires `--as <person>`
- `--state` is not supported with `--as`
- group `list` returns visible group message metadata oldest-first
- compact group `list` output includes `message_id`, `group_id`,
  `group_address`, `person`, `message_created_at`, `subject`, `content_type`,
  `read`, `first_read_at`, `read_count`, and `eligible_count`

### `stale`

List personal queues whose oldest currently receivable delivery is older than
the requested threshold.

```bash
waypost stale --for <address> [--for <address> ...] --older-than 10m [--json | --yaml]
```

Use `--json` or `--yaml`. One of them is required.

Notes:

- `--older-than` uses Go duration syntax such as `30s`, `5m`, `120ms`, or `1h`
- repeat `--for` to check multiple queues in one query
- duplicate `--for` values are ignored after the first occurrence
- plain-text mode is not supported in v1
- unseen addresses behave like empty queues
- known group addresses fail explicitly; this command is personal-waypost-only
- queued deliveries count when `visible_at <= now`
- expired leased deliveries count when `lease_expires_at <= now`
- future invisible deliveries do not count as stale
- results are ordered by `oldest_eligible_at`, then `address`
- each result object contains `address`, `oldest_eligible_at`, and `claimable_count`

### `group create`

Reserve a group address explicitly.

```bash
waypost group create --group <address> [--json | --yaml]
```

Notes:

- group addresses are explicit objects; there is no lazy create during send
- creating a group on top of an existing endpoint address fails
- structured output returns `group_id`, `address`, and `created_at`

### `group add-member`

Add one person to an existing group.

```bash
waypost group add-member --group <address> --person <person> [--json | --yaml]
```

Notes:

- the group must already exist
- the person record is created automatically on first use
- adding the same active member twice fails explicitly
- structured output returns membership metadata including `membership_id`,
  `group_id`, `group_address`, `person_id`, `person`, `joined_at`, and `active`

### `group remove-member`

Close the active membership for one person in a group.

```bash
waypost group remove-member --group <address> --person <person> [--json | --yaml]
```

Notes:

- removing a person who has no active membership fails explicitly
- historical visibility remains even after removal
- structured output returns the membership record with `left_at` and `active=false`

### `group members`

List active and historical membership records for one group.

```bash
waypost group members --group <address> [--json | --yaml]
```

Notes:

- output is ordered newest membership first
- each entry includes `membership_id`, `group_id`, `group_address`, `person_id`,
  `person`, `joined_at`, optional `left_at`, and `active`

### `address inspect`

Inspect whether an address is currently unbound, a personal endpoint, or a
group.

```bash
waypost address inspect --address <address> [--json | --yaml]
```

Notes:

- structured output returns `address`, `kind`, and the relevant id field
- `kind` is one of `endpoint`, `group`, or `unbound`
- this is a debugging and introspection command; it does not change routing

## Exit Codes

- `0`: success
- `2`: `recv` found no message, or `wait --timeout ...` found no matching
  delivery
- other non-zero: usage error or operational failure
