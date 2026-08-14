# Waypost Session-Create Ownership Revision

## Status And Scope

- Task: `waypost-session-host-revision`
- Round: 2
- Design branch: `feature/waypost-session-host-revision`
- Baseline: Waypost `94dc7715b1cfc5e87cdc51058dedbf3b1bd764e5`
- Repository scope: Waypost only

This design revises the newly shipped generic `session_create` boundary. It
removes Waypost-owned role/profile configuration and replaces the logical
`launch_profile` input with two caller-supplied mutable launch values:

- `full_command_line` for the fixed `agent-deck` adapter;
- `thurbox_agent_key` for the fixed `thurbox` adapter.

The caller may forward both values without branching. After existing host
selection, Waypost consumes only the value applicable to the selected adapter.
Agentgear owns ordered role-candidate resolution and the correctness of the
values it returns. Waypost remains a two-host session adapter and durable
Waypost transport; it does not become a role resolver, profile store, command
registry, Thurbox agent catalog, or generic host framework.

Waypost 0.6 removes the separate resolve tools. `session_require` now owns
lookup as well as readiness enforcement, returns structured `not_found`, and
accepts `auto_restart=false` for read-only inspection. The remaining two-host
behavior stays in force: `session_create`, `session_require`, the two legacy
Agent Deck create/require tools, same-host parent and target workdir checks,
fixture-pinned Thurbox parsing, recovery-safe create/require results, binding,
notification, and wake behavior.

## Problem

The current implementation makes Waypost load an immutable JSON file mapping a
logical `launch_profile` to an Agent Deck command and a Thurbox agent. That puts
role/profile ownership in the wrong component:

- Agentgear already owns resolving an ordered set of candidates for a workflow
  role.
- Launch values are mutable caller data, not Waypost durable state or Waypost
  process configuration.
- Waypost cannot validate a Thurbox key without crossing into Thurbox-owned
  `agents.toml`, which is explicitly outside its authority.
- Retaining mapping, caching, loading, or reload behavior would duplicate
  resolution policy and couple Waypost to another product's configuration.

The revised boundary must still let one host-neutral caller operate in a nested
Agent Deck or Thurbox context without adding host-specific control flow to the
workflow prompt.

## Goals And Success Criteria

The revision is complete when:

1. `session_create` accepts `full_command_line` and `thurbox_agent_key` as
   optional sibling string properties and no longer exposes `launch_profile`.
2. The selected host requires and consumes only its applicable property.
3. A valid irrelevant sibling property is accepted and ignored without being
   validated, executed, returned, logged, or recorded as a diagnostic.
4. Waypost has no session-host config type, JSON loader, startup mapping,
   profile lookup, immutable copy, reload behavior, or configuration field in
   its MCP service/session manager.
5. Waypost never reads or validates Agentgear configuration or Thurbox
   `agents.toml`; it treats the selected launch value as opaque caller input.
6. Existing `agent_deck_create_session` behavior remains unchanged;
   `agent_deck_require_session` gains the same structured lookup and optional
   restart control as the generic require tool.
7. Existing generic host selection, same-host parent validation, target
   workdir validation, fixture-pinned Thurbox behavior, create/require
   recovery, binding, notification, and wake behavior remain unchanged.
8. Generic results never contain the `full_command_line` or
   `thurbox_agent_key` properties, and Waypost never directly interpolates
   their schema-valid string values into handler-produced errors, diagnostics,
   or logs. Known raw create-command output channels are redacted as specified
   below.
9. A zero-exit create with malformed output or no usable child ID returns one
   fixed public recovery detail and never exposes its parser cause or raw
   stdout.
10. Schema-invalid non-string property values remain ordinary SDK validation
   errors before the handler and are explicitly outside Waypost's launch-value
   non-disclosure guarantee.
11. An upgraded global CLI can still be started by an existing launcher that
   passes `--session-host-config`, but the option is inert and no file is read.
12. No Waypost durable schema or state migration is required.

## Non-Goals

This revision does not:

- modify the Agentgear worktree or define Agentgear's resolver implementation;
- receive a workflow role, candidate list, or profile name in Waypost;
- inspect, discover, parse, or validate Thurbox `agents.toml`;
- add generic list, delete, reset, arbitrary restart, send, group, raw-agent,
  or startup-message tools;
- add a host registry, adapter interface framework, dynamic factory,
  capability table, or third-host extension point;
- change host selection, address formats, binding, durable delivery, notify,
  wake, same-host parent or target workdir invariants, or recovery result
  schemas;
- route legacy Agent Deck handlers through the generic tools;
- replace the typed MCP registration with a custom raw handler or duplicate the
  SDK's object/schema validation solely to redact values that violate the
  advertised string schema;
- provide compatibility semantics for an old `launch_profile`-only
  `session_create` call, because doing so would require the prohibited mapping.

## Ownership And Data Flow

The ownership boundary is:

```text
workflow role
    -> Agentgear ordered-candidate resolver
    -> {full_command_line, thurbox_agent_key}
    -> Waypost session_create
    -> existing host selection
       -> agent-deck adapter consumes full_command_line
       -> thurbox adapter consumes thurbox_agent_key
```

Agentgear decides candidate order, fallbacks, and user-maintained values. It
may return one or both launch fields. Waypost receives no role identifier and
does not know which candidate produced a value. A host-neutral caller may copy
both resolver fields into one `session_create` request; a caller that already
selects a host explicitly may provide only that host's field.

Waypost's responsibility begins at validating the selected value is a non-empty
JSON string and ends at passing it as one argument to the selected, hard-coded
host CLI. Host-side semantic correctness belongs to Agent Deck or Thurbox.

## Public MCP API

### Request schema

`session_create` has this logical request shape:

```json
{
  "host": "agent-deck",
  "session_name": "architect-reviewer-20260808",
  "workdir": "/workspace/waypost",
  "parent_session_id": "parent-session-id",
  "full_command_line": "codex --model gpt-5.6",
  "thurbox_agent_key": "codex"
}
```

The generated object schema has `additionalProperties: false` and these
properties:

| Property | Schema required | Runtime meaning |
| --- | --- | --- |
| `host` | No | Existing strict literal override: `agent-deck` or `thurbox`; omitted host uses existing detection. |
| `session_name` | Yes | Existing conservative generic session-name token. |
| `workdir` | Yes | Existing explicit workdir, canonicalized and verified. |
| `parent_session_id` | Yes | Existing same-host parent ID requirement. |
| `full_command_line` | No | Required after selection only for `agent-deck`; opaque command string passed to Agent Deck. |
| `thurbox_agent_key` | No | Required after selection only for `thurbox`; opaque key passed to Thurbox. |

The two launch properties are optional in JSON Schema because requiredness is
conditional on runtime host selection, including omitted-host detection. A
`oneOf` schema or host-specific nested union is deliberately avoided: it would
make callers branch on host and would interact poorly with an omitted `host`.

`launch_profile` is removed from the schema. The generic schema also continues
to exclude legacy-only `ensure_cmd`, group placement, detachment, and
`startup_instruction` inputs.

### Minimal valid examples

Explicit Agent Deck:

```json
{
  "host": "agent-deck",
  "session_name": "architect-author",
  "workdir": "/workspace/waypost",
  "parent_session_id": "agent-parent",
  "full_command_line": "codex --model gpt-5.6"
}
```

Explicit Thurbox:

```json
{
  "host": "thurbox",
  "session_name": "architect-author",
  "workdir": "/workspace/waypost",
  "parent_session_id": "11111111-1111-4111-8111-111111111111",
  "thurbox_agent_key": "codex"
}
```

Host-neutral forwarding:

```json
{
  "session_name": "architect-author",
  "workdir": "/workspace/waypost",
  "parent_session_id": "same-host-parent-id",
  "full_command_line": "codex --model gpt-5.6",
  "thurbox_agent_key": "codex"
}
```

