package mailbox

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

type outputFormat uint8

const (
	outputFormatText outputFormat = iota
	outputFormatJSON
	outputFormatYAML
)

type outputFlags struct {
	json bool
	yaml bool
}

func (f *outputFlags) register(fs *flag.FlagSet, jsonUsage, yamlUsage string) {
	fs.BoolVar(&f.json, "json", false, jsonUsage)
	fs.BoolVar(&f.yaml, "yaml", false, yamlUsage)
}

func (f outputFlags) resolve() (outputFormat, error) {
	if f.json && f.yaml {
		return outputFormatText, errors.New("--json and --yaml are mutually exclusive")
	}
	if f.yaml {
		return outputFormatYAML, nil
	}
	if f.json {
		return outputFormatJSON, nil
	}
	return outputFormatText, nil
}

func (f outputFlags) resolveStructured() (outputFormat, error) {
	format, err := f.resolve()
	if err != nil {
		return outputFormatText, err
	}
	if format == outputFormatText {
		return outputFormatText, errors.New("either --json or --yaml is required")
	}
	return format, nil
}

func (a *App) writeStructuredOutput(format outputFormat, value any) error {
	switch format {
	case outputFormatJSON:
		encoder := json.NewEncoder(a.stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	case outputFormatYAML:
		return writeYAML(a.stdout, value)
	default:
		return fmt.Errorf("unsupported structured output format %d", format)
	}
}

func (a *App) writeSendResultText(result SendResultCompact) error {
	if result.Mode == SendModeGroup {
		eligibleCount := 0
		if result.EligibleCount != nil {
			eligibleCount = *result.EligibleCount
		}
		_, err := fmt.Fprintf(
			a.stdout,
			"message_id=%s group=%s eligible_count=%d\n",
			result.MessageID,
			result.GroupAddress,
			eligibleCount,
		)
		return err
	}
	_, err := fmt.Fprintf(a.stdout, "delivery_id=%s\n", result.DeliveryID)
	return err
}

func (a *App) writeSendResultFullText(result SendResultFull) error {
	if result.Mode == SendModeGroup {
		eligibleCount := 0
		if result.EligibleCount != nil {
			eligibleCount = *result.EligibleCount
		}
		_, err := fmt.Fprintf(
			a.stdout,
			"message_id=%s group=%s eligible_count=%d\n",
			result.MessageID,
			result.GroupAddress,
			eligibleCount,
		)
		return err
	}
	_, err := fmt.Fprintf(
		a.stdout,
		"message_id=%s delivery_id=%s blob_id=%s\n",
		result.MessageID,
		result.DeliveryID,
		result.BlobID,
	)
	return err
}

func (a *App) writeReceivedMessageText(message ReceivedMessageCompact) error {
	if _, err := fmt.Fprintf(
		a.stdout,
		"delivery_id=%s recipient_address=%s lease_token=%s content_type=%s subject=%q\n",
		message.DeliveryID,
		message.RecipientAddress,
		message.LeaseToken,
		message.ContentType,
		message.Subject,
	); err != nil {
		return err
	}
	if _, err := fmt.Fprint(a.stdout, message.Body); err != nil {
		return err
	}
	if !strings.HasSuffix(message.Body, "\n") {
		if _, err := fmt.Fprintln(a.stdout); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) writeReceivedMessageFullText(message ReceivedMessage) error {
	if _, err := fmt.Fprintf(
		a.stdout,
		"delivery_id=%s message_id=%s recipient_address=%s lease_token=%s lease_expires_at=%s subject=%q\n",
		message.DeliveryID,
		message.MessageID,
		message.RecipientAddress,
		message.LeaseToken,
		message.LeaseExpiresAt,
		message.Subject,
	); err != nil {
		return err
	}
	if _, err := fmt.Fprint(a.stdout, message.Body); err != nil {
		return err
	}
	if !strings.HasSuffix(message.Body, "\n") {
		if _, err := fmt.Fprintln(a.stdout); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) writeGroupReceivedMessageText(message GroupReceivedMessageCompact) error {
	if _, err := fmt.Fprintf(
		a.stdout,
		"message_id=%s group=%s person=%s first_read_at=%s content_type=%s subject=%q read_count=%d eligible_count=%d\n",
		message.MessageID,
		message.GroupAddress,
		message.Person,
		message.FirstReadAt,
		message.ContentType,
		message.Subject,
		message.ReadCount,
		message.EligibleCount,
	); err != nil {
		return err
	}
	if _, err := fmt.Fprint(a.stdout, message.Body); err != nil {
		return err
	}
	if !strings.HasSuffix(message.Body, "\n") {
		if _, err := fmt.Fprintln(a.stdout); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) writeGroupReceivedMessageFullText(message GroupReceivedMessage) error {
	if _, err := fmt.Fprintf(
		a.stdout,
		"message_id=%s group=%s person=%s first_read_at=%s content_type=%s subject=%q read_count=%d eligible_count=%d\n",
		message.MessageID,
		message.GroupAddress,
		message.Person,
		message.FirstReadAt,
		message.ContentType,
		message.Subject,
		message.ReadCount,
		message.EligibleCount,
	); err != nil {
		return err
	}
	if _, err := fmt.Fprint(a.stdout, message.Body); err != nil {
		return err
	}
	if !strings.HasSuffix(message.Body, "\n") {
		if _, err := fmt.Fprintln(a.stdout); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) writeReceiveResultText(result ReceiveResultCompact) error {
	for index, message := range result.Messages {
		if index > 0 {
			if _, err := io.WriteString(a.stdout, "---\n"); err != nil {
				return err
			}
		}
		if err := a.writeReceivedMessageText(message); err != nil {
			return err
		}
	}
	if result.HasMore {
		if _, err := io.WriteString(a.stdout, "notice=more_messages_available\n"); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) writeReceiveResultFullText(result ReceiveResult) error {
	for index, message := range result.Messages {
		if index > 0 {
			if _, err := io.WriteString(a.stdout, "---\n"); err != nil {
				return err
			}
		}
		if err := a.writeReceivedMessageFullText(message); err != nil {
			return err
		}
	}
	if result.HasMore {
		if _, err := io.WriteString(a.stdout, "notice=more_messages_available\n"); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) writeDeliveryTransitionResultText(result DeliveryTransitionResult) error {
	switch {
	case result.AckedAt != "":
		_, err := fmt.Fprintf(a.stdout, "delivery_id=%s state=%s acked_at=%s attempt_count=%d\n", result.DeliveryID, result.State, result.AckedAt, result.AttemptCount)
		return err
	case result.VisibleAt != "":
		_, err := fmt.Fprintf(a.stdout, "delivery_id=%s state=%s visible_at=%s attempt_count=%d\n", result.DeliveryID, result.State, result.VisibleAt, result.AttemptCount)
		return err
	default:
		_, err := fmt.Fprintf(a.stdout, "delivery_id=%s state=%s attempt_count=%d\n", result.DeliveryID, result.State, result.AttemptCount)
		return err
	}
}

func (a *App) writeLeaseRenewResultText(result LeaseRenewResult) error {
	_, err := fmt.Fprintf(a.stdout, "delivery_id=%s lease_token=%s lease_expires_at=%s\n", result.DeliveryID, result.LeaseToken, result.LeaseExpiresAt)
	return err
}

func (a *App) writeReadDeliveryText(delivery ReadDelivery) error {
	if delivery.AckedAt != nil {
		if _, err := fmt.Fprintf(
			a.stdout,
			"delivery_id=%s recipient_address=%s state=%s visible_at=%s acked_at=%s content_type=%s subject=%q\n",
			delivery.DeliveryID,
			delivery.RecipientAddress,
			delivery.State,
			delivery.VisibleAt,
			*delivery.AckedAt,
			delivery.ContentType,
			delivery.Subject,
		); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(
			a.stdout,
			"delivery_id=%s recipient_address=%s state=%s visible_at=%s content_type=%s subject=%q\n",
			delivery.DeliveryID,
			delivery.RecipientAddress,
			delivery.State,
			delivery.VisibleAt,
			delivery.ContentType,
			delivery.Subject,
		); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprint(a.stdout, delivery.Body); err != nil {
		return err
	}
	if !strings.HasSuffix(delivery.Body, "\n") {
		if _, err := fmt.Fprintln(a.stdout); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) writeReadMessageText(message ReadMessage) error {
	if _, err := fmt.Fprintf(
		a.stdout,
		"message_id=%s content_type=%s subject=%q\n",
		message.MessageID,
		message.ContentType,
		message.Subject,
	); err != nil {
		return err
	}
	if _, err := fmt.Fprint(a.stdout, message.Body); err != nil {
		return err
	}
	if !strings.HasSuffix(message.Body, "\n") {
		if _, err := fmt.Fprintln(a.stdout); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) writeReadMessageResultText(result readMessageResult) error {
	for index, message := range result.Items {
		if index > 0 {
			if _, err := io.WriteString(a.stdout, "---\n"); err != nil {
				return err
			}
		}
		if err := a.writeReadMessageText(message); err != nil {
			return err
		}
	}
	if result.HasMore {
		if _, err := io.WriteString(a.stdout, "notice=more_items_available\n"); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) writeReadDeliveryResultText(result readDeliveryResult) error {
	for index, delivery := range result.Items {
		if index > 0 {
			if _, err := io.WriteString(a.stdout, "---\n"); err != nil {
				return err
			}
		}
		if err := a.writeReadDeliveryText(delivery); err != nil {
			return err
		}
	}
	if result.HasMore {
		if _, err := io.WriteString(a.stdout, "notice=more_items_available\n"); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) writeListedDeliveryText(delivery ListedDelivery) error {
	if delivery.AckedAt != nil {
		_, err := fmt.Fprintf(
			a.stdout,
			"delivery_id=%s recipient_address=%s state=%s visible_at=%s acked_at=%s subject=%q\n",
			delivery.DeliveryID,
			delivery.RecipientAddress,
			delivery.State,
			delivery.VisibleAt,
			*delivery.AckedAt,
			delivery.Subject,
		)
		return err
	}
	_, err := fmt.Fprintf(
		a.stdout,
		"delivery_id=%s recipient_address=%s state=%s visible_at=%s subject=%q\n",
		delivery.DeliveryID,
		delivery.RecipientAddress,
		delivery.State,
		delivery.VisibleAt,
		delivery.Subject,
	)
	return err
}

func (a *App) writeWaitedDeliveryText(delivery ListedDeliveryCompact) error {
	_, err := fmt.Fprintf(
		a.stdout,
		"delivery_id=%s recipient_address=%s content_type=%s subject=%q\n",
		delivery.DeliveryID,
		delivery.RecipientAddress,
		delivery.ContentType,
		delivery.Subject,
	)
	return err
}

func (a *App) writeGroupListedMessageText(message GroupListedMessage) error {
	_, err := fmt.Fprintf(
		a.stdout,
		"message_id=%s group=%s person=%s read=%t read_count=%d eligible_count=%d created_at=%s subject=%q\n",
		message.MessageID,
		message.GroupAddress,
		message.Person,
		message.Read,
		message.ReadCount,
		message.EligibleCount,
		message.MessageCreatedAt,
		message.Subject,
	)
	return err
}

func (a *App) writeGroupWaitedMessageText(message GroupListedMessageCompact) error {
	_, err := fmt.Fprintf(
		a.stdout,
		"message_id=%s group=%s person=%s read=%t read_count=%d eligible_count=%d content_type=%s subject=%q\n",
		message.MessageID,
		message.GroupAddress,
		message.Person,
		message.Read,
		message.ReadCount,
		message.EligibleCount,
		message.ContentType,
		message.Subject,
	)
	return err
}

func (a *App) newWatchEmitter(format outputFormat) (func(ListedDelivery) error, error) {
	switch format {
	case outputFormatText:
		return a.writeListedDeliveryText, nil
	case outputFormatJSON:
		encoder := json.NewEncoder(a.stdout)
		return func(delivery ListedDelivery) error {
			return encoder.Encode(delivery)
		}, nil
	case outputFormatYAML:
		return func(delivery ListedDelivery) error {
			if _, err := io.WriteString(a.stdout, "---\n"); err != nil {
				return err
			}
			return writeYAML(a.stdout, delivery)
		}, nil
	default:
		return nil, fmt.Errorf("unsupported watch output format %d", format)
	}
}
