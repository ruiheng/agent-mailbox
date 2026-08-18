package waypost

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

type SendResultCompactWithNotification struct {
	Mode             string  `json:"mode,omitempty"`
	DeliveryID       string  `json:"delivery_id,omitempty"`
	MessageID        string  `json:"message_id,omitempty"`
	GroupID          string  `json:"group_id,omitempty"`
	GroupAddress     string  `json:"group_address,omitempty"`
	EligibleCount    *int    `json:"eligible_count,omitempty"`
	MessageCreatedAt string  `json:"message_created_at,omitempty"`
	NotifyStatus     string  `json:"notify_status"`
	NotifyScheme     *string `json:"notify_scheme"`
	NotifyError      *string `json:"notify_error"`
}

type SendResultFullWithNotification struct {
	Mode             string  `json:"mode,omitempty"`
	MessageID        string  `json:"message_id,omitempty"`
	DeliveryID       string  `json:"delivery_id,omitempty"`
	BlobID           string  `json:"blob_id,omitempty"`
	GroupID          string  `json:"group_id,omitempty"`
	GroupAddress     string  `json:"group_address,omitempty"`
	EligibleCount    *int    `json:"eligible_count,omitempty"`
	MessageCreatedAt string  `json:"message_created_at,omitempty"`
	NotifyStatus     string  `json:"notify_status"`
	NotifyScheme     *string `json:"notify_scheme"`
	NotifyError      *string `json:"notify_error"`
}

// SendBatchOutput is the opt-in, multi-recipient CLI result envelope. Single
// recipient sends continue to use the legacy result types above.
type SendBatchOutput struct {
	Status         string                `json:"status"`
	ToAddresses    []string              `json:"to_addresses"`
	RecipientCount int                   `json:"recipient_count"`
	SentCount      int                   `json:"sent_count"`
	FailedCount    int                   `json:"failed_count"`
	Results        []SendBatchItemOutput `json:"results"`
}

// SendBatchItemOutput is one CLI batch item's compact or full projection.
type SendBatchItemOutput struct {
	ToAddress        string  `json:"to_address"`
	Status           string  `json:"status"`
	Mode             string  `json:"mode,omitempty"`
	MessageID        string  `json:"message_id,omitempty"`
	DeliveryID       string  `json:"delivery_id,omitempty"`
	BlobID           string  `json:"blob_id,omitempty"`
	GroupID          string  `json:"group_id,omitempty"`
	GroupAddress     string  `json:"group_address,omitempty"`
	EligibleCount    *int    `json:"eligible_count,omitempty"`
	MessageCreatedAt string  `json:"message_created_at,omitempty"`
	NotifyStatus     *string `json:"notify_status,omitempty"`
	NotifyScheme     *string `json:"notify_scheme,omitempty"`
	NotifyError      *string `json:"notify_error,omitempty"`
	Error            string  `json:"error,omitempty"`
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
	SenderAddress        *string `json:"sender_address,omitempty"`
	Subject              string  `json:"subject"`
	ContentType          string  `json:"content_type,omitempty"`
	Body                 string  `json:"body"`
}

type ReceiveResultCompact struct {
	Messages         []ReceivedMessageCompact `json:"messages"`
	RemainingByState map[string]int           `json:"remaining_by_state,omitempty"`
}

type personalReceiveOutput struct {
	Status           string                   `json:"status"`
	Addresses        []string                 `json:"addresses"`
	Delivery         *ReceivedMessageCompact  `json:"delivery,omitempty"`
	Deliveries       []ReceivedMessageCompact `json:"deliveries,omitempty"`
	RemainingByState map[string]int           `json:"remaining_by_state,omitempty"`
}

type groupReceiveOutput struct {
	Status    string                       `json:"status"`
	Addresses []string                     `json:"addresses"`
	AsPerson  string                       `json:"as_person"`
	Message   *GroupReceivedMessageCompact `json:"message,omitempty"`
}

type readMessageResult struct {
	Items []ReadMessage `json:"items"`
}

