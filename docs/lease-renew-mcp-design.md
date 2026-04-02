# Lease Renew For Local Mailbox MCP

## Summary

Add explicit lease renewal to personal mailbox delivery, then move lease
liveness management into the local mailbox MCP that already lives and dies with
the agent process.

The mailbox core should stop treating lease duration as a caller-side estimate
of total task runtime. In the MCP-driven workflow, lease duration should mean
"how quickly abandoned work becomes claimable again if the current agent
session disappears."

This design keeps the mailbox core generic while letting the local MCP hide
renewal complexity from the model-facing interface.

## Problem

Current lease semantics are correct in principle but awkward in real agent use:

- `recv` assigns a fixed lease timeout of `5m`
- callers do not know how long processing will take
- a short timeout risks duplicate work during legitimate long-running tasks
- a long timeout slows recovery after agent death

That is the wrong boundary.

The component that knows whether the worker is still alive is not the model and
not the mailbox caller. It is the local mailbox MCP process that is co-located
with the agent process and exits when that agent session exits.

So the current design asks the wrong layer to guess the wrong thing.

## Goals

- Preserve exclusive claim semantics for personal mailbox delivery.
- Make abandoned work reclaimable quickly after agent or MCP death.
- Remove lease-duration guessing from normal mailbox MCP users.
- Keep mailbox core transport-agnostic and adapter-agnostic.
- Keep crash recovery daemon-free.
- Maintain auditable delivery lifecycle transitions.

## Non-Goals

- No change to group mailbox semantics.
- No remote or distributed lease coordination.
- No mailbox-core knowledge of specific agent runtimes or session managers.
- No mandatory background daemon owned by the mailbox store.
- No redesign of message storage or blob handling.
- No attempt in this round to remove `delivery_id` from persisted schema.

## Core Judgment

The root problem is not that leasing exists. The root problem is that lease
duration is currently modeled as a user-selected processing-time estimate.

That is bad taste.

Lease duration should instead be a short failure-detection window, and the
owner that extends it should be the live MCP session that currently holds the
claim.

So the correct change is:

1. add explicit lease renewal to mailbox core
2. keep default lease TTL short
3. let the local mailbox MCP renew active leases automatically
4. stop expecting agent callers to predict total work duration

## Design Overview

Split responsibility cleanly:

- mailbox core owns claim, renewal, expiry validation, and recovery
- mailbox MCP owns the in-memory renewal loop for leases it currently holds
- model-facing consumers continue to think in terms of "receive work" and
  "finish work", not "guess a lease duration"

The MCP is the right owner because it is local, trusted, and shares lifecycle
with the agent process in the current deployment model.

## Delivery Model Changes

The persisted delivery model stays mostly the same:

- `delivery_id`
- `message_id`
- `recipient_endpoint_id`
- `state`
- `visible_at`
- `lease_token`
- `lease_expires_at`
- `acked_at`
- `attempt_count`
- `last_error_code`
- `last_error_text`

No schema migration is required for the first version of renewal.

The key semantic change is this:

- `lease_expires_at` is no longer mainly "expected task completion deadline"
- `lease_expires_at` becomes "latest proof-of-liveness deadline"

That better matches how the system is actually used.

## Mailbox Core API

Add one new personal-mailbox lifecycle operation:

```text
agent-mailbox renew --delivery <delivery_id> --lease-token <lease_token> --for 15s
```

Store API:

```go
type LeaseRenewResult struct {
    DeliveryID     string `json:"delivery_id"`
    LeaseToken     string `json:"lease_token"`
    LeaseExpiresAt string `json:"lease_expires_at"`
}

func (s *Store) Renew(ctx context.Context, deliveryID, leaseToken string, extendBy time.Duration) (LeaseRenewResult, error)
```

Rules:

- require a valid, current, unexpired lease
- require `extendBy > 0`
- update only `lease_expires_at`
- keep the same `lease_token`
- fail if the lease already expired
- fail if another claimer already replaced the lease
- record a `delivery_lease_renewed` event

Keeping the same token is the simplest correct design here. Renewal is not a
re-claim. It is proof that the current owner is still alive.

## Why Keep The Same Lease Token

Rotating the token on every renewal sounds stricter, but it is a mess for the
wrong reason:

- the MCP would need to atomically update every in-memory holder of that token
- any race between renewal and a later `ack` would create needless local churn
- the trusted local MCP does not gain meaningful security from token rotation

The important protection is already:

- only the current unexpired lease may transition
- expired leases cannot be renewed
- a new claimer gets a different token when re-claim happens

That is enough.

## Lease TTL Policy

Shorten the default lease TTL for MCP-driven receive paths.

Recommended starting values:

- lease TTL: `15s`
- renewal cadence: every `5s`

These numbers are not sacred. The invariant is what matters:

- renewal cadence should be comfortably shorter than TTL
- TTL should be short enough that dead sessions release work quickly
- TTL should be long enough to tolerate brief scheduler pauses and local load

This is a liveness window, not a task-duration estimate.

## MCP Renewal Ownership

