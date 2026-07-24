# Waypost MCP And CLI Tool Surface

## Summary

Reduce the default Waypost MCP surface from a broad mirror of the CLI to a
small set of high-frequency operations and operations that depend on the live
MCP process state.

Keep the complete operational surface in the CLI. Add task-oriented `doc`
commands so an agent can load the workflow, invariants, and recovery guidance
for an uncommon operation on demand instead of carrying that guidance and
every command schema in its prompt at all times.

The target hybrid MCP surface is fourteen tools:

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

All existing Waypost CLI capabilities remain available. Low-frequency MCP
operations move to CLI-driven paths documented by `waypost doc` and, for
Agent Deck-owned behavior, `agent-deck doc`.

## Problem

The current MCP server exposes twenty-six tools: twenty-three Waypost tools and
three Agent Deck session tools. This largely mirrors command inventory rather
than representing the small set of operations most agents use in normal
workflow turns.

That creates three concrete problems:

- every agent pays the tool-schema and tool-selection cost for uncommon
  administration, inspection, group-management, and recovery commands
- shared skills repeat detailed transport and session procedures so agents can
  choose among the large tool set correctly
- the same operating rules are duplicated across skills and drift as the CLI
  and MCP evolve

Moving every uncommon operation to CLI without improving documentation would
not solve the real problem. `--help` explains flags and syntax; it does not
explain the normal workflow, ownership rules, state transitions, or recovery
path an agent needs to use a command safely.

The design therefore has two inseparable parts:

1. expose only common and MCP-stateful operations as MCP tools
2. make the CLI carry version-matched, task-oriented operational documentation

## Goals

- Reduce the Agent Deck hybrid MCP tool surface without removing Waypost
  capabilities.
- Keep high-frequency workflow operations directly callable through MCP.
- Keep operations that read or mutate live MCP process state in MCP.
- Preserve the Agent Deck session tools used by normal dispatch and reply
  workflows.
- Keep all low-frequency Waypost operations available through the CLI.
- Replace repeated generic skill instructions with versioned CLI documentation.
- Clearly separate Waypost concurrency semantics from workflow-level
  serialization policy.
- Let one `recv` response reveal non-acknowledged work still in scope without a
  follow-up inventory call.
- Keep tool selection predictable enough that agents do not need exploratory
  `--help` calls during the normal path.

## Non-Goals

- No removal of existing Waypost CLI commands.
- No change to personal delivery, lease, group-read, or notification semantics.
- No generic operation-dispatch MCP tool with a free-form subcommand field.
- No invented lifecycle abstraction that merges `ack`, `release`, `defer`, and
  `fail` into one ambiguous model-facing operation.
- No replacement of workflow-specific message schemas or role policies with CLI
  documentation.
- No claim that Waypost itself requires one-at-a-time receive processing.
- No redesign of Agent Deck session management in this document.

## Core Judgment

The MCP boundary should be based on two properties:

1. the operation is frequent enough to belong in the normal agent path
2. correctness or recovery depends on state owned by the live MCP process

Frequency alone is not sufficient: some low-frequency recovery operations must
stay in MCP because an external CLI process cannot inspect or repair the MCP
instance's in-memory bindings or tracked leases.

Statefulness alone is not sufficient either: a common structured operation such
as Agent Deck session creation is valuable as a direct MCP primitive even when
its durable state lives outside the Waypost MCP process.

The CLI remains the complete product interface. MCP is a curated agent-facing
fast path, not a second copy of every CLI command.

## Tool Classification

### Keep In The Hybrid MCP Surface

#### MCP bootstrap and binding state

- `waypost_status`
  - required bootstrap gate for the current MCP process
  - performs auto-detection and reports binding warnings
  - establishes whether the process is ready for all other tools
- `waypost_bind`
  - repairs or extends the current MCP process bindings when auto-bind is
    incomplete
  - cannot be replaced by a separate CLI process because CLI execution does not
    mutate the long-lived MCP instance's binding state
- `waypost_debug`
  - exposes the current MCP instance's detected identities, bindings, and
    related diagnostic state
  - remains necessary for diagnosing a failed or surprising bootstrap

These three tools are one stateful bootstrap capability. Hiding `status` or
`bind` behind automatic behavior would make auto-bind failures impossible for
the model to inspect and repair correctly.

#### Common message operations

- `waypost_send`
  - the normal outbound workflow operation
  - consumes bound sender context and participates in MCP-owned notification
    behavior
- `waypost_forward`
  - reuses the MCP send path after loading stored content
  - preserves bound default-sender handling and the same best-effort target
    notification behavior as `waypost_send`
  - does not currently have CLI parity because CLI `forward` persists the
    forwarded message without using the MCP notification/session path
- `waypost_recv`
  - the normal inbound workflow operation
  - consumes bound queue context and registers personal delivery leases in the
    MCP active-lease tracker

Group send and group receive continue to use these same tools. Group management
does not need to remain in MCP merely because group message transfer does.

#### MCP-owned lease recovery and completion

- `waypost_claim_history`
  - recovers delivery and lease information tracked by the current MCP instance
  - can expose the current instance's lease token when explicit recovery needs
    it
- `waypost_ack`
- `waypost_release`
- `waypost_defer`
- `waypost_fail`
  - validate completion against the MCP active-lease tracker
  - update terminal or requeue state in the store
  - update the current MCP process's tracked lease state so automatic renewal
    stops for that delivery

Keep the four lifecycle tools explicit. They have different meanings, required
arguments, continuation rules, and operational consequences. A smaller tool
count is not worth replacing those distinctions with an action enum that makes
invalid combinations easier to express.

#### Common Agent Deck session lifecycle

- `agent_deck_resolve_session`
- `agent_deck_create_session`
- `agent_deck_require_session`

These operations are used repeatedly by planner, reviewer, architect, browser,
and roundtable workflows. They also provide structured, race-aware session
resolution, explicit workdir verification, launch, parent linkage, and group
placement.

They remain in the hybrid MCP surface even though they are not Waypost store
operations. The useful boundary is the common agent workflow, not the Go
package that owns the durable state.

### Move To CLI-Driven Paths

The following tools operate on durable shared state, are uncommon in normal
turns, or are primarily administration and inspection operations:

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