type readDeliveryResult struct {
	Items      []ReadDelivery `json:"items"`
	HasMore    bool           `json:"has_more,omitempty"`
	NextCursor string         `json:"next_cursor,omitempty"`
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
	SenderAddress        *string `json:"sender_address,omitempty"`
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
	SenderAddress        *string `json:"sender_address,omitempty"`
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

func notificationOutputFields(outcome SendNotificationOutcome) (string, *string, *string) {
	status := outcome.Status
	if status == "" {
		status = "unknown"
	}
	var scheme *string
	if outcome.Scheme != "" {
		value := outcome.Scheme
		scheme = &value
	}
	var notifyError *string
	if outcome.Err != nil {
		value := outcome.Err.Error()
		notifyError = &value
	}
	return status, scheme, notifyError
}

func CompactSendResultWithNotification(result SendResult, outcome SendNotificationOutcome) SendResultCompactWithNotification {
	compact := CompactSendResult(result)
	status, scheme, notifyError := notificationOutputFields(outcome)
	return SendResultCompactWithNotification{
		Mode:             compact.Mode,
		DeliveryID:       compact.DeliveryID,
		MessageID:        compact.MessageID,
		GroupID:          compact.GroupID,
		GroupAddress:     compact.GroupAddress,
		EligibleCount:    compact.EligibleCount,
		MessageCreatedAt: compact.MessageCreatedAt,
		NotifyStatus:     status,
		NotifyScheme:     scheme,
		NotifyError:      notifyError,
	}
}

func FullSendResultWithNotification(result SendResult, outcome SendNotificationOutcome) SendResultFullWithNotification {
	full := FullSendResult(result)
	status, scheme, notifyError := notificationOutputFields(outcome)
	return SendResultFullWithNotification{
		Mode:             full.Mode,
		MessageID:        full.MessageID,
		DeliveryID:       full.DeliveryID,
		BlobID:           full.BlobID,
		GroupID:          full.GroupID,
		GroupAddress:     full.GroupAddress,
		EligibleCount:    full.EligibleCount,
		MessageCreatedAt: full.MessageCreatedAt,
		NotifyStatus:     status,
		NotifyScheme:     scheme,
		NotifyError:      notifyError,
	}
}

// SendBatchCLIOutput projects a completed shared batch for the CLI. The full
// and notification switches affect only the new batch contract; they do not
// alter legacy single-recipient projections.
func SendBatchCLIOutput(result SendBatchResult, full, includeNotification bool) SendBatchOutput {
	output := SendBatchOutput{
		Status:         result.Status(),
		ToAddresses:    append([]string(nil), result.ToAddresses...),
		RecipientCount: len(result.ToAddresses),
		SentCount:      result.SentCount,
		FailedCount:    result.FailedCount,
		Results:        make([]SendBatchItemOutput, 0, len(result.Items)),
	}
	for _, item := range result.Items {
		projected := SendBatchItemOutput{
			ToAddress: item.ToAddress,
			Status:    "sent",
		}
		if item.Err != nil {
			projected.Status = "failed"
			projected.Error = item.Err.Error()
			if includeNotification {
				notAttempted := "not_attempted"
				projected.NotifyStatus = &notAttempted
			}
			output.Results = append(output.Results, projected)
			continue
		}

		if full {
			receipt := FullSendResult(item.Result)
			projected.Mode = receipt.Mode
			projected.MessageID = receipt.MessageID
			projected.DeliveryID = receipt.DeliveryID
			projected.BlobID = receipt.BlobID
			projected.GroupID = receipt.GroupID
			projected.GroupAddress = receipt.GroupAddress
			projected.EligibleCount = receipt.EligibleCount
			projected.MessageCreatedAt = receipt.MessageCreatedAt
		} else {
			receipt := CompactSendResult(item.Result)
			projected.Mode = receipt.Mode
			projected.MessageID = receipt.MessageID
			projected.DeliveryID = receipt.DeliveryID
			projected.GroupID = receipt.GroupID
			projected.GroupAddress = receipt.GroupAddress
			projected.EligibleCount = receipt.EligibleCount
			projected.MessageCreatedAt = receipt.MessageCreatedAt
		}
		if includeNotification {
			if item.Notification == nil {
				unknown := "unknown"
				projected.NotifyStatus = &unknown
			} else {
				status, scheme, notifyError := notificationOutputFields(*item.Notification)
				projected.NotifyStatus = &status
				projected.NotifyScheme = scheme
				projected.NotifyError = notifyError
			}
		}
		output.Results = append(output.Results, projected)
	}
	return output
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
		SenderAddress:        message.SenderAddress,
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
		Messages:         messages,
		RemainingByState: result.RemainingByState,
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
		SenderAddress:        message.SenderAddress,
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
		SenderAddress:        message.SenderAddress,
		MessageCreatedAt:     message.MessageCreatedAt,
		Subject:              message.Subject,
		ContentType:          message.ContentType,
		Body:                 message.Body,
		ReadCount:            message.ReadCount,
		EligibleCount:        message.EligibleCount,
		FirstReadAt:          message.FirstReadAt,
	}
}
