package mailbox

type sendResultSummary struct {
	Mode             string `json:"mode,omitempty"`
	DeliveryID       string `json:"delivery_id,omitempty"`
	MessageID        string `json:"message_id,omitempty"`
	GroupID          string `json:"group_id,omitempty"`
	GroupAddress     string `json:"group_address,omitempty"`
	EligibleCount    *int   `json:"eligible_count,omitempty"`
	MessageCreatedAt string `json:"message_created_at,omitempty"`
}

type sendResultFull struct {
	Mode             string `json:"mode,omitempty"`
	MessageID        string `json:"message_id,omitempty"`
	DeliveryID       string `json:"delivery_id,omitempty"`
	BlobID           string `json:"blob_id,omitempty"`
	GroupID          string `json:"group_id,omitempty"`
	GroupAddress     string `json:"group_address,omitempty"`
	EligibleCount    *int   `json:"eligible_count,omitempty"`
	MessageCreatedAt string `json:"message_created_at,omitempty"`
}

type receivedMessageSummary struct {
	DeliveryID       string `json:"delivery_id"`
	RecipientAddress string `json:"recipient_address"`
	LeaseToken       string `json:"lease_token"`
	Subject          string `json:"subject"`
	ContentType      string `json:"content_type,omitempty"`
	Body             string `json:"body"`
}

type receiveResultSummary struct {
	Messages []receivedMessageSummary `json:"messages"`
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

type listedDeliverySummary struct {
	DeliveryID       string `json:"delivery_id"`
	RecipientAddress string `json:"recipient_address"`
	Subject          string `json:"subject"`
	ContentType      string `json:"content_type,omitempty"`
}

type groupListedMessageSummary struct {
	MessageID        string  `json:"message_id"`
	GroupID          string  `json:"group_id"`
	GroupAddress     string  `json:"group_address"`
	Person           string  `json:"person"`
	MessageCreatedAt string  `json:"message_created_at"`
	Subject          string  `json:"subject"`
	ContentType      string  `json:"content_type,omitempty"`
	Read             bool    `json:"read"`
	FirstReadAt      *string `json:"first_read_at,omitempty"`
	ReadCount        int     `json:"read_count"`
	EligibleCount    int     `json:"eligible_count"`
}

type groupReceivedMessageSummary struct {
	MessageID        string `json:"message_id"`
	GroupID          string `json:"group_id"`
	GroupAddress     string `json:"group_address"`
	Person           string `json:"person"`
	MessageCreatedAt string `json:"message_created_at"`
	Subject          string `json:"subject"`
	ContentType      string `json:"content_type,omitempty"`
	Body             string `json:"body"`
	ReadCount        int    `json:"read_count"`
	EligibleCount    int    `json:"eligible_count"`
	FirstReadAt      string `json:"first_read_at"`
}

func summarizeSendResult(result SendResult) sendResultSummary {
	if result.Mode == SendModeGroup {
		eligibleCount := result.EligibleCount
		return sendResultSummary{
			Mode:             SendModeGroup,
			MessageID:        result.MessageID,
			GroupID:          result.GroupID,
			GroupAddress:     result.GroupAddress,
			EligibleCount:    &eligibleCount,
			MessageCreatedAt: result.MessageCreatedAt,
		}
	}
	return sendResultSummary{
		DeliveryID: result.DeliveryID,
	}
}

func fullSendResult(result SendResult) sendResultFull {
	if result.Mode == SendModeGroup {
		eligibleCount := result.EligibleCount
		return sendResultFull{
			Mode:             SendModeGroup,
			MessageID:        result.MessageID,
			GroupID:          result.GroupID,
			GroupAddress:     result.GroupAddress,
			EligibleCount:    &eligibleCount,
			MessageCreatedAt: result.MessageCreatedAt,
		}
	}
	return sendResultFull{
		MessageID:  result.MessageID,
		DeliveryID: result.DeliveryID,
		BlobID:     result.BodyBlobRef,
	}
}

func summarizeReceivedMessage(message ReceivedMessage) receivedMessageSummary {
	return receivedMessageSummary{
		DeliveryID:       message.DeliveryID,
		RecipientAddress: message.RecipientAddress,
		LeaseToken:       message.LeaseToken,
		Subject:          message.Subject,
		ContentType:      message.ContentType,
		Body:             message.Body,
	}
}

func summarizeReceiveResult(result ReceiveResult) receiveResultSummary {
	messages := make([]receivedMessageSummary, 0, len(result.Messages))
	for _, message := range result.Messages {
		messages = append(messages, summarizeReceivedMessage(message))
	}
	return receiveResultSummary{
		Messages: messages,
		HasMore:  result.HasMore,
	}
}

func summarizeListedDelivery(delivery ListedDelivery) listedDeliverySummary {
	return listedDeliverySummary{
		DeliveryID:       delivery.DeliveryID,
		RecipientAddress: delivery.RecipientAddress,
		Subject:          delivery.Subject,
		ContentType:      delivery.ContentType,
	}
}

func summarizeGroupListedMessage(message GroupListedMessage) groupListedMessageSummary {
	return groupListedMessageSummary{
		MessageID:        message.MessageID,
		GroupID:          message.GroupID,
		GroupAddress:     message.GroupAddress,
		Person:           message.Person,
		MessageCreatedAt: message.MessageCreatedAt,
		Subject:          message.Subject,
		ContentType:      message.ContentType,
		Read:             message.Read,
		FirstReadAt:      message.FirstReadAt,
		ReadCount:        message.ReadCount,
		EligibleCount:    message.EligibleCount,
	}
}

func summarizeGroupReceivedMessage(message GroupReceivedMessage) groupReceivedMessageSummary {
	return groupReceivedMessageSummary{
		MessageID:        message.MessageID,
		GroupID:          message.GroupID,
		GroupAddress:     message.GroupAddress,
		Person:           message.Person,
		MessageCreatedAt: message.MessageCreatedAt,
		Subject:          message.Subject,
		ContentType:      message.ContentType,
		Body:             message.Body,
		ReadCount:        message.ReadCount,
		EligibleCount:    message.EligibleCount,
		FirstReadAt:      message.FirstReadAt,
	}
}
