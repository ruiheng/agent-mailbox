# Waypost

`waypost` is a local-first handoff point for agent-managed workflows. It keeps
messages, delivery state, and audit history so one participant can persist work
immediately and another can claim it later.

The current MVP is intentionally narrow:

- one local Unix user on one machine
- direct delivery by endpoint address
- SQLite metadata plus blob-backed message bodies
- explicit `send`, `recv`, `wait`, `watch`, `list`, `stale`, `ack`, `renew`, `release`, `defer`, `undefer`, and `fail`
- no daemon, no network transport, no adapter-specific correctness dependency

## Rename note

Waypost was previously named `agent-mailbox`. New releases use the `waypost`
CLI, `waypost_*` MCP tools, and `WAYPOST_STATE_DIR`.

To move the previous default local state directory once, stop all
previous-version processes and run:

```bash
waypost migrate
```

The command moves the directory into the current Waypost state path. Across
filesystem boundaries on Unix-like systems, it durably copies the complete
state and writes a copy-commit marker before removing the old directory. On
Windows cross-volume migrations, it keeps the old directory as an explicit
recovery copy because Windows does not expose durable directory syncing; the
command reports that retained source. When the previous default state exists,
normal commands refuse to
initialize the new default state until you migrate it. A new migration refuses
to overwrite an existing destination; if an interrupted migration left its own
state behind, rerun the same command to finish it. While that migration is
incomplete, normal Waypost commands refuse to create or open a new database at
the destination. Source and destination paths must not overlap.

If an older interrupted migration lacks a durable copy-commit marker and its
source is already absent, Waypost refuses to guess that the destination is
complete; inspect or restore the source before retrying.

For a previous custom location, provide both paths explicitly:

```bash
waypost --state-dir /new/waypost-state migrate --from /old/legacy-state
```

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

On Windows without GNU `make`, use the PowerShell wrapper with the same targets:

```powershell
./make.ps1 build
```

This project still requires CGO. If `go env CGO_ENABLED` prints `0`, install a
Windows C toolchain first and rerun with CGO enabled, for example:

```powershell
$env:CGO_ENABLED = 1
$env:CC = "C:/msys64/ucrt64/bin/gcc.exe"
$env:CXX = "C:/msys64/ucrt64/bin/g++.exe"
./make.ps1 build
```

This produces:

```text
./bin/waypost
```

On Windows, the PowerShell wrapper builds:

```text
.\bin\waypost.exe
```

Run the full test suite:

```bash
make test
```

Windows:

```powershell
./make.ps1 test
```

Show the available build targets:

```bash
make help
```

Windows:

```powershell
./make.ps1 help
```

Run the stdio MCP server from the main binary:

```bash
make run-mcp
```

Windows:

```powershell
./make.ps1 run-mcp
```

## Install

Install into `/usr/local/bin`:

```bash
make build
sudo make install
```

On Windows, `install` writes a stable launcher into `%USERPROFILE%\.local\bin`
and stores the real CLI under `%USERPROFILE%\.local\lib\waypost\versions`:

```powershell
./make.ps1 install
```

This avoids replacing a running `waypost.exe`; existing processes keep
using their version, while new processes go through the launcher to the active
version recorded in `lib\waypost\active-version.json`.

Install into a user-local prefix without needing root:

```bash
make build
make install PREFIX="$HOME/.local"
```

Windows PowerShell equivalent:

```powershell
$env:PREFIX = "$HOME\\.local"
./make.ps1 install
```

If you use a user-local prefix, make sure `"$HOME/.local/bin"` is in your
`PATH`.

## User Guide

For day-to-day CLI usage, examples, receive semantics, exit codes, and command
reference, see [`docs/cli.md`](docs/cli.md).

## MCP Server

This repo ships an in-repo Go stdio MCP server that now runs as a built-in
subcommand of the main `waypost` binary.

Build the main binary locally:

```bash
make build
```

Run the MCP server directly:

```bash
./bin/waypost mcp
```

Windows:

```powershell
.\bin\waypost.exe mcp
```

Or use the convenience target:

```bash
make run-mcp
```

Example MCP config:

```toml
[mcp_servers.waypost]
command = "/absolute/path/to/waypost/bin/waypost"
args = ["mcp"]
```

Windows example:

