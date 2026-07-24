# Waypost MCP And CLI Surface

## Scope

This design changes only the Waypost repository and the `waypost` binary.

It does not add or modify:

- Agent Deck CLI commands or session behavior
- host-side adapters or process execution protocols
- external skills or prompt repositories
- cross-repository configuration

The goal is one small MCP surface for frequent agent work, with the complete
Waypost capability surface available through CLI.

## Hard-Cut Decision

Waypost MCP exposes exactly fourteen tools after this change. There is no
`full` profile, `hybrid` profile, profile flag, legacy tool set, capability
manifest, or runtime capability registry.

The hard-cut condition is simple: every removed MCP operation must already have
a complete structured CLI path and concise `waypost doc` guidance. Once that
condition is met, the old MCP registrations are deleted.

CLI completeness is the functional replacement. Existing MCP response shapes
and removed MCP tool names are not preserved.

## MCP Surface

The retained tools are:

- `waypost_status`
- `waypost_bind`
- `waypost_debug`
- `waypost_send`
- `waypost_forward`
- `waypost_recv`
- `waypost_claim_history`
- `waypost_ack`
- `waypost_release`
- `waypost_defer`
- `waypost_fail`
- `agent_deck_resolve_session`
- `agent_deck_create_session`
- `agent_deck_require_session`

### Why they remain

`status`, `bind`, and `debug` own live state in the long-running MCP
process. A separate CLI process cannot repair that MCP instance.

`send`, `recv`, claim history, and the four lease lifecycle operations form
the common message path. Receive and completion also interact with the MCP
active-lease tracker and renewal loop.

`forward` remains because the current MCP path includes target notification
behavior not yet provided by CLI forward.

The three Agent Deck session tools remain because they are frequent structured
operations. Their implementation is unchanged and remains outside the rest of
this design.

Lease lifecycle operations stay separate. There is no synthetic `settle`
operation.

## CLI-Owned Operations

These MCP tools are deleted:

- `waypost_wait`
- `waypost_list`
- `waypost_read`
- `waypost_undefer`
- `waypost_group_create`
- `waypost_group_add_member`
- `waypost_group_remove_member`
- `waypost_group_members`
- `waypost_group_add_subscriber`
- `waypost_group_remove_subscriber`
- `waypost_group_subscribers`
- `waypost_address_inspect`

They operate on durable Waypost state and do not need live MCP binding or lease
tracker mutation when address, group, person, and state directory are explicit.

Before deletion:

- add group subscriber add, remove, and list CLI commands
- add structured JSON output to `undefer`
- preserve acknowledged-message recovery through `read --latest --state acked`
- freeze success, empty-result, ordering, error, exit-code, and stream behavior
  with integration tests
- provide a relevant `waypost doc` topic

## Agent-Facing CLI Contract

Agent guidance uses one canonical `--json` path. Text and YAML remain
human-facing formats and are not part of the MCP replacement contract.

For CLI-owned operations:

- success: exit `0`, one JSON document on stdout, empty stderr
- empty collection: exit `0` with `[]` or `{"items":[]}`
- `wait` with no matching message: exit `2`, both streams empty
- failure: exit `1`, empty stdout, one JSON error document on stderr

The error document is:

```json
{
  "status": "error",
  "error_code": "not_found",
  "message": "delivery \"dlv_...\" not found",
  "retryable": false
}
```

Stable error codes are:

- `invalid_argument`: missing, malformed, or conflicting input
- `not_found`: an explicitly named object does not exist
- `already_exists`: create or add duplicates active state
- `invalid_state`: the object exists but the transition is illegal
- `integrity_error`: body metadata does not match its blob
- `busy`: transient SQLite contention
- `receive_recovery_required`: post-claim bookkeeping failed and at least one
  new lease could not be rolled back
- `internal`: unexpected storage or I/O failure

Only `busy` is retryable. An unknown code is treated as non-retryable
`internal`; the agent stops instead of guessing.

## CLI Operation Matrix

Full record names below refer to the existing exported Waypost JSON records.