Their CLI commands already support structured output or can be extended to do
so. They do not require a model-facing MCP schema in every agent session.

Removing these names from the hybrid MCP tool list does not remove the
capability. It changes discovery and execution to:

```text
read the relevant CLI doc topic -> run the documented CLI command with
structured output -> continue the workflow
```

## Supported Runtime Profiles

The reduced surface is not valid for every MCP client.

Define two explicit runtime profiles:

- `hybrid`
  - exposes the fourteen-tool MCP surface in this design
  - requires a host with an argv-safe local process execution facility
  - requires permission to execute the exact Waypost CLI binary reported by the
    MCP process
  - requires the CLI process to access the same resolved state directory
- `full`
  - exposes the existing twenty-six-tool surface
  - supports MCP-only clients that do not have shell/process execution or a
    matching local CLI
  - remains the compatibility and rollback profile

The product must not claim that a capability was relocated to CLI for a client
that cannot execute the CLI. Such a client must use `full`, or it is explicitly
unsupported.

Agent Deck-managed coding agents are the initial target for `hybrid`, but their
host adapter must first demonstrate argv-safe execution; a generic shell-string
tool does not satisfy that prerequisite by itself. Raw third-party MCP clients
remain on `full` until their host prerequisites are known.

## MCP-To-CLI Context Contract

A CLI subprocess does not inherit the live MCP process's bindings or tracked
leases. It must not guess the executable, version, store, address, sender, or
person identity.

Extend `waypost_status` with a machine-readable CLI context:

```json
{
  "server_version": "<build version>",
  "build_id": "<immutable build id>",
  "executable_sha256": "<startup executable digest>",
  "mcp_instance_id": "<session-scoped instance id>",
  "tool_profile": "hybrid",
  "capability_manifest_version": 1,
  "active_lease_count": 0,
  "resolved_state_dir": "/absolute/path/to/waypost-state",
  "bound_addresses": ["agent-deck/<session-id>"],
  "default_sender": "agent-deck/<session-id>",
  "default_workdir": "/absolute/workspace",
  "binding_bootstrap": {
    "durable_workflow_address": "agent-deck/<session-id>",
    "required_bound_addresses": ["agent-deck/<session-id>"],
    "restored_bound_addresses": ["agent-deck/<session-id>"],
    "transient_bound_addresses": ["codex/<current-tool-session-id>"],
    "missing_required_addresses": [],
    "configured_default_sender": "agent-deck/<session-id>",
    "default_sender_matches": true
  },
  "cli_context": {
    "executable": "/absolute/path/to/waypost",
    "version": "<same build version>",
    "build_id": "<same immutable build id>",
    "executable_sha256": "<same startup executable digest>",
    "argv_prefix": ["--state-dir", "/absolute/path/to/waypost-state"]
  },
  "hybrid_preflight": {
    "waypost_local": {
      "status": "healthy",
      "capability_manifest_sha256": "<manifest digest>"
    },
    "agent_deck_adapter_attestation": {
      "status": "attested",
      "schema_version": 1,
      "session_id": "<current-agent-deck-session-id>",
      "mcp_instance_id": "<same session-scoped instance id>",
      "tool_profile": "hybrid",
      "agent_deck_executable": "/absolute/path/to/agent-deck",
      "agent_deck_build_id": "<immutable build id>",
      "agent_deck_executable_sha256": "<startup executable digest>",
      "request_runtime_dir": "/private/agent-deck/runtime/session-id",
      "waypost_executable_sha256": "<same Waypost digest>",
      "capability_manifest_version": 1,
      "capability_manifest_sha256": "<same manifest digest>"
    }
  }
}
```

Add:

```text
waypost version --json
```

Waypost owns the local half of hybrid preflight. It verifies:

- its startup-cached executable digest and immutable build id
- the currently resolved state directory
- the selected tool profile
- the capability manifest version and digest generated from its authoritative
  registry

Agent Deck owns the external adapter half. At Agent Deck session creation it
chooses the initial tool profile and one exact configured Agent Deck executable,
assigns the MCP instance id, verifies that the configured adapter subcommand
exists, and records the executable/build/digest and Waypost manifest identity
in the session-start attestation above. The attestation is delivered once over
an inherited read-only startup channel rather than a workspace file, then
cached immutably for that MCP instance.

The attestation channel and payload are schema-versioned independently from the
Waypost capability manifest. Hybrid startup rejects a missing or unsupported
attestation schema with `hybrid_context_mismatch`; it does not guess fields,
silently downgrade, or enable message tools. The compatibility matrix covers
old Agent Deck/new Waypost and new Agent Deck/old Waypost combinations.

`waypost_status` reports these two results separately. Waypost may validate
that the supplied attestation is structurally bound to its session id, MCP
instance id, profile, executable digest, and manifest identity, but it must not
claim that it independently inspected or executed the external Agent Deck
adapter.

The per-command hybrid CLI preflight is:

1. call `waypost_status`
2. require the Waypost-local result to be healthy and the Agent Deck
   attestation to be present and consistent with the active session/profile
3. invoke exactly the attested `agent_deck_executable`, not a fresh `PATH`
   lookup
4. let that adapter execute exactly `cli_context.executable` and verify its
   `version --json`, immutable build id, executable SHA-256, and capability
   manifest against both status and the session-start attestation
5. prepend `cli_context.argv_prefix` to every stateful Waypost CLI command
6. pass the operation's address, sender, and person identity explicitly

If the executable cannot run, the versions differ, or the resolved state
directory is inaccessible, stop with a `hybrid_context_mismatch` error. Do not
fall back to another `waypost` found on `PATH`.

The MCP computes and caches its executable digest during process startup. It
must not re-hash the path only when `status` is called, because the file at that
path may have been replaced while the old process is still running.

In `hybrid`, the first `waypost_status` completes Waypost-local preflight and
checks that a matching Agent Deck attestation was supplied before enabling
message tools. If either side is unhealthy or inconsistent, status returns the
separate results and all tools except status/bind/debug remain gated. This
prevents a known-bad hybrid process from claiming work without overstating what
Waypost verified about another executable.

Bindings are candidates, not implicit CLI authority. A skill or workflow
context still chooses the exact address. CLI-only commands must use explicit
arguments such as `--for`, `--from`, `--group`, and `--as`; they must not infer
an identity from environment variables.

