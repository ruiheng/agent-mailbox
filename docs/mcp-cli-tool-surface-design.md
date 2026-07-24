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
- keep each MCP tool until its exact CLI result, error behavior, and prompt
  topic pass parity tests

`waypost_forward` is not removed until CLI forward has notification parity.

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
  "capability_manifest_version": 1,
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

Conceptually:

```go
type Capability struct {
    Name          string
    ToolName      string
    MCPProfiles   []string
    CLICommand    string
    CLIReady      bool
    DocTopic      string
}
```

Typed MCP schemas and handlers remain explicit. The registry selects which
handlers are registered; it does not introduce a generic command-dispatch MCP
tool.

Startup validation fails when:

- a profile names a missing typed handler
- a hybrid-removed operation has no ready structured CLI path
- a CLI-owned operation has no prompt topic
- two entries claim the same MCP tool or CLI capability

## Receive Result Contract

### Personal receive

Personal structured `recv` results may include:

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

Rules:

- count the same resolved personal address scope used by the call
- exclude deliveries returned by the current call
- exclude `acked`
- use persisted states as keys; deferred deliveries remain `queued` because
  defer changes `visible_at`, not state
- omit zero-valued keys
- omit `remaining_by_state` when every count is zero
- include the sparse map on `received`, `no_message`, and `active_leases` when
  any count is non-zero
- do not emit `has_more`, including batch receive

The count is an informational snapshot taken immediately after the claim
decision. Concurrent sends and transitions may change it immediately. It is not
a lock, drain condition, or future-delivery guarantee.

Use the existing recipient/state index and avoid body/blob reads. Add a focused
benchmark with a 100,000-delivery fixture; the grouped count may add at most
10 ms p95 to local receive.

### Group receive

Group receive marks one group message read for one person and does not use
personal delivery states. It does not return `remaining_by_state`, `has_more`,
or a replacement remaining-count field.

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

- names the retained MCP operations
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
- add concise embedded doc topics and prompt-size tests
- add missing CLI parity and structured-output tests
- keep `full` as the default and keep all existing tools available

Phase 2:

- expose `hybrid` as an explicit opt-in
- remove each tool from `hybrid` only when its individual parity gate passes
- keep `full` for MCP-only clients and rollback

Changing the raw default to `hybrid`, or deleting `full`, requires a separate
product decision. This design does not make that decision.

## Verification

Automated checks must cover:

- `full` exposes the existing twenty-six-tool set
- target `hybrid` exposes exactly the fourteen listed tools after all parity
  gates pass
- every removed tool has a ready structured CLI command and doc topic
- status/bind/debug bootstrap and repair still work
- send/forward/recv/claim-history/lifecycle tools still work through MCP
- Agent Deck resolve/create/require tools remain unchanged
- `recv.remaining_by_state` is sparse, excludes returned/acked deliveries, and
  never appears for group receive
- personal and batch `recv` do not emit `has_more`
- `read` emits `has_more` only when true
- concurrent and batch receive preserve existing claim correctness
- every doc topic stays within its word budget
- a forbidden-content test rejects Agent Deck lifecycle, git workflow, human
  migration/Web UI, YAML/plain/legacy-choice, and CLI execution guidance for
  retained-MCP commands from agent topics
- `docs/cli.md` remains outside embedded prompt resources
- old clients continue to work under `full`

## Risks

- Prompt and binary versions can drift. Embedding topics in the Waypost binary
  and testing command examples keeps mechanics version-matched.
- A client may opt into `hybrid` without CLI execution capability. `full`
  remains the fail-safe supported profile.
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

1. add the capability registry and `full|hybrid` registration tests
2. add `capabilities --json` and expanded status context
3. add the embedded prompt-topic loader and concise initial topics
4. add missing subscriber and undefer CLI parity
5. add personal receive remaining-state counts and remove receive `has_more`
6. complete read recovery parity and prompt examples
7. remove tools from `hybrid` one capability at a time as gates pass
8. run the full compatibility, concurrency, prompt-size, and performance suite
