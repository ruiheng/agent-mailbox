# Stale Address Query Design

## Summary

Add a read-only mailbox query that tells a caller which addresses currently have
old, still-unclaimed personal mail. The mailbox must not know where those
addresses came from, whether they belong to `agent-deck`, or whether the caller
plans to wake anything up.

## Problem

External session managers want to periodically check whether a set of live
workers has unread mail that has been sitting too long.

The mailbox already has the authoritative delivery state, but it does not yet
expose an address-level query for:

- a caller-provided address set
- currently claimable personal deliveries only
- addresses whose oldest queued mail is older than a threshold

The wrong direction is to teach `agent-mailbox` about one concrete session
manager, session liveness, or notification state. Those belong outside the
mailbox.

## Goals

- Provide one read-only query over a caller-provided address set.
- Return stale addresses, not full message bodies.
- Use existing mailbox truth: personal `deliveries` in `queued` state.
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
- unseen addresses behave like empty inboxes
- return success with an empty result when nothing is stale
- support `--json` and `--yaml`; plain text may be added, but machine-readable
  output is the primary use case

Recommended JSON shape:

```json
{
  "addresses": [
    {
      "address": "agent-deck/183b87b0-1774276513",
      "oldest_visible_at": "2026-03-31T12:00:00Z",
      "queued_count": 3
    }
  ]
}
```

Recommended YAML shape mirrors the same object.

## Query Semantics

An address is stale when all of the following are true:

1. the address resolves to a personal endpoint
2. there exists at least one delivery for that endpoint with:
   - `state = 'queued'`
   - `visible_at <= now`
3. the oldest such `visible_at` is older than `now - older_than`

Important exclusions:

- do not consider `leased` deliveries stale for this query
- do not consider invisible future deliveries stale
- do not include group addresses in v1

This is intentionally a claimability-based definition, not a "mail was ever
sent long ago" definition.

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
    Address         string `json:"address" yaml:"address"`
    OldestVisibleAt string `json:"oldest_visible_at" yaml:"oldest_visible_at"`
    QueuedCount     int    `json:"queued_count" yaml:"queued_count"`
}

type StaleAddressesResult struct {
    Addresses []StaleAddress `json:"addresses" yaml:"addresses"`
}

func (s *Store) ListStaleAddresses(ctx context.Context, params StaleAddressesParams) (StaleAddressesResult, error)
```

This keeps the capability explicit and avoids overloading `List`.

## SQL Direction

Implementation should:

1. normalize and deduplicate input addresses
2. resolve them to endpoint ids with existing personal-address rules
3. query only matching endpoints
4. aggregate over currently visible queued deliveries

Representative SQL shape:

```sql
SELECT
  ea.address,
  MIN(d.visible_at) AS oldest_visible_at,
  COUNT(*) AS queued_count
FROM endpoint_addresses AS ea
JOIN deliveries AS d
  ON d.recipient_endpoint_id = ea.endpoint_id
WHERE ea.address IN (...)
  AND d.state = 'queued'
  AND d.visible_at <= :now
GROUP BY ea.address, ea.endpoint_id
HAVING MIN(d.visible_at) <= :stale_before
ORDER BY oldest_visible_at ASC, ea.address ASC
```

This is simple, deterministic, and matches the mailbox truth directly.

## Output Contract

Return one entry per stale address, ordered by:

1. oldest visible mail first
2. address lexicographically as a stable tiebreaker

Each result entry includes:

- `address`
- `oldest_visible_at`
- `queued_count`

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

## Risks and Tradeoffs

- Large address sets will create larger `IN (...)` queries. This is acceptable
  for the current local-first scope; if it becomes a problem later, batch the
  input or add a temp-table strategy.
- The query is personal-mailbox-only in v1. If callers later want group
  semantics, that should be a separate design.
- The result only describes current queued state. A caller that needs "recently
  failed to receive" or "leased too long" is asking for a different query.

## Open Questions

- Should plain-text output be added in v1, or should the command stay
  structured-output-only?
- Should the CLI subcommand be named `stale`, `stale-addresses`, or
  `list-stale-addresses`? `stale` is concise, but `stale-addresses` is more
  explicit.
- Should a future version accept `--max` to cap how many stale addresses are
  returned, or is caller-side filtering enough?