The last form lets existing host detection select the adapter while the caller
forwards the resolver output unchanged.

## Validation And Ignored-Field Semantics

### Ordered validation

The handler uses this order:

1. MCP JSON Schema rejects a non-object, missing common required properties,
   unknown properties, or a present launch property whose JSON type is not
   string. Type validation applies to every present property before host
   selection, even if that property would later be irrelevant.
2. Waypost selects the host using the existing strict override/detection rules.
   An invalid explicit host or an unknown omitted host returns the existing
   host-selection MCP error. With omitted host, the existing Agent Deck
   detection probe may occur before selected launch-value validation; no
   create/preflight command has run at that point.
3. Waypost validates the existing generic `session_name` rules.
4. Waypost trims leading and trailing Unicode whitespace from the selected
   launch value and requires the result to be non-empty:
   - selected `agent-deck`: missing or blank `full_command_line` returns
     `full_command_line is required when creating an agent-deck session`;
   - selected `thurbox`: missing or blank `thurbox_agent_key` returns
     `thurbox_agent_key is required when creating a thurbox session`.
5. Only after selected launch-value validation does Waypost canonicalize the
   child workdir and perform the existing parent lookup, parent identity
   verification, target-name preflight, and host create command.

Thus an explicit-host request with a missing applicable value invokes no host
command. A request with no detectable host fails host selection before Waypost
can choose which launch field is required.

### Schema-invalid values and the secrecy boundary

The public contract defines both launch properties as strings. The pinned
`google/jsonschema-go v0.3.0` validator runs before the typed handler and
formats a type mismatch with the actual invalid instance. Consequently, a
request such as an array or object in either launch property may be echoed in
the JSON-RPC invalid-parameters error before Waypost code receives the request.

Waypost's launch-value non-disclosure guarantee therefore begins after the MCP
request passes schema validation. It covers Waypost's direct handling of
selected and irrelevant schema-valid strings, create-command execution, raw
create output, recovery error text, diagnostics, and Waypost-owned logs. It
does not cover:

- a non-string instance supplied in violation of the advertised schema;
- MCP client or transport logging of the request;
- validation behavior inside the pinned SDK before the handler;
- a host deliberately copying a launch value into an otherwise public,
  parseable session identity/path field returned by its API.

Callers must not place sensitive content in schema-invalid property shapes.
Unknown-property errors expose property names, not their values. A custom raw
tool registration is rejected because it would require duplicating the SDK's
object, required-property, unknown-property, type, default, output, and tool
error behavior for one endpoint merely to protect inputs that are outside the
declared contract.

### Irrelevant sibling property

Once the request has passed schema/type validation, the property irrelevant to
the selected host is silently ignored:

- it is not trimmed or semantically validated;
- it does not satisfy the selected host's required property;
- it is never passed to a command;
- it is never returned in a normal or recovery result;
- it is never included in an error, warning, log, status field, or diagnostic;
- it does not change host selection or any create preflight.

Examples:

| Selected host | Inputs | Result |
| --- | --- | --- |
| `agent-deck` | non-empty `full_command_line`, no key | accepted |
| `agent-deck` | non-empty command and any string key | accepted; key ignored |
| `agent-deck` | no/blank command and non-empty key | MCP error for missing command |
| `thurbox` | non-empty key, no command | accepted |
| `thurbox` | non-empty key and any string command | accepted; command ignored |
| `thurbox` | no/blank key and non-empty command | MCP error for missing key |

Silent ignore is preferable to rejection because rejection would force the
caller to branch after resolver output, defeating the host-neutral forwarding
goal. A result or warning diagnostic is also rejected: it adds public surface,
can reveal the presence of sensitive launch data, and provides no correctness
benefit because the behavior is deterministic and documented.

### Intentionally absent validation

Waypost does not:

- split, parse, shell-expand, normalize internal whitespace in, or allowlist
  `full_command_line`;
