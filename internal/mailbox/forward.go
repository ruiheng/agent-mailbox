package mailbox

import (
	"context"
	"fmt"
	"strings"
)

type ForwardParams struct {
	MessageID   string
	DeliveryID  string
	ToAddress   string
	FromAddress string
	Subject     string
	Group       bool
}

type PreparedForward struct {
	SendParams       SendParams
	SourceMessageID  string
	SourceDeliveryID string
}

type ForwardResult struct {
	SendResult
	SourceMessageID  string `json:"source_message_id"`
	SourceDeliveryID string `json:"source_delivery_id,omitempty"`
}

type ForwardSourceReader interface {
	ReadMessages(context.Context, []string) ([]ReadMessage, error)
	ReadDeliveries(context.Context, []string) ([]ReadDelivery, error)
}

func PrepareForward(ctx context.Context, reader ForwardSourceReader, label string, params ForwardParams) (PreparedForward, error) {
	hasMessageID := strings.TrimSpace(params.MessageID) != ""
	hasDeliveryID := strings.TrimSpace(params.DeliveryID) != ""
	if hasMessageID == hasDeliveryID {
		return PreparedForward{}, fmt.Errorf("%s requires exactly one of message_id or delivery_id", forwardLabel(label))
	}

	prepared := PreparedForward{
		SendParams: SendParams{
			Group: params.Group,
		},
	}
	var err error
	prepared.SendParams.ToAddress, err = NormalizeAddress(params.ToAddress)
	if err != nil {
		return PreparedForward{}, err
	}
	prepared.SendParams.FromAddress, err = NormalizeOptionalAddress(params.FromAddress)
	if err != nil {
		return PreparedForward{}, err
	}

	switch {
	case hasMessageID:
		messageID := strings.TrimSpace(params.MessageID)
		messages, err := reader.ReadMessages(ctx, []string{messageID})
		if err != nil {
			return PreparedForward{}, err
		}
		if len(messages) != 1 {
			return PreparedForward{}, fmt.Errorf("%s source message not found: %s", forwardLabel(label), messageID)
		}
		source := messages[0]
		prepared.SendParams.Subject = buildForwardSubject(source.Subject, params.Subject)
		prepared.SendParams.ContentType = source.ContentType
		prepared.SendParams.SchemaVersion = source.SchemaVersion
		prepared.SendParams.ForwardedMessageID = source.MessageID
		prepared.SendParams.ForwardedFromAddress = forwardedFromAddress(source.ForwardedFromAddress, source.SenderAddress)
		prepared.SendParams.Body = []byte(source.Body)
		prepared.SourceMessageID = source.MessageID
	case hasDeliveryID:
		deliveryID := strings.TrimSpace(params.DeliveryID)
		deliveries, err := reader.ReadDeliveries(ctx, []string{deliveryID})
		if err != nil {
			return PreparedForward{}, err
		}
		if len(deliveries) != 1 {
			return PreparedForward{}, fmt.Errorf("%s source delivery not found: %s", forwardLabel(label), deliveryID)
		}
		source := deliveries[0]
		prepared.SendParams.Subject = buildForwardSubject(source.Subject, params.Subject)
		prepared.SendParams.ContentType = source.ContentType
		prepared.SendParams.SchemaVersion = source.SchemaVersion
		prepared.SendParams.ForwardedMessageID = source.MessageID
		prepared.SendParams.ForwardedFromAddress = forwardedFromAddress(source.ForwardedFromAddress, source.SenderAddress)
		prepared.SendParams.Body = []byte(source.Body)
		prepared.SourceMessageID = source.MessageID
		prepared.SourceDeliveryID = source.DeliveryID
	}

	return prepared, nil
}

func (o *Operations) Forward(ctx context.Context, params ForwardParams) (ForwardResult, error) {
	prepared, err := PrepareForward(ctx, o, "forward", params)
	if err != nil {
		return ForwardResult{}, err
	}
	sendResult, err := o.store.Send(ctx, prepared.SendParams)
	if err != nil {
		return ForwardResult{}, err
	}
	return ForwardResult{
		SendResult:       sendResult,
		SourceMessageID:  prepared.SourceMessageID,
		SourceDeliveryID: prepared.SourceDeliveryID,
	}, nil
}

func forwardedFromAddress(existing, sender *string) string {
	if existing != nil && strings.TrimSpace(*existing) != "" {
		return strings.TrimSpace(*existing)
	}
	if sender != nil {
		return strings.TrimSpace(*sender)
	}
	return ""
}

func buildForwardSubject(original, override string) string {
	if trimmed := strings.TrimSpace(override); trimmed != "" {
		return trimmed
	}
	base := strings.TrimSpace(original)
	if base == "" {
		return "Fwd"
	}
	if strings.HasPrefix(strings.ToLower(base), "fwd:") {
		return base
	}
	return "Fwd: " + base
}

func forwardLabel(label string) string {
	trimmed := strings.TrimSpace(label)
	if trimmed == "" {
		return "forward"
	}
	return trimmed
}
