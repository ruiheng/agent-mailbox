# Multi-recipient `send`

Status: round-1 draft for independent review  
Scope: CLI `send` and MCP `waypost_send` only

## Summary

Add an opt-in batch form at the two existing send coordination boundaries while
leaving `Store.Send` as the one-recipient, one-transaction primitive.

- CLI: make `--to` repeatable. One occurrence keeps the current input and output
  contract; two or more occurrences select the batch result contract.
- MCP: retain `to_address` and add `to_addresses`. Exactly one field must be
  present. `to_address` keeps the current result/error contract;
  `to_addresses`, including a one-element array, selects the batch contract.
- Normalize all batch recipients before the first write, preserve first-seen
  order, and discard duplicate normalized addresses after their first
  occurrence. Apply the existing 100-item adapter limit to the raw list before
  deduplication.
- Execute unique recipients sequentially. Each item calls the existing
  `Store.Send` and, after durable success, the existing notification path.
  Continue after a per-recipient durable-send error so every requested unique
  recipient has an ordered outcome.
- Do not add a cross-recipient transaction, shared broadcast record, new group
  model, or batch method on `Store`.

Repository conventions make these choices defensible without a requester
decision: repeatable address inputs already preserve order and deduplicate via
`NormalizeAddressList`, and MCP session-require batches return ordered per-item
errors without preventing later items from running.

## Goals and boundaries

The change must:

1. Send the same body and metadata to multiple personal addresses or, with the
   existing group switch, to multiple existing group addresses.
2. Preserve every existing single-recipient CLI and MCP input, success output,
   durable failure, and notification behavior.
3. Make partial durable success explicit and machine-readable.
4. Preserve each individual send's transaction and notification semantics.

The change does not:

- extend `forward`, `recv`, `read`, or any lifecycle/session tool;
- create one message shared by several personal deliveries;
- make a batch atomic or idempotent;
- introduce concurrency or configurable failure policy;
- change group membership, group subscriber fan-out, or notification retry
  behavior.

## Current behavior and ownership

- `internal/waypost/command_send.go:prepareSendCommand` binds `--to` to one
  string, reads the body once, calls `Store.Send` once, optionally invokes the
  configured `SendNotifier`, and writes one compact or full receipt.
- `internal/waypost/store.go:Store.Send` normalizes one target and sender, writes
  the body, message, and personal delivery or group message in one transaction,
  and returns one `SendResult`. This is the correct durable primitive to retain.
- `internal/mcpserver/waypost_tools.go:sendWaypostMessageWithService` normalizes
  one `to_address`, resolves the effective sender once, calls the sender once,
  notifies once, and builds the current MCP result map.
- `internal/mcpserver/waypost_tools.go:notifyWaypostSend` and
  `internal/mcpserver/notifier.go` already distinguish personal wakeups from
  group subscriber fan-out. Notification failure is informational after a
  durable success.
- `internal/waypost/address.go:NormalizeAddressList` and repeatable
  `stringListFlag` users establish first-occurrence ordering and normalized
  deduplication. `MaxInputItems` establishes a 100-item adapter fan-out limit.
- `internal/mcpserver/session_tools.go` establishes the batch convention of
  continuing after an item error and returning ordered outcomes.

## Public input contracts

### CLI

The syntax becomes:

```text
waypost send --to ADDRESS [--to ADDRESS ...] --body-file PATH [options]
```

`--to` uses the existing `stringListFlag` implementation. A caller supplies
multiple recipients by repeating the flag:

```bash
waypost send \
  --to agent-deck/alpha \
  --to codex/beta \
  --body-file message.md \
  --json
```

Comma-separated spelling is intentionally unsupported. A comma is never a
recipient separator and must not be split by the parser; a single flag value is
validated as one literal address under the existing address grammar. This
avoids colliding with generic address schemes whose identifiers may contain
punctuation.

Mode selection is based on the raw number of `--to` occurrences:

- one occurrence: legacy single-recipient path and exact legacy output;
- two or more occurrences: batch path, even if deduplication leaves one unique
  recipient.

Zero occurrences remains `--to is required`.

### MCP

Evolve `waypostSendInput` additively:

```go
type waypostSendInput struct {
    ToAddress   string   `json:"to_address,omitempty"`
    ToAddresses []string `json:"to_addresses,omitempty"`
    // existing fields unchanged
}
```

The registered input schema must express an exclusive choice:

- `to_address`: non-batch, legacy form;
- `to_addresses`: batch form with `minItems: 1` and `maxItems: 100`;
- both or neither: invalid tool arguments before the Waypost service is opened;
- an empty string or an invalid item: normal address validation error.