```toml
[mcp_servers.waypost]
command = "C:\\absolute\\path\\to\\waypost\\bin\\waypost.exe"
args = ["mcp"]
```

The Go MCP entrypoint exposes these Waypost tool names:

- `waypost_bind`
- `waypost_status`
- `waypost_send`
- `waypost_forward`
- `waypost_wait`
- `waypost_recv`
- `waypost_list`
- `waypost_read`
- `waypost_ack`
- `waypost_release`
- `waypost_defer`
- `waypost_undefer`
- `waypost_fail`
- `waypost_debug`
- `waypost_group_create`
- `waypost_group_add_member`
- `waypost_group_remove_member`
- `waypost_group_members`
- `waypost_group_add_subscriber`
- `waypost_group_remove_subscriber`
- `waypost_group_subscribers`
- `waypost_address_inspect`
- `agent_deck_resolve_session`
- `agent_deck_create_session`
- `agent_deck_require_session`
- `session_resolve`
- `session_create`
- `session_require`

The host-neutral `session_create` tool takes the caller-supplied optional
strings `full_command_line` (Agent Deck) and `thurbox_agent_key` (Thurbox).
After host selection it consumes only the applicable value; it does not load a
Waypost profile mapping. Existing launchers may still pass
`waypost mcp --session-host-config PATH`; that deprecated option is accepted
and ignored.

Call `waypost_status` once after starting each MCP server process. It
auto-binds detectable session addresses from `agent-deck session current`,
tool environment variables such as `CODEX_THREAD_ID`, `CLAUDE_CODE_SESSION_ID`,
`GEMINI_SESSION_ID`, and `OPENCODE_SESSION_ID`. When an agent-deck session is
already known, it can also use the agent-deck state database to fill in a Codex
thread synced later for that same workdir and session.
That yields addresses such as `agent-deck/<session-id>`, `codex/<session-id>`,
`claude/<session-id>`, `gemini/<session-id>`, and `opencode/<session-id>`.
Except for `waypost_debug`, all other Waypost tools fail until
`waypost_status` succeeds, so callers get the
current binding state and any recovery warnings before they read, send, claim,
ack, or alter waypost state. If auto-bind cannot find a supported tool session
address, call `waypost_status` again after agent-deck has synced state for the
current session or call `waypost_bind` manually.
Tool session environment variable values must look like hex session ids; invalid
values are ignored and reported in the `waypost_status` warnings.

Use `waypost_debug` before or after `waypost_status` when auto-bind behavior is
unclear. It is read-only, does not auto-bind, and reports only allowlisted tool
session environment diagnostics for `CODEX_THREAD_ID`,
`CLAUDE_CODE_SESSION_ID`, `GEMINI_SESSION_ID`, and `OPENCODE_SESSION_ID`,
including whether each value is present, accepted by validation, and what
address it would produce. Its broader debug environment diagnostics also include
`AGENTDECK_INSTANCE_ID` and `TMUX`. On Linux it inspects the parent process
chain for those same allowlisted variables so callers can tell whether a tool
omitted a variable or failed to pass it into the MCP process.

`waypost_send` always uses the fixed wakeup text for supported remote notify
paths. Set `disable_notify_message = true` to skip only that immediate send-time
notify.

For MCP receivers that need to block until work appears, use `waypost_wait`
with a `timeout` such as `30s`, then call `waypost_recv` to claim the
delivery. `waypost_wait` is observe-only. `waypost_recv` has no `timeout`
parameter and returns immediately, so an abandoned long-running wait cannot
claim delivery into an unreachable result.

`waypost_forward` forwards exactly one stored message selected by `message_id`
or `delivery_id` to a new recipient through the normal `waypost_send` path. It
reuses the original body, `content_type`, and `schema_version`.

For group waypost flows over MCP, create a group with
`waypost_group_create`, manage people with `waypost_group_add_member` and
`waypost_group_remove_member`, then call `waypost_send` with `group = true`.
Use `waypost_wait` or `waypost_recv` with one `group/...` address and
`as_person` to read the group stream. Group reads return compact group message
payloads and do not use delivery leases, `waypost_ack`, `waypost_release`,
`waypost_defer`, `waypost_undefer`, or `waypost_fail`.

