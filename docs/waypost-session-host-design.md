# Two-Host Waypost Session Layer

## Summary

Add three additive, host-neutral MCP tools:

- session_resolve
- session_create
- session_require

They support exactly two hard-coded hosts: agent-deck and thurbox.  Waypost is
the sole durable message and lease authority.  A Thurbox session send is only
a fixed, best-effort wake hint after Waypost durably accepts a delivery; it
never carries workflow input and never changes the delivery.

The existing agent_deck_resolve_session, agent_deck_create_session, and
agent_deck_require_session tools remain separate compatibility tools.  Their
schemas, inputs, result maps, group placement, startup-instruction behavior,
Agent Deck auto-binding, and Agent Deck notification behavior are unchanged.
The generic tools do not call through legacy handlers because that would either
expose Agent Deck-only inputs or alter legacy results.

The implementation uses one small normalized session record and explicit
switches over the two host literals.  It adds no plugin, registry, dynamic
factory, capability table, host discovery framework, or generic lifecycle
framework.

## Problem And Scope

A workflow that can run under Agent Deck or Thurbox should not branch on host,
know an Agent Deck command, or decide whether Agent Deck group placement
applies.  It needs only:

- an existing or new same-host child session;
- a same-host parent;
- a verified workspace;
- a Waypost address to receive durable workflow input.

Host launch configuration belongs to an operator, not a workflow prompt.
Agent Deck grouping remains useful legacy behavior but is not a portable
session invariant.  Thurbox's native mailbox must not become a second
workflow transport.

The layer deliberately covers resolve, create, and require only.  It does not
add generic list, delete, reset, arbitrary restart, send, group, raw command,
raw agent, or startup-message tools.  There is no generic Waypost CLI session
surface and no Agentgear change.

## Goals And Success Criteria

The design is successful when:

1. A host-neutral workflow prompt uses the same resolve, create, and require
   calls for either supported host.
2. The prompt supplies a logical launch profile, never an Agent Deck command,
   a Thurbox agent value, group placement, or a host-specific startup input.
3. A valid nested THURBOX_SESSION selects Thurbox when host is omitted, even
   when Agent Deck is concurrently detectable.
4. Existing agent_deck_* tools retain their current public behavior.
5. Waypost delivery remains successful and recoverable independently of a
   Thurbox wake outcome or host-operation verification outcome.
6. The generic contract relies only on a same-host parent and verified
   workdir, never an Agent Deck group.

## Release Prerequisite For Thurbox

Real Thurbox support is not enabled on a guessed CLI contract.  Before the
Thurbox host is merged or released, the implementation must contain
version-pinned, fixture-backed evidence for the selected thurbox-cli version:

- the exact JSON envelopes and required fields for session get, list, and
  create;
- the identity and exact-name resolution rules;
- the one declared effective-workdir field and its canonicalization behavior;
- the allowed active and stopped status vocabulary;
- the restart result needed to re-read a ready session.

The adapter parser and tests are written from that selected grammar, not from
fallback guesses over every similarly named JSON field.  It fails closed on
malformed, missing, changed, or unclassified data.

Before this prerequisite is met, the current released behavior remains
Agent-Deck-only: no public generic Thurbox host, no Thurbox notifier, and no
Thurbox auto-binding is shipped.  A development build reports Thurbox
unavailable without running thurbox-cli rather than inferring a path or restart
state.  The final two-host release is enabled only after the fixture gate
lands; profile configuration cannot bypass it.

Version-pinned fixture tests and fail-closed parsing remain the drift defense
after release.  A future Thurbox CLI upgrade needs a reviewed fixture/parser
update before Waypost relies on any changed grammar.

## Public MCP Surface

The three generic tools are additive and, like legacy Agent Deck session tools,
are usable before waypost_status.  They do not claim, mutate, or read Waypost
durable state, so they do not have a Waypost status gate.

### Shared host selection

Every generic request accepts optional host:

    host: "agent-deck" | "thurbox"

When present, the literal value selects the host after strict validation.  When
absent, selection is:

1. A valid THURBOX_SESSION selects thurbox.
2. Otherwise, current Agent Deck detection selects agent-deck when it finds a
   session.
3. Otherwise the call is an MCP error: session host is unknown; set host
   explicitly.

There is deliberately no ambiguous-host error for valid nested Thurbox
context.  THURBOX_SESSION identifies the immediate host, while a concurrent
Agent Deck identity usually identifies an outer host.  Explicit host remains
available to operate on a non-current host.

Only the two literal values are accepted.  Supporting another host later is an
intentional product and code change, not a configuration-only extension.

### Shared result fields

Found, created, ready, and recoverable-operation records use these common
fields:

    {
      "host": "thurbox",
      "status": "found",
      "session_id": "tb-123",
      "session_ref": "architect-author",
      "session_name": "architect-author",
      "session_status": "waiting",
      "path": "/workspace/waypost",
      "parent_session_id": "tb-001",
      "addresses": ["thurbox/tb-123"]
    }

session_name, session_status, path, and parent_session_id are null when a host
does not report them.  addresses is exactly one normalized host address when
session_id is known.  The generic surface never returns an Agent Deck title,
group, raw launch command, raw Thurbox agent, or profile contents.

### session_resolve

Request exactly one of session and a non-empty sessions array:

    {
      "host": "agent-deck",
      "session": "architect-author"
    }

or:

    {
      "host": "thurbox",
      "sessions": ["architect-author", "architect-reviewer"]
    }

session and each sessions element are host session references.  An adapter may
recognize a host-issued ID or an exact name under its documented grammar.  A
duplicate exact name is an error, never an arbitrary selection.

A missing single target returns:

    {
      "host": "thurbox",
      "status": "not_found",
      "session_ref": "architect-author"
    }

The batch response is normatively:

    {
      "host": "thurbox",
      "results": [
        {
          "host": "thurbox",
          "status": "found",
          "session_id": "tb-123",
          "session_ref": "architect-author",
          "session_name": "architect-author",
          "session_status": "waiting",
          "path": "/workspace/waypost",
          "parent_session_id": "tb-001",
          "addresses": ["thurbox/tb-123"]
        },
        {
          "host": "thurbox",
          "status": "not_found",
          "session_ref": "architect-reviewer"
        }
      ]
    }

Results preserve input order.  A per-item operational error uses host, status
error, session_ref, and error.  Malformed top-level input and host-selection
errors are MCP errors, matching the current Agent Deck batch convention.

### session_create

Request:

    {
      "host": "thurbox",
      "session_name": "architect-author-20260808",
      "workdir": "/workspace/waypost",
      "parent_session_id": "tb-001",
      "launch_profile": "architect"
    }

All fields except host are required.  session_name and launch_profile use the
conservative host-neutral token form: an ASCII letter or digit followed by
letters, digits, dot, underscore, or hyphen.  Existing legacy Agent Deck title
rules remain unchanged.

parent_session_id is a same-host ID, not an address or name.  Before host
creation, the adapter resolves it and verifies that the parent path exists and
canonically equals workdir.  A missing parent, wrong-host parent, unverifiable
path, or different workdir is a pre-create MCP error.

workdir must exist and is canonicalized using the existing symlink-aware
helper.  The child must also report the same canonical effective workdir.
Parentless and detached generic creation are intentionally excluded because
they do not have one portable lifecycle meaning.  The explicit Agent Deck
compatibility tool retains its existing support for those cases.

Creation is not an ensure operation.  Before creating, the adapter resolves
the requested name.  An existing name is verified against workdir and then
returns target session already exists.  The caller uses resolve and require
for an existing target.

#### Normal successful create

After a parseable create result and successful post-create workdir verification,
the response is:

    {
      "host": "thurbox",
      "status": "created",
      "created_target": true,
      "started_session": true,
      "recovery_required": false,
      "verification": {
        "state": "verified",
        "requested_workdir": "/workspace/waypost",
        "observed_path": "/workspace/waypost"
      },
      "session_id": "tb-123",
      "session_ref": "architect-author-20260808",
      "session_name": "architect-author-20260808",
      "session_status": "waiting",
      "path": "/workspace/waypost",
      "parent_session_id": "tb-001",
      "addresses": ["thurbox/tb-123"]
    }