| Removed MCP tool | Canonical CLI route | Success and ordering | Empty/error behavior | Doc topic |
| --- | --- | --- | --- | --- |
| `waypost_wait` | `wait --for ADDRESS [--as PERSON] --timeout D --json` | Personal compact delivery or group compact message; oldest eligible first | No match: exit `2`, silent. Unknown group: `not_found` | `mcp-cli-boundary` |
| `waypost_list` | `list --for ADDRESS [--state STATE | --as PERSON] --json` | Personal `ListedDelivery[]`, visible/created/id ascending; group summaries created/id ascending | Unknown personal address: `[]`. Unknown group: `not_found` | `history` |
| `waypost_read` | `read (--delivery ID... | --message ID... | --latest --for ADDRESS... [--state STATE] [--limit N]) --json` | `{"items":[...]}`; direct reads preserve input order and latest reads are newest first | Missing direct id: atomic `not_found`. Unknown latest address: empty items. Blob mismatch: `integrity_error` | `recovery`, `history` |
| `waypost_undefer` | `undefer --delivery ID --json` | `delivery_id`, `state`, `visible_at`, `attempt_count` | Missing: `not_found`. Non-deferred state: `invalid_state` | `recovery` |
| `waypost_group_create` | `group create --group ADDRESS --json` | `GroupRecord` | Existing group: `already_exists`; endpoint-owned address: `invalid_state` | `groups` |
| `waypost_group_add_member` | `group add-member --group ADDRESS --person PERSON --json` | `GroupMembershipRecord` | Missing group: `not_found`; active membership: `already_exists` | `groups` |
| `waypost_group_remove_member` | `group remove-member --group ADDRESS --person PERSON --json` | Updated `GroupMembershipRecord` | Missing group: `not_found`; no active membership: `invalid_state` | `groups` |
| `waypost_group_members` | `group members --group ADDRESS --json` | Memberships joined/id ascending, including history | Missing group: `not_found` | `groups` |
| `waypost_group_add_subscriber` | `group add-subscriber --group ADDRESS --notify-address ADDRESS --person PERSON --json` | `GroupNotificationSubscriberRecord` | Missing group: `not_found`; active subscriber: `already_exists` | `groups` |
| `waypost_group_remove_subscriber` | `group remove-subscriber --group ADDRESS --notify-address ADDRESS --json` | Updated subscriber record | Missing group: `not_found`; no active subscriber: `invalid_state` | `groups` |
| `waypost_group_subscribers` | `group subscribers --group ADDRESS --json` | Active subscribers created/id ascending | Missing group: `not_found` | `groups` |
| `waypost_address_inspect` | `address inspect --address ADDRESS --json` | `AddressInspection`; unbound is a successful `kind: "unbound"` result | Malformed address: `invalid_argument` | `diagnostics` |

## MCP Registration

Registration remains explicit and typed. There is no metadata-driven
dispatcher or capability table.

Conceptually:

```go
func registerWaypostTools(server *mcp.Server) {
    registerStatusBindDebug(server)
    registerMessagePath(server)
    registerLeaseLifecycle(server)
    registerAgentDeckSessionTools(server)
}
```

Tests assert the exact fourteen tool names. A removed tool appearing in the
MCP list is a test failure.

`waypost_status` continues to report the live MCP information needed to use
CLI against the same Waypost state:

```json
{
  "server_version": "<version>",
  "executable": "/absolute/path/to/waypost",
  "resolved_state_dir": "/absolute/path/to/state",
  "bound_addresses": ["ADDRESS"],
  "default_sender": "ADDRESS"
}
```

An agent invoking CLI uses that executable and state directory and passes
address, group, and person explicitly. It does not guess from `PATH` or an
unrelated Waypost installation. How an MCP host executes a local process is
outside this design.

## Receive Contract

`recv.has_more` is deleted from MCP and CLI results. No legacy serializer,
alias, schema selector, or fallback remains.

Waypost continues to support concurrent receive and CLI batch receive. MCP
receive claims at most one delivery per call; atomic claim behavior decides
which concurrent caller receives work.

### MCP personal receive

Received:

```json
{
  "status": "received",
  "addresses": ["ADDRESS"],
  "delivery": {
    "delivery_id": "dlv_...",
    "recipient_address": "ADDRESS",
    "lease_token": "lease_...",
    "subject": "subject",
    "content_type": "text/plain",
    "body": "body"
  },
  "remaining_by_state": {"queued": 2, "leased": 1},
  "warnings": []
}
```

No message:

```json
{
  "status": "no_message",
  "addresses": ["ADDRESS"],
  "remaining_by_state": {"queued": 2},
  "warnings": []
}
```

Active leases:

```json
{
  "status": "active_leases",
  "addresses": ["ADDRESS"],
  "active_lease_count": 1,
  "claimed_delivery_ids": ["dlv_..."],
  "known_delivery_ids": [],
  "claim_history_tool": "waypost_claim_history",
  "remaining_by_state": {"leased": 1},
  "warnings": []
}
```

`remaining_by_state` is a top-level sibling of the status-specific payload.
Existing active-lease hints remain.

### CLI personal receive

Single JSON receive uses the same `status`, `addresses`, `delivery`, and
`remaining_by_state` fields as MCP.

Batch receive replaces `delivery` with ordered `deliveries`. Each element
uses the same compact delivery fields as single receive.

