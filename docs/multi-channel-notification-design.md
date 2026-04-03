# Best-Effort Multi-Channel Mailbox Notification Design

## Summary

Add a unified best-effort notification design for the local mailbox MCP so
mailbox-backed work can be hinted through multiple channels without treating
any one transport as authoritative.

The mailbox remains the only source of truth. Notification channels such as
`agent-deck` wake messages and MCP resource updates are only hints that new
work may exist or that pending work still has not been claimed.

This design separates:

- immediate send-time notification, which is an active sender behavior
- delayed reminder escalation, which is a fallback scheduler behavior
- mailbox state reads, which remain the authoritative way to learn the real
  delivery state
- wake scope identity, which determines which notification channels are allowed
  to act for a given pending-mail scope

It also adds a minimal mailbox MCP resource surface so standards-compliant
clients can read summary mailbox state and, when supported, subscribe to
resource-update hints. That subscription path is explicitly best-effort and is
not required for correctness. In this design it is only a local-instance hint,
not a general cross-session wake transport.

## Problem

Current notification behavior is split and under-modeled:

- `mailbox_send` performs an immediate notify attempt through `agent-deck`
- stale unread mail later triggers a separate unread-push loop
- the two paths have different intent, timing, and state
- MCP currently has no mailbox resource that exposes summary pending-mail state
- MCP resource notifications are attractive as a standard mechanism, but client
  support is inconsistent and notification delivery itself is not reliable
- the current implementation still couples stale-unread wake routing to the
  mailbox address scheme itself
- the repository already ships an external stale-wake helper script with its
  own timing and recheck policy

That means the system has the wrong abstraction boundary.

The mailbox is already the durable truth. The missing design is a clean model
for how multiple best-effort hint channels should be attempted over time
without pretending that any notification result means the receiver has actually
seen or processed the mail.

## Goals

- Keep mailbox delivery state authoritative.
- Treat every notification mechanism as best-effort only.
- Preserve immediate send-time notification as an active sender behavior.
- Add a delayed escalation path for still-pending mail.
- Support multiple channels without firing them all at once.
- Prefer lower-cost hints before heavier wake mechanisms in fallback stages.
- Add a minimal MCP resource for summary mailbox state.
- Support MCP resource subscription if the implementation cost stays low.
- Avoid requiring MCP notification support for correctness.

## Non-Goals

- No guarantee that any notification reaches or wakes the receiver.
- No guarantee that a successful notify call means the receiver saw the hint.
- No mailbox-core knowledge of any specific notification manager.
- No change to mailbox delivery state semantics.
- No replacement of mailbox polling or `mailbox_recv` with pushed message
  bodies.
- No attempt in this round to make MCP a general remote session manager.
- No dependence on MCP subscription support for message delivery correctness.

## Core Judgment

The root issue is not that one more channel is needed.

The real issue is that notification has been treated as if one successful call
could stand in for observed mailbox progress. That is wrong. All notification
mechanisms can fail, be delayed, be ignored, or arrive after the mail was
already handled by another path.

So the right design is:

1. keep mailbox state as the only truth
2. model notification as staged, best-effort hinting
3. separate immediate sender behavior from delayed fallback behavior
4. define a canonical wake scope that owns escalation state
5. allow several channels over time, but with explicit spacing and cooldowns

## Design Overview

Use one notification model with two distinct execution paths:

1. direct notify path
   - triggered immediately by `mailbox_send`
   - active sender intent
   - tries only cross-session-routable channels in priority order until one
     delivery attempt succeeds
2. wake scheduler path
   - triggered later while mail is still pending
   - fallback escalation intent
   - attempts channels over time with stage delays, inter-channel gaps, and
     per-channel cooldowns

Both paths share the same channel backends, but they do not share the same
timing policy.

Both paths must operate on the same canonical wake-scope model rather than
deriving wake decisions directly from raw mailbox addresses.

## Wake Scope

Introduce one first-class data model:

```go
type WakeTarget struct {
    Channel WakeChannelName
    Target  string
}

type WakeScope struct {
    ScopeID          string
    MailboxAddresses []string
    WakeTargets      []WakeTarget
    OldestEligibleAt string
}
```

The wake scope is the unit of reminder policy ownership.

Rules:

- scheduler runtime is keyed by `WakeScope.ScopeID`
- `PendingSince`, `LastAnyWakeAt`, and per-channel cooldown state belong to the
  wake scope, not to an entire MCP server instance