started_session means the host created a runnable session.  It does not mean
the session received or processed Waypost work.

#### Recoverable post-create result

Host creation is externally mutating, so a successful host create must never
be hidden behind a plain tool error if Waypost cannot prove the child
postcondition.  A parseable host result with a child ID returns a successful
MCP result with status created_unverified:

    {
      "host": "thurbox",
      "status": "created_unverified",
      "created_target": true,
      "started_session": true,
      "recovery_required": true,
      "verification": {
        "state": "path_mismatch",
        "requested_workdir": "/workspace/waypost",
        "observed_path": "/workspace/other",
        "error": "session path mismatch"
      },
      "session_id": "tb-123",
      "session_ref": "architect-author-20260808",
      "session_name": "architect-author-20260808",
      "session_status": "waiting",
      "path": "/workspace/other",
      "parent_session_id": "tb-001",
      "addresses": ["thurbox/tb-123"]
    }

The allowed non-verified states are path_mismatch, path_unavailable, and
post_create_lookup_failed.  observed_path is null when unavailable.  Callers
must not retry session_create blindly.  They first call session_resolve with
the returned host and session_ref, then choose an operator-approved recovery
path.  The generic layer performs no delete, replacement, or cleanup.

If the create command reports success but its output is malformed or lacks a
usable child ID, a child may still exist but Waypost cannot safely return an
address.  The tool returns a successful recovery record instead of an MCP
error:

    {
      "host": "thurbox",
      "status": "create_recovery_required",
      "created_target": null,
      "started_session": null,
      "recovery_required": true,
      "verification": {
        "state": "create_output_unparseable",
        "requested_workdir": "/workspace/waypost",
        "observed_path": null,
        "error": "thurbox session create returned invalid JSON"
      },
      "session_id": null,
      "session_ref": "architect-author-20260808",
      "session_name": "architect-author-20260808",
      "session_status": null,
      "path": null,
      "parent_session_id": "tb-001",
      "addresses": []
    }

Its recovery instruction is to resolve the requested name before any retry.
If resolution finds a session, the caller has a host ref and can inspect or
require it; if it does not, the operator decides whether another create is
safe.  No generic cleanup is attempted.

A non-zero host create command is an MCP error because creation was not
confirmed.  The distinction is based on whether the host reported successful
creation, not on an assumption that all failures are rollback-safe.

### session_require

Single-target request:

    {
      "host": "agent-deck",
      "session_id": "ad-123",
      "workdir": "/workspace/waypost"
    }

or:

    {
      "session_ref": "architect-author",
      "workdir": "/workspace/waypost"
    }

Exactly one of session_id, session_ref, and a non-empty sessions array is
required.  workdir is always required.  session_id is a host-issued ID;
session_ref and each sessions element use the same reference semantics as
session_resolve.

Require resolves the target and verifies canonical effective-path equality with
workdir.  If the target is already active, it re-reads enough host state to
return a fully verified ready record; any failure before a confirmed host
start/restart is an ordinary MCP error for a single request or an ordinary
per-item error for a batch.

For a fixture-proven stopped target, require invokes the selected host's safe
start behavior.  Agent Deck retains its existing start behavior.  Thurbox
restarts only fixture-proven stopped statuses.  Unknown, failed, deleting, or
unclassified states never receive an optimistic restart.  This does not claim
that Agent Deck start and Thurbox restart preserve identical host lifecycle
semantics.

#### Fully verified ready result

ready is reserved for a target re-read and verified after all relevant
operations:

    {
      "host": "thurbox",
      "status": "ready",
      "created_target": false,
      "started_session": true,
      "recovery_required": false,
      "verification": {
        "state": "verified",
        "requested_workdir": "/workspace/waypost",
        "observed_path": "/workspace/waypost"
      },
      "session_id": "tb-123",
      "session_ref": "architect-author",
      "session_name": "architect-author",
      "session_status": "waiting",
      "path": "/workspace/waypost",
      "parent_session_id": "tb-001",
      "addresses": ["thurbox/tb-123"]
    }