```json
{
  "status": "received",
  "addresses": ["ADDRESS_A", "ADDRESS_B"],
  "deliveries": [
    {"delivery_id": "dlv_1", "recipient_address": "ADDRESS_A"},
    {"delivery_id": "dlv_2", "recipient_address": "ADDRESS_B"}
  ],
  "remaining_by_state": {"queued": 3}
}
```

Batch order is `visible_at`, message creation time, then delivery id,
ascending.

CLI no-message returns exit `2`, empty stderr, and:

```json
{
  "status": "no_message",
  "addresses": ["ADDRESS"],
  "remaining_by_state": {"queued": 2}
}
```

### Remaining-state semantics

- use the same resolved personal address scope as the receive call
- exclude every delivery returned by the current call
- exclude `acked`
- possible keys are `queued`, `leased`, and `dead_letter`
- future-visible deferred deliveries remain `queued`
- omit zero-valued keys
- omit the whole map when all counts are zero
- include the map on MCP `received`, `no_message`, and `active_leases`
- include the map on CLI `received` and `no_message`
- never interpret `queued` as claimable-now work; only `recv` or `wait`
  answers availability

The count is an informational snapshot immediately after the receive-path
decision and any claims. Concurrent sends and state transitions may change it
immediately.

Use one grouped `COUNT(*)` query over resolved endpoint ids and unfinished
states, excluding returned ids. It must not read message rows or body blobs.

### Post-claim count failure

A receive call must not hide a newly claimed lease behind an ordinary error.

After successful claims:

1. run the remaining-state query
2. if it fails, release every new claim back to `queued` at its pre-claim
   `visible_at`
3. if every release succeeds, return the original count error
4. if any release fails, return only the unreleased claims in an explicit
   recovery result

Each unreleased claim contains `delivery_id`, `lease_token`,
`recipient_address`, and `lease_expires_at`.

MCP tracks and renews every unreleased claim before returning:

```json
{
  "status": "receive_recovery_required",
  "addresses": ["ADDRESS"],
  "error_code": "receive_recovery_required",
  "message": "remaining-state query failed and claim rollback was incomplete",
  "remaining_by_state_status": "unavailable",
  "claims": [
    {
      "delivery_id": "dlv_...",
      "lease_token": "lease_...",
      "recipient_address": "ADDRESS",
      "lease_expires_at": "RFC3339"
    }
  ],
  "claim_history_tool": "waypost_claim_history",
  "release_tool": "waypost_release"
}
```

CLI returns exit `1`, empty stdout, and the normal JSON error document with:

```json
{
  "status": "error",
  "error_code": "receive_recovery_required",
  "message": "remaining-state query failed and claim rollback was incomplete",
  "retryable": false,
  "details": {
    "remaining_by_state_status": "unavailable",
    "claims": [
      {
        "delivery_id": "dlv_...",
        "lease_token": "lease_...",
        "recipient_address": "ADDRESS",
        "lease_expires_at": "RFC3339"
      }
    ]
  }
}
```

The caller releases every listed claim before another receive. It must not ack,
defer, or fail a claim without the missing message context.

`remaining_by_state` omission means zero only for normal receive statuses.
Recovery results explicitly report `remaining_by_state_status: "unavailable"`.

### Group receive

Group receive marks one group message read for one person and does not use
personal delivery states.

MCP and CLI use the same `status`, `addresses`, `as_person`, and `message`
fields. CLI no-message returns exit `2` with a `status: "no_message"` JSON
document.

Group receive never returns `remaining_by_state`, `has_more`, or another
remaining-count field.

### Count-query performance

Two checks prevent an unbounded receive-path regression:

1. `EXPLAIN QUERY PLAN` must use
   `idx_deliveries_recipient_state_visible` and must not scan deliveries.
2. A dedicated Linux amd64 benchmark seeds 100 addresses and 100,000
   deliveries: 50,000 visible queued, 10,000 future-visible queued, 20,000
   leased, 10,000 dead-letter, and 10,000 acked. It runs 30 interleaved
   count-free/count-enabled pairs on independent fixture copies. Regression
   fails when count-enabled p95 is both more than 25% and more than 2 ms slower.

The benchmark records SQLite version, pragmas, CPU, storage class, and raw
samples. The performance threshold is blocking only on the declared benchmark
runner; the query-plan assertion is blocking everywhere.

## Read Pagination

Structured `read` always returns `items`.

- latest read emits `has_more: true` only when another matching item exists
  beyond the requested limit
- false is omitted
- direct message-id and delivery-id reads omit the field

## `waypost doc` Agent Prompts

`docs/cli.md` remains a human/operator manual and is never loaded wholesale
as an agent prompt.

Add:

```text
waypost doc
waypost doc --list
waypost doc <topic>
```

Initial topics:

- `mcp-cli-boundary`
- `recovery`
- `history`
- `groups`
- `diagnostics`