`as_person` is per group operation, not a global MCP default. The workflow must
carry it explicitly into the CLI invocation.

The MCP active-lease set never crosses this boundary. That is why claim-history
and personal lease completion remain MCP tools.

## Current Capability Matrix

This matrix is the required parity decision for every currently registered
tool. `CLI contract` names the exact current command or the command that must be
added before removing the MCP tool.

| Tool | Hybrid exposure | CLI contract | State/identity owner | Structured result and failure contract | Doc topic |
|---|---|---|---|---|---|
| `waypost_status` | MCP | None | Live MCP bindings, detected sessions, profile and CLI context | Object above; warnings are data, bootstrap failure is an error | `mcp-cli-boundary` |
| `waypost_bind` | MCP | None | Live MCP bindings/default sender | Updated binding object; invalid/colliding addresses fail | `mcp-cli-boundary` |
| `waypost_debug` | MCP | None | Live MCP detection and binding diagnostics | Diagnostic object; no durable mutation | `diagnostics` |
| `waypost_send` | MCP | `waypost send --to A --from S --body-file P --json` exists but is not the normal hybrid path | Store plus MCP bound sender and notifier | Delivery/group acknowledgement; validation or persistence failure | `quickstart` |
| `waypost_forward` | MCP | `waypost forward (--message M \| --delivery D) --to A --from S --json` lacks MCP notify parity | Store plus MCP bound sender and notifier | Forward acknowledgement and notify result; source/target validation failure | `history` |
| `waypost_wait` | CLI | `waypost --state-dir D wait --for A [--as P] [--timeout T] --json` | Store; address/person explicit | Compact metadata; exit `2` on timeout/no message; never claims | `history` |
| `waypost_recv` | MCP | `waypost recv ... --json` remains available for non-MCP consumers | Store plus MCP bindings and active leases | Claimed message/lease or no-message result plus sparse remaining-work snapshot; MCP tracks personal leases | `delivery-lifecycle` |
| `waypost_claim_history` | MCP | None | MCP active-lease history | Current/terminal tracked lease data; token disclosure remains explicit | `recovery` |
| `waypost_list` | CLI | `waypost --state-dir D list --for A [--state S \| --as P] --json` | Store; address/person explicit | JSON array; empty list is success; invalid filter combination fails | `history` |
| `waypost_read` | CLI after acknowledged-history parity test | Recovery uses `waypost --state-dir D read --latest --for A --state acked --limit 1 --json`; direct id modes remain `--message M` or `--delivery D` | Store; address/state/limit or identifiers explicit | `{items,has_more}` newest-first; each delivery item contains delivery/message ids and body; selector and integrity failures are errors | `recovery` |
| `waypost_ack` | MCP | CLI exists for non-MCP leases | Store plus MCP active lease | Ack result; stale/unowned lease fails and local tracker is updated only on success | `delivery-lifecycle` |
| `waypost_release` | MCP | CLI exists for non-MCP leases | Store plus MCP active lease | Requeue result; stale/unowned lease fails | `delivery-lifecycle` |
| `waypost_defer` | MCP | CLI exists for non-MCP leases | Store plus MCP active lease | Deferred visibility result; invalid time or stale lease fails | `delivery-lifecycle` |
| `waypost_undefer` | CLI after structured parity addition | Extend to `waypost --state-dir D undefer --delivery D --json` | Store; delivery id explicit; no active lease | Delivery-transition object; missing, wrong-state, or already-visible delivery fails | `recovery` |
| `waypost_fail` | MCP | CLI exists for non-MCP leases | Store plus MCP active lease | Requeue/dead-letter result; reason required; stale lease fails | `delivery-lifecycle` |
| `waypost_group_create` | CLI | `waypost --state-dir D group create --group G --json` | Store; group explicit | Group object; address collision fails | `groups` |
| `waypost_group_add_member` | CLI | `waypost --state-dir D group add-member --group G --person P --json` | Store; group/person explicit | Membership object; duplicate active membership fails | `groups` |
| `waypost_group_remove_member` | CLI | `waypost --state-dir D group remove-member --group G --person P --json` | Store; group/person explicit | Closed membership object; absent active membership fails | `groups` |
| `waypost_group_members` | CLI | `waypost --state-dir D group members --group G --json` | Store; group explicit | JSON array of active/history records; unknown group fails | `groups` |
| `waypost_group_add_subscriber` | CLI after parity addition | Add `waypost group add-subscriber --group G --notify-address A --person P --json` | Store; all identities explicit | Subscriber object; invalid membership/address or duplicate fails | `groups` |
| `waypost_group_remove_subscriber` | CLI after parity addition | Add `waypost group remove-subscriber --group G --notify-address A --json` | Store; group/notify address explicit | Removed subscriber object; absent active subscriber fails | `groups` |
| `waypost_group_subscribers` | CLI after parity addition | Add `waypost group subscribers --group G --json` | Store; group explicit | JSON array; unknown group fails | `groups` |
| `waypost_address_inspect` | CLI | `waypost --state-dir D address inspect --address A --json` | Store; address explicit | `{address,kind,...}` including `unbound`; invalid address fails | `diagnostics` |
| `agent_deck_resolve_session` | MCP | `agent-deck session show REF --json` is diagnostic but not the workflow contract | Agent Deck session registry | `found/not_found/error` structured result, including batch resolution | `agent-deck:session-lifecycle` |
| `agent_deck_create_session` | MCP | `agent-deck launch ...` exists but the MCP tool owns the normal structured parent/group/workdir contract | Agent Deck session registry and launcher | Created session with authoritative id; existing target/workdir/group failure | `agent-deck:session-lifecycle` |
| `agent_deck_require_session` | MCP | No single current CLI command with equivalent verify-and-start semantics | Agent Deck session registry and launcher | Required/started session or explicit workdir/not-found error | `agent-deck:session-lifecycle` |

The three subscriber CLI commands and structured `undefer --json` output are
implementation prerequisites. Their MCP tools cannot be removed from any
profile until the commands and structured contracts exist and pass parity
tests.

`waypost_forward` remains in the hybrid MCP profile because current CLI forward
does not use the MCP notifier and therefore is not behaviorally equivalent.

## Why `waypost_read` Moves To CLI

`waypost_read` is important for context recovery but is not part of most normal
turns and does not depend on live MCP-only state.