started_session is false when the verified session was already active and true
only when this call confirmed the host accepted a start or restart action.

#### Recoverable post-start or post-restart result

Once a host accepts a start or restart, require has performed an external
mutation.  It must not return an ordinary error if it subsequently cannot
re-read and verify the same target.  Instead, a single request returns a
successful MCP result with status ready_unverified:

    {
      "host": "thurbox",
      "status": "ready_unverified",
      "created_target": false,
      "started_session": true,
      "recovery_required": true,
      "verification": {
        "state": "post_start_lookup_failed",
        "requested_workdir": "/workspace/waypost",
        "observed_path": null,
        "error": "target session not found after restart"
      },
      "session_id": "tb-123",
      "session_ref": "architect-author",
      "session_name": "architect-author",
      "session_status": "stopped",
      "path": "/workspace/waypost",
      "parent_session_id": "tb-001",
      "addresses": ["thurbox/tb-123"]
    }

The identity, address, and last known fields come from the verified pre-start
record.  Allowed non-verified states are post_start_lookup_failed,
post_start_output_unparseable, post_start_disappeared, post_start_not_ready,
and post_start_path_mismatch.  observed_path is null when a re-read did not
produce one.

Callers must not automatically retry require, delete, restart, replace, or
roll back after ready_unverified.  They first call session_resolve using the
returned host and session_id or session_ref, then use an operator-approved
recovery path.  The generic layer performs no cleanup.  A start/restart command
that returns non-zero remains an ordinary error because it was not confirmed.

### Require batch contract

Batch request:

    {
      "host": "thurbox",
      "sessions": ["architect-author", "architect-reviewer"],
      "workdir": "/workspace/waypost"
    }

The response is normatively:

    {
      "host": "thurbox",
      "results": [
        {
          "host": "thurbox",
          "status": "ready",
          "created_target": false,
          "started_session": true,
          "recovery_required": false,
          "verification": {
            "state": "verified",
            "requested_workdir": "/workspace/waypost",
            "observed_path": "/workspace/waypost"
          },
          "session_id": "tb-123",
          "session_ref": "architect-author",
          "session_name": "architect-author",
          "session_status": "waiting",
          "path": "/workspace/waypost",
          "parent_session_id": "tb-001",
          "addresses": ["thurbox/tb-123"]
        },
        {
          "host": "thurbox",
          "status": "ready_unverified",
          "created_target": false,
          "started_session": true,
          "recovery_required": true,
          "verification": {
            "state": "post_start_output_unparseable",
            "requested_workdir": "/workspace/waypost",
            "observed_path": null,
            "error": "thurbox session get returned invalid JSON"
          },
          "session_id": "tb-124",
          "session_ref": "architect-reviewer",
          "session_name": "architect-reviewer",
          "session_status": "stopped",
          "path": "/workspace/waypost",
          "parent_session_id": "tb-001",
          "addresses": ["thurbox/tb-124"]
        }
      ]
    }

The batch is explicitly non-transactional.  It processes each session reference
in input order even if an earlier item is error or ready_unverified.  It never
rolls back a confirmed start or restart before a later item fails.  Every
confirmed host-mutating action is visible as started_session true in its ready
or ready_unverified record.  A status error record is reserved for an item
whose start/restart was not confirmed or which failed before mutation:

    {
      "host": "thurbox",
      "status": "error",
      "session_ref": "architect-reviewer",
      "started_session": false,
      "error": "session path mismatch"
    }

Malformed top-level input, unknown host, and invalid shared workdir fail before
the loop as MCP errors.  Per-item resolution, path, status, and unconfirmed
start/restart failures become ordered records.  The surface has no generic
delete, reset, or arbitrary restart endpoint.

## Operator-Owned Launch Profiles

Generic creation maps a logical launch_profile to host-specific values outside
workflow prompts.  A profile name is not an executable command.

