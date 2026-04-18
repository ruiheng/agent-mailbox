package mailbox

type SendResultCompact struct {
	Mode             string `json:"mode,omitempty"`
	DeliveryID       string `json:"delivery_id,omitempty"`
	MessageID        string `json:"message_id,omitempty"`
	GroupID          string `json:"group_id,omitempty"`
	GroupAddress     string `json:"group_address,omitempty"`
	EligibleCount    *int   `json:"eligible_count,omitempty"`
	MessageCreatedAt string `json:"message_created_at,omitempty"`
}

type SendResultFull struct {
	Mode             string `json:"mode,omitempty"`
	MessageID        string `json:"message_id,omitempty"`
	DeliveryID       string `json:"delivery_id,omitempty"`
	BlobID           string `json:"blob_id,omitempty"`
	GroupID          string `json:"group_id,omitempty"`
	GroupAddress     string `json:"group_address,omitempty"`
	EligibleCount    *int   `json:"eligible_count,omitempty"`
	MessageCreatedAt string `json:"message_created_at,omitempty"`
}

type ForwardResultCompact struct {
	Mode             string `json:"mode,omitempty"`
	DeliveryID       string `json:"delivery_id,omitempty"`
	MessageID        string `json:"message_id,omitempty"`
	GroupID          string `json:"group_id,omitempty"`
	GroupAddress     string `json:"group_address,omitempty"`
	EligibleCount    *int   `json:"eligible_count,omitempty"`
	MessageCreatedAt string `json:"message_created_at,omitempty"`
	SourceMessageID  string `json:"source_message_id"`
	SourceDeliveryID string `json:"source_delivery_id,omitempty"`
}

type ForwardResultFull struct {
	Mode             string `json:"mode,omitempty"`
	MessageID        string `json:"message_id,omitempty"`
	DeliveryID       string `json:"delivery_id,omitempty"`
	BlobID           string `json:"blob_id,omitempty"`
	GroupID          string `json:"group_id,omitempty"`
	GroupAddress     string `json:"group_address,omitempty"`
	EligibleCount    *int   `json:"eligible_count,omitempty"`
	MessageCreatedAt string `json:"message_created_at,omitempty"`
	SourceMessageID  string `json:"source_message_id"`
	SourceDeliveryID string `json:"source_delivery_id,omitempty"`
}

type ReceivedMessageCompact struct {
	DeliveryID           string  `json:"delivery_id"`
	RecipientAddress     string  `json:"recipient_address"`
	LeaseToken           string  `json:"lease_token"`
	ForwardedFromAddress *string `json:"forwarded_from_address,omitempty"`
	Subject              string  `json:"subject"`
	ContentType          string  `json:"content_type,omitempty"`
	Body                 string  `json:"body"`
}

type ReceiveResultCompact struct {
	Messages []ReceivedMessageCompact `json:"messages"`
	HasMore  bool                     `json:"has_more"`
}

type readMessageResult struct {
	Items   []ReadMessage `json:"items"`
	HasMore bool          `json:"has_more"`
}

type readDeliveryResult struct {
	Items   []ReadDelivery `json:"items"`
	HasMore bool           `json:"has_more"`
}

type ListedDeliveryCompact struct {
	DeliveryID           string  `json:"delivery_id"`
	RecipientAddress     string  `json:"recipient_address"`
	ForwardedFromAddress *string `json:"forwarded_from_address,omitempty"`
	Subject              string  `json:"subject"`
	ContentType          string  `json:"content_type,omitempty"`
}

type GroupListedMessageCompact struct {
	MessageID            string  `json:"message_id"`
	ForwardedFromAddress *string `json:"forwarded_from_address,omitempty"`
	GroupID              string  `json:"group_id"`
	GroupAddress         string  `json:"group_address"`
	Person               string  `json:"person"`
	MessageCreatedAt     string  `json:"message_created_at"`
	Subject              string  `json:"subject"`
	ContentType          string  `json:"content_type,omitempty"`
	Read                 bool    `json:"read"`
	FirstReadAt          *string `json:"first_read_at,omitempty"`
	ReadCount            int     `json:"read_count"`
	EligibleCount        int     `json:"eligible_count"`
}

type GroupReceivedMessageCompact struct {
	MessageID            string  `json:"message_id"`
	ForwardedFromAddress *string `json:"forwarded_from_address,omitempty"`
	GroupID              string  `json:"group_id"`
	GroupAddress         string  `json:"group_address"`
	Person               string  `json:"person"`
	MessageCreatedAt     string  `json:"message_created_at"`
	Subject              string  `json:"subject"`
	ContentType          string  `json:"content_type,omitempty"`
	Body                 string  `json:"body"`
	ReadCount            int     `json:"read_count"`
	EligibleCount        int     `json:"eligible_count"`
	FirstReadAt          string  `json:"first_read_at"`
}