- check that its executable exists or that flags/models/profiles are valid;
- execute the string through a Waypost-owned shell;
- constrain `thurbox_agent_key` to a guessed token grammar;
- read or stat Thurbox `agents.toml`, list Thurbox agents, or verify the key;
- compare either value with Agentgear configuration;
- cache or persist either value.

The only semantic validation is non-empty-after-trim for the selected field.
The selected host CLI owns later acceptance or rejection.

## Adapter Command Construction

The fixed adapter switch remains explicit.

### Agent Deck

For the only command-launching host in this design, Waypost invokes the
equivalent argv:

```text
agent-deck launch --json --title <session_name> --cmd <full_command_line> --parent <parent_session_id> <canonical_workdir>
```

`full_command_line` is one argv element following `--cmd`. Waypost does not
tokenize it and does not invoke it itself. Agent Deck retains ownership of how
that command string launches the child session.

### Thurbox

Waypost invokes the existing fixture-backed form:

```text
thurbox-cli session create --json --name <session_name> --repo-path <canonical_workdir> --agent <thurbox_agent_key> --parent <parent_session_id>
```

`thurbox_agent_key` is one argv element following `--agent`. Waypost neither
discovers nor validates it against Thurbox configuration.

There is no shared launch-profile object and no adapter registry. One helper
may select and normalize the applicable string, followed by the existing
two-case switch.

## Command Redaction And Safety

Schema-valid string launch values are caller data and may contain
operator/user-sensitive content. From typed handler entry onward, the generic
create path keeps the existing redacted command runner and generalizes its
contract from an operator-owned profile value to a caller-supplied launch
value:

- never include argv in a returned error;
- never include stdout, stderr, or an underlying runner error in a non-zero or
  runner-failure result;
- use only a fixed public operation label, timeout text, and numeric exit code;
- never marshal request launch fields into success or recovery results;
- never add logs or diagnostics containing the input struct or create argv;
- never copy a successful create parser error into a public recovery result;
- map malformed zero-exit output and a missing usable child ID, for either
  host, to `verification.state = "create_output_unparseable"` with the exact
  public detail `generic session create returned unusable output`.

The stable command-failure forms remain equivalent to:

```text
generic agent-deck session create failed
generic agent-deck session create failed with exit code <n>
generic thurbox session create failed
generic thurbox session create failed with exit code <n>
```

The Agent Deck and Thurbox parsers may retain rich internal errors for unit
tests and control flow, but `createHostSession` discards that cause before it
calls `createRecoveryResult`. The raw successful stdout is never returned or
logged. The same fixed detail is used for both hosts and for parse failure or a
missing ID, so neither parser grammar nor host output is exposed through this
recovery path.

This closes the direct command-failure and raw create-output echo channels for
schema-valid string values. It is not an information-flow guarantee against a
host deliberately laundering a value into otherwise public, valid normalized
session fields. The MCP request necessarily contains the value; pre-handler
schema-invalid errors and logging performed by an MCP client, transport
wrapper, or host outside Waypost remain outside this guarantee.

Passing each selected value as one argv element prevents Waypost-side option or
shell injection. It does not make an untrusted `full_command_line` safe to run:
the caller and Agent Deck own that trust decision.

## Create Lifecycle And Recovery

Apart from launch-value selection, the current generic create lifecycle and
result contracts are preserved:

1. Select one of the two hard-coded hosts.
2. Validate the generic name and selected launch value.
3. Canonicalize the existing workdir.
4. Require a non-empty same-host `parent_session_id`, reject addresses/names in
   the ID field, and resolve the parent. The parent and child may use different
   workdirs.
5. Resolve the requested child name; if it exists, verify workdir and return
   `target session already exists` rather than ensuring or replacing it.
6. Invoke the selected create command through the redacted runner.
7. Parse the create result, re-resolve the child by returned ID, verify ID,
   requested name, parent, and workdir, then return the existing result shape.

The existing mutation-aware error boundary does not change:

- A non-zero or runner-failed create command is an MCP error because creation
  was not confirmed. The error is redacted as above.
