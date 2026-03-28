package mailbox

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

type listedDeliverySummary struct {
	DeliveryID       string `json:"delivery_id"`
	RecipientAddress string `json:"recipient_address"`
	Subject          string `json:"subject"`
	ContentType      string `json:"content_type,omitempty"`
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
