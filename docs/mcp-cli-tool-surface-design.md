# Waypost-Only MCP And CLI Surface

## Scope

This design changes only the Waypost repository and the `waypost` binary.

It does not add or modify:

- Agent Deck CLI commands
- Agent Deck session lifecycle or restart behavior
- host-side command adapters or request-file protocols
- cross-binary attestations
- external skills or prompt repositories

The existing `agent_deck_resolve_session`, `agent_deck_create_session`, and
`agent_deck_require_session` MCP tools remain available because they are common
structured workflow operations already exposed by Waypost MCP. Keeping them
does not expand this design into Agent Deck implementation work.

## Problem

Waypost MCP currently exposes twenty-six tools. Most agents use only a small
subset during ordinary message workflows, but every agent pays the schema and
tool-selection cost for uncommon history, group-administration, inspection,
and recovery operations.

Moving uncommon operations to CLI is useful only if the CLI also provides
short, task-oriented guidance. `--help` explains syntax; it does not tell an
agent when to use a command, which identity must be explicit, how to interpret
the result, or when to stop instead of guessing.

The solution is:

1. keep common and live-MCP-state operations as direct MCP tools
2. keep the complete durable-state capability surface in the Waypost CLI
3. add concise `waypost doc <topic>` prompts for CLI-only tasks

## Fixed Decisions

- `waypost_status` remains the bootstrap gate because automatic binding does
  not always succeed.
- `waypost_bind` and `waypost_debug` remain beside status so the current MCP
  instance can be repaired and inspected.
- `agent_deck_resolve_session`, `agent_deck_create_session`, and
  `agent_deck_require_session` remain MCP tools because they are frequent.
- `ack`, `release`, `defer`, and `fail` remain separate tools. There is no
  synthetic `settle` operation.
- Waypost supports concurrent and batch receive. Agent workflows may choose a
  stricter one-at-a-time policy, but that is not a Waypost restriction.
- Personal `recv` reports sparse remaining unfinished counts and does not emit
  `has_more`.
- Group `recv` gets no remaining-count field.
- `read` emits `has_more: true` only when a latest read is truncated; otherwise
  the field is omitted.

## Target Hybrid MCP Surface

The target hybrid profile exposes fourteen tools:

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

### Why these stay in MCP

`status`, `bind`, and `debug` own live binding and detection state in the
long-running MCP process. A separate CLI invocation cannot update that state.

`send`, `recv`, claim history, and the four lease lifecycle operations are the
normal message path. Personal receive and completion also interact with the
MCP active-lease tracker and renewal loop.

`forward` remains MCP because the current CLI path does not provide the same
target notification behavior as the MCP send path.

The three Agent Deck session tools stay because they are frequent structured
operations. This design does not change their implementation.

## CLI-Owned Operations

The hybrid profile removes these MCP tools only after their CLI parity gates
pass:

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

These operate on durable Waypost state and do not need live MCP-only bindings
or lease-tracker mutation when all identities are explicit.

### Required parity before removal

- add structured CLI commands for group subscriber add, remove, and list
- make `undefer` return stable structured output
- preserve acknowledged-history recovery with:

  ```text
  waypost --state-dir D read \
    --latest --for ADDRESS --state acked --limit 1 --json
  ```

- return delivery id, message id, and body for that recovery read
- preserve deterministic newest-first ordering
- keep each MCP tool until its exact CLI success, empty-result, error, ordering,
  exit-code, output-stream, and prompt contracts pass parity tests

`waypost_forward` is not removed until CLI forward has notification parity.

### Agent-facing CLI machine contract

The parity path is `--json`. Existing text and YAML output remain human-facing
formats and are not used to justify MCP removal.

For every CLI-owned operation:

- success returns exit `0`, one JSON document on stdout, and empty stderr
- an expected empty collection returns exit `0` with `[]` or `{"items":[]}`
- `wait` with no available message returns exit `2` with both streams empty
- failure returns exit `1`, empty stdout, and one JSON error document on stderr

The stable error document is:

```json
{
  "error_schema_version": 1,
  "status": "error",
  "error_code": "not_found",
  "message": "delivery \"dlv_...\" not found",
  "retryable": false
}
```

Stable `error_code` values are:

- `invalid_argument`: missing, malformed, or conflicting input; correct input
  before retrying