- A zero-exit create whose output is malformed or lacks a usable child ID
  returns `create_recovery_required` with
  `verification.state = "create_output_unparseable"` and the fixed
  `verification.error = "generic session create returned unusable output"`;
  callers resolve by requested name before considering a retry. The internal
  parse/no-ID cause and raw stdout are not copied into the result.
- A create with a usable ID followed by failed lookup, identity mismatch,
  unavailable path, or path mismatch returns `created_unverified` with the
  existing verification state and recovery instruction.
- A fully re-read and verified child returns `created`.
- No recovery path retries, deletes, replaces, rolls back, or mutates Waypost
  delivery state.

Neither `full_command_line` nor `thurbox_agent_key` appears in `created`,
`created_unverified`, or `create_recovery_required`. Existing normalized
session identity, address, verification, and recovery fields remain unchanged.

`session_require` remains launch-value-free. It returns `not_found` for an
absent target, returns `not_ready` for a stopped target when
`auto_restart=false`, and otherwise starts/restarts only an existing verified
target. It retains the `ready` / `ready_unverified` behavior and
non-transactional ordered batch contract.

## Preserved Two-Host Behavior

The following are explicit invariants, not redesign targets:

- Supported generic hosts remain exactly `agent-deck` and `thurbox`.
- A valid nested `THURBOX_SESSION` wins omitted-host selection; otherwise
  existing Agent Deck detection applies; explicit host overrides detection.
- Generic create still requires a same-host parent. The parent and child may
  use different workdirs. Parentless/detached/group/startup behavior remains
  legacy Agent Deck surface only.
- Thurbox get/list/create/restart parsing remains pinned to the checked-in
  v1.7.1 fixtures and continues to fail closed on grammar drift.
- The existing normalized session records and addresses remain unchanged.
- The existing `created`, `created_unverified`,
  `create_recovery_required`, `ready`, and `ready_unverified` schemas remain
  unchanged.
- Valid Thurbox auto-binding, manual-binding precedence, Waypost-durable send,
  fixed wake notice, best-effort notification, and nested wake targeting remain
  unchanged.
- Waypost remains the sole workflow mailbox; neither launch field is workflow
  payload or wake content.

## Compatibility And Migration

### Legacy MCP tools

The legacy Agent Deck create and require tools remain. They do not call generic
`session_create`; require gains `auto_restart` and the structured `not_found`
result. The resolve registration is removed rather than retained as an alias.
The fixed two-host scope is unchanged.

### Generic `session_create`

The generic create schema intentionally makes a breaking correction from
`launch_profile` to caller-supplied values. There is no honest compatibility
implementation for a `launch_profile`-only request after Waypost relinquishes
mapping ownership. Therefore:

- `launch_profile` is removed rather than retained as an alias;
- a new server rejects it as an unknown property;
- callers must use the advertised tool schema and supply the selected value;
- a still-running old MCP process continues its old in-memory behavior until
  it is restarted;
- workflows requiring the revised generic create boundary must ensure they are
  connected to a revised Waypost MCP process, or use the unchanged legacy
  Agent Deck tool when that is the intended host.

No version-negotiation endpoint, dual mapping path, or temporary profile cache
is added. Such mechanisms would retain the prohibited ownership and enlarge
the public surface.

### CLI startup compatibility

Removing `--session-host-config` outright would cause an existing supervisor or
global MCP launcher to fail immediately after a binary upgrade, before it can
discover the new tool schema. The minimal compatibility shim is therefore:

- keep the flag name accepted by `waypost mcp` as deprecated syntax;
- implement it with a no-op flag callback that discards its string argument;
- never open, stat, parse, validate, retain, clone, or pass the value to
  `mcpserver.Options`;
- make omission and presence behaviorally identical;
- document it in MCP help as `deprecated; accepted and ignored`;
- do not add a status warning or persisted marker.

This is launch-command compatibility only, not configuration compatibility.
It can be removed only through a separately approved CLI cleanup after callers
stop passing it. Retaining the JSON loader, config structs, options field, or
profile lookup is not part of the shim.