Use `waypost_group_add_subscriber` to register a routable notify target such as
`agent-deck/<session-id>` for group-message wakeups. Group send queues a normal
personal delivery for active subscribers whose `person` is a current group
member, skips a subscriber whose `notify_address` matches the sender's
`from_address`, rejects group-prefixed notify targets, and keeps immediate
external wake notification failure best-effort.

`agent_deck_create_session` is for lifecycle allocation only. It creates a new
session, errors if the target already exists, supports explicit group placement
through `group_path` or `group_parent_session_id` plus `child_group_name`, and
can launch detached sessions with `no_parent_link = true`. `startup_instruction`
is optional startup-only input passed to `agent-deck launch --message`; do not
use it for task payloads or normal wakeups.

`agent_deck_require_session` is the send-time guard. It never creates a
session; it resolves `session_id` or `session_ref`, verifies the existing
session already belongs to the explicit `workdir`, and starts it if needed.
`waypost_send` remains transport-only and does not create downstream sessions.
Pass `sessions` as a non-empty array of session IDs or refs to require multiple
sessions in one call. All batch items use the same explicit `workdir`, return
ordered `results`, and
report a per-session `error` without preventing the other sessions from being
required.

`agent_deck_resolve_session` is read-only. Pass one `session` for its original
single-session response, or pass `sessions` as a non-empty array to resolve
multiple references in one MCP call. Batch responses contain ordered `results`;
each item is a normal `found` or `not_found` response, or an `error` response
for an individual lookup failure without preventing the other lookups.

## Quick Start

Use `--state-dir` for demos and tests so waypost state stays isolated:

```bash
export WAYPOST_STATE_DIR=/tmp/waypost-demo
```

Address prefixes such as `workflow/...` or `agent/...` are naming conventions for
humans and tooling.

The default state directory is:

- `$WAYPOST_STATE_DIR` when set
- otherwise `$XDG_STATE_HOME/ai-agent/waypost`
- otherwise `~/.local/state/ai-agent/waypost`

You can set `WAYPOST_STATE_DIR` once, or pass `--state-dir` per command.
Structured-output commands (`send`, `forward`, `list`, `recv`, `wait`, `watch`, and `stale`) accept
either `--json` or `--yaml`, but not both together.
`send`, `forward`, `recv`, and `wait` also accept `--full` when you need the full legacy
payload instead of the default compact view.

## Minimal Example

Send a message from stdin:

```bash
printf 'review request body\n' | \
waypost --state-dir /tmp/waypost-demo \
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
waypost --state-dir /tmp/waypost-demo \
  send --to group/ops --group --from agent/sender --body-file - --json
```

Group send keeps personal send semantics unchanged: plain `send --to <address>`
still targets the personal queue path and fails if `<address>` is already a
known group address.

Open a local read-only transcript UI for group delivery:

```bash
waypost --state-dir /tmp/waypost-demo \
  group web --group group/ops
```

The web UI listens on `127.0.0.1:0` by default, so the OS chooses a free local
port. It prints the actual URL on startup and shows group messages without
marking them read. In an interactive terminal it also offers to open or copy the
URL. Use `--listen 127.0.0.1:8765` if you want a stable port.

Forward a stored message to a new recipient by message id or delivery id:

```bash
waypost --state-dir /tmp/waypost-demo \
  forward --message msg_123 --to workflow/reviewer/task-456 --from agent/sender --json
```

`forward` reuses the original body, `content_type`, and `schema_version`, and
defaults the subject to `Fwd: <original subject>` unless `--subject` overrides it.

Receive the next claimable message:

```bash
waypost --state-dir /tmp/waypost-demo \
  recv --for workflow/reviewer/task-123 --json
```

Receive across multiple queues with one command:

```bash
waypost --state-dir /tmp/waypost-demo \
  recv --for workflow/reviewer/task-123 --for workflow/reviewer/task-456 --json
```

Claim up to 10 messages in one call:

```bash
waypost --state-dir /tmp/waypost-demo \
  recv --for workflow/reviewer/task-123 --max 10 --json
```

Ask for the full receive payload only when you need internal metadata such as
lease expiry or blob references:

```bash
waypost --state-dir /tmp/waypost-demo \
  recv --for workflow/reviewer/task-123 --json --full
```

Observe matching queued deliveries without claiming them:

```bash
waypost --state-dir /tmp/waypost-demo \
  wait --for workflow/reviewer/task-123 --timeout 30s --json
```

The default `recv`/`wait` JSON and YAML payloads are intentionally compact.
Use `--full` to include the full legacy metadata shape.

Stream matching queued deliveries without claiming them:

```bash
waypost --state-dir /tmp/waypost-demo \
  watch --for workflow/reviewer/task-123 --timeout 30s --json
```

`--timeout` uses Go duration syntax such as `30s`, `5m`, `120ms`, or `1m30s`.

Use `--yaml` when you want the same `list`, `recv`, `wait`, `watch`, or `stale`
payloads in YAML. `wait` returns one YAML mapping. `watch` returns a YAML
document stream with one delivery per `---` document.

Find personal queues with receivable delivery older than a threshold:

```bash
waypost --state-dir /tmp/waypost-demo \
  stale --for workflow/reviewer/task-123 --older-than 10m --json
```

`stale` is structured-output-only in v1: use `--json` or `--yaml`.

Ack the leased delivery using the returned `delivery_id` and `lease_token`:

```bash
waypost --state-dir /tmp/waypost-demo \
  ack --delivery <delivery_id> --lease-token <lease_token>
```

Renew an active lease when a worker needs more time before acking:

```bash
waypost --state-dir /tmp/waypost-demo \
  renew --delivery <delivery_id> --lease-token <lease_token> --for 10m
```

Defer a leased delivery until a future time, then make it claimable early if the
blocking condition clears:

```bash
waypost --state-dir /tmp/waypost-demo \
  defer --delivery <delivery_id> --lease-token <lease_token> --until 2026-03-18T12:00:00Z

waypost --state-dir /tmp/waypost-demo \
  undefer --delivery <delivery_id>
```

After `undefer`, call `recv` again and use the new lease token before acking.

List previously acked deliveries for one queue:

```bash
waypost --state-dir /tmp/waypost-demo \
  list --for workflow/reviewer/task-123 --state acked --json
```

Read one persisted delivery body later by `delivery_id`:

```bash
waypost --state-dir /tmp/waypost-demo \
  read <delivery_id> --json
```

Read the same body directly by `message_id`:

```bash
waypost --state-dir /tmp/waypost-demo \
  read <message_id> --json
```

`read` infers a delivery when the ID starts with `dlv_`; all other IDs are
read as message IDs. The explicit `--delivery` and `--message` forms remain
available.

Reading by a message ID (with `--message` or a positional non-`dlv_` ID) is a
raw body read in the trusted local environment. It does not update group read
tracking; group `recv` remains the operation that records reads and advances
group unread/read state.

For the common "read the previous message from this queue" case, skip `list`
and read the latest acked delivery in one step:

```bash
waypost --state-dir /tmp/waypost-demo \
  read --latest --for workflow/reviewer/task-123 --json
```

For the full command reference, see [`docs/cli.md`](docs/cli.md).

`recv` v1 contract for multiple `--for` flags:

- repeated `--for` searches the union of the requested queues
- `--max` limits how many deliveries one command will claim, up to `10`
- default output returns a compact leased message view
- `--full` returns the full legacy leased-message payload
- when `--max` is provided, structured output returns `messages` plus `has_more`
- when `has_more=true`, additional claimable deliveries still remain after this batch
- unseen addresses behave like empty queues
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
  --state-dir /tmp/waypost-demo
```

The script:

- lists `waiting` and `idle` sessions
- runs `waypost stale` for their `agent-deck/<session-id>` queues
- waits briefly, rechecks both session status and staleness, then sends
  `agent-deck session send --no-wait` only if the session is still idle/waiting

Install it into a bin directory:

```bash
scripts/wake-stale-agent-deck-sessions.sh install --prefix "$HOME/.local"
```

`--all-delivery-states` tells the script to pass `--all` through to `agent-deck list`,
so it includes sessions that the default list view may hide.

## Local State Layout

The waypost state directory contains:

- `waypost.db`: authoritative SQLite state for endpoints, messages, deliveries,
  and events
- `blobs/`: immutable message body files referenced by `messages.body_blob_ref`

The event log is append-only audit history. Current-state tables remain the
source of truth for delivery behavior.

## MVP Boundaries

What this repository does today:

- durable direct waypost delivery
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
