package mailbox

import (
	"context"
	"errors"
	"flag"
	"io"
	"strings"
)

func (a *App) prepareForwardCommand(args []string) (preparedCommand, error) {
	fs := flag.NewFlagSet("agent-mailbox forward", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var messageID string
	var deliveryID string
	var toAddress string
	var fromAddress string
	var subject string
	var groupMode bool
	var full bool
	var formats outputFlags

	fs.StringVar(&messageID, "message", "", "source message id")
	fs.StringVar(&deliveryID, "delivery", "", "source delivery id")
	fs.StringVar(&toAddress, "to", "", "recipient address")
	fs.StringVar(&fromAddress, "from", "", "sender address")
	fs.StringVar(&subject, "subject", "", "override forwarded subject")
	fs.BoolVar(&groupMode, "group", false, "forward to a known group address")
	fs.BoolVar(&full, "full", false, "emit the full payload")
	formats.register(fs, "emit JSON", "emit YAML")

	if err := a.parseCommandFlags(fs, args, a.writeForwardHelp); err != nil {
		return nil, err
	}
	if err := requireFlag(toAddress, "--to"); err != nil {
		return nil, err
	}
	hasMessageID := strings.TrimSpace(messageID) != ""
	hasDeliveryID := strings.TrimSpace(deliveryID) != ""
	if hasMessageID == hasDeliveryID {
		return nil, errors.New("exactly one of --message or --delivery is required")
	}
	format, err := formats.resolve()
	if err != nil {
		return nil, err
	}

	params := ForwardParams{
		MessageID:   messageID,
		DeliveryID:  deliveryID,
		ToAddress:   toAddress,
		FromAddress: fromAddress,
		Subject:     subject,
		Group:       groupMode,
	}

	return func(ctx context.Context, store *Store) error {
		ops := NewOperations(store)
		result, err := ops.Forward(ctx, params)
		if err != nil {
			return err
		}
		return a.writeForwardOutput(format, full, result)
	}, nil
}

func (a *App) writeForwardHelp() {
	writeHelp(a.stdout, []string{
		"Usage:",
		"  agent-mailbox forward (--message ID | --delivery ID) --to ADDRESS [options] [--json | --yaml] [--full]",
		"",
		"Options:",
		"  --message ID        Source message id",
		"  --delivery ID       Source delivery id",
		"  --to ADDRESS        Recipient address",
		"  --from ADDRESS      Sender address",
		"  --group             Forward to a known group address",
		"  --subject TEXT      Override forwarded subject",
		"  --json              Emit JSON",
		"  --yaml              Emit YAML",
		"  --full              Emit the full payload",
	})
}