- a wake scope may contain one or more mailbox addresses
- wake channels act on `WakeTargets`, not on raw mailbox addresses

This is the canonical answer to "what receiver are we trying to wake?"

### First-Rollout Local Scheduler Scope Membership

The first rollout defines one local scheduler-owned wake scope per MCP server
instance.

Membership rule:

- include every mailbox address currently bound in that MCP server instance in
  the same local scheduler scope

This means that when the local server is bound to both:

- `agent-deck/<session-id>`
- `codex/<session-id>`

both addresses contribute to:

- pending-state evaluation
- `PendingSince`
- scheduler cooldown state

This rule is intentional. The local scheduler owns reminder policy for the
current MCP-managed session as a whole, not for one address at a time.

Suggested first-rollout scope identity:

- prefer a stable local session-derived id, using detected current
  `agent-deck` session id when available
- otherwise use detected current AI-agent session id
- otherwise fall back to a stable deterministic key derived from sorted bound
  addresses

### First-Rollout Wake Targets

The first rollout should keep wake targets narrow and explicit even though scope
membership covers all currently bound local inboxes.

Supported targeted wake mappings:

- `agent-deck/<session-id>` mailbox address
  - allowed wake targets:
    - `agent_deck` target `<session-id>`

Non-goal in this round:

- no implicit cross-manager wake aliasing such as "a delivery to `codex/<id>`
  should also wake `agent-deck/<other-id>`" unless that mapping is represented
  explicitly in a future wake-scope resolver

This avoids inventing hidden cross-scheme rules without an owning data model.

### Local MCP Resource Hint Scope

`mcp_resource_updated` is not modeled as a remote wake target in the first
rollout.

Instead, it is a local-instance hint emitted by the MCP server that already
owns the bound inboxes and the `mailbox://bound/overview` resource.

Rules:

- it is valid only for the current connected MCP server instance
- it does not identify another session's MCP server
- it is not a cross-session wake transport
- it may coexist with external wake targets on the same local wake scope
- it is available only when the local overview resource has subscribers

This removes the earlier ambiguity around `codex/<session-id>` and remote MCP
delivery.

## Mailbox Truth Model

The mailbox remains authoritative.

Notification must never be treated as equivalent to progress.

These rules are mandatory:

- delivery is pending because mailbox state says it is pending
- delivery is no longer pending only because mailbox state changed
- no notification success result may suppress future fallback attempts by
  itself
- fallback stops only when mailbox state no longer shows pending work
- no wake target may be derived ad hoc from mailbox state outside the wake
  scope resolver

For this design, pending work means:

- at least one visible personal delivery is claimable by the same rules used by
  `recv`

The implementation should not use `read/unread` language for personal mailbox
deliveries. The actual state model is still `queued`, `leased`, `acked`, and
`dead_letter`.

## Notification Stages

### Stage 1: Immediate Direct Notify

This stage belongs to `mailbox_send`.

Intent:

- the sender wants to actively nudge the receiver now

Rules:

- no initial delay
- no scheduler cooldown gating
- try channels in strict priority order
- stop after the first channel that reports a successful delivery attempt
- if a channel is unavailable or returns failure, try the next channel
- even after one immediate attempt succeeds, later fallback stages may still
  run if the mailbox state shows the work remains pending

Recommended direct-notify priority:

1. `agent-deck`
2. other future external session managers

Reason:

- external session managers are closer to an explicit wakeup intent
- `mcp_resource_updated` is intentionally excluded because it is not a
  cross-session-routable wake mechanism in the first rollout

### Stage 2: Early Follow-Up Reminder

This stage belongs to the fallback scheduler.

Intent:

- the original direct notify may have failed, been missed, or not been handled
  yet
- try a lower-cost reminder before escalating again to a heavier wake path

Recommended defaults:

- `mcp_resource_updated.initial_delay = 1m`
- `mcp_resource_updated.cooldown = 2m`

The one-minute delay exists mainly to avoid colliding with immediate notify
paths and to allow normal agent processing time before a reminder starts.

### Stage 3: Later Wake Escalation

This stage also belongs to the fallback scheduler.

Intent:

- work is still pending after the earlier hint window
- escalate to a stronger wake mechanism again

Recommended defaults:

- `agent_deck.initial_delay = 3m`
- `agent_deck.cooldown = 5m`

This later stage must be obviously later than the MCP follow-up stage.

### Inter-Channel Gap

Recommended default:

- `inter_channel_gap = 1m`

Rule:

- the scheduler may emit at most one notification attempt per evaluation cycle
- after any delivered attempt, the next channel attempt must wait for
  `inter_channel_gap`

This avoids simultaneous duplicate wakes across channels.

## Channel Model

Represent notification mechanisms as two related but distinct backend
categories.

### Targeted Wake Channels

These are cross-session-routable wake mechanisms.

Suggested interface:

```go
type WakeChannelName string

const (
    WakeChannelAgentDeck          WakeChannelName = "agent_deck"
    WakeHintMCPResourceUpdated    WakeChannelName = "mcp_resource_updated"
)

type WakeSnapshot struct {
    ScopeID                   string
    HasPendingVisibleDelivery bool
    PendingSince              string
    BoundAddresses            []string
    WakeTargets               []WakeTarget
}

type WakeChannel interface {
    Name() WakeChannelName
    Available(context.Context, WakeSnapshot) (bool, string)
    Deliver(context.Context, WakeSnapshot) notificationOutcome
}
```

Required first targeted wake backend:

- `agent-deck`

The availability rule is intentionally channel-specific:

- `agent-deck` is available when the wake scope includes an `agent_deck`
  target that resolves to a wakeable session

### Local Hint Emitters

These are local-instance side effects emitted by the server that already owns
the mailbox scope. They are not modeled as cross-session targets.

Suggested interface:

```go
type LocalHintEmitter interface {
    Name() WakeChannelName
    Available(context.Context, WakeSnapshot) (bool, string)
    Emit(context.Context, WakeSnapshot) notificationOutcome
}
```

Required first local hint emitter:

- `mcp_resource_updated`

Its availability rule is:

- available only when the mailbox overview resource currently has at least one
  subscriber on the same MCP server instance that is evaluating the wake scope

## Direct Notify Path

Refactor current send-time notify logic into a direct-notify coordinator.

Suggested behavior:

```text
for channel in direct_notify_priority:
  if channel unavailable:
    continue
  outcome = deliver(channel)
  record attempt result
  if outcome.status == sent:
    stop
```

This path should keep a simple immediate sender-intent toggle through
`disable_notify_message`.

It should not wait for fallback delays and should not consult scheduler runtime
state.

The direct notify path must resolve allowed targeted wake channels from the
wake scope first. It must not infer future cross-scheme wake behavior from the
mailbox address string itself.

In the first rollout, direct notify must only use cross-session-routable
targets such as `agent-deck`. It must not attempt `mcp_resource_updated`.

## Wake Scheduler Path

Refactor the current unread-push loop into a generic wake scheduler.

The scheduler should:

1. read authoritative mailbox state for the current bound inbox scope
2. determine whether visible pending work exists
3. if nothing is pending, clear scheduler runtime for that scope
4. if work is pending, evaluate channel attempts by stage timing
5. attempt at most one channel per tick

Suggested runtime state:

```go
type WakeRuntime struct {
    ScopeID            string
    PendingSince       string
    LastAnyWakeAt     string
    LastWakeByChannel map[WakeChannelName]string
}
```

`PendingSince` should be derived from mailbox truth when possible, not from
best-effort in-memory observation. The existing stale-address query already
returns the oldest eligible timestamp and is the right base signal.

Suggested scheduler algorithm:

```text
if no pending visible delivery:
  reset runtime
  return

for channel in fallback_priority:
  if now < pending_since + channel.initial_delay:
    continue
  if now < last_any_wake_at + inter_channel_gap:
    continue
  if now < last_wake_at[channel] + channel.cooldown:
    continue
  if channel unavailable:
    continue

  outcome = deliver(channel)
  record attempt result
  if outcome.status == sent:
    update last_any_wake_at and last_wake_at[channel]
    stop this tick
```

Initial fallback priority should be:

1. local hint emitters such as `mcp_resource_updated`
2. targeted wake channels such as `agent-deck`

This keeps the lighter local hint earlier and the external wake later.

The scheduler must evaluate one wake scope at a time. A server-wide summary
resource is useful for hints and UI, but it is not the identity key for
cooldowns or escalation ownership.

This priority order is valid only because the scheduler is local to the server
instance that already owns the inbox scope. It is not a statement that
`mcp_resource_updated` can wake arbitrary remote sessions.

The scheduler therefore has two sequential evaluation phases for each scope:

1. local hint emitters
2. targeted wake channels

