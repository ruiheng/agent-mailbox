# Waypost MCP And CLI Tool Surface

## Summary

Reduce the default Waypost MCP surface from a broad mirror of the CLI to a
small set of high-frequency operations and operations that depend on the live
MCP process state.

Keep the complete operational surface in the CLI. Add task-oriented `doc`
commands so an agent can load the workflow, invariants, and recovery guidance
for an uncommon operation on demand instead of carrying that guidance and
every command schema in its prompt at all times.

The target default MCP surface is thirteen tools:

- `waypost_status`
- `waypost_bind`
- `waypost_debug`
- `waypost_send`
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

- Reduce the default MCP tool surface without removing Waypost capabilities.
- Keep high-frequency workflow operations directly callable through MCP.
- Keep operations that read or mutate live MCP process state in MCP.
- Preserve the Agent Deck session tools used by normal dispatch and reply
  workflows.
- Keep all low-frequency Waypost operations available through the CLI.
- Replace repeated generic skill instructions with versioned CLI documentation.
- Clearly separate Waypost concurrency semantics from workflow-level
  serialization policy.
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

### Keep In The Default MCP Surface

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

They remain in the default MCP surface even though they are not Waypost store
operations. The useful boundary is the common agent workflow, not the Go
package that owns the durable state.

### Move To CLI-Driven Paths

The following tools operate on durable shared state, are uncommon in normal
turns, or are primarily administration and inspection operations:

- `waypost_forward`
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

Removing these names from the default MCP tool list does not remove the
capability. It changes discovery and execution to:

```text
read the relevant CLI doc topic -> run the documented CLI command with
structured output -> continue the workflow
```

## Why `waypost_read` Moves To CLI

`waypost_read` is important for context recovery but is not part of most normal
turns and does not depend on live MCP-only state.

The recovery path remains available:

1. use `waypost_status` to inspect the current bound addresses when identity is
   uncertain
2. run `waypost doc recovery`
3. use `waypost read --latest ... --json` or read by delivery/message id

This is exactly the kind of infrequent but important operation the on-demand
documentation model is intended to support.

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

## MCP Tool Registration

Define one explicit default registration list rather than registering every
handler automatically.

Conceptually:

```go
var defaultMCPTools = []string{
    "waypost_status",
    "waypost_bind",
    "waypost_debug",
    "waypost_send",
    "waypost_recv",
    "waypost_claim_history",
    "waypost_ack",
    "waypost_release",
    "waypost_defer",
    "waypost_fail",
    "agent_deck_resolve_session",
    "agent_deck_create_session",
    "agent_deck_require_session",
}
```

Handlers for CLI-only operations may remain reusable internally, but they are
not registered in the default MCP `tools/list` response.

Avoid a generic `waypost_command` MCP tool. That would hide the large command
surface behind one schema while preserving the same model-selection problem
and weakening argument validation.

## Compatibility And Rollout

The CLI is the compatibility surface and remains unchanged except for additive
`doc` commands.

The MCP tool-list change affects existing prompts and clients that directly
call a removed MCP tool. Roll it out as one coordinated change:

1. add `waypost doc` topics and any missing structured CLI output
2. add companion `agent-deck doc` topics in the Agent Deck repository
3. update skills to use CLI-only paths for removed MCP operations
4. update MCP registration to expose the thirteen-tool default set
5. verify every workflow skill against the new tool list

If installations can update the binary and prompts independently, provide a
temporary explicit compatibility profile:

```text
waypost mcp --tool-profile full
```

Rules for that profile:

- `core` is the intended default after prompt migration
- `full` exposes the previous tool list only during the compatibility window
- the profile is explicit and observable in `waypost_status`
- remove `full` after installed prompts and binaries are known to move together

Do not keep two permanent tool surfaces without a real compatibility need.

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

- default MCP `tools/list` contains exactly the intended thirteen tools
- CLI-only tools are absent from the default MCP surface
- compatibility profile, if shipped, exposes the previous list
- `waypost_status` reports the active tool profile
- every `waypost doc` topic is embedded and readable
- `waypost doc --list` includes every shipped topic
- unknown topics fail with a useful topic list
- examples in documentation are covered by focused CLI integration tests where
  practical
- existing Waypost CLI command tests remain unchanged and passing

Required workflow verification:

- direct send/receive/lease completion still works through MCP
- failed auto-bind can be inspected with `status`, repaired with `bind`, and
  diagnosed with `debug`
- claim history still recovers MCP-owned lease information
- Agent Deck resolve/create/require workflows still work through MCP
- history recovery works through `waypost doc recovery` and CLI
- roundtable group setup works through `waypost doc groups` and CLI
- concurrent CLI receivers and batch `recv --max` retain existing behavior
- Agent Deck personal workflow continues to process one delivery at a time by
  policy

## Risks And Tradeoffs

- A coordinated binary and prompt rollout is required to avoid unknown-tool
  failures.
- CLI subprocess calls are slightly more expensive than MCP calls for uncommon
  operations.
- Agents may skip reading a required doc topic unless skills name the topic
  directly.
- Embedded documentation can still drift from behavior unless examples and
  topic registration are tested.
- Thirteen tools are more than an aggressively minimal MCP, but each remaining
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

1. add the `waypost doc` topic registry and embedded Markdown loader
2. write the initial Waypost topics from the current authoritative CLI and MCP
   behavior
3. add or coordinate companion `agent-deck doc` topics
4. audit CLI-only commands for stable structured output
5. update shared and action skills to reference doc topics instead of repeating
   command procedures
6. add explicit MCP tool-profile registration and status reporting
7. switch the default MCP surface to the thirteen-tool set
8. run MCP, CLI, prompt-reference, and workflow integration verification
9. remove any temporary full compatibility profile after the migration window

This sequence fixes discoverability before shrinking the tool surface.