- `not_found`: an explicitly named delivery, message, group, membership, or
  subscriber does not exist; do not retry unchanged input
- `already_exists`: create or add would duplicate active state; inspect the
  existing state before deciding whether work is already complete
- `invalid_state`: the object exists but the requested transition is illegal;
  inspect current state and stop guessing
- `integrity_error`: stored body metadata does not match its blob; stop and
  report corruption
- `busy`: transient SQLite contention; bounded retry is allowed
- `receive_recovery_required`: receive bookkeeping failed after claim and at
  least one new lease could not be rolled back; release every listed claim
  before another receive
- `internal`: unexpected storage or I/O failure; stop unless operator policy
  explicitly permits retry

Only `busy` is `retryable: true`. Error messages may add detail, but clients
branch only on `error_code` and `retryable`.

Error schema `1` evolution rules:

- consumers ignore unknown optional fields
- new `error_code` values may be added only because consumers must treat an
  unknown code as `internal` with `retryable: false`
- existing code meaning and retryability never change within schema `1`
- removing or renaming a field/code, changing a field type, or changing code
  meaning/retryability requires `error_schema_version: 2`

### Per-operation parity matrix

All result fields below are required unless marked optional. Full record field
sets are the existing exported Waypost JSON records; parity tests freeze their
field names.

| MCP capability | Canonical CLI route | Success and ordering | Empty/not-found and invalid state | Doc topic |
| --- | --- | --- | --- | --- |
| `waypost_wait` | `wait --for ADDRESS [--as PERSON] --timeout D --json` | Personal: one compact delivery (`delivery_id`, `recipient_address`, `subject`, optional `content_type` and `forwarded_from_address`). Group: one compact group message. Select the oldest eligible item by existing visible/created/id ordering. | Timeout/no message: exit `2`, silent. Unknown personal address is no message. Unknown group is `not_found`. | `mcp-cli-boundary` |
| `waypost_list` | `list --for ADDRESS [--state STATE | --as PERSON] --json` | Personal: JSON array of `ListedDelivery`, oldest visible/created/id first. Group: compact group-message array, created/id first. | Unknown personal address: `[]`. Unknown group: `not_found`. Invalid state filter: `invalid_argument`. | `history` |
| `waypost_read` | `read (--delivery ID... | --message ID... | --latest --for ADDRESS... [--state STATE] [--limit N]) --json` | `{"items":[...]}`. Direct reads preserve caller ID order and fail atomically. Latest reads are newest first; `has_more` appears only as `true`. Every item includes its id and body. | Missing direct id: `not_found`, no partial stdout. Unknown latest address: empty `items`. Invalid selector/state/limit: `invalid_argument`. Blob mismatch: `integrity_error`. | `recovery`, `history` |
| `waypost_undefer` | `undefer --delivery ID --json` | `delivery_id`, `state`, `visible_at`, `attempt_count`. | Missing delivery: `not_found`. Non-queued or already-visible delivery: `invalid_state`. | `recovery` |
| `waypost_group_create` | `group create --group ADDRESS --json` | `GroupRecord`. | Existing group: `already_exists`; endpoint-owned address: `invalid_state`. | `groups` |
| `waypost_group_add_member` | `group add-member --group ADDRESS --person PERSON --json` | `GroupMembershipRecord`. | Missing group: `not_found`; active membership: `already_exists`. | `groups` |
| `waypost_group_remove_member` | `group remove-member --group ADDRESS --person PERSON --json` | Updated `GroupMembershipRecord`. | Missing group: `not_found`; no active membership: `invalid_state`. | `groups` |
| `waypost_group_members` | `group members --group ADDRESS --json` | `GroupMembershipRecord[]`, joined/id first, including historical records. | Missing group: `not_found`. | `groups` |
| `waypost_group_add_subscriber` | `group add-subscriber --group ADDRESS --notify-address ADDRESS --person PERSON --json` | `GroupNotificationSubscriberRecord`. | Missing group: `not_found`; active subscriber: `already_exists`. | `groups` |
| `waypost_group_remove_subscriber` | `group remove-subscriber --group ADDRESS --notify-address ADDRESS --json` | Updated `GroupNotificationSubscriberRecord`. | Missing group: `not_found`; no active subscriber: `invalid_state`. | `groups` |
| `waypost_group_subscribers` | `group subscribers --group ADDRESS --json` | Active `GroupNotificationSubscriberRecord[]`, created/id first. | Missing group: `not_found`. | `groups` |
| `waypost_address_inspect` | `address inspect --address ADDRESS --json` | `AddressInspection`. Unbound is a successful record with `kind: "unbound"`; endpoint and group records include their corresponding id. | Malformed address: `invalid_argument`. Unbound is not `not_found`. | `diagnostics` |