func CompactSendResult(result SendResult) SendResultCompact {
	if result.Mode == SendModeGroup {
		eligibleCount := result.EligibleCount
		return SendResultCompact{
			Mode:             SendModeGroup,
			MessageID:        result.MessageID,
			GroupID:          result.GroupID,
			GroupAddress:     result.GroupAddress,
			EligibleCount:    &eligibleCount,
			MessageCreatedAt: result.MessageCreatedAt,
		}
	}
	return SendResultCompact{
		DeliveryID: result.DeliveryID,
	}
}

func FullSendResult(result SendResult) SendResultFull {
	if result.Mode == SendModeGroup {
		eligibleCount := result.EligibleCount
		return SendResultFull{
			Mode:             SendModeGroup,
			MessageID:        result.MessageID,
			GroupID:          result.GroupID,
			GroupAddress:     result.GroupAddress,
			EligibleCount:    &eligibleCount,
			MessageCreatedAt: result.MessageCreatedAt,
		}
	}
	return SendResultFull{
		MessageID:  result.MessageID,
		DeliveryID: result.DeliveryID,
		BlobID:     result.BodyBlobRef,
	}
}

func CompactForwardResult(result ForwardResult) ForwardResultCompact {
	send := CompactSendResult(result.SendResult)
	return ForwardResultCompact{
		Mode:             send.Mode,
		DeliveryID:       send.DeliveryID,
		MessageID:        send.MessageID,
		GroupID:          send.GroupID,
		GroupAddress:     send.GroupAddress,
		EligibleCount:    send.EligibleCount,
		MessageCreatedAt: send.MessageCreatedAt,
		SourceMessageID:  result.SourceMessageID,
		SourceDeliveryID: result.SourceDeliveryID,
	}
}

func FullForwardResult(result ForwardResult) ForwardResultFull {
	send := FullSendResult(result.SendResult)
	return ForwardResultFull{
		Mode:             send.Mode,
		MessageID:        send.MessageID,
		DeliveryID:       send.DeliveryID,
		BlobID:           send.BlobID,
		GroupID:          send.GroupID,
		GroupAddress:     send.GroupAddress,
		EligibleCount:    send.EligibleCount,
		MessageCreatedAt: send.MessageCreatedAt,
		SourceMessageID:  result.SourceMessageID,
		SourceDeliveryID: result.SourceDeliveryID,
	}
}

func CompactReceivedMessage(message ReceivedMessage) ReceivedMessageCompact {
	return ReceivedMessageCompact{
		DeliveryID:           message.DeliveryID,
		RecipientAddress:     message.RecipientAddress,
		LeaseToken:           message.LeaseToken,
		ForwardedFromAddress: message.ForwardedFromAddress,
		Subject:              message.Subject,
		ContentType:          message.ContentType,
		Body:                 message.Body,
	}
}

func CompactReceiveResult(result ReceiveResult) ReceiveResultCompact {
	messages := make([]ReceivedMessageCompact, 0, len(result.Messages))
	for _, message := range result.Messages {
		messages = append(messages, CompactReceivedMessage(message))
	}
	return ReceiveResultCompact{
		Messages: messages,
		HasMore:  result.HasMore,
	}
}

func CompactListedDelivery(delivery ListedDelivery) ListedDeliveryCompact {
	return ListedDeliveryCompact{
		DeliveryID:           delivery.DeliveryID,
		RecipientAddress:     delivery.RecipientAddress,
		ForwardedFromAddress: delivery.ForwardedFromAddress,
		Subject:              delivery.Subject,
		ContentType:          delivery.ContentType,
	}
}

func CompactGroupListedMessage(message GroupListedMessage) GroupListedMessageCompact {
	return GroupListedMessageCompact{
		MessageID:            message.MessageID,
		ForwardedFromAddress: message.ForwardedFromAddress,
		GroupID:              message.GroupID,
		GroupAddress:         message.GroupAddress,
		Person:               message.Person,
		MessageCreatedAt:     message.MessageCreatedAt,
		Subject:              message.Subject,
		ContentType:          message.ContentType,
		Read:                 message.Read,
		FirstReadAt:          message.FirstReadAt,
		ReadCount:            message.ReadCount,
		EligibleCount:        message.EligibleCount,
	}
}

func CompactGroupReceivedMessage(message GroupReceivedMessage) GroupReceivedMessageCompact {
	return GroupReceivedMessageCompact{
		MessageID:            message.MessageID,
		ForwardedFromAddress: message.ForwardedFromAddress,
		GroupID:              message.GroupID,
		GroupAddress:         message.GroupAddress,
		Person:               message.Person,
		MessageCreatedAt:     message.MessageCreatedAt,
		Subject:              message.Subject,
		ContentType:          message.ContentType,
		Body:                 message.Body,
		ReadCount:            message.ReadCount,
		EligibleCount:        message.EligibleCount,
		FirstReadAt:          message.FirstReadAt,
	}
}
