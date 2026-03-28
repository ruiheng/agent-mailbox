# Group Mailbox Design Notes

## Problem Statement

Add a new mailbox capability that behaves like a group address / lightweight group chat:

- A group has one external address.
- Anyone can send to the group address.
- Multiple people can read messages from the group.
- Each person tracks read state independently.
- New members can see history from before they joined.
- Leaving the group only stops receiving new messages; historical visibility remains.
- A member can list group message metadata, especially subject and read status.
- The system must expose group-level read status for each message.
- Wait semantics are needed for group messages.
- Messages are broadcast only; no reply/thread model is required.

Primary use case: support multi-agent meeting / discussion workflows.

## Current Model Constraints

Current mailbox semantics are centered on two objects:

- `message`: the content record.
- `delivery`: one delivery to one recipient endpoint, with state such as `queued`, `leased`, `acked`, `failed`.

This works well for personal inbox / queue semantics, but group chat requirements do not fit the existing model cleanly because:

1. New members must see old messages.
2. Group-level read status must be derived across multiple readers.
3. Membership changes over time.
4. Group messages are a shared history, not a queue consumed once.

Because of that, group chat should not be implemented as a thin patch on top of the existing single-recipient delivery queue.

## Core Judgment

Treat personal mailbox and group mailbox as two different semantics:

- Personal mailbox: queue semantics.
- Group mailbox: append-only message stream plus per-person read state.

Do not force group behavior into the current delivery/lease/ack model.

## Recommended Data Model Direction

Introduce explicit group-side data structures instead of reusing delivery as the primary abstraction.

Suggested concepts:

- `person`
  - A stable identity for read-state tracking.
  - Deduplicate by person, not by endpoint address.

- `group`
  - The group itself.
  - Has one external address such as `group/<name>`.

- `group_membership`
  - Membership history.
  - Supports dynamic join/leave.
  - Leaving stops future delivery eligibility but keeps historical access.

- `group_message`
  - One stored group message record per logical message.
  - No content fan-out duplication.

- `group_read`
  - Per-person read state for a group message.
  - This is the source of member read/unread status.

### Important Principle

For group mail:

- store content per logical group message
- store read state per `(person, group_message)`

Do not model this as a single shared delivery with complex multi-reader state.
Do not model this as pure send-time fan-out deliveries either, because that makes history for newly added members awkward and patchy.

## Read-State Semantics

Recommended behavior:

- New member joins:
  - can see full history immediately
  - old messages start unread for that person unless a different policy is later chosen explicitly

- Member leaves:
  - no longer counts as a recipient for future messages
  - keeps historical visibility
  - keeps previous read records

- Member re-joins later:
  - preserve prior read history; do not reset old messages

## Group-Level Read Status

Need a precise rule for `read_count / eligible_count`.

Recommended default:

- compute group-level read status against the membership snapshot at message creation time

Reason:

- if computed against current membership, old messages change their read ratio when members join later
- that makes group history unstable and confusing

So each group message should carry enough membership-snapshot information, directly or indirectly, to support stable read statistics.

## API / CLI Direction

Keep the external group address model visible to users.

Suggested high-level operations:

- `send --to group/<name>`
  - anyone may send

- `list --for group/<name> --as <person>`
  - list message metadata visible to that person
  - include at least:
    - `message_id`
    - `subject`
    - `message_created_at`
    - per-person `read/unread`
    - group-level `read_count`
    - group-level `eligible_count`

- `wait --for group/<name> --as <person>`
  - wait until this person has an unread group message

- `recv --for group/<name> --as <person>`
  - return the next unread group message for that person
  - mark it read for that person

## Non-Goals For First Version

Do not add these in the first cut:

- reply / thread semantics
- mention semantics
- admin / role systems
- message edit / revoke
- watch-stream redesign
- forcing group operations through queue lease/retry APIs

## Compatibility Guidance

Keep existing personal mailbox behavior unchanged.

Recommended split:

- existing queue-oriented commands continue to work for personal inbox addresses
- group addresses introduce group-stream semantics with explicit `--as <person>` identity where needed

Do not overload old personal queue meaning in ways that make existing callers ambiguous.

## Biggest Design Risks

1. Mixing queue semantics and group-stream semantics too early.
2. Reusing delivery as the only read-state object.
3. Computing group read status against current membership instead of message-time membership.
4. Not introducing a real person identity layer and trying to deduplicate by raw address alone.
5. Building history support by retroactively creating synthetic deliveries for newly added members.

## Recommended Next Step

Write a proper design spec before implementation, covering:

- schema changes
- identity model for `person`
- group membership history model
- message-time recipient snapshot rule
- CLI surface changes
- MCP exposure changes
- migration / backward-compatibility plan
- exact read/unread transition rules