The recovery path remains available:

1. use `waypost_status` to inspect the current bound addresses when identity is
   uncertain
2. run `waypost doc recovery`
3. recover the latest acknowledged workflow input with:

   ```text
   waypost --state-dir <status-state-dir> read \
     --latest --for <session-address> --state acked --limit 1 --json
   ```

4. require `items[0]` to contain the acknowledged delivery id, message id, and
   body; an empty `items` array means no acknowledged input exists
5. for an older acknowledged input, use `waypost list --for <address> --state
   acked --json`, select the intended delivery id, then use `waypost read
   --delivery <id> --json`

`read --latest` ordering must remain deterministic newest-first using the
store's delivery ordering contract. The parity test must cover multiple queued,
leased, dead-letter, and acknowledged deliveries and prove that explicit
`--state acked` selects the same recovery input as the former MCP path.

This is exactly the kind of infrequent but important operation the on-demand
documentation model is intended to support.

Keep `waypost_read` in `hybrid` until this exact acknowledged-history parity
test passes; the capability registry then removes it with one membership
change.

## Receive Concurrency Semantics

Waypost supports concurrent receivers and batch receive.

Core and CLI semantics remain:

- multiple consumers may call `recv` concurrently
- transactional claim and lease ownership prevent the same delivery from being
  successfully leased to two consumers at the same time
- CLI `recv --max N` may claim a bounded batch
- MCP callers that intentionally continue while the current MCP instance holds
  known leases may use the existing known-delivery recovery contract

Agent Deck personal workflow currently uses a stricter policy:

```text
recv one -> perform the workflow action -> ack/release/defer/fail -> recv next
```

That is a workflow reliability rule intended to prevent one language-model turn
from accumulating several unresolved obligations. It is not a Waypost
concurrency restriction and must not be documented as one.

Group receive has different semantics:

- group `recv` marks a group message read for one person
- it does not create a personal delivery lease
- group-drain workflows may loop until no unread message remains

The CLI documentation must name these distinctions explicitly.

### Remaining-work snapshot

Personal `recv` structured results add a sparse `remaining_by_state` object:

```json
{
  "status": "received",
  "delivery": {"delivery_id": "dlv_..."},
  "remaining_by_state": {
    "queued": 2,
    "leased": 1,
    "dead_letter": 1
  }
}
```

The contract is:

- count the same resolved personal address scope used by the `recv`
- exclude deliveries returned by the current call
- exclude `acked`, because it is completed history rather than unfinished work
- use persisted delivery states as keys; a deferred delivery remains `queued`
  because defer changes `visible_at`, not the delivery state
- omit zero-valued keys, and omit `remaining_by_state` entirely when every
  unfinished-state count is zero
- include the sparse map on `received`, `no_message`, and `active_leases`
  results when any count is non-zero

The store-level receive operation captures the count snapshot immediately after
its claim decision, using the claim transaction where the receive path permits
it. Under concurrent senders, receivers, and lifecycle transitions the object
is informational at that instant, not a guarantee about a future call.

This snapshot is an independent user requirement for omission visibility, not
a profile-restart, mailbox-retirement, or drain guarantee. A positive count
does not block MCP-process restart, and a zero count does not protect against a
later send. Its measured workflow benefit is eliminating a follow-up inventory
call when `recv` returns no newly claimable item while future-visible, leased,
or dead-letter work still requires attention; sparse omission keeps the common
all-zero response free of extra tokens.

The personal count query must use the existing
`idx_deliveries_recipient_state_visible` index, select only the resolved
recipient endpoint ids and non-acked states, and avoid message/blob reads. Add
a committed 100,000-delivery SQLite benchmark fixture; the grouped count may
add at most 10 ms p95 to local `recv`. If it misses the budget, optimize the
query or index before shipping rather than omitting counts silently.

Group mode has no personal delivery states, so this design does not add
`remaining_by_state` or another remaining-count field to group `recv`.

## Identity-Preserving MCP Profile Restart

The Agent Deck session id is the durable Waypost personal-mailbox identity:
peers send to `agent-deck/<session-id>`. It must survive a clean profile change.

Tool profile is therefore immutable for one Waypost MCP instance, not for the
durable Agent Deck session. A profile change performs an identity-preserving
Agent Deck `session restart <id>` that reloads MCPs under the same session id
and mailbox address. It creates a new MCP instance id and attestation, but it
must not create a new Agent Deck session row or Waypost address.

`agent-deck/<session-id>` is the durable workflow address. Provider/tool-session
addresses such as `codex/<id>`, `claude/<id>`, or `gemini/<id>` are transient
detection results for the current execution process. Workflows must not persist
or advertise them as cross-restart destinations unless a separate stable-
address contract is introduced.

### Prevent bad claims

In `hybrid`, `waypost_status` completes the separated local-preflight and
session-attestation checks before `waypost_recv` is enabled. A known-bad hybrid
context therefore cannot claim new personal work.

### Turn-boundary restart

`waypost_status` reports `active_lease_count`. Agent Deck performs a clean
profile restart only at an agent-turn boundary:

1. the authorized Agent Deck controller creates one pending restart record with
   a cryptographically random `restart_attempt_id`, session id, current MCP
   instance id, current/requested profiles, and state `awaiting_ready`; the id
   is supplied through Agent Deck-owned session control context, not a Waypost
   message
2. the current agent stops calling `recv`, settles or defers every claimed
   personal delivery, verifies `active_lease_count=0`, and makes the final
   action of the turn an Agent Deck-internal `profile_restart_ready` transition
   bound to the restart attempt id, session id, MCP instance id, requested
   profile, and final zero lease count
3. Agent Deck atomically consumes readiness once, rejects ordinary Waypost
   input, stale/repeated attempt ids, mismatched instances/profiles, or a
   non-zero final lease count, and queues new wakeups/input for the same session
   instead of starting another turn
4. the controller updates only that session's MCP launch profile and runs the
   identity-preserving `agent-deck session restart <same-session-id>` primitive
5. the restarted session calls `waypost_status`, verifies the same session id
   and mailbox address, a new MCP instance id and attestation, the requested
   profile, required bindings, and default sender, then Agent Deck marks the
   attempt complete and resumes queued wakeups

