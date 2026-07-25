package waypost

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
)

var cliDocTopics = map[string]string{
	"mcp-cli-boundary": `# MCP/CLI boundary
Use when: you need a Waypost operation that is not exposed as a common MCP tool.

## Required context
- The executable and resolved state directory reported by waypost_status.
- Explicit ADDRESS, GROUP_ADDRESS, and PERSON values for the durable operation.

## Do
1. Run WAYPOST --state-dir STATE_DIR forward (--message ID | --delivery ID) --to ADDRESS --json for durable forwarding.
2. Use wait, list, read, fail, undefer, group, or address inspect with --json for their durable-state work.
3. Use MCP for its retained Waypost operations: waypost_status, waypost_bind, waypost_debug, waypost_send, waypost_recv, waypost_claim_history, waypost_ack, waypost_release, and waypost_defer. Claim history is the token-recovery path for a delivery this MCP process already claimed.

## Interpret
- error_code decides the next action; retry only when retryable is true.
- A successful CLI forward is durable-only. It does not guarantee notification or wakeup.

## Stop
- Stop if the executable or state directory is absent or differs from the reported values.
- Do not guess an address, group, person, binary, or state directory.
`,
	"recovery": `# Recover persisted input
Use when: message context was lost after a durable transition or a receive reports recovery work.

## Required context
- The reported executable and resolved state directory.
- ADDRESS for latest recovery, or the exact DELIVERY_ID or MESSAGE_ID.

## Do
1. Run WAYPOST --state-dir STATE_DIR read --latest --for ADDRESS --state acked --limit 1 --json to recover acknowledged input.
2. Run WAYPOST --state-dir STATE_DIR undefer --delivery DELIVERY_ID --json only for a deferred delivery that must be visible now.
3. Run WAYPOST --state-dir STATE_DIR fail --delivery DELIVERY_ID --lease-token TOKEN --reason TEXT --json for an exceptional processing failure.

## Interpret
- items: [] means no matching persisted input.
- has_more: true means another latest item matches beyond the limit.
- A receive recovery error lists every claim that must be settled before another receive.

## Stop
- Do not act on an empty read result.
- Do not guess a missing id or lease token.
`,
	"history": `# Inspect Waypost history
Use when: you need durable delivery history or a stored body.

## Required context
- The reported executable and resolved state directory.
- ADDRESS for a queue query, or exact MESSAGE_ID or DELIVERY_ID values.

## Do
1. Run WAYPOST --state-dir STATE_DIR list --for ADDRESS --state acked --json for delivery summaries.
2. Run WAYPOST --state-dir STATE_DIR read --delivery DELIVERY_ID --json for a delivery body.
3. Run WAYPOST --state-dir STATE_DIR read --message MESSAGE_ID --json when the message identity, rather than one delivery, is known.

## Interpret
- Direct reads preserve the supplied id order.
- Latest reads are newest first and expose has_more only when another matching item exists.
- not_found for a direct id is atomic: do not use a partial result.

## Stop
- Do not substitute a message id for a delivery id or vice versa.
- Stop on an error unless retryable is true.
`,
	"groups": `# Manage group membership and subscribers
Use when: durable group membership or notification-subscriber state must change.

## Required context
- The reported executable and resolved state directory.
- Explicit GROUP_ADDRESS, PERSON, and NOTIFY_ADDRESS values.

## Do
1. Run WAYPOST --state-dir STATE_DIR group create --group GROUP_ADDRESS --json to create a group.
2. Run WAYPOST --state-dir STATE_DIR group add-member --group GROUP_ADDRESS --person PERSON --json to add membership.
3. Run WAYPOST --state-dir STATE_DIR group add-subscriber --group GROUP_ADDRESS --notify-address NOTIFY_ADDRESS --person PERSON --json to add a subscriber.
4. Use the matching remove-member, remove-subscriber, members, or subscribers command with --json for the corresponding durable state.

## Interpret
- already_exists means the active record already exists.
- invalid_state means the requested active record is absent or cannot transition.

## Stop
- Do not infer a person from an address.
- Stop if the group is not found.
`,
	"diagnostics": `# Diagnose an address
Use when: an address may be unbound, endpoint-owned, or group-owned.

## Required context
- The reported executable and resolved state directory.
- One explicit ADDRESS.

## Do
1. Run WAYPOST --state-dir STATE_DIR address inspect --address ADDRESS --json.
2. Use kind: "endpoint", kind: "group", or kind: "unbound" to choose the durable operation.
3. Use waypost_status for live MCP binding information and address inspect for durable state.

## Interpret
- unbound is a successful inspection result, not an error.
- invalid_argument means the address itself is malformed.

## Stop
- Do not treat live binding as proof of a durable endpoint or group record.
- Do not create or target a different address to work around an inspection result.
`,
}

func (a *App) runDocCommand(args []string) error {
	fs := flag.NewFlagSet("waypost doc", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var list bool
	fs.BoolVar(&list, "list", false, "list available topics")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			a.writeDocHelp()
			return ErrHelpRequested
		}
		return err
	}

	remaining := fs.Args()
	if list {
		if len(remaining) != 0 {
			return errors.New("doc --list does not accept a topic")
		}
		return a.writeDocTopics()
	}
	if len(remaining) == 0 {
		a.writeDocHelp()
		return ErrHelpRequested
	}
	if len(remaining) != 1 {
		return errors.New("doc accepts exactly one topic")
	}
	topic, ok := cliDocTopics[remaining[0]]
	if !ok {
		return fmt.Errorf("unknown doc topic %q", remaining[0])
	}
	_, err := fmt.Fprint(a.stdout, topic)
	return err
}

func (a *App) writeDocTopics() error {
	topics := make([]string, 0, len(cliDocTopics))
	for topic := range cliDocTopics {
		topics = append(topics, topic)
	}
	sort.Strings(topics)
	for _, topic := range topics {
		if _, err := fmt.Fprintln(a.stdout, topic); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) writeDocHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  waypost doc --list",
		"  waypost doc TOPIC",
	})
}