The MCP process accepts one optional explicit startup flag:

    waypost mcp --session-host-config /absolute/path/session-hosts.json

The configuration lifecycle is deterministic:

1. If the flag is omitted, the MCP server starts normally.  session_resolve
   and session_require work, while session_create fails before any host command
   with generic session creation requires session-host configuration.
2. If the flag is supplied, the server parses and validates that one file once
   before accepting MCP connections.  A missing, unreadable, malformed, or
   schema-invalid file fails MCP startup; no partially configured server runs.
3. A valid file is immutable for that server lifetime.  Waypost does not watch,
   reload, or partially apply file changes.  An operator restarts the MCP
   server to use a changed file.

This is only MCP startup configuration.  It does not add a generic Waypost CLI
session surface or a Waypost session subcommand.  There is no implicit
directory scan, environment-derived command, or fallback profile.

The strict JSON file shape is:

    {
      "profiles": {
        "architect": {
          "agent_deck_command": "codex --model gpt-5.6",
          "thurbox_agent": "codex"
        }
      }
    }

Unknown fields are rejected.  Profile keys use the logical token form.  Each
profile contains at least one non-empty value:

- creation on agent-deck requires agent_deck_command;
- creation on thurbox requires thurbox_agent.

The file is trusted operator configuration because agent_deck_command is
ultimately executable host configuration.  Tool results never expose its
contents.  With a valid file, an unknown profile or a profile missing the
selected host's value fails before any create command.

The fixed translations are:

| Selected host | Create translation |
| --- | --- |
| agent-deck | agent-deck launch with mapped Agent Deck command, child name, canonical workdir, and same-host parent |
| thurbox | thurbox-cli session create with child name, canonical workdir as repo-path, mapped Thurbox agent, and same-host parent |

Agent Deck may retain its existing internal group placement when a parent is
used, but generic code neither supplies, verifies, nor returns a group.  Its
only portable invariant is parent plus verified workdir.

## Adapter Behavior

Use one normalized internal record containing host, ID, name, status,
effective path, and parent ID.  The implementation switches explicitly on the
two literal hosts; it has no host interface registry or dynamic factory.

Agent Deck calls existing show, launch, and start helpers.  Generic result
normalization is separate from legacy output mapping, so legacy behavior does
not change.

The fixture-approved Thurbox adapter uses:

- session get for a known ID;
- session list only to resolve an exact unique name;
- session create for generic creation;
- session restart only from the approved stopped status set.

It chooses exactly one fixture-proven effective-path field.  It does not fall
back arbitrarily across repo_path, cwd, path, and worktree_path.  A found
session without the declared field can be returned by resolve with path null,
but require fails before mutation because it cannot verify workdir.  A
post-create lack of that field returns created_unverified; a post-start lack
after a confirmed host action returns ready_unverified.

Host command failures, malformed get/list output, duplicate exact names, and
unverifiable paths are ordinary errors only when they happen before a confirmed
create, start, or restart.  The two recoverable result states preserve all
known identity and state when a confirmed external action cannot be verified.
No error path creates a replacement, deletes a target, or changes Waypost
delivery state.

## Binding, Addresses, And Host Context

The released two-host version adds thurbox to Waypost's strict session-address
schemes.  A valid THURBOX_SESSION auto-binds thurbox/<id> during normal
automatic binding.  Invalid values are ignored with an allowlisted diagnostic
warning.

Existing Agent Deck discovery still runs and its address remains bound when
detected.  An automatic nested context may therefore have both addresses.  In
that situation the default sender and generic omitted-host selection use valid
Thurbox identity first because it is the immediate host.  With no valid
Thurbox context, Agent Deck address ordering and default-sender behavior stay
as they are today.

An explicit waypost_bind is authoritative.  Automatic detection never appends
Thurbox or Agent Deck addresses to a user-supplied binding afterward.  This
preserves manual binding and existing Agent Deck workflows.

waypost_status and waypost_debug may add detected_thurbox_session_id and
corresponding diagnostics.  Existing fields retain their names and meaning.