Do not create one topic per command. Topics exist only when workflow guidance
beyond `--help` is needed.

### Audience

The audience is an agent performing a Waypost CLI task.

The prompt may explain:

- when a CLI-owned operation is appropriate
- required explicit state directory and Waypost identity
- the shortest safe JSON command sequence
- fields that change the next action
- Waypost-specific stop and recovery conditions

It must not mention:

- Agent Deck session creation, restart, hierarchy, or configuration
- planner, reviewer, coder, browser-tester, or roundtable role policy
- git branches, worktrees, commits, or review workflow
- how an MCP host executes local processes
- provider-specific addresses as authoritative examples
- YAML, legacy payloads, migration history, Web UI, or demo setup

It does not teach CLI `send`, `recv`, `ack`, `release`, `defer`, or
`fail` as the normal agent path because those operations remain direct MCP
tools.

### Prompt shape

Each topic uses:

```markdown
# <task>
Use when: <one sentence>

## Required context
- <values the command cannot infer safely>

## Do
1. <short command or decision>

## Interpret
- <fields that change the next action>

## Stop
- <conditions where the agent must not guess>
```

Rules:

- one canonical JSON path
- no flag catalog copied from `--help`
- no fields that do not affect the next action
- placeholders such as `ADDRESS` and `DELIVERY_ID`
- branch on `error_code`; retry only when `retryable` is true
- at most 300 words per initial topic

Topic responsibilities:

- `mcp-cli-boundary`: retained Waypost MCP operations, CLI-owned operation
  groups, and binary/state-directory mismatch stop rule
- `recovery`: acknowledged-input recovery, empty items, sparse read
  `has_more: true`
- `history`: list/read history and message-id versus delivery-id
- `groups`: membership and subscriber management with explicit identities
- `diagnostics`: address inspection and live MCP binding versus durable state

## Verification

Automated checks cover:

- MCP exposes exactly the fourteen retained tool names
- every deleted MCP tool is absent
- every deleted tool's CLI route satisfies the operation matrix
- status, bind, and debug bootstrap/repair behavior
- send, forward, recv, claim history, and lease lifecycle through MCP
- Agent Deck resolve/create/require behavior remains unchanged
- CLI JSON error codes, retryability, exit codes, stdout, and stderr
- `recv` never emits `has_more`
- `remaining_by_state` is sparse and excludes returned and acked deliveries
- deferred queued deliveries are counted but never described as claimable now
- group receive never returns remaining counts
- complete count-failure rollback returns no hidden lease
- incomplete rollback returns every unreleased id/token and MCP tracks exactly
  those claims
- concurrent and batch claims retain existing correctness
- `read.has_more` appears only when true
- every prompt stays within its word budget and contains no forbidden material
- `docs/cli.md` remains outside embedded prompt resources
- the count query passes query-plan and benchmark checks

## Risks

- An MCP tool could be removed before its CLI path is complete. The hard-cut
  test matrix makes that impossible to merge.
- Prompt text can drift from CLI mechanics. Embedding prompts in the binary and
  testing their commands keeps them version-matched.
- Remaining-state counting adds receive-path work. The index, query-plan test,
  rollback invariant, and benchmark bound the risk.
- Fourteen tools are not the theoretical minimum, but each retained tool is
  justified by frequency or live MCP state.

## Rejected Alternatives

### Runtime profiles

Rejected. There is one intended MCP surface, so `full|hybrid` creates a legacy
mode and rollout machinery with no product value.

### Public capabilities command or internal capability registry

Rejected. Tool registration is a small explicit typed list. Tests, not runtime
metadata, prove CLI completeness and the exact MCP surface.

### Keep every CLI command as MCP

Rejected because uncommon schemas and choices consume agent context on every
turn.

### Move lease completion to CLI

Rejected because the MCP active-lease tracker and renewal loop must observe
completion.

### Move Agent Deck session tools to CLI

Rejected because they are frequent structured operations and are explicitly
retained.

### Generic `waypost_command` MCP tool

Rejected because it hides the same broad surface behind weaker validation.

### Use `docs/cli.md` as the prompt

Rejected because it mixes operator setup, every command, multiple output modes,
and human-oriented detail.

## Implementation Order

1. complete missing subscriber and `undefer --json` CLI paths
2. add the stable CLI JSON error contract and operation-matrix tests
3. add concise embedded `waypost doc` topics and prompt tests
4. hard-cut MCP registration to the fourteen retained typed tools
5. delete `recv.has_more` and add exact personal/group receive results
6. add remaining-state counts and post-claim rollback/recovery
7. add executable and resolved state directory to `waypost_status`
8. run the full CLI, MCP, concurrency, prompt, query-plan, and benchmark suite