The readiness transition is an Agent Deck lifecycle API/CLI operation described
by `agent-deck doc session-lifecycle`; it is not a Waypost-routed workflow
message and does not add an MCP tool. The restart record is the single state
owner for authorization, idempotency, retry, and audit.

No `recv` can occur between the zero-lease observation and process termination
because the reporting turn has ended and Agent Deck has not started the next
turn. New and late messages remain queued at the unchanged mailbox address;
deferred, dead-letter, and crash-expiry deliveries also retain the same
recipient identity. Peers and cached session references require no redirect.

This turn-boundary protocol removes the runtime guard, drain request, PID
ownership, stale-record, mailbox migration, and forwarding machinery. The
zero-active-lease invariant remains explicit without inventing a shared
filesystem state machine.

### Binding bootstrap after restart

Bindings owned only by the old MCP instance are not serialized wholesale.
Restart bootstrap classifies them explicitly:

- the stable `agent-deck/<session-id>` address is required and re-derived from
  the unchanged Agent Deck session identity
- provider/tool-session addresses are transient and re-detected for the new
  execution process; old values are discarded
- manually added addresses and a non-default sender are MCP-instance-local by
  default; any value required across restart must be declared in Agent
  Deck-owned session configuration and re-applied during bootstrap

The session-start attestation carries the required durable binding set and
configured default sender. Before queued wakeups resume, `waypost_status`
reports required, restored, transient, and missing bindings separately. Missing
required addresses or a wrong default sender keep the restart attempt pending
and message tools gated; the agent repairs live state through `waypost_bind`,
then status must pass again. There is no silent fallback to a transient provider
address as default sender.

### Restart failure

If the new MCP process, attestation, preflight, binding bootstrap, or status
verification fails after the old turn ended, Agent Deck:

- keeps the unchanged mailbox and all wakeups/input queued
- records a structured failed stage and error on the same restart attempt
- leaves the session in maintenance rather than silently stopped or resumed
- permits an idempotent retry of the requested profile, or an explicit
  controller rollback that restores the prior launch profile and starts a new
  MCP instance under the same session id

Wakeups resume only after one profile starts successfully and passes status and
binding verification. If neither requested-profile retry nor explicit rollback
succeeds, the attempt remains visibly blocked with exact manual recovery data.

A force-kill remains possible at the operating-system level, but it is crash
recovery rather than a clean restart. Renewal stops and normal lease expiry
applies on the same mailbox address.

### Mid-action hybrid mismatch

A per-command digest or context check may fail after `recv` while the current
MCP process still holds a lease. Do not restart immediately.

The current action must use the existing MCP process to resolve its claimed
delivery first:

- `ack` only if the required workflow action completed durably
- `release` when processing did not begin and immediate retry is safe
- `fail` when processing failed before irreversible side effects and the normal
  retry/dead-letter policy should apply
- `defer` when a prerequisite is temporarily unavailable or partial external
  side effects make immediate replay unsafe

For the partial-side-effect case, the action skill must first send or persist an
explicit blocker report containing the delivery id, completed effects, unknown
effects, and required recovery decision. It must not silently release or fail
the delivery into an unsafe replay.

After the lifecycle operation succeeds, call `waypost_status` again. Emit the
Agent Deck-internal restart-ready transition only when the active lease count
reaches zero, end the turn, and let Agent Deck restart the same session identity
under the required profile.

If the MCP is itself unavailable and cannot perform a lifecycle transition,
treat the process as crashed. Do not report a clean rollback; wait for lease
expiry and use normal duplicate/partial-side-effect recovery rules.

### Required tests

- hybrid bootstrap failure prevents `recv`
- tool profile cannot change within one MCP instance
- identity-preserving profile restart requires an observed zero active-lease
  count and a matching turn-boundary restart-ready control transition
- restart readiness is accepted only through the authenticated Agent Deck
  control record; stale, repeated, Waypost-routed, and mismatched attempts are
  rejected
- each lifecycle transition removes the lease and permits the restart-ready
  control transition afterward
- mid-action CLI mismatch settles or defers before the reporting turn ends
- restart preserves Agent Deck session id and Waypost address while producing a
  new MCP instance id and adapter attestation
- stable/session-configured bindings and default sender are restored and
  verified before wakeups resume; transient provider addresses are replaced
- queued, future-visible, dead-letter, late-arriving, and crash-expiry
  deliveries remain reachable through the unchanged mailbox
- restart failure keeps wakeups queued and supports idempotent retry or explicit
  rollback to the prior profile under the same session id
- forced process death stops renewal and makes the delivery claimable after
  expiry
- structured restart logs include the stable session id, old/new MCP instance
  ids, old/new profiles, and the final active-lease count

## CLI Documentation Design

### Command Shape

Add task-oriented documentation commands:

```text
waypost doc
waypost doc --list
waypost doc <topic>
```

Recommended initial topics:

- `quickstart`
- `mcp-cli-boundary`
- `delivery-lifecycle`
- `recovery`
- `history`
- `groups`
- `diagnostics`

The Agent Deck CLI should provide the equivalent domain-owned documentation:

```text
agent-deck doc
agent-deck doc --list
agent-deck doc session-lifecycle
agent-deck doc session-hierarchy
agent-deck doc workflow-dispatch
agent-deck doc troubleshooting
```

Waypost may contain a short cross-tool integration topic, but it must not copy
the authoritative Agent Deck session documentation.

### `help` Versus `doc`

`--help` remains syntax-oriented:

- flags
- accepted values
- command grammar
- short examples

`doc` is task-oriented:

- when to use an operation
- the normal sequence of operations
- state and ownership invariants
- success continuation
- failure and recovery paths
- common invalid combinations
- structured-output examples intended for agents

For example, `waypost doc delivery-lifecycle` should explain both the generic
Waypost capability and the optional workflow policy:

```text
generic Waypost: recv one or a batch -> process each claimed delivery ->
complete each lease explicitly

Agent Deck personal workflow default: recv one -> process ->
ack/release/defer/fail -> recv next
```

It must not state that repeated or parallel `recv` is forbidden by Waypost.

### Documentation Ownership And Versioning

Documentation should be embedded in the binary and released with the commands
it describes.

Recommended implementation:

- Markdown source files under a dedicated package directory
- Go `embed` for runtime access
- one topic index with title and one-line description
- deterministic plain Markdown output by default
- no pager when stdout is not an interactive terminal
- stable non-zero exit for unknown topics, including the valid topic list

The binary-owned document is authoritative for command mechanics. Skills should
not duplicate it.

Agent Deck documentation follows the same ownership rule in the Agent Deck
binary. Add `agent-deck version --json`; any skill that leaves the retained MCP
session path for an Agent Deck CLI administration command must resolve one
configured executable, verify its version, and read documentation from that same
binary. Waypost does not duplicate Agent Deck command documentation or claim
cross-binary version equality.

### Structured Output

The document body is intended to be read as text, so Markdown is the default
and sufficient first format.

`waypost doc --list --json` may be added for topic discovery. Returning the full
document body as JSON is unnecessary unless a real consumer requires it.

## Skill Simplification

Skills continue to own workflow-specific facts:

- role behavior
- action-specific message bodies
- routing metadata
- user decision gates
- exact completion conditions
- branch and workspace ownership rules

Generic command mechanics move to CLI documentation:

- how to inspect Waypost history
- how to operate group membership and subscriptions
- how personal lease completion works
- how to recover after lost context
- how Agent Deck session resolve/create/require works

A skill that uses a CLI-only path should contain a short directive such as:

```text
Run `waypost doc groups`, then use the documented structured-output CLI path.
```

It should add only the workflow-specific group address, person, or completion
rule. It should not restate the whole group command manual.

The shared workflow prompt still owns its deliberate one-at-a-time personal
message policy, because that policy belongs to Agent Deck workflow rather than
the Waypost CLI.

## Safe CLI Invocation

Agent Deck owns the argv-safe execution adapter required by hybrid mode.

Add this Agent Deck command before Phase 2:

```text
<attested-agent-deck-executable> waypost exec \
  --request-file <agent-deck-private-runtime-path>
```

Request shape:

```json
{
  "session_id": "<current-agent-deck-session-id>",
  "mcp_instance_id": "<current-waypost-mcp-instance-id>",
  "attestation_schema_version": 1,
  "tool_profile": "hybrid",
  "capability_manifest_version": 1,
  "capability_manifest_sha256": "<manifest digest>",
  "nonce": "<one-shot random nonce>",
  "cli_context": "<exact object returned by waypost_status>",
  "args": ["read", "--latest", "--for", "agent-deck/id", "--state", "acked", "--limit", "1", "--json"],
  "max_duration": "30s"
}
```

The adapter:

- is invoked through the exact absolute Agent Deck executable in the
  session-start attestation, never through `PATH`
- accepts request files only from an Agent Deck-owned per-session runtime
  directory with mode `0700`; request files must be owner-only regular files,
  must not be symlinks, and have a bounded size such as 64 KiB
- requires `session_id`, `mcp_instance_id`, profile, and manifest identity to
  match the active Agent Deck session and attestation, and rejects unsupported
  attestation schema versions; the one-shot nonce must match the request
  filename and must not have been consumed previously
- opens with no-follow semantics, verifies metadata with the open descriptor,
  unlinks before execution, and reads from that descriptor so every request is
  consumed exactly once
- removes unconsumed request files on normal completion, cancellation, and
  session cleanup
- cross-checks the executable against the Waypost MCP command recorded for the
  specified Agent Deck session
- verifies build id, executable digest, and capability manifest identity
- runs the exact digest-matched Waypost binary's `capabilities --json`, maps the
  requested argv to one canonical capability, and permits it only when the
  active manifest marks that capability `hybrid_cli_allowed=true`
- permits the inert `doc`, `version`, and `capabilities` routes explicitly
- rejects every MCP-retained operation, MCP-server route, unknown command, and
  unlisted route before spawning the requested Waypost operation
- rejects caller-supplied context flags such as `--state-dir`, profile/config
  selectors, and unlisted command aliases; the adapter alone supplies the
  attested state-dir prefix
- prepends the status-provided state-dir argv prefix
- executes with Go/Rust-style argv spawning, never `sh -c`
- captures stdout, stderr, exit status, duration, and cancellation as structured
  JSON

Skills access CLI-only Waypost operations through this fixed adapter. They do
not invoke `waypost` through an arbitrary shell command.

Hybrid mode therefore requires the adapter artifact, not merely a vague host
promise of safe execution. Workflow code must not concatenate message-derived
values into shell substitutions or command strings.

Rules:

- invoke `cli_context.executable` with an argument array
- add `cli_context.argv_prefix` as separate arguments
- pass addresses, group names, persons, delivery ids, message ids, durations,
  and paths as individual argv elements
- pass message bodies through `--body-file` or stdin, never as shell syntax
- let Waypost's command parser and normalization functions remain the final
  validation owner for addresses, ids, states, and times
- treat a host without the Agent Deck adapter or an equivalent audited argv
  executor as unsupported for `hybrid`

Skills may validate expected workflow shape before execution, but they must not
replace CLI validation or construct a second address grammar.

### Blocking command cancellation

Hybrid agents must always give CLI `wait` an explicit Waypost `--timeout` and a
slightly larger adapter `max_duration`.

The adapter owns child-process cancellation:

- propagate host cancellation to the child process group
- send graceful termination first, then force termination after a bounded grace
  period
- always reap the child
- report `cancelled=true` separately from Waypost exit `2`
- never leave a background `waypost wait` process running after the agent turn
  is cancelled or compacted

The 500 ms process-overhead metric applies only to non-blocking commands.
Blocking `wait` verification instead checks timeout accuracy, cancellation
latency, and absence of orphan child processes.

## MCP Tool Registration

Use one authoritative operation-capability registry instead of maintaining an
MCP list, CLI mapping, doc index, and parity expectations separately.

Conceptually, each entry contains:

```go
type Capability struct {
    Name             string
    ToolName         string
    MCPProfiles      []string
    CLICommand       string
    CLIRoute         []string
    CLIPresent       bool
    HybridCLIAllowed bool
    StateOwner       StateOwner
    IdentityInputs   []string
    DocTopic         string
}
```

The registry drives:

- MCP tool registration for `hybrid` and `full`
- `waypost capabilities --json`
- the generated operation index in `waypost doc mcp-cli-boundary`
- validation that every CLI-only capability has an existing structured CLI
  contract and a doc topic
