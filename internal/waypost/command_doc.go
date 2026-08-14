package waypost

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
)

const cliDocOverview = `# Waypost workflow
Use when: you need to exchange durable messages or inspect or change Waypost state.

## Required context
- Call waypost_status before other waypost_* tools; with --include-debug-tool, waypost_debug may run first.
- Status is compact; use include_diagnostics=true or include_active_leases=true for details, and paginate only lease details.
- Use its executable and resolved state directory for every stateful CLI command.
- Use exact ADDRESS, GROUP_ADDRESS, PERSON, ids, and tokens; do not infer them.

## Do
1. Use MCP for the common live flow: waypost_send, waypost_recv, waypost_claim_history, waypost_ack, waypost_release, and waypost_defer.
2. After a personal recv, settle its lease exactly once: ack after success; release for immediate retry without recording failure; defer until a known time; or CLI fail for a processing failure that increments attempts and may dead-letter.
3. Use CLI --json for wait, list, read, forward, fail, undefer, group, and address inspection. A successful CLI forward is durable-only; it does not guarantee notification or wakeup.
4. Run WAYPOST doc --list, then WAYPOST doc TOPIC... for focused guidance.

## Interpret
- CLI success: exit 0 and one JSON document on stdout.
- MCP waypost_recv no message: successful result with status: "no_message".
- CLI recv no message: exit 2 with status: "no_message" JSON on stdout.
- CLI wait no message: exit 2 with no output.
- Failure: exit 1 and one JSON error on stderr. Branch on error_code; retry only when retryable is true.
- Personal recv returns a lease token and must be settled. Group recv marks one message read and has no lease lifecycle.

## Stop
- Do not use a different binary or state directory than waypost_status reports.
- Do not settle a delivery without its message context.
- Do not guess missing identities, delivery ids, or lease tokens.
`

var cliDocTopics = map[string]string{
	"mcp-cli-boundary": `# MCP/CLI boundary
Use when: you need a Waypost operation that is not exposed as a common MCP tool.

## Required context
- The executable and resolved state directory reported by waypost_status.
- Explicit ADDRESS, GROUP_ADDRESS, and PERSON values for the durable operation.

## Do
1. Run WAYPOST --state-dir STATE_DIR forward (--message ID | --delivery ID) --to ADDRESS --json for durable forwarding.
2. Use wait, list, read, fail, undefer, group, or address inspect with --json for their durable-state work.
3. Use MCP for its retained Waypost operations: waypost_status, waypost_bind, waypost_send, waypost_recv, waypost_claim_history, waypost_ack, waypost_release, and waypost_defer. waypost_debug is available only when the server starts with --include-debug-tool. Claim history is the token-recovery path for a delivery this MCP process already claimed.

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
- next_cursor continues the same latest query without increasing the page size.
- A receive recovery error lists every claim that must be settled before another receive.

## Stop
- Do not act on an empty read result.
- Do not guess a missing id or lease token.
`,
	"history": `# Inspect Waypost history
Use when: you need durable delivery history or a stored body.

## Required context
- The reported executable and resolved state directory.
- ADDRESS for a queue query, optional FROM_ADDRESS for sender filtering, or exact MESSAGE_ID or DELIVERY_ID values.

## Do
1. Run WAYPOST --state-dir STATE_DIR list --for ADDRESS --state acked --json for the first page of delivery summaries.
2. Add --from FROM_ADDRESS to list or read --latest when only messages from one sender are relevant.
3. Run WAYPOST --state-dir STATE_DIR read DELIVERY_ID --json for a delivery body.
4. Run WAYPOST --state-dir STATE_DIR read MESSAGE_ID --json when the message identity, rather than one delivery, is known.

## Interpret
- Direct reads preserve the supplied id order.
- Latest reads are newest first and expose has_more only when another matching item exists.
- List results contain at most 100 items; pass next_cursor back with --cursor to continue the same query.
- sender_address is the current sender or forwarder; forwarded_from_address preserves the original source.
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
	if err := validateInputItemCount("doc topics", len(remaining)); err != nil {
		return err
	}
	if list {
		if len(remaining) != 0 {
			return errors.New("doc --list does not accept topics")
		}
		return a.writeDocTopics()
	}
	if len(remaining) == 0 {
		_, err := fmt.Fprint(a.stdout, cliDocOverview)
		return err
	}
	if len(remaining) == 1 {
		topic, ok := cliDocTopics[remaining[0]]
		if !ok {
			return fmt.Errorf("unknown doc topic %q", remaining[0])
		}
		_, err := fmt.Fprint(a.stdout, topic)
		return err
	}

	output, err := formatDocTopicBlocks(remaining)
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(a.stdout, output)
	return err
}

func formatDocTopicBlocks(topics []string) (string, error) {
	var output strings.Builder
	seen := make(map[string]struct{}, len(topics))
	written := 0
	for _, topicName := range topics {
		topic, ok := cliDocTopics[topicName]
		if !ok {
			return "", fmt.Errorf("unknown doc topic %q", topicName)
		}
		if _, exists := seen[topicName]; exists {
			continue
		}
		seen[topicName] = struct{}{}
		if written > 0 {
			output.WriteString("\n\n")
		}
		fmt.Fprintf(&output, "waypost: %s\n", topicName)
		for _, line := range strings.Split(strings.TrimSuffix(topic, "\n"), "\n") {
			output.WriteString("  ")
			output.WriteString(line)
			output.WriteByte('\n')
		}
		written++
	}
	return output.String(), nil
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
		"  waypost doc",
		"  waypost doc --list",
		"  waypost doc TOPIC...",
	})
}