When THURBOX_SESSION is valid, an Agent Deck probe failure is diagnostic only;
it cannot prevent Thurbox selection or auto-binding.  When it is absent,
existing Agent Deck auto-binding failure behavior remains.

## Durable Delivery And Wake Behavior

Waypost is the sole workflow mailbox:

1. A sender calls waypost_send to thurbox/<session-id>.
2. Waypost durably stores the delivery and returns its delivery ID.
3. Only after that success, Waypost may send the fixed notice
   "NOTICE: There might be new delivery in waypost." through
   thurbox-cli session send.
4. The Thurbox session wakes and calls waypost_recv for the actual body and
   lease.

The Thurbox notice contains no workflow body, headers, lease token, delivery
ID, or decision data.  It is a best-effort interrupt/hint, not native Thurbox
workflow delivery.  If it fails, Waypost still returns status sent with normal
notify_status and notify_error diagnostics.  The delivery remains queued and
is never rolled back, acknowledged, released, deferred, or deleted because a
wake failed.

Agent Deck direct notification and its wake probe remain unchanged.  Thurbox
uses a separate notifier and wake channel.  Direct delivery to one host address
never aliases to the other host.

In valid nested Thurbox context, the local wake scope includes all durable
bound addresses for pending-work accounting but its targeted wake target is
only the current Thurbox session.  It does not wake a concurrently detected
outer Agent Deck session.  With no nested Thurbox context, the existing Agent
Deck local wake target stays unchanged.

The scheduler may add a Thurbox targeted-wake stage with the existing
best-effort targeted-wake category, Waypost-truth check, one-attempt-per-cycle
policy, cooldown, and inter-channel gap.  A wake attempt is not evidence of
delivery progress.

## Compatibility And Migration

This is additive:

1. Keep all three agent_deck_* registrations and handlers unchanged.  Do not
   route them through generic host selection, profile lookup, validation, or
   response normalization.
2. Add the three session_* tools beside them.  The exact MCP inventory changes
   from twelve to fifteen names, and hard-cut documentation/test wording
   changes accordingly.
3. After the Thurbox fixture prerequisite, add its address recognition,
   context detection, generic adapter, and separate notifier.
4. Future host-neutral workflow prompts prefer session_*.  Existing Agent Deck
   prompts continue using legacy tools without conversion.
5. Retain Agent Deck compatibility tools until a separately approved migration
   says otherwise.  This design does not schedule removal.

Omitting session-host configuration affects only generic creation.  Supplying
an invalid configuration prevents MCP startup.  Neither case changes legacy
Agent Deck create/resolve/require calls, Waypost delivery, or Agent Deck
notification behavior.

The current uncommitted exploration is design input only.  The accepted
implementation must not:

- pass launch_profile directly as an Agent Deck command;
- make a valid nested Thurbox identity ambiguous with Agent Deck;
- append auto-detected addresses to manual bindings;
- guess a Thurbox JSON path or restart vocabulary;
- return a plain error after a confirmed host create;
- return a plain error after a confirmed host start/restart;
- hide a confirmed start/restart in a partially failed require batch;
- wake both hosts by default from nested Thurbox context.

No WIP is staged, committed, globally installed, or copied into formal
documentation during this design round.

## Implementation Boundaries

Waypost-only touch points are:

- internal/mcpserver/session_tools.go for public generic schemas, response
  maps, partial-create and partial-require results, and batch contracts;
- internal/mcpserver/session_manager.go plus a small session-host helper for
  context selection, normalized records, and the fixed host switch;
- internal/mcpserver/server.go and mcp startup parsing for one immutable
  profile-file load;
- internal/mcpserver/notifier.go and wake_scheduler.go for Thurbox wake;
- internal/waypost/address.go for the released Thurbox address scheme;
- server tests and the MCP surface document.

Agentgear is out of scope.  It may later consume generic MCP tools, but this
work does not modify it or introduce generic Waypost CLI session commands.

## Focused Test Plan

