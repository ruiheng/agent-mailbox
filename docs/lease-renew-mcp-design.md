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
2. keep the legacy default receive TTL unchanged for compatibility
3. add an MCP-only short-TTL receive policy path
4. let the local mailbox MCP renew active leases automatically
5. stop expecting agent callers to predict total work duration

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

Renew must use the same compare-and-swap discipline as terminal lease
transitions. The exact store rule should be:

1. load the currently leased row inside one write transaction
2. validate `state = leased`
3. validate `lease_token` matches
4. validate current `lease_expires_at > now`
5. compute `new_lease_expires_at = now + extendBy`
6. update with a CAS predicate on the same row:

```sql
UPDATE deliveries
SET lease_expires_at = :new_lease_expires_at
WHERE delivery_id = :delivery_id
  AND state = 'leased'
  AND lease_token = :lease_token
  AND lease_expires_at = :previous_lease_expires_at
```

If the update affects zero rows, renewal must fail as a stale lease update, not
silently succeed.

This keeps renew, reclaim, ack, release, defer, and fail deterministic under
races. `lease_expires_at` acts as the row-version field for the current lease.

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

Shorten the lease TTL only for MCP-driven receive paths.

Recommended starting values:

- legacy default receive TTL: keep `5m`
- initial MCP-path lease TTL: `30s`
- initial renewal cadence: every `10s`
- initial renewal jitter: up to `2s`

These numbers are not sacred. The invariant is what matters:

- renewal cadence should be comfortably shorter than TTL
- TTL should be short enough that dead sessions release work quickly
- TTL should be long enough to tolerate brief scheduler pauses and local load

For the first rollout, the design should be conservative. A `30s` TTL with
`10s` cadence gives a healthier pause budget than jumping directly to `15s`
without evidence. Tightening to `15s` can be a later operational tuning
decision after failure testing.

This is a liveness window, not a task-duration estimate.

## Compatibility Boundary For Short TTL

Preserving current non-MCP personal-mailbox behavior is a hard requirement in
this design.

That means the short TTL must not arrive by silently changing the global
default.

The boundary should be:

- existing CLI `recv` keeps the current `5m` lease default
- existing public `Store.Receive` and `Store.ReceiveBatch` semantics stay on the
  legacy default path
- mailbox core factors claim logic so a narrow internal policy input can supply
  a shorter TTL
- mailbox MCP uses that internal short-TTL policy path

One acceptable implementation shape is:

```go
type ReceiveLeasePolicy struct {
    LeaseTTL time.Duration
}
```

Then:

- `Receive` / `ReceiveBatch` call the claim path with the legacy default policy
- MCP-only code calls the same claim path with a short-TTL policy

This keeps compatibility honest. The design is no longer hand-waving about a
future global default change while claiming non-MCP callers are unaffected.

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

Recommended operating limits:

- renewal attempts should start when roughly one third of the TTL has elapsed
- renewal scheduling should include small jitter to avoid bursty writes when one
  MCP holds many leases
- transient SQLite busy/locked renew failures may retry quickly, but only within
  the remaining local deadline budget
- once the remaining time budget drops below `10s` in the initial `30s`-TTL
  rollout, the MCP should stop retrying indefinitely and treat the lease as at
  risk

Required MCP observability:

- active lease count
- renew success count
- renew failure count by cause
- renewal latency
- time remaining before expiry at successful renewal
- stale-claim completion attempts
- consecutive renew failures per lease

These can start as structured logs if the MCP does not yet expose metrics.

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

Also do not change the CLI default receive TTL in this round. The compatibility
boundary is explicit: short TTL is introduced only through the MCP-owned
internal receive policy path.

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

Stale-claim completion semantics must also be explicit.

If local work finishes after the MCP has already lost lease ownership, mailbox
completion must fail with a dedicated stale-claim error. The mailbox must not
silently convert that into success.

Expected caller and operator model:

- completion result is ambiguous with respect to externally visible side effects
- the caller should treat the work result as potentially duplicated
- handlers that can cause side effects should prefer idempotency keyed by
  message identity or a workload-specific idempotency key
- operator recovery should inspect downstream side effects before manually
  replaying or discarding the re-queued message

This is not pretty, but it is honest. Once ownership is lost, the mailbox can
no longer promise that the local finisher is still the sole owner.

## Compatibility

This design is additive at the core API level.

Compatibility rules:

- existing `recv`, `ack`, `release`, `defer`, `fail`, `list`, and `read`
  behavior remains valid
- persisted schema stays unchanged
- old clients that do not call `renew` still work under current TTL rules
- MCP-driven clients can opt into short TTL plus auto-renew through the new
  MCP-only receive policy path

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

### Change the global default receive TTL to a short value

Rejected for the first rollout.

That would break the compatibility promise for existing non-MCP callers before
the new MCP-only short-TTL path proves stable.

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

The first rollout therefore should explicitly test:

- scheduler pauses longer than one renewal cadence
- SQLite contention during renew and terminal transitions
- MCP shutdown during in-flight renewal
- local completion after lease loss

## Open Questions

- Should `renew` accept an absolute `--until` in addition to relative `--for`,
  or is relative-only simpler and good enough?
- Should later MCP cleanup work collapse raw `{delivery_id, lease_token}` into
  a single session-scoped claim handle?
- After the MCP-only path is proven under failure testing, is there any reason
  left to keep the legacy `5m` default for non-MCP callers?

## Suggested Rollout

1. add core `Renew` store method and CLI `renew`
2. add tests for successful renewal, expired-lease rejection, and stale-token
   rejection
3. add `delivery_lease_renewed` event coverage
4. factor claim logic so MCP can use a short-TTL receive policy without
   changing legacy `recv` semantics
5. update mailbox MCP to track active leases and renew them automatically
6. run failure testing under scheduler pause, SQLite contention, and MCP
   shutdown races
7. only after evidence, consider tightening MCP TTL below the initial `30s`

This sequencing keeps the root fix small and testable.