- the Agent Deck adapter allowlist for the exact active manifest
- parity and profile-membership tests

`HybridCLIAllowed` is false for every operation retained in the hybrid MCP
surface even when a standalone CLI spelling exists. It becomes true for a
CLI-owned operation only after structured-output, documentation, and parity
gates pass. `doc`, `version`, and `capabilities` are inert manifest entries and
are the only non-operation routes allowed by the adapter.

`CLIRoute` is the canonical subcommand-token sequence, such as `["group",
"add-member"]`. The manifest exports routes as arrays rather than asking the
adapter to infer them from help text or a display string.

Typed MCP input schemas and handlers remain explicit. The registry selects
which typed handlers are registered; it does not replace them with generic
dispatch.

At startup, fail fast if:

- a profile names a tool without a registered typed handler
- a hybrid-removed tool lacks a CLI mapping or is marked `CLIPresent=false`
- a CLI-only operation lacks a doc topic
- an operation is both exposed in `hybrid` MCP and marked
  `HybridCLIAllowed=true`
- two capability entries claim the same tool name

The three group-subscriber entries, `waypost_undefer`, and `waypost_read`
remain in `hybrid` until their CLI commands or exact recovery contracts pass
parity. Once parity is implemented, changing registry membership is the single
source change that removes them from `tools/list` and enables the corresponding
tests. The target fourteen-tool surface is reached only after those
prerequisites.

Avoid a generic `waypost_command` MCP tool. That would hide the large command
surface behind one schema while preserving the same model-selection problem
and weakening argument validation.

## Compatibility And Rollout

The relevant installation units update independently:

- Waypost binary and MCP server
- Agent Deck binary
- MCP host configuration
- installed `config_files` skills and shared prompts

The design therefore requires profiles; they are not an optional mitigation.

Add:

```text
waypost mcp --tool-profile full|hybrid
```

Profile selection is an MCP process startup decision because MCP clients cache
or discover `tools/list` per server instance. Changing it requires restarting
the MCP session.

Rollout phases:

### Phase 1: parity release

- ship the capability registry, `version --json`, status CLI context, and doc
  topics
- add the missing group-subscriber CLI commands and structured `undefer` output
- add the Agent Deck `waypost exec` argv adapter, private one-shot request
  transport, session-start attestation, and manifest allowlist
- make the selected tool profile immutable for one MCP instance and add the
  identity-preserving, turn-boundary Agent Deck restart contract
- ship both profiles with `full` as the raw `waypost mcp` default
- keep existing host configurations unchanged
- run the old-skill/new-binary and new-skill/new-binary compatibility suites

### Phase 2: Agent Deck hybrid opt-in

- update Agent Deck-managed MCP configuration to pass
  `--tool-profile hybrid`
- update installed skills in the same configuration release to use documented
  CLI paths
- leave raw third-party `waypost mcp` on `full`
- record the active profile and manifest version in `waypost_status` and
  structured startup logs

### Phase 3: broader default decision

Do not automatically change the raw binary default.

Changing the default to `hybrid` requires evidence that every supported MCP
host provides argv-safe local execution and exact CLI-context parity. If
MCP-only clients remain supported, `full` remains a supported explicit profile.

Deleting `full` is allowed only after an explicit product decision to stop
supporting MCP-only clients. Prompt migration alone is not sufficient.

Rollback from hybrid is:

1. create an Agent Deck restart attempt targeting `full`
2. stop calling `recv`, settle or defer every active lease, verify
   `active_lease_count=0`, consume readiness through the authenticated attempt,
   and end the turn
3. keep new wakeups queued, update that same session's MCP launch profile to
   `full`, and run `agent-deck session restart <same-session-id>`
4. call `waypost_status` after restart
5. verify the unchanged session id/address, new MCP instance id and attestation,
   `tool_profile=full`, expected capability manifest, required bindings, and
   default sender
6. complete the attempt and resume queued wakeups; on failure, keep maintenance
   state and retry `full` or explicitly restore the previous profile

Hybrid becomes the Agent Deck default only when all gates pass:

- all twelve removed tools have capability-matrix entries and parity tests
- all required structured CLI commands exist
- all supported Agent Deck workflow skills pass an automated removed-tool
  reference audit
- all hybrid CLI calls pass exact executable/version/state-dir preflight tests
- old prompts continue to work under `full`
- new prompts complete the workflow integration suite under `hybrid`
- rollback to `full` is covered by an end-to-end test

The CLI remains the stable complete capability surface. MCP profile membership
is an agent-interface compatibility decision.

## Required Prompt Updates

The following workflow areas require coordinated changes:

- message-history recovery: use `waypost doc recovery` plus CLI `read/list`
- roundtable setup and membership: use `waypost doc groups` plus group CLI
- group subscriber administration: use the group CLI
- diagnostics that only inspect durable address state: use CLI
- shared prompt language: distinguish generic Waypost concurrency from the
  workflow's serialized personal-message policy

Normal message transfer and Agent Deck target lifecycle remain direct MCP paths,
so most action skills retain their shortest path.

## Failure Semantics

### Documentation unavailable

If `waypost doc <topic>` is unavailable while a skill expects the new CLI path,
the agent must report a binary/prompt version mismatch instead of guessing from
`--help` or improvising a destructive command.

### Hybrid context mismatch

If the exact executable, version, state directory, or argv-safe execution
contract cannot be established, fail before the CLI command performs any read
or mutation. Report:

- MCP server version
- expected CLI executable and version
- observed CLI version or execution error
- resolved state directory
- active tool profile

If no personal delivery is claimed, the recovery path is to repair the
installation or restart the same session identity cleanly under `full`.

If a delivery is already claimed, first follow the Identity-Preserving MCP
Profile Restart section: settle or explicitly defer the delivery through the
current MCP, reach zero active leases, consume readiness through the authorized
Agent Deck restart attempt, and end the turn. Do not silently use another binary
or state directory, and do not call a profile restart clean while a lease
remains active.

### Removed MCP tool still referenced

An unknown-tool failure after migration indicates prompt drift. The recovery
message should name:

- the missing tool
- the expected CLI doc topic
- the installed Waypost version
- whether the compatibility profile is available

### CLI structured output missing

An agent-facing doc topic must not recommend parsing unstable human output. If
an operation lacks suitable `--json` or `--yaml` output, add structured output
before removing its MCP tool.