### Rollback

There is no durable migration. Rolling back to the prior binary restores the
old generic-create schema and uses an explicitly supplied config file if the
old launcher still provides one. Rolling forward ignores that flag and expects
caller-supplied launch values. Waypost delivery, lease, group, address, and
session-host durable data do not change in either direction.

## Implementation Boundaries

The implementation should stay within these narrow changes:

### `internal/mcpserver/session_tools.go`

- Replace `LaunchProfile string` with these optional fields:

  ```go
  FullCommandLine string `json:"full_command_line,omitempty"`
  ThurboxAgentKey string `json:"thurbox_agent_key,omitempty"`
  ```

- Update the `session_create` description to state that the selected adapter
  consumes its applicable caller-supplied value.
- Remove comments claiming the generic surface accepts only logical profiles.
- Pass both values into the fixed create helper; do not add a role or candidate
  object.

### `internal/mcpserver/session_host.go`

- Rename the shared conservative token regexp so it describes generic session
  names rather than launch profiles.
- Delete `validateLogicalLaunchProfile`.
- Add one small explicit two-host selector/validator for the applicable launch
  value and the errors defined above.
- Change `createHostSession` to accept the two request values, select one, and
  feed it to the existing Agent Deck or Thurbox command.
- Keep redacted execution and post-create identity/workdir checks unchanged.
- Replace `parseErrOrMissingID` with a fixed public create-output recovery
  detail. Parser errors remain available locally for detecting failure but are
  never passed to `createRecoveryResult`.
- Move the generic `ensureJSONEOF` helper from the deleted config file into
  `session_host.go` beside `parseThurboxSessionList` /
  `parseStrictJSONObject`, add the required `io` import there, and retain
  multiple-JSON-value coverage.

### Remove session-host configuration ownership

- Delete `internal/mcpserver/session_host_config.go`.
- Remove `SessionHostConfig` from `mcpserver.Options`.
- Remove `sessionHostConfig` from `sessionManager` and the startup clone.
- Remove all profile lookup/configuration gates from generic create.
- Delete the config file only after relocating `ensureJSONEOF`; the helper is
  parser infrastructure, not configuration ownership.

### `internal/rootcmd/root.go`

- Remove config loading, validation, and propagation into `mcpserver.Options`.
- Retain only the deprecated no-op `--session-host-config` parser described
  above and update help text.

### Documentation

- Replace the launch-profile/config sections and examples in
  `docs/waypost-session-host-design.md` with this ownership boundary, request
  schema, ignored-field rule, redaction behavior, and compatibility shim.
- Update `docs/mcp-cli-tool-surface-design.md` so its generic-tool description
  does not incorrectly imply that generic create has no opaque launch input.
- Update public README/tool guidance where the generic session tools and MCP
  startup syntax are listed.
- Update the MCP tool description and remove all claims that Waypost maps an
  operator profile.

No notifier, wake scheduler, address, durable Waypost service, Thurbox fixture,
or Agentgear file needs modification. The only parser support movement is the
shared EOF helper described above.

## Focused Test Plan

### Public schema and compatibility

1. Assert `session_create` requires only `session_name`, `workdir`, and
   `parent_session_id` at schema level.
2. Assert it exposes optional `full_command_line` and
   `thurbox_agent_key`, excludes `launch_profile` and legacy-only fields, and
   rejects unknown properties.
3. Assert all legacy `agent_deck_*` schemas and tool inventory are unchanged.
4. Retain existing pre-`waypost_status` availability tests for generic and
   legacy session tools.

### Selected-value matrix

5. For explicit `agent-deck`, verify command-only and both-field requests
   succeed; missing/blank command fails before any host command; an arbitrary
   string key is ignored.
6. For explicit `thurbox`, verify key-only and both-field requests succeed;
   missing/blank key fails before any host command; an arbitrary command is
   ignored.