Build the schema from `waypostSendInput`, remove `to_address` from the root's
unconditional required list, and add a `oneOf` with mutually exclusive required
properties. Do not set `uniqueItems`: duplicates are valid input and are
normalized/deduplicated by the batch contract. Keep a runtime exact-one check as
defense in depth; like the session-require validators, it must inspect property
presence in `req.Params.Arguments` rather than infer presence from decoded zero
values. Update the tool description to state the choice. Do not assign
precedence and do not merge the two fields.

An existing `to_address` caller therefore submits the same JSON and receives
the same single result. A `to_addresses` caller always receives a batch envelope,
including for a one-element array.

## Recipient preparation

Add a pure helper in a new `internal/waypost/send_batch.go`, used only by the
new batch paths:

```go
func NormalizeSendRecipients(values []string, group bool) ([]string, error)
```

It performs, in order:

1. Reject more than `MaxInputItems` raw values. Counting before deduplication
   prevents duplicate-heavy input from bypassing the adapter limit.
2. Require at least one value.
3. Normalize every value with `NormalizeAddress` before any durable call.
4. Deduplicate by normalized string while retaining the first occurrence and
   its position.
5. Validate the whole list's mode: every target must be `group/...` when
   `group` is true, and no target may be `group/...` when it is false.

The adapters also finish all request-wide validation before execution:

- CLI resolves output format, normalizes `--from`, reads the body once, and
  rejects an empty body.
- MCP validates the recipient selector, resolves the effective sender once,
  trims the existing metadata fields, rejects `as_person` without group mode,
  and rejects an empty body.
- A group-prefixed sender is rejected before the first send.

These checks prevent caller-controlled syntax or mode errors late in a batch.
State-dependent validation remains in `Store.Send`: for example, whether each
group currently exists and whether the database can commit. Adding read
preflights for those conditions would be racy and would duplicate store logic.

Static preparation failure produces the existing ordinary CLI/MCP error and no
batch result because no recipient was attempted and no durable side effect
occurred.

## Batch execution

The same new file defines a small coordination helper, not a storage API:

```go
type SendBatchItem struct {
    ToAddress    string
    Result       SendResult
    Notification *SendNotificationOutcome
    Err          error
}

type SendBatchResult struct {
    ToAddresses []string
    Items       []SendBatchItem
    SentCount   int
    FailedCount int
}

type SendBatchNotifier func(
    context.Context,
    SendNotificationRequest,
) SendNotificationOutcome

func ExecuteSendBatch(
    ctx context.Context,
    sender interface {
        Send(context.Context, SendParams) (SendResult, error)
    },
    base SendParams,
    recipients []string,
    notify SendBatchNotifier,
) SendBatchResult
```

Exact names may follow local naming style, but the ownership and behavior must
remain as above. This helper exists so CLI and MCP cannot drift on sequencing,
continuation, or notification eligibility. It must not begin a transaction or
perform address normalization.

Execution is sequential in normalized first-seen order:

```text
for recipient in recipients:
    params = copy(base); params.ToAddress = recipient
    result, err = sender.Send(ctx, params)
    if err:
        append failed item; continue
    if notify is configured:
        outcome = notify(ctx, {Params: params, Result: result})
    append sent item with optional notification outcome
```

Sequential execution is deliberate: it gives deterministic call and wake order,
avoids adding database contention, and most closely models repeated current
calls. The existing group notifier may still fan out to a group's subscribers
with its current internal concurrency.

### Durable semantics

Every unique recipient receives a separate `Store.Send` call:

- personal mode creates a separate message and delivery with distinct IDs;
- group mode creates a separate group message for each target group and uses
  that group's existing subscriber-delivery behavior;
- each call commits or rolls back independently;
- a failed call does not roll back earlier calls and does not suppress later
  calls;
- there is no cross-recipient transaction or atomicity claim.

`Store.Send`, `SendParams`, and `SendResult` need no semantic or schema change.
The same body bytes may resolve to the same content-addressed blob, but callers
must treat the returned message and delivery receipts as independent.

### Notification semantics

Notification is invoked immediately after each successful durable call and
before advancing to the next recipient.

- CLI configures the callback only when `--notify` is present; otherwise batch
  items omit notification fields, matching current CLI behavior. If the app has
  no configured notifier, every durably sent item receives the current
  `failed: send notification is not configured` outcome.
