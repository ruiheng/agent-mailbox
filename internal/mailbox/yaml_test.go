package mailbox

import (
	"bytes"
	"testing"
)

func TestWriteYAMLFormatsReceivedMessage(t *testing.T) {
	t.Parallel()

	senderID := "ep_sender"
	message := ReceivedMessage{
		DeliveryID:          "dlv_123",
		MessageID:           "msg_123",
		RecipientAddress:    "workflow/reviewer/task-123",
		RecipientEndpointID: "ep_recipient",
		SenderEndpointID:    &senderID,
		State:               "leased",
		VisibleAt:           "2026-03-20T21:53:02Z",
		LeaseToken:          "lease_123",
		LeaseExpiresAt:      "2026-03-20T21:58:02Z",
		AttemptCount:        0,
		MessageCreatedAt:    "2026-03-20T21:53:01Z",
		Subject:             "review request",
		ContentType:         "text/plain",
		SchemaVersion:       "v1",
		BodyBlobRef:         "blob_123",
		BodySize:            6,
		BodySHA256:          "abc123",
		Body:                "hello\n",
	}

	var stdout bytes.Buffer
	if err := writeYAML(&stdout, message); err != nil {
		t.Fatalf("writeYAML() error = %v", err)
	}

	want := "" +
		"delivery_id: \"dlv_123\"\n" +
		"message_id: \"msg_123\"\n" +
		"recipient_address: \"workflow/reviewer/task-123\"\n" +
		"recipient_endpoint_id: \"ep_recipient\"\n" +
		"sender_endpoint_id: \"ep_sender\"\n" +
		"state: \"leased\"\n" +
		"visible_at: \"2026-03-20T21:53:02Z\"\n" +
		"lease_token: \"lease_123\"\n" +
		"lease_expires_at: \"2026-03-20T21:58:02Z\"\n" +
		"attempt_count: 0\n" +
		"message_created_at: \"2026-03-20T21:53:01Z\"\n" +
		"subject: \"review request\"\n" +
		"content_type: \"text/plain\"\n" +
		"schema_version: \"v1\"\n" +
		"body_blob_ref: \"blob_123\"\n" +
		"body_size: 6\n" +
		"body_sha256: \"abc123\"\n" +
		"body: \"hello\\n\"\n"
	if stdout.String() != want {
		t.Fatalf("writeYAML(received message) = %q, want %q", stdout.String(), want)
	}
}

func TestWriteYAMLFormatsDeliveryLists(t *testing.T) {
	t.Parallel()

	deliveries := []ListedDelivery{
		{
			DeliveryID:          "dlv_123",
			MessageID:           "msg_123",
			RecipientAddress:    "workflow/reviewer/task-123",
			RecipientEndpointID: "ep_recipient",
			State:               "queued",
			VisibleAt:           "2026-03-20T21:53:02Z",
			MessageCreatedAt:    "2026-03-20T21:53:01Z",
			Subject:             "review request",
			ContentType:         "text/plain",
			SchemaVersion:       "v1",
			BodyBlobRef:         "blob_123",
			BodySize:            6,
			BodySHA256:          "abc123",
		},
	}

	var stdout bytes.Buffer
	if err := writeYAML(&stdout, deliveries); err != nil {
		t.Fatalf("writeYAML() error = %v", err)
	}

	want := "" +
		"-\n" +
		"  delivery_id: \"dlv_123\"\n" +
		"  message_id: \"msg_123\"\n" +
		"  recipient_address: \"workflow/reviewer/task-123\"\n" +
		"  recipient_endpoint_id: \"ep_recipient\"\n" +
		"  state: \"queued\"\n" +
		"  visible_at: \"2026-03-20T21:53:02Z\"\n" +
		"  message_created_at: \"2026-03-20T21:53:01Z\"\n" +
		"  subject: \"review request\"\n" +
		"  content_type: \"text/plain\"\n" +
		"  schema_version: \"v1\"\n" +
		"  body_blob_ref: \"blob_123\"\n" +
		"  body_size: 6\n" +
		"  body_sha256: \"abc123\"\n"
	if stdout.String() != want {
		t.Fatalf("writeYAML(deliveries) = %q, want %q", stdout.String(), want)
	}
}

func TestWriteYAMLFormatsReceiveResult(t *testing.T) {
	t.Parallel()

	result := ReceiveResult{
		Messages: []ReceivedMessage{
			{
				DeliveryID:          "dlv_123",
				MessageID:           "msg_123",
				RecipientAddress:    "workflow/reviewer/task-123",
				RecipientEndpointID: "ep_recipient",
				State:               "leased",
				VisibleAt:           "2026-03-20T21:53:02Z",
				LeaseToken:          "lease_123",
				LeaseExpiresAt:      "2026-03-20T21:58:02Z",
				AttemptCount:        0,
				MessageCreatedAt:    "2026-03-20T21:53:01Z",
				Subject:             "review request",
				ContentType:         "text/plain",
				SchemaVersion:       "v1",
				BodyBlobRef:         "blob_123",
				BodySize:            6,
				BodySHA256:          "abc123",
				Body:                "hello\n",
			},
		},
		HasMore: true,
	}

	var stdout bytes.Buffer
	if err := writeYAML(&stdout, result); err != nil {
		t.Fatalf("writeYAML() error = %v", err)
	}

	want := "" +
		"messages:\n" +
		"  -\n" +
		"    delivery_id: \"dlv_123\"\n" +
		"    message_id: \"msg_123\"\n" +
		"    recipient_address: \"workflow/reviewer/task-123\"\n" +
		"    recipient_endpoint_id: \"ep_recipient\"\n" +
		"    state: \"leased\"\n" +
		"    visible_at: \"2026-03-20T21:53:02Z\"\n" +
		"    lease_token: \"lease_123\"\n" +
		"    lease_expires_at: \"2026-03-20T21:58:02Z\"\n" +
		"    attempt_count: 0\n" +
		"    message_created_at: \"2026-03-20T21:53:01Z\"\n" +
		"    subject: \"review request\"\n" +
		"    content_type: \"text/plain\"\n" +
		"    schema_version: \"v1\"\n" +
		"    body_blob_ref: \"blob_123\"\n" +
		"    body_size: 6\n" +
		"    body_sha256: \"abc123\"\n" +
		"    body: \"hello\\n\"\n" +
		"has_more: true\n"
	if stdout.String() != want {
		t.Fatalf("writeYAML(receive result) = %q, want %q", stdout.String(), want)
	}
}