7. Verify an irrelevant property with the wrong JSON type is rejected by the
   schema, without asserting non-disclosure for the SDK's pre-handler error;
   verify separately that an irrelevant property that is any valid string
   cannot alter the selected command argv or result.
8. Verify omitted-host selection still prefers nested Thurbox and consumes the
   key even when a command is also present; otherwise detected Agent Deck
   consumes the command.
9. Verify the selected value is trimmed at its outer boundary and internal
   content is passed unchanged as one argv element.

### Ownership removal and startup compatibility

10. Delete config loader/immutability/profile-map tests and replace root tests
    with proof that `waypost mcp --session-host-config <nonexistent-or-malformed>`
    starts without reading the path or altering `mcpserver.Options`.
11. Assert MCP help labels the accepted flag deprecated and ignored.
12. Retain `ensureJSONEOF` under `session_host.go` and its strict
    multiple-JSON-value parser coverage before deleting the config file.
13. Use repository search to assert no production reference remains to
    `SessionHostConfig`, `SessionHostProfile`, `LoadSessionHostConfig`,
    `profileForHost`, or `launch_profile`.

### Command mapping, redaction, and recovery

14. Verify Agent Deck receives only the selected command after `--cmd`, and
    Thurbox receives only the selected key after `--agent`.
15. For both hosts, simulate runner errors and non-zero results whose runner
    error/stdout/stderr echo a secret value; assert the returned error contains
    neither selected nor ignored field content.
16. For both hosts, simulate a zero-exit malformed create stdout containing
    both selected and ignored secret strings; assert
    `create_recovery_required`, the exact fixed public detail, and absence of
    both secrets and the internal parser cause.
17. Also cover a zero-exit parseable result with no usable ID and assert the
    same fixed public detail.
18. Assert normal, `created_unverified`, and `create_recovery_required` results
    contain neither launch field nor `launch_profile`.
19. Update existing parent, preflight failure, malformed create output,
    post-create path mismatch, and identity mismatch tests to pass the new
    selected fields without changing their expected recovery behavior.
20. Retain all session require, Thurbox fixture/parser, binding, notify, wake,
    and durable-delivery regressions unchanged.

### Verification commands

- Run focused root and MCP server tests first.
- Run `go test ./...`.
- Run `go vet ./...` if it is part of the repository's normal verification.
- Confirm `git diff --check` and inspect the final public schema/tool help.

## Persisted Data Changes

None.

The removed JSON file was process startup configuration, not Waypost durable
state. The revised launch values are request-scoped and are not persisted.
Waypost message, delivery, lease, group, address, binding, and recovery schemas
remain unchanged.

## Risks And Tradeoffs

- The public request now carries a full command string. This correctly places
  resolution ownership with the caller but means MCP client/transport logs may
  observe it; Waypost can guarantee only that its own results/errors/logs do not
  echo a schema-valid string after typed handler entry.
- The pinned SDK may echo a schema-invalid non-string instance before the
  handler. A custom raw registration could close that gap only by duplicating
  substantial SDK validation/output behavior; this design instead documents
  the boundary and requires callers not to place secrets in invalid shapes.
- A host could deliberately copy a launch value into a valid public session
  identity/path field. Filtering all normalized host data would break existing
  recovery observability and is outside this direct redaction boundary; the
  adapter and host CLIs remain trusted components.
- Fixed create-output recovery text reduces runtime parser observability. The
  fixture/parser unit tests retain detailed failure evidence without exposing
  host stdout or parser causes to workflow callers.
- Silent ignore of the irrelevant sibling can conceal stale resolver output.
  The alternative forces host branching, so deterministic documented ignore
  is the smaller and more useful contract.
- Non-empty-only validation moves semantic failures to Agent Deck or Thurbox.
  That is intentional: deeper validation would duplicate host or Agentgear
  ownership and would drift.
- The inert CLI flag leaves a small deprecated parser surface. It is justified
  only by global launcher compatibility and contains no configuration logic.
- Old and new generic-create callers are not wire-compatible across an MCP
  process restart. Supporting the old semantic contract would require exactly
  the mapping this revision removes. Legacy Agent Deck tools remain the stable
  compatibility path.