## Verification

Required automated coverage:

- hybrid MCP `tools/list` contains exactly the intended fourteen tools after
  subscriber CLI parity lands
- CLI-only tools are absent from the hybrid MCP surface
- `full` exposes the previous twenty-six-tool list
- `waypost_status` reports profile, manifest identity, executable, version,
  resolved state directory, Waypost-local checks, and Agent Deck attestation as
  separate results
- `waypost version --json` matches the running MCP binary in parity tests
- startup-cached executable digest detects a binary replaced at the same path
- capability registry validation covers every current tool exactly once
- every hybrid-removed tool has a structured CLI parity test and doc topic
- every `waypost doc` topic is embedded and readable
- `waypost doc --list` includes every shipped topic
- unknown topics fail with a useful topic list
- examples in documentation are covered by focused CLI integration tests where
  practical
- existing Waypost CLI command tests remain unchanged and passing
- unsafe shell-string execution is rejected by the supported hybrid host adapter
- the adapter rejects every retained-MCP and unlisted Waypost command before
  operation execution
- request files outside the private runtime directory, symlinks, oversized
  files, stale nonces, and mismatched session/instance/manifest bindings fail
- unsupported session-attestation schema versions gate hybrid message tools
- a tool profile cannot change within one MCP instance
- an identity-preserving restart keeps the Agent Deck session id/address and
  changes the MCP instance id/profile only after the turn-boundary gate
- restart readiness is authenticated, one-shot, idempotent, and bound to the
  authorized controller's attempt id
- required bindings/default sender are verified before wakeups resume, and
  transient provider addresses are never treated as durable workflow targets
- failed restart stages retain queued wakeups and cover requested-profile retry
  plus explicit prior-profile rollback
- CLI `wait` cancellation reaps its child and distinguishes cancellation from
  Waypost exit `2`

Required workflow verification:

- direct send/receive/lease completion still works through MCP
- personal `recv` returns sparse post-claim counts for queued, other leased, and
  dead-letter deliveries; zero keys and an all-zero map are omitted
- deliveries returned by the current `recv` are excluded from those counts, and
  a no-message result still reports positive unfinished-state counts
- the indexed remaining-work query stays within the committed 10 ms p95
  hot-path budget
- the additive remaining-work fields preserve existing structured-result
  compatibility in both `full` and `hybrid`
- failed auto-bind can be inspected with `status`, repaired with `bind`, and
  diagnosed with `debug`
- claim history still recovers MCP-owned lease information
- Agent Deck resolve/create/require workflows still work through MCP
- history recovery works through `waypost doc recovery` and CLI
- latest acknowledged recovery uses explicit `--state acked --limit 1`, returns
  ids and body, and preserves deterministic newest-first ordering
- roundtable group setup works through `waypost doc groups` and CLI
- group subscriber creation, removal, and inspection work through CLI
- concurrent CLI receivers and batch `recv --max` retain existing behavior
- Agent Deck personal workflow continues to process one delivery at a time by
  policy

Measured rollout acceptance:

- visible tool count drops from twenty-six to fourteen for hybrid sessions
- serialized `tools/list` schema bytes drop by at least 35 percent from the
  committed twenty-six-tool baseline fixture
- automated skill audit finds zero executable references to hybrid-removed MCP
  tools
- the supported old/new binary and prompt matrix produces zero unknown-tool or
  wrong-state-directory mutations
- non-blocking CLI-only operations complete within a 500 ms p95 local process
  overhead budget in the integration benchmark
- profile selection, context mismatch, and rollback events are emitted as
  structured logs

## Risks And Tradeoffs

- A coordinated binary and prompt rollout is required to avoid unknown-tool
  failures.
- CLI subprocess calls are slightly more expensive than MCP calls for uncommon
  operations.
- Agents may skip reading a required doc topic unless skills name the topic
  directly.
- Embedded documentation can still drift from behavior unless examples and
  topic registration are tested.
- Fourteen tools are more than an aggressively minimal MCP, but each remaining
  tool has either high workflow frequency or a live MCP-state justification.

These costs are preferable to globally exposing every administrative and
inspection command.

## Alternatives Considered

### Keep every CLI command as an MCP tool

Rejected.

It forces every agent to carry schemas and selection choices for operations it
rarely uses, and it encourages duplicated prompt guidance.

### Move every operation except `status`, `bind`, `send`, and `recv` to CLI

Rejected.

MCP-owned lease recovery and completion must stay synchronized with the active
lease tracker. Agent Deck session lifecycle is also common enough that moving it
to shell would make the normal workflow longer and more failure-prone.

### Move Agent Deck session tools to CLI

Rejected.

Resolve/create/require occur throughout normal workflows and provide a useful
structured atomic boundary. They are not merely administrative commands.

### Merge lease completion into one MCP tool

Rejected.

`ack`, `release`, `defer`, and `fail` represent distinct state transitions and
continuation policies. Reducing the visible count by introducing an operation
enum does not simplify the model agents must understand.

### Rely on `--help` for CLI-only operations

Rejected.

Syntax help cannot replace workflow guidance, state ownership rules, or recovery
instructions.

### Store all operational guidance in skills

Rejected.

It duplicates the same command mechanics across roles, increases prompt size,
and allows installed skills to drift from the installed binary.

## Suggested Rollout

1. add the capability registry, immutable build identity, and expanded status
   context
2. add the `waypost doc` topic registry and embedded Markdown loader
3. write the initial Waypost topics from current authoritative behavior
4. add the three missing group-subscriber CLI commands, structured `undefer`
   output, and parity tests
5. add the Agent Deck argv adapter, private one-shot request transport,
   versioned session-start attestation, manifest allowlist, and
   identity-preserving turn-boundary MCP profile restart
6. add or coordinate companion `agent-deck doc` topics and version identity
7. update shared and action skills to reference doc topics instead of repeating
   command procedures
8. ship `full` and `hybrid` profiles with raw MCP defaulting to `full`
9. run the binary/prompt/profile compatibility matrix and measure schema size
10. opt Agent Deck-managed configurations into `hybrid`
11. retain `full` for rollback and MCP-only clients unless product support scope
    explicitly changes

This sequence fixes discoverability before shrinking the tool surface.