1. Public-surface regression
   - Assert exactly fifteen MCP tool names.
   - Preserve all agent_deck_* schemas, result maps, command invocations,
     group behavior, and startup-instruction behavior.
   - Confirm generic and legacy session tools run before waypost_status.

2. Generic contract tests
   - Reject unknown hosts, raw host-specific creation fields, empty/conflicting
     batch inputs, missing parent/workdir/profile, and invalid neutral names.
   - Verify ordered single and batch resolve/require response shapes.
   - Verify batch require continues after per-item error or ready_unverified,
     is non-transactional, and reports every started_session action.
   - Verify generic results expose neither group nor operator command data.

3. Create and require recovery tests
   - Verify successful create returns created and a verified host address.
   - Simulate known-ID post-create path mismatch, unavailable path, and lookup
     failure; assert created_unverified includes ID/ref/address, observed state,
     and recovery_required.
   - Simulate successful host create with malformed/no-ID output; assert
     create_recovery_required includes name/ref and requires resolve before
     retry.
   - Simulate a confirmed start/restart followed by failed re-read, malformed
     re-read, disappearance, not-ready status, or path mismatch; assert
     ready_unverified includes pre-action identity/address,
     started_session true, and recovery_required.
   - Assert no generic delete, replacement, rollback, or automatic retry
     occurs in any recovery state; an unconfirmed create/start/restart is an
     ordinary error.

4. Host selection, configuration, and Thurbox contract tests
   - Valid THURBOX_SESSION wins over concurrent Agent Deck detection.
   - Explicit host overrides detection; invalid THURBOX_SESSION only warns.
   - Explicit waypost_bind is not mutated by automatic detection.
   - An omitted profile flag starts MCP but makes only generic create
     unavailable.
   - A supplied missing, unreadable, malformed, or schema-invalid profile file
     fails MCP startup before it accepts a connection.
   - A valid configuration is read once; later file changes do not affect the
     running server.
   - Profile mapping passes only mapped Agent Deck command or Thurbox agent;
     missing selected-host mapping invokes no host create.
   - Before the fixture prerequisite, Thurbox is unavailable and runs no CLI.
   - Fixture tests pin get/list/create JSON, effective path, active/stopped
     statuses, restart output, duplicate-name rejection, and strict parsing.

5. Delivery and wake tests
   - A Waypost delivery commits before Thurbox send is attempted.
   - Thurbox wake uses only the fixed notice, never workflow input.
   - Wake success or failure leaves durable delivery and lease state intact;
     failure appears only in notification diagnostics.
   - Existing Agent Deck notification assertions remain unchanged.
   - Nested Thurbox wake targets Thurbox and not outer Agent Deck.

6. Operational tests
   - Use fake host CLIs and checked-in version-pinned fixtures; require no
     global Thurbox installation.
   - Update the hard-cut document's exact tool count and compatibility text.

## Persisted Data Changes

None.  Waypost message, delivery, lease, group, and state schemas do not
change.  The optional profile JSON is immutable operator process configuration,
not Waypost durable state.

## Risks And Tradeoffs

- Explicit profile configuration adds operator setup but prevents prompts from
  executing a host-specific command or guessing a Thurbox agent.
- Failing MCP startup for an explicitly supplied invalid profile catches
  operator mistakes early; omitting the flag remains useful for resolve and
  require-only deployments.
- The fixture prerequisite delays Thurbox enablement, but prevents guessed
  path or lifecycle behavior from becoming public.
- A strict workdir gate may reject a Thurbox layout with a separate generated
  worktree.  That is safer than claiming a false portable invariant; an
  explicit repo-versus-worktree design needs separate approval.
- A confirmed create, start, or restart can need operator recovery.  A
  structured partial result is safer than automatic cleanup because generic
  deletion has no portable safe meaning.
- A Thurbox wake can be lost; Waypost correctness does not depend on it.
- A third host requires explicit code and design.  That is intentional and
  avoids unneeded registry machinery.

## Open Questions

None requiring requester input.  Capturing the Thurbox grammar/status fixture
is an implementation merge gate, and recovery after a partial host operation
is an operator action rather than a product-scope choice.