This is an intentional modeling distinction, not an implementation accident.

## MCP Resource Surface

Add one minimal mailbox summary resource:

- `mailbox://bound/overview`

This resource is intentionally local to the current MCP server instance. It is
not a general routing destination.

Suggested content:

```json
{
  "bound_addresses": ["agent-deck/<id>", "codex/<id>"],
  "default_sender": "agent-deck/<id>",
  "has_claimable_delivery": true,
  "claimable_delivery_count": 2,
  "oldest_claimable_at": "2026-04-03T00:40:00Z"
}
```

The resource content is a snapshot only. Clients that need real work must still
call mailbox tools such as `mailbox_recv`.

This resource is intentionally coarse. It does not replace wake-scope runtime
identity. In the first rollout it summarizes the same local scheduler scope
that contains all currently bound inbox addresses for the server instance.

## MCP Capability Declaration

The MCP server must advertise resources correctly during initialization.

Required:

- `capabilities.resources.listChanged = true`

If subscription support is implemented in this round:

- `capabilities.resources.subscribe = true`

Rationale:

- `mailbox://bound/overview` is a standard MCP resource surface
- list-changed capability should be declared from the first rollout
- subscription is optional for correctness but acceptable when implementation
  cost is low

## MCP Subscription Support

Support resource subscription if the implementation cost stays low, but treat
it as optional best-effort behavior.

Rules:

- clients may subscribe to `mailbox://bound/overview`
- the server may emit `notifications/resources/updated`
- receiver behavior is client-dependent and must not be relied on for
  correctness
- absence of subscribers must not block other channels
- this path is local to the MCP server instance that owns the resource and is
  not a remote wake transport

The implementation should track whether `mailbox://bound/overview` currently
has subscribers so the `mcp_resource_updated` local hint emitter can cheaply
answer availability.

## MCP Update Trigger Points

If subscription support is implemented, the server should emit
`resources/updated` best-effort after successful operations that may change the
overview snapshot.

Minimum trigger points:

- `mailbox_bind`
- `mailbox_send`
- `mailbox_recv`
- `mailbox_ack`
- `mailbox_release`
- `mailbox_defer`
- `mailbox_fail`

This is intentionally a hint channel only. Missing an update notification must
not break correctness because the resource remains readable on demand.

## Relation To Existing `mail_hint`

The existing `mail_hint` field in tool results remains a separate local
best-effort hint in the first rollout.

Rules:

- `mail_hint` stays as the immediate inline hint returned from mailbox MCP tool
  calls
- `mailbox://bound/overview` is the standard MCP-readable summary surface
- `resources/updated` is an optional best-effort push hint for subscribed
  clients

`mail_hint` and the resource path overlap in purpose, but not in transport
shape:

- `mail_hint` piggybacks on a tool result that is already being returned
- the overview resource supports out-of-band reads and optional standard MCP
  updates
- the resource update path remains local-instance only, unlike external
  session-manager wakes

Do not remove `mail_hint` in the first rollout. Revisit removal only after the
resource path proves useful in real clients.

## Server Lifetime Requirement

The mailbox MCP service must hold one long-lived `*mcp.Server` instance.

Reason:

- MCP sessions, resource registration, and subscription state belong to the
  server instance
- recreating the server on each `Service.Server()` call would lose session
  state and make resource subscription meaningless

So `Service` should lazily create and cache one server instance, then reuse it
for the full service lifetime.

## Existing External Stale-Wake Helper

The current external helper script
[`scripts/wake-stale-agent-deck-sessions.sh`](/home/ruiheng/agent-mailbox/scripts/wake-stale-agent-deck-sessions.sh)
already implements stale-mail wake behavior outside the MCP process.

It must not become a second owner of reminder policy.

First-rollout rule:

- the in-process wake scheduler becomes the canonical owner of timing,
  cooldown, and per-scope escalation policy for the MCP-managed path
- the external helper remains supported only as a compatibility or operational
  fallback path for environments where the MCP process is not the policy owner

Required follow-up direction:

- either deprecate the helper for normal MCP-managed workflows
- or reduce it later to a thin wrapper that reuses the same wake-scope policy
  definitions instead of carrying an independent timing model

The implementation round should not silently leave both as equal long-term
owners.

## Observability

Wake behavior cannot be debugged responsibly without explicit attempt and
suppression data.

Minimum required observability for the first rollout:

- wake scope id
- mailbox addresses in the scope
- chosen backend
- backend category: local hint emitter or targeted wake channel
- attempted target when applicable
- attempt timestamp
- attempt result status
- suppression reason when a channel is skipped
- channel availability decision
- pending-since timestamp used for policy evaluation
- current cooldown and initial-delay gate that caused suppression

These may begin as structured logs, but they are not optional design detail.

## Compatibility

This design is additive.

- mailbox delivery semantics remain unchanged
- `mailbox_send` still supports immediate notify behavior
- notification results remain advisory
- clients that do not support MCP resources or subscriptions still work
- external session-manager wake paths still work without MCP subscription
  support
- existing `mail_hint` behavior remains valid in the first rollout

## Alternatives Considered

### Treat one notification channel as authoritative

Rejected.

That is dishonest. All channels are best-effort and mailbox state is already
the durable truth.

### Fire all available channels immediately on send

Rejected.

That creates noisy duplicate wakes and removes any control over escalation.

### Use MCP notification only and remove external wake paths

Rejected.

Client support is inconsistent and MCP is not currently a stronger wake
mechanism than `agent-deck`.

### Keep direct notify and stale fallback as unrelated implementations

Rejected.

That keeps notification policy duplicated and makes future extension harder.

### Refuse MCP subscription support because some clients handle it poorly

Rejected for the first design round.

The feature is standard MCP functionality and cheap to expose. It is acceptable
so long as the design keeps it explicitly non-authoritative and best-effort.

### Keep deriving wake targets directly from mailbox address schemes

Rejected.

That preserves the current hidden coupling and does not explain future
cross-manager routing or per-scope policy ownership.

### Force local MCP hints into the same target model as cross-session wakeups

Rejected.

That reintroduces a fake uniformity. `mcp_resource_updated` is a local
server-instance side effect, not a remote target delivery mechanism, so the
design should say that directly.

## Risks And Tradeoffs

- MCP subscription support may not help in some clients even when implemented.
- Direct notify may succeed while the receiver still never processes the mail.
- Multiple best-effort channels increase policy complexity if timing is not
  centralized.
- A one-minute first fallback delay may still be too aggressive or too passive
  for some workflows.
- Session-manager wake probes may report a session as wakeable even when local
  conditions later prevent useful handling.
- The first-rollout wake-scope mapping intentionally avoids cross-manager alias
  wakeups, which may leave some future routing scenarios unsupported until a
  stronger resolver model is designed.
- The first-rollout scheduler scope intentionally merges all currently bound
  local inbox addresses into one scope, which is simple and matches current
  MCP-managed-session ownership but may later need refinement if one server
  process ever needs multiple independent reminder domains.
- The MCP resource hint channel is intentionally local-only in the first
  rollout, so it cannot help with remote session wake scenarios.
- The scheduler now has two backend categories instead of one uniform target
  model, which is slightly more explicit but also less superficially elegant.

These are acceptable tradeoffs if the mailbox remains authoritative and the
scheduler logic stays centralized.

## Open Questions

- Should `agent_deck.initial_delay` start at `3m` or `5m` in the first
  rollout?
- Should the mailbox overview resource later split into one resource per bound
  address, or is one summary resource enough?
- Should direct notify expose attempted-channel summaries in tool output in
  addition to required structured logs?
- Should a future wake-scope resolver support explicit cross-manager aliasing
  between mailbox addresses and wake targets?
- If remote MCP-to-MCP wake is ever needed, what component should own discovery
  and delivery to another session's MCP server instance?
- If future local hint emitters other than `mcp_resource_updated` appear,
  should they share one local-emitter interface or split by transport family?
- If a future MCP server instance can bind unrelated inbox groups, should local
  scheduler scope membership stay "all bound addresses" or become explicitly
  partitioned?

## Suggested Rollout

1. cache one long-lived MCP server instance inside `Service`
2. add `mailbox://bound/overview` resource registration
3. ensure initialize advertises `resources.listChanged = true`
4. optionally add MCP subscribe support and resource-updated hints
5. refactor immediate send-time notify into a direct-notify coordinator
6. introduce wake-scope resolution and key scheduler runtime by wake scope
7. refactor stale unread loop into a generic wake scheduler
8. wire first local hint-emitter stage to `mcp_resource_updated`
9. wire later targeted wake stage to `agent-deck`
10. add tests for stage timing, inter-channel gap, wake-scope ownership, and
   fallback after direct
   notify

This keeps the root boundary clean: mailbox truth first, notification hints
second.