- A caller can provide an unsafe command. Waypost passes it to Agent Deck as
  opaque trusted launch configuration; it does not make or claim a safety
  decision on the caller's behalf.

## Alternatives Rejected

### Keep or reload the Waypost profile JSON

Rejected because it preserves the wrong ownership, duplicates Agentgear, and
directly violates the required boundary.

### Pass `launch_profile` directly as an Agent Deck command

Rejected because the field name would lie about its semantics, Thurbox would
still need mapping, and callers could accidentally turn a logical identifier
into executable content.

### Use a host-discriminated `oneOf` or nested launch object

Rejected because omitted-host detection cannot choose a schema branch in
advance and callers would have to branch on host. Two optional siblings are
the smallest schema that lets Agentgear output be forwarded unchanged.

### Reject a supplied irrelevant field

Rejected because it makes the caller remove one resolver value after host
selection, leaking host-specific control flow back into the workflow.

### Return an ignored-field diagnostic

Rejected because it adds result surface and may disclose the presence of
sensitive launch data without changing behavior.

### Validate Thurbox keys against `agents.toml`

Rejected because the file and its correctness belong to Thurbox/user-managed
Agentgear configuration. It would cross repository and ownership boundaries.

### Add an adapter registry or general launch-value map

Rejected because the product scope is exactly two hosts and two explicit
values. A registry or arbitrary map adds speculative flexibility and weakens
schema clarity.

### Add a custom raw MCP handler for invalid-type redaction

Rejected because the typed SDK validator is the only pre-handler disclosure
path, and only for values that violate the advertised string schema. Bypassing
it would require Waypost to duplicate input and output validation, defaults,
unknown-property behavior, JSON-RPC error mapping, and tool-result assembly for
one tool. That change is disproportionate to the contract and increases drift
risk.

## Open Questions

None requiring requester input. Non-disclosure intentionally covers
schema-valid string launch fields from typed handler entry onward; pre-handler
SDK errors for schema-invalid values are outside that guarantee. The design
also uses fixed public create-output recovery text, deterministic silent ignore
for the irrelevant valid string, and a deprecated inert CLI-flag shim.

## Compatibility Amendment: Agent Deck Parent-Group Snapshot (2026-08-09)

This Waypost-only amendment records the accepted
`waypost-agent-deck-group-inference` design
(`.agent-artifacts/design-spec/9bb9e861-1786247385/r002.md`) against the
baseline commits `760e083` and `0b55ce0`. It does not change the generic
`session_create` request or result schemas, Thurbox behavior, legacy Agent Deck
tools, Agentgear, or durable delivery semantics.

For generic Agent Deck creation, Waypost reads the direct parent's `group`
from the same authoritative `session show` record used for parent identity
validation, requires a top-level parent with a non-empty group, and passes that
exact preflight snapshot explicitly as `--group <snapshot>` alongside the
requested `--parent`. The child is created with one redacted launch command,
trusts only the receipt ID, then refreshes the child and verifies name, parent,
workdir, and the captured group before returning `created`.

Root/empty-group parents and parents that are themselves child sessions fail
before launch with fixed redacted errors. A non-empty group path missing from
Agent Deck's registry may be recreated by Agent Deck's explicit-group behavior.
The guarantee is snapshot-based (`child.group == parent.group` observed during
Waypost preflight), not atomic against a concurrent later parent move. A
refreshed group mismatch returns `created_unverified` with recovery state
`post_create_group_mismatch`; Waypost does not move, delete, or relaunch the
child.

The implementation is limited to `internal/mcpserver/session_host.go`, focused
tests, the generic tool description, and this documentation amendment. It adds
only an internal normalized `Group` field, explicit `--group` argv, pre-create
parent-shape gates, and refreshed-group verification. No group-list probe,
second launch, post-create move, fallback launch, or Agent Deck version probe
is permitted. Thurbox argv and all legacy group-placement behavior remain
unchanged.