The local mailbox MCP should own renewal for every lease it claims on behalf of
the agent session.

Recommended MCP behavior:

1. `recv` claims a message and records `{delivery_id, lease_token,
   lease_expires_at}` in an in-memory active-lease set
2. a background renewal loop periodically renews every active lease
3. `ack`, `release`, `defer`, and `fail` remove that lease from the active set
4. MCP shutdown stops the loop; no cleanup write is required
5. expired unrenewed leases become claimable again by normal mailbox rules

This gives the desired property:

- if the agent dies, the local MCP dies
- if the MCP dies, renewal stops
- if renewal stops, the short lease expires quickly
- if the lease expires, work returns automatically

## MCP-Facing Interface Direction

The model-facing MCP interface should not force the model to manage lease
timers.

Recommended direction:

- mailbox MCP internally tracks `delivery_id` and `lease_token`
- normal model-facing operations stay high level
- renewal is automatic and hidden for the common path

For the current MCP surface, it is acceptable to keep returning delivery
metadata, but the MCP should treat lease management as its own responsibility.

Longer term, the cleaner shape is a claim handle owned by the MCP session
rather than exposing raw mailbox lease internals at every call boundary.

That is a later simplification, not required for this round.

## CLI Contract

Add:

```text
agent-mailbox renew --delivery ID --lease-token TOKEN --for DURATION
```

Behavior:

- validate required flags
- require `DURATION > 0`
- require delivery state `leased`
- require current lease token match
- require current lease not expired
- set `lease_expires_at = now + duration`
- print `delivery_id`, `lease_token`, and `lease_expires_at`

Do not add `--lease-timeout` to `recv` in this round.

Reason:

- it preserves the bad boundary by pushing runtime estimation back to callers
- the MCP auto-renew path already solves the real use case more directly

If a later non-MCP caller needs explicit custom claim TTL, that can be designed
separately with a clear justification.

## Event Log

Add a new audit event:

- `delivery_lease_renewed`

Suggested detail payload:

```json
{
  "recipient_address": "workflow/reviewer/task-123",
  "previous_lease_expires_at": "2026-04-02T10:00:15Z",
  "lease_expires_at": "2026-04-02T10:00:30Z"
}
```

This keeps lease extensions inspectable and helps debug stuck or noisy renewal
loops.

## Failure Semantics

Renewal failure means one of three real things:

1. the lease already expired
2. the delivery was already transitioned by the current owner
3. the mailbox store is temporarily unavailable

MCP handling rules:

- if renewal fails with lease-expired or token-mismatch semantics, drop the
  local claim and surface a stale-claim error if later completion is attempted
- if renewal fails because the store is temporarily unavailable, retry quickly
  within the remaining local deadline budget
- if the deadline budget is exhausted, stop pretending ownership is valid

Do not silently continue processing forever after renewal failure.

## Compatibility

This design is additive at the core API level.

Compatibility rules:

- existing `recv`, `ack`, `release`, `defer`, `fail`, `list`, and `read`
  behavior remains valid
- persisted schema stays unchanged
- old clients that do not call `renew` still work under current TTL rules
- MCP-driven clients can opt into short TTL plus auto-renew

This avoids breaking existing personal-mailbox callers while improving the
primary agent path.

## Alternatives Considered

### Keep fixed 5-minute lease

Rejected.

Recovery after agent death stays too slow, and the system still asks callers to
accept an arbitrary compromise.

### Let callers pass `--lease-timeout` to `recv`

Rejected for this round.

That only moves the guessing problem around.

### Use a very long lease and no renewal

Rejected.

That makes abandoned work linger for too long and weakens recovery semantics.

### Rotate lease token on every renewal

Rejected.

It adds coordination complexity without meaningful benefit in the trusted local
deployment model.

### Store heartbeat rows in the database

Rejected.

The mailbox already has the right primitive: `lease_expires_at`. A renewal
operation updates the one field that matters.

## Risks And Tradeoffs

- Very short TTLs may flap under local machine pauses or scheduler stalls.
- Renewal loops that run too frequently create unnecessary SQLite write churn.
- MCP bugs in active-lease tracking could keep renewing a lease longer than
  intended.
- Mixed clients will still have different operational behavior until the MCP
  path becomes the dominant path.

These are manageable. They are better tradeoffs than forcing every caller to
guess a total runtime.

## Open Questions

- Should the mailbox core default TTL be reduced globally, or should the MCP
  receive path later gain an explicit internal short-TTL receive variant?
- Should `renew` accept an absolute `--until` in addition to relative `--for`,
  or is relative-only simpler and good enough?
- Should later MCP cleanup work collapse raw `{delivery_id, lease_token}` into
  a single session-scoped claim handle?

## Suggested Rollout

1. add core `Renew` store method and CLI `renew`
2. add tests for successful renewal, expired-lease rejection, and stale-token
   rejection
3. add `delivery_lease_renewed` event coverage
4. update mailbox MCP to track active leases and renew them automatically
5. shorten MCP-path lease TTL after renewal proves stable

This sequencing keeps the root fix small and testable.