- MCP always configures the callback and passes the existing
  `disable_notify_message` value to every item. Local, unsupported, disabled,
  already-claimed, and failed outcomes remain those produced today.
- Group items call the existing group-subscriber notification path separately
  for each successfully stored group message.
- A durable-send failure has no notification attempt.
- A notification failure stays attached to a durably sent item, does not change
  its `status: sent`, does not increment `failed_count`, does not affect CLI
  exit status, and does not stop the batch.
- Do not add batch-level retry or resend behavior.

## Output contracts

### Legacy single-recipient output

When CLI receives one `--to`, retain the current text, JSON, and YAML projections
exactly, including compact/full and `--notify` variants.

When MCP receives `to_address`, retain the current result map exactly, including
`status`, effective `from_address`, normalized `to_address`, `subject`, durable
receipt fields, and `notify_*` fields. Existing durable-send errors remain MCP
tool errors with no receipt. The MCP `waypost_forward` implementation continues
to construct this single form internally and is unchanged in scope and output.

### Batch structured output

CLI JSON and YAML serialize the same mapping. MCP `to_addresses` returns the
same outer batch fields and uses its current single-send fields inside each
successful item:

```json
{
  "status": "partial_failed",
  "to_addresses": ["agent-deck/alpha", "agent-deck/beta"],
  "recipient_count": 2,
  "sent_count": 1,
  "failed_count": 1,
  "results": [
    {
      "to_address": "agent-deck/alpha",
      "status": "sent",
      "delivery_id": "dlv_...",
      "notify_status": "sent",
      "notify_scheme": "agent-deck",
      "notify_error": null
    },
    {
      "to_address": "agent-deck/beta",
      "status": "failed",
      "error": "commit send transaction: ...",
      "notify_status": "not_attempted",
      "notify_scheme": null,
      "notify_error": null
    }
  ]
}
```

Rules:

- `to_addresses` and `results` contain normalized, unique recipients in the
  same order.
- `recipient_count == len(to_addresses) == len(results)` and
  `recipient_count == sent_count + failed_count`.
- Outer `status` is `sent` when every durable call succeeded,
  `partial_failed` when both counts are non-zero, and `failed` when no durable
  call succeeded.
- Item `status` is `sent` or `failed`. Failed items contain `error` and no
  durable receipt.
- CLI items omit `notify_*` unless `--notify` was requested. With `--notify`, a
  failed durable item uses `notify_status: not_attempted`.
- MCP items always include the existing effective `from_address`, `subject`,
  and `notify_*` fields. A failed item includes the already-resolved sender and
  subject plus `notify_status: not_attempted`.
- Personal CLI compact items contain `delivery_id`; `--full` additionally uses
  the existing `message_id` and `blob_id` projection. Group items use the
  existing `mode`, `message_id`, `group_id`, `group_address`, `eligible_count`,
  and `message_created_at` projection.
- MCP successful items are formed by the same mapper as the legacy result so
  personal and group fields cannot diverge.

YAML uses the same field names and nesting, with one document rather than a
document stream.

### Batch text output

Emit one line per result followed by one aggregate line. Examples:

```text
to_address=agent-deck/alpha status=sent delivery_id=dlv_...
to_address=agent-deck/beta status=failed error="commit send transaction: ..."
status=partial_failed recipient_count=2 sent_count=1 failed_count=1
```

`--full`, group receipt fields, and notification suffixes reuse the current
single text formatting after the new `to_address` and item `status` prefixes.
Use quoted formatting for error text.

## Failure and completion semantics

| Condition | Durable effects | Remaining recipients | CLI | MCP batch |
|---|---|---|---|---|
| Selector/schema, count, address, mode, sender, or body validation fails | none | none attempted | existing error, exit 1, no batch output | tool error, no batch result |
| Service/runtime cannot open or sender resolution fails | none | none attempted | existing error, exit 1 | tool error |
| One `Store.Send` fails | earlier commits remain | continue sequentially | complete envelope on stdout, exit 1 | successful tool result with `partial_failed` or `failed` and per-item errors |
| One notification fails after send | that send remains committed | continue | item remains sent; exit 0 if all durable calls succeeded | item remains sent; outer durable status unchanged |
| Output serialization/write fails | all completed sends remain | execution has already completed | output error, exit 1; do not retry automatically | normal MCP serialization error; completed sends remain |
| Process dies mid-loop | completed transactions remain | unknown/unattempted | no atomic recovery claim | no atomic recovery claim |

