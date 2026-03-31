# Stale Address Query Design

## Summary

Add a read-only mailbox query that tells a caller which supplied personal
addresses currently have old, still-receivable mail. The mailbox must not know
where those addresses came from, whether they belong to `agent-deck`, or
whether the caller plans to wake anything up.

## Problem

External session managers want to periodically check whether a set of live
workers has unread mail that has been sitting too long.

The mailbox already has the authoritative delivery state, but it does not yet
expose an address-level query for:

- a caller-provided address set
- currently receivable personal deliveries only
- addresses whose oldest receivable mail is older than a threshold

The wrong direction is to teach `agent-mailbox` about one concrete session
manager, session liveness, or notification state. Those belong outside the
mailbox.

## Goals

- Provide one read-only query over a caller-provided address set.
- Return stale addresses, not full message bodies.
- Use existing mailbox truth: deliveries that are currently receivable by the
  existing `recv` rules.
- Keep the result small but actionable for an external scheduler.
- Preserve the current mailbox layering: mailbox core first, adapter second.

## Non-Goals

- No wakeup transport integration.
- No session discovery, liveness checks, or manager-specific filtering.
- No notification cooldown or "already nudged" state.
- No writes, side effects, or background daemon.
- No group mailbox support in the first version.

## Core Judgment

This should be a new address-level query, not a patch on `list`.

`list` returns delivery snapshots. The new capability answers a different
question: "among these addresses, which inboxes currently have mail that has
been waiting too long?" That is a grouped, thresholded query over deliveries.

## Proposed CLI

Add a new command:

```bash
agent-mailbox stale --for ADDRESS --for ADDRESS ... --older-than 10m [--json | --yaml]
```

Rules:

- require at least one `--for`
- deduplicate repeated `--for` values
- require `--older-than > 0`
- personal mailbox only
- known group addresses fail explicitly, matching current personal-mailbox
  commands
- unseen addresses behave like empty inboxes
- return success with an empty result when nothing is stale
- support `--json` and `--yaml`
- structured output only in v1; no plain-text mode

Recommended JSON shape:

```json
[
  {
    "address": "agent-deck/183b87b0-1774276513",
    "oldest_eligible_at": "2026-03-31T12:00:00Z",
    "claimable_count": 3
  }
]
```

Recommended YAML shape mirrors the same object.

## Query Semantics

A supplied address is stale when all of the following are true:

1. the address resolves to a personal endpoint
2. there exists at least one delivery for that endpoint that is currently
   receivable by the same eligibility rules used by `recv`
3. the oldest current eligibility timestamp for that endpoint is older than
   `now - older_than`

For this query, define delivery eligibility timestamp as:

- `visible_at` when `state = 'queued'` and `visible_at <= now`
- `lease_expires_at` when `state = 'leased'`, `lease_expires_at IS NOT NULL`,
  and `lease_expires_at <= now`

Important exclusions:

- do not consider invisible future deliveries stale
- do not silently ignore known group addresses in v1

This is intentionally a receivability-based definition, not a "mail was ever
sent long ago" definition and not a queued-only definition.

## Data Model

No schema change is required.

Use existing tables:

- `endpoint_addresses`
- `deliveries`

No event writes are needed because the feature is query-only.

## Store API

Add a read-only store method:

```go
type StaleAddressesParams struct {
    Addresses []string
    OlderThan time.Duration
}

type StaleAddress struct {
    Address          string `json:"address" yaml:"address"`
    OldestEligibleAt string `json:"oldest_eligible_at" yaml:"oldest_eligible_at"`
    ClaimableCount   int    `json:"claimable_count" yaml:"claimable_count"`
}

func (s *Store) ListStaleAddresses(ctx context.Context, params StaleAddressesParams) ([]StaleAddress, error)
```

This keeps the capability explicit and avoids overloading `List`.

## SQL Direction

Implementation should:

1. normalize and deduplicate input addresses
2. resolve them to endpoint ids with existing personal-address rules
3. reject known group-address collisions the same way personal mailbox commands
   already do
4. query only matching endpoints
5. aggregate over currently receivable deliveries

Multiple supplied addresses may resolve to the same endpoint. The query must
deduplicate by endpoint and return at most one result row per resolved endpoint.
Use the first supplied address that resolved to that endpoint as the result
label. This preserves caller scope without duplicating one underlying inbox.

Representative SQL shape:

```sql
SELECT
  d.recipient_endpoint_id,
  MIN(
    CASE
      WHEN d.state = 'queued' THEN d.visible_at
      WHEN d.state = 'leased' THEN d.lease_expires_at
    END
  ) AS oldest_eligible_at,
  COUNT(*) AS claimable_count
FROM deliveries AS d
WHERE d.recipient_endpoint_id IN (...)
  AND (
    (d.state = 'queued' AND d.visible_at <= :now)
    OR
    (d.state = 'leased' AND d.lease_expires_at IS NOT NULL AND d.lease_expires_at <= :now)
  )
GROUP BY d.recipient_endpoint_id
HAVING oldest_eligible_at <= :stale_before
ORDER BY oldest_eligible_at ASC, d.recipient_endpoint_id ASC
```

This is simple, deterministic, and matches the mailbox truth directly.

## Output Contract

Return one entry per stale endpoint, ordered by:

1. oldest eligible mail first
2. chosen caller-scoped address lexicographically as a stable tiebreaker

Each result entry includes:

- `address`
- `oldest_eligible_at`
- `claimable_count`

Do not return message ids, subjects, bodies, or sender metadata. The caller can
issue more specific mailbox queries later if it needs them.

## Compatibility

This feature is additive.

- existing `send`, `recv`, `wait`, `watch`, `list`, and lifecycle commands stay
  unchanged
- no existing output schema changes
- no persisted-state migration

## Alternatives Considered

### Extend `list` with stale filters

Rejected.

That would mix delivery listing with address-level aggregation and either bloat
`list` output or create awkward mode-dependent shapes.

### Add notification write state to mailbox

Rejected.

Whether an external manager already notified someone is not mailbox truth. The
mailbox should not own transport policy or cooldown state for this feature.

### Query all addresses globally without caller input

Rejected.

The mailbox should not guess which addresses correspond to currently live
workers. The caller already owns that policy and should pass the scope
explicitly.

### Keep the query queued-only

Rejected.

That would conflict with current mailbox receive eligibility, which already
treats expired leases as claimable again. A stale query that ignored expired
leases would under-report receivable work.

## Risks and Tradeoffs

- Large address sets will create larger `IN (...)` queries. This is acceptable
  for the current local-first scope; if it becomes a problem later, batch the
  input or add a temp-table strategy.
- The query is personal-mailbox-only in v1. If callers later want group
  semantics, that should be a separate design.
- The result only describes current receivable state. A caller that needs
  message metadata, send age, or explicit failure reasons is asking for a
  different query.

## Open Questions

- Should the CLI subcommand be named `stale`, `stale-addresses`, or
  `list-stale-addresses`? `stale` is concise, but `stale-addresses` is more
  explicit.
- Should a future version accept `--max` to cap how many stale addresses are
  returned, or is caller-side filtering enough?