## Runtime Profiles

Add:

```text
waypost mcp --tool-profile full|hybrid
```

- `full` exposes the existing twenty-six tools
- `hybrid` exposes the target surface as parity gates land
- raw `waypost mcp` defaults to `full` initially

The profile is fixed for one MCP process. Changing it requires restarting that
process. Waypost does not define how an external host restarts or reconfigures
its sessions.

`waypost_status` reports at least:

```json
{
  "tool_profile": "hybrid",
  "receive_result_schema_version": 2,
  "capability_manifest_schema_version": 1,
  "server_version": "<version>",
  "executable": "/absolute/path/to/waypost",
  "resolved_state_dir": "/absolute/path/to/state",
  "bound_addresses": ["agent-deck/<session-id>"],
  "default_sender": "agent-deck/<session-id>"
}
```

A CLI-using client must execute the reported Waypost binary against the
reported state directory and pass address, sender, group, and person explicitly.
It must not guess from `PATH`, environment identity, or an unrelated Waypost
installation. The mechanism used by an external host to execute a local CLI is
outside this design.

If a client cannot execute the matching CLI, it must use `full`.

## Capability Registry

Use one Waypost-owned registry as the source of truth for:

- MCP membership in `full` and `hybrid`
- canonical CLI command mapping
- structured-output readiness
- required `waypost doc` topic
- parity tests
- `waypost capabilities --json`

`waypost capabilities --json` emits one deterministic manifest:

```json
{
  "manifest_schema_version": 1,
  "server_version": "0.x.y",
  "receive_result_schema_version": 2,
  "tool_profiles": {
    "full": {"tools": ["waypost_ack", "..."]},
    "hybrid": {"tools": ["agent_deck_create_session", "..."]}
  },
  "capabilities": [
    {
      "name": "read",
      "mcp_tool": "waypost_read",
      "cli_route": ["read"],
      "doc_topics": ["recovery", "history"],
      "mcp_profiles": ["full"],
      "hybrid_target": "cli",
      "cli_result_schema_version": 1,
      "error_schema_version": 1,
      "gates": {
        "route": "ready",
        "result": "ready",
        "error": "ready",
        "doc": "ready",
        "parity_tests": "ready"
      }
    }
  ]
}
```

Rules:

- profile tool arrays are actual registered tools, sorted lexicographically
- capability entries are sorted by `name`
- `cli_route` is an argument-token array, not a shell command string
- retained MCP capabilities use `hybrid_target: "mcp"`; their CLI fields and
  gates may be absent
- a CLI-target capability remains in `mcp_profiles: ["full", "hybrid"]`
  until all five gates are `ready`
- each gate value is exactly `ready` or `blocked`; a blocked gate also carries
  a deterministic diagnostic in a sibling `<gate>_reason` field
- removing it from `hybrid` while any gate is not `ready` makes MCP startup
  fail; the server must not start a partially valid hybrid profile
- additive fields may be added within manifest schema `1`; removing, renaming,
  or changing the meaning or type of a field requires a new manifest schema
  version
- manifest-schema-1 consumers ignore unknown additive fields
- consumers reject an unsupported manifest schema version instead of guessing

Typed MCP schemas and handlers remain explicit. The registry selects which
handlers are registered; it does not introduce a generic command-dispatch MCP
tool.

Startup validation fails when:

- a profile names a missing typed handler
- a hybrid-removed operation has no ready structured CLI path
- a CLI-owned operation has no prompt topic
- two entries claim the same MCP tool or CLI capability

## Receive Result Contract

### Breaking receive-schema migration

Receive payload compatibility is independent of MCP tool membership. The
`full` profile does not preserve a client if the `waypost_recv` payload changes.

The current implicit schema `1` exposes receive `has_more` and silent CLI
no-message output. This design chooses one explicit breaking migration to schema
`2`; it does not add dual response branches or runtime schema-selection flags.