For CLI per-item durable failures, `prepareSendCommand` writes the full batch
envelope first and then returns a batch-incomplete error so `runCommand` exits
1. Stderr contains one concise summary such as
`send batch failed for 1 of 2 recipients; inspect stdout results`. This
batch-only contract deliberately preserves the receipts needed to reconcile
partial success; preparation failures still produce no stdout.

For MCP, completing the plural aggregation is a successful protocol call even
when an item failed, matching existing MCP batch conventions. Callers must
branch on the outer `status` and item statuses. Only failures before or outside
item orchestration are MCP tool errors.

Because no idempotency key exists, blindly retrying a partial batch can duplicate
already successful sends. Documentation must tell callers to retry only failed
addresses after inspecting `results`.

## Code changes

### New shared coordination

- Add `internal/waypost/send_batch.go` with
  `NormalizeSendRecipients`, `SendBatchItem`, `SendBatchResult`, the aggregate
  status/count logic, and `ExecuteSendBatch`.
- Add `internal/waypost/send_batch_test.go` with fake-sender and fake-notifier
  tests for order, continued execution, notification eligibility, and counts.

### CLI

- `internal/waypost/command_send.go`
  - replace the single `toAddress` flag binding with repeatable
    `stringListFlag`;
  - retain the current branch for one raw value;
  - prepare and execute the batch branch for multiple raw values;
  - update usage/help and document that `--to` is repeatable.
- `internal/waypost/cli_payloads.go`
  - add batch output structs and projections for compact/full and optional
    notification fields; do not change existing single structs.
- `internal/waypost/output_modes.go` and `internal/waypost/output.go`
  - add structured and text batch writers; continue using existing single
    receipt formatters for each successful item.
- `internal/waypost/app_test.go`
  - cover preparation, notifier calls, projections, invalid-before-write, and
    direct `App` batch-incomplete errors.
- `cmd/waypost/main_test.go`
  - cover end-to-end stdout/stderr/exit behavior and durable records.

No special case is required in `cmd/waypost/main.go`; the returned
batch-incomplete error already produces exit 1 and a stderr summary after the
command has written its result envelope.

### MCP

- `internal/mcpserver/waypost_tools.go`
  - add `ToAddresses`;
  - register the exact-one schema and expanded tool description;
  - branch in `waypostSend` between the unchanged legacy helper and a batch
    helper;
  - open the Waypost service and resolve the sender once for the batch;
  - adapt the existing notifier to `SendBatchNotifier`;
  - extract the current successful-send map construction so legacy and batch
    items share it;
  - return a mutation result for completed batches even when an item failed so
    overview updates still occur.
- `internal/mcpserver/server_test.go`
  - extend schema, routing, output, partial failure, and notification coverage.

`internal/mcpserver/notifier.go` needs no behavior change. It is called once per
successful batch item and retains its current personal and group behavior.

### Documentation

- `docs/cli.md`: repeatable syntax, no comma splitting, dedup/order, limit,
  output examples, partial exit behavior, group batches, and retry warning.
- `README.md`: MCP exact-one input forms and batch envelope; update CLI quick
  start notes without replacing the single example.
- Update `writeSendHelp` in code; there is no database or configuration
  migration document.

## Compatibility and migration

- Database/schema migration: none.
- Durable model: unchanged. Existing readers see ordinary independent messages
  and deliveries/group messages.
- CLI single input/output: unchanged for one `--to` in text, JSON, YAML,
  compact, full, notify, personal, and group modes.
- MCP single input/output: unchanged for `to_address`. The generated schema
  changes from an unconditional required property to one branch of an exact-one
  choice, but the property name and value type remain intact.
- Internal forwarding: unchanged and remains single-recipient.
- Error compatibility: legacy single errors retain their current MCP/CLI path.
  New partial-result behavior exists only in the opt-in batch forms.
- No feature flag or staged data rollout is needed. Documentation and code must
  land together because callers need to understand partial retry behavior.

## Implementation sequence

1. Add and unit-test recipient preparation and sequential batch execution in
   `internal/waypost`.
2. Add batch projection/output types and text/JSON/YAML writers without touching
   legacy single projections.
3. Convert CLI `--to` to a repeatable flag, retain the explicit single branch,
   and add end-to-end CLI tests.
4. Add the MCP `to_addresses` schema and exact-one validation.
5. Extract the existing MCP single-result mapper, add the plural orchestration
   branch, and test persistence/notification/error behavior with the fake
   Waypost service.
6. Update help, `docs/cli.md`, and `README.md`.
7. Run focused tests, then the full Go test suite and formatting/static checks.

