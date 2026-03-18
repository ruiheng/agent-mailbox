# agent-mailbox

`agent-mailbox` is a local-first messaging system for AI agents.

It is intentionally separate from any single workflow or transport.
`agent-deck` may be one adapter, but it is not the protocol, the source of truth,
or the message store.

## Status

This repository starts as a docs-first project.
The initial commit captures the first-pass architecture and operating model
before any implementation code is added.

## Problem

Existing workflow messaging mixes four concerns:

- message persistence
- delivery mechanics
- transport-specific behavior
- workflow protocol semantics

That coupling creates avoidable problems:

- sender latency depends on receiver readiness
- message state is tied to one project working directory
- transport details leak into workflow logic
- receivers have little control over when they consume work

## Scope

`agent-mailbox` is responsible for:

- durable local message storage
- endpoint identity and alias resolution
- direct mailbox delivery semantics
- lease / ack / release / defer lifecycle
- a CLI for send / wait / recv / ack / release / defer / list
- optional transport adapters

`agent-mailbox` is not responsible for:

- workflow-specific task semantics
- project-local artifact authoring
- forcing one transport model on all agents

## Design Direction

The system is pull-first and adapter-friendly:

- the authoritative source of truth is a centralized mailbox store
- receivers may block waiting for new mail without changing message state
- receivers may explicitly claim a message when ready to process it
- transports such as `agent-deck` are optional adapters, ideally defaulting to
  notification rather than forced full-body delivery

The current recommendation is:

- centralized runtime state under `$XDG_STATE_HOME/ai-agent/mailbox`
- SQLite for metadata and concurrency control
- a blob store for large bodies or attachments
- endpoint aliases mapped to stable internal endpoint ids
- direct mailbox as the first supported routing primitive

## Repository Layout

- `README.md`: project boundary and intent
- `docs/initial-design.md`: first-pass architecture

## Next Steps

- freeze the initial identity model
- freeze the mailbox state machine
- define the first CLI contract
- decide which adapter, if any, should be implemented first