The release gate is:

1. inventory every Waypost-owned receive consumer with repository search and
   record the paths in the implementation change
2. update the store payloads, MCP handlers, CLI tests, MCP tests, README, and
   `docs/cli.md` atomically to the contracts below
3. make `waypost_status` and `waypost capabilities --json` report
   `receive_result_schema_version: 2`
4. mark the release as a receive-schema break and require external clients to
   migrate or pin the preceding Waypost release
5. delete receive `has_more`; no schema `1` serializer, fallback, flag, or
   compatibility branch remains

This repository does not modify external consumers. The version marker and
release gate make the break explicit instead of pretending `full` preserves it.

### MCP personal receive

MCP remains a single-claim operation (`Max: 1`). It may be called concurrently;
existing claim atomicity decides which calls receive work.

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
  "known_delivery_id_hint": "If you are already handling these deliveries, suppress their ids on the next receive; recover a lost token through claim history.",
  "remaining_by_state": {"leased": 1},
  "warnings": []
}
```

The existing hint and warning fields remain. `remaining_by_state` is always a
top-level sibling of the status-specific payload.

### CLI personal receive

Single receive uses the same `status`, `addresses`, `delivery`, and sparse
`remaining_by_state` fields as MCP. Success is exit `0`, one JSON document on
stdout, and empty stderr.

Batch receive replaces `delivery` with ordered `deliveries`:

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

Every `deliveries` element uses the same compact delivery fields as single
receive.

Batch order remains `visible_at`, message creation time, then delivery id,
ascending. No-message returns exit `2`, this JSON document on stdout, and empty
stderr:

```json
{
  "status": "no_message",
  "addresses": ["ADDRESS"],
  "remaining_by_state": {"queued": 2}
}
```

When every count is zero, the map is omitted. This intentional schema-2 change
lets one receive call report deferred or otherwise unfinished work even when
nothing is claimable now.

### Remaining-state semantics

- count the same resolved personal address scope used by the call
- exclude every delivery returned by the current call
- exclude `acked`; possible keys are `queued`, `leased`, and `dead_letter`
- include currently invisible deferred deliveries under `queued` because
  defer changes `visible_at`, not persisted state
- omit zero-valued keys and omit the whole map when all counts are zero
- include the map on MCP `received`, `no_message`, and `active_leases`, and on
  CLI `received` and `no_message`
- never infer claimable-now work from `queued`; only `recv` or `wait` answers
  availability

The count is an informational snapshot taken immediately after the receive-path
decision and any successful claims. `no_message` and `active_leases` have no new
claim; their count is taken immediately after that decision. Concurrent sends
and transitions may change it immediately. It is not a lock, drain condition,
or future-delivery guarantee.

Implement one grouped `COUNT(*)` query over the resolved endpoint ids and
unfinished states, excluding returned ids. It must not read message rows or
body blobs.

### Post-claim count failure invariant

A receive call must never hide a newly claimed lease behind an ordinary error.
After one or more successful claims:

1. run the remaining-state query
2. if it fails, release every newly claimed delivery back to `queued` at its
   pre-claim `visible_at` using its lease token
3. if every release succeeds, return the original count error; the caller
   receives no claim and may follow that error's retry rule
4. if any release fails, return a typed recovery result containing only the
   unreleased claims; never return a plain error or silently omit the count

The rollback helper returns per-delivery outcomes rather than only a joined
error. Each unreleased claim contains `delivery_id`, `lease_token`,
`recipient_address`, and `lease_expires_at`.

MCP recognizes the typed recovery result, adds every unreleased claim to the
active-lease tracker, starts lease renewal, and returns:

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

CLI returns exit `1`, empty stdout, and this schema-1 error document on stderr:

```json
{
  "error_schema_version": 1,
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

The caller must release every listed claim before calling receive again. It must
not ack, defer, or fail a claim without the missing message context. MCP callers
may recover current-process metadata through claim history if needed.
`remaining_by_state` omission means zero only for normal `received`,
`no_message`, and `active_leases` results; recovery results state
`remaining_by_state_status: "unavailable"` explicitly.

Performance validation has two gates:

1. `EXPLAIN QUERY PLAN` on the production query must use
   `idx_deliveries_recipient_state_visible` and must not scan the deliveries
   table.
2. A dedicated Linux amd64 benchmark job seeds 100 addresses and 100,000
   deliveries: 50,000 visible queued, 10,000 future-visible queued, 20,000
   leased, 10,000 dead-letter, and 10,000 acked. It uses WAL mode,
   `synchronous=NORMAL`, a warmed page cache, a ten-address receive scope, and
   30 interleaved A/B pairs; every pair starts from independent copies of the
   same seeded database. Regression fails only when count-enabled p95 is both
   more than 25% slower and more than 2 ms slower than the count-free receive
   baseline. The job records CPU model, storage class, SQLite version, and raw
   samples. This performance threshold is blocking only on that declared runner
   class; the query-plan assertion is blocking everywhere.

### Group receive

Group receive marks one group message read for one person and does not use
personal delivery states. MCP keeps the existing status envelope; CLI uses the
same `status`, `addresses`, `as_person`, and `message` fields. No-message is exit
`2` with a `status: "no_message"` JSON document. Group results never return
`remaining_by_state`, `has_more`, or a replacement remaining-count field.

### Read pagination

Structured `read` always returns `items`.

- latest read includes `has_more: true` only when another matching item exists
  beyond the requested limit
- false is omitted
- direct message-id and delivery-id reads omit the field

## `waypost doc` As Agent Prompt

`docs/cli.md` remains a human/operator reference manual. It must never be
loaded wholesale as an agent prompt.

Add task-oriented topics:

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

Do not create a topic for every command. A topic exists only when an agent needs
workflow guidance beyond `--help`.

### Audience and ownership

The audience is an agent performing a Waypost CLI-only task.

Waypost doc owns:

- when a CLI-owned Waypost operation is appropriate
- required explicit Waypost identity and state-directory inputs
- the shortest safe command sequence
- how to interpret structured output
- stop conditions and Waypost-specific recovery

Waypost doc does not own and must not mention:

- Agent Deck session creation, restart, hierarchy, or configuration
- planner, reviewer, coder, browser-tester, or roundtable role policy
- git branches, worktrees, commits, or review workflow
- how an MCP host executes local processes
- provider-specific addresses as durable workflow identities
- product rollout history or implementation-review discussion

External workflow prompts remain responsible for role behavior, routing, user
decision gates, and whether that workflow processes one delivery at a time.

### Prompt format

Each topic uses this fixed structure:

```markdown
# <task>
Use when: <one sentence>

## Required context
- <only values the command cannot infer safely>

## Do
1. <short command or decision>

## Interpret
- <only fields that change the next action>

## Stop
- <conditions where the agent must not guess or continue>
```

Prompt rules:

- default to one canonical JSON command path
- do not offer JSON/YAML/plain-text choices unless the task requires one
- do not describe `--full` or legacy payloads
- do not repeat flag catalogs from `--help`
- do not list fields that do not affect the next action
- branch on `error_code`, and retry only when `retryable` is `true`
- do not include demo setup, migration instructions, Web UI instructions, or
  shell conveniences
- do not teach CLI `send`, `recv`, `ack`, `release`, `defer`, or `fail` as the
  normal hybrid path; `mcp-cli-boundary` explicitly says these remain MCP
- do not rely on lazy address creation; require the caller's explicit target
- use placeholders such as `ADDRESS` and `DELIVERY_ID`, not invented workflow
  addresses that look authoritative
- keep each initial topic at or below 300 words; exceeding the budget requires
  a documented reason and a prompt-size test update

Examples in agent docs must use only structured output and must be covered by
focused integration tests where practical.

### Topic responsibilities

`mcp-cli-boundary`:

- names the retained Waypost messaging MCP operations
- names the CLI-owned operation groups
- tells the agent to stop on binary/state-directory mismatch

`recovery`:

- recovers acknowledged input with explicit address and `state acked`
- explains empty `items`
- explains sparse `has_more: true`

`history`:

- lists or reads persisted delivery history
- distinguishes message id from delivery id

`groups`:

- manages group membership and subscribers
- requires explicit group and person/notification address
- does not restate group workflow role policy

`diagnostics`:

- uses address inspection and status output
- distinguishes live MCP binding problems from durable address state

## Compatibility And Rollout

Phase 1:

- add the capability registry and profile validation
- add `waypost capabilities --json`
- add the stable JSON error contract and per-operation parity tests
- add concise embedded doc topics and prompt-size tests
- add missing subscriber and undefer structured CLI paths
- implement receive schema `2`, migrate every Waypost-owned consumer, and
  delete receive schema `1`
- keep `full` as the default and keep all existing tools available

Phase 2:

- publish the explicitly breaking receive-schema release
- expose `hybrid` as an explicit opt-in
- remove each tool from `hybrid` only when its individual parity gate passes
- keep `full` for MCP-only clients and tool-surface rollback; it does not roll
  back the receive schema

Changing the raw default to `hybrid`, or deleting `full`, requires a separate
product decision. This design does not make that decision.

## Verification

Automated checks must cover:

- `full` exposes the existing twenty-six-tool set
- target `hybrid` exposes exactly the fourteen listed tools after all parity
  gates pass
- a hybrid startup fails if a removed tool has any non-ready route, result,
  error, doc, or parity-test gate
- `capabilities --json` is deterministic and matches actual profile membership,
  CLI routes, schema versions, doc topics, and gate state
- every removed tool satisfies the parity-matrix success fields, ordering,
  empty/not-found behavior, invalid-state behavior, exit code, and stream rules
- JSON failures use only stable error codes and retryability semantics
- status/bind/debug bootstrap and repair still work
- send/forward/recv/claim-history/lifecycle tools still work through MCP
- Agent Deck resolve/create/require tools remain unchanged
- schema `2` MCP received/no-message/active-leases and CLI single/batch/no-message
  results match their exact documented envelopes
- `recv.remaining_by_state` is sparse, excludes returned/acked deliveries, and
  never appears for group receive
- personal and batch `recv` never emit `has_more`
- queued remaining counts include future-visible deferred deliveries and are
  never documented as claimable-now counts
- a post-claim count failure either rolls back every new lease before returning
  an ordinary error or returns the exact recovery payload with all unreleased
  ids and tokens; MCP tracks and renews those claims
- `read` emits `has_more` only when true
- concurrent and batch receive preserve existing claim correctness
- the remaining-count query passes its query-plan assertion and reproducible
  baseline-delta benchmark gate
- every doc topic stays within its word budget
- a forbidden-content test rejects Agent Deck lifecycle, git workflow, human
  migration/Web UI, YAML/plain/legacy-choice, and CLI execution guidance for
  retained-MCP commands from agent topics
- `docs/cli.md` remains outside embedded prompt resources

## Risks

- Prompt and binary versions can drift. Embedding topics in the Waypost binary
  and testing command examples keeps mechanics version-matched.
- A client may opt into `hybrid` without CLI execution capability. `full`
  remains the fail-safe supported profile.
- Receive schema `2` intentionally changes both success and no-message payloads.
  The explicit version marker and release gate expose that break instead of
  hiding it under the `full` profile.
- Remaining-state counting adds receive-path work. The index and performance
  gate prevent an unbounded hot-path regression.
- Fourteen tools are not the theoretical minimum, but each retained tool is
  justified by frequency or live MCP state.

## Alternatives Rejected

### Keep every CLI command as MCP

Rejected because uncommon schemas and choices remain in every agent context.

### Move lease completion to CLI

Rejected because the MCP active-lease tracker and renewal loop must observe
successful completion.

### Move Agent Deck session tools to CLI

Rejected because they are frequent structured operations and the user requires
them to remain.

### Add a generic `waypost_command` MCP tool

Rejected because it hides the same large surface behind weaker validation.

### Use `docs/cli.md` as the prompt

Rejected because it mixes operator setup, every command, multiple output modes,
legacy details, and human examples. Agent prompts must be task-specific and
short.

## Implementation Order

1. add the capability registry, manifest schema, and profile validation tests
2. add `capabilities --json`, receive schema version, and expanded status
3. add the JSON error envelope and freeze each CLI-owned result contract
4. add the embedded prompt-topic loader and concise initial topics
5. add missing subscriber and undefer CLI parity
6. add receive schema `2`, remaining-state counts, post-claim rollback/recovery,
   migrate in-repo consumers, and delete receive schema `1`
7. complete read recovery parity and prompt examples
8. remove tools from `hybrid` one capability at a time as gates pass
9. publish the explicitly breaking receive-schema release
10. run the tool-profile, receive-migration, concurrency, prompt-size,
    query-plan, and baseline-delta performance suite