## Test matrix

### Shared orchestration

- preserves normalized first-seen order;
- rejects zero and more than 100 raw recipients;
- deduplicates exact and whitespace-normalized duplicates;
- rejects any invalid address before the fake sender is called;
- enforces all-personal or all-group mode before the first call;
- calls the sender sequentially with identical base metadata and a different
  `ToAddress`;
- continues to item three when item two fails;
- calls the notifier only for successful durable items and immediately after
  each success;
- notification failure does not alter sent/failed counts.

### CLI

- one `--to` retains exact current compact/full text, JSON, YAML, notification,
  personal, and group outputs;
- repeated `--to` creates one distinct personal message/delivery per unique
  recipient and preserves result order;
- batch text has ordered item lines and an aggregate line;
- JSON and YAML have the specified single-envelope shape in compact/full modes;
- raw duplicates select batch mode but produce only one durable send/result;
- a comma-containing single value is never split into multiple targets;
- an invalid later recipient, empty body, invalid sender, mixed group/personal
  list, or 101 raw flags creates no durable records;
- two existing groups each get an independent group message;
- an existing group, a missing group, and another existing group demonstrate a
  mid-batch store failure, continued execution, ordered results, retained first
  and third messages, stdout envelope, and exit 1;
- `--notify` calls once per durable success, reports each outcome, and remains
  exit 0 when only notification fails.

### MCP

- schema exposes both selectors with exact-one semantics and array bounds;
- legacy `to_address` success/error output is unchanged;
- both selectors, neither selector, an empty array, or more than 100 items fail
  before opening/calling the sender;
- `to_addresses` with one item still returns a batch envelope;
- sender resolution and Waypost service open occur once per batch;
- duplicate normalized targets produce one sender/notification call;
- ordered distinct targets produce ordered independent receipts;
- fake failure at the middle target does not prevent the final call and returns
  `partial_failed` with the failed item's error;
- all durable failures return outer `failed` as a successful MCP batch result;
- notification failure, disabled notification, local target, unsupported target,
  already-claimed delivery, and group subscriber fan-out preserve current
  per-send outcomes;
- every call in group mode receives `Group: true` and the same `AsPerson`;
- `waypost_forward` remains on the legacy single helper and its tests do not
  change contract.

### Verification

Run `gofmt` on changed Go files, focused package tests for
`./internal/waypost`, `./internal/mcpserver`, and `./cmd/waypost`, followed by
`go test ./...` and the repository's existing vet/static-check command.

## Risks and mitigations

- **Partial retry duplicates earlier successes.** Ordered receipts, explicit
  aggregate status, exit semantics, and documentation require retrying only
  failed addresses.
- **A batch can take longer because notifications are sequential.** This is the
  closest semantic match to repeated sends and keeps ordering deterministic;
  the existing per-group subscriber fan-out remains concurrent.
- **Schema clients must understand the new exact-one choice.** Keep
  `to_address` unchanged, express the choice in JSON Schema and the tool
  description, and enforce it again at runtime.
- **Single behavior could accidentally drift during refactoring.** Keep an
  explicit single branch and regression-test exact fields/output rather than
  routing all calls through the new envelope.
- **Errors may expose partial state.** That is inherent to independent sends;
  the result contract makes it observable instead of claiming atomicity.

## Alternatives rejected

- **Comma-separated CLI values:** ambiguous with the existing generic address
  grammar and inconsistent with repeatable address flags.
- **Replace `to_address` with an array:** breaks existing MCP callers and schema
  expectations.
- **Accept both MCP fields and merge or prioritize them:** makes double-send
  behavior ambiguous; exact-one validation is safer.
- **Repeat duplicate destinations:** an accidental repeated flag would create
  duplicate durable messages; repository address-list convention deduplicates.
- **Stop at the first item error:** with independent transactions it withholds
  outcomes for explicitly requested later recipients and conflicts with the
  existing MCP batch convention.
- **Run recipients concurrently:** changes deterministic persistence/wake order,
  complicates notification result association, and adds avoidable write
  contention.
- **Add `Store.SendBatch` or one cross-recipient transaction:** misstates the
  requested aggregation as a new storage primitive and complicates personal and
  group semantics without benefit.

## Acceptance criteria

The feature is complete when one CLI invocation or one MCP call can target up
to 100 raw recipient values, returns ordered outcomes for unique normalized
targets, preserves each successful send and notification's current semantics,
exposes partial durable failures without stopping later targets, and leaves all
legacy single-recipient contracts and unrelated operations unchanged.
