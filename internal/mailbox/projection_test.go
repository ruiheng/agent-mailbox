package mailbox

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSharedMailboxProjectionShapes(t *testing.T) {
	t.Parallel()

	firstReadAt := "2026-04-03T09:25:00Z"
	forwardedFromAddress := "agent/source"

	testCases := []struct {
		name string
		got  any
		want map[string]any
	}{
		{
			name: "compact personal send result",
			got: CompactSendResult(SendResult{
				DeliveryID:  "dlv_123",
				MessageID:   "msg_hidden",
				BodyBlobRef: "blob_hidden",
			}),
			want: map[string]any{
				"delivery_id": "dlv_123",
			},
		},
		{
			name: "full personal send result",
			got: FullSendResult(SendResult{
				DeliveryID:  "dlv_123",
				MessageID:   "msg_123",
				BodyBlobRef: "blob_123",
			}),
			want: map[string]any{
				"delivery_id": "dlv_123",
				"message_id":  "msg_123",
				"blob_id":     "blob_123",
			},
		},
		{
			name: "compact receive batch",
			got: CompactReceiveResult(ReceiveResult{
				Messages: []ReceivedMessage{{
					DeliveryID:       "dlv_123",
					RecipientAddress: "agent-deck/coder",
					LeaseToken:       "lease_123",
					Subject:          "delegate",
					ContentType:      "text/plain",
					Body:             "body",
				}},
				HasMore: true,
			}),
			want: map[string]any{
				"messages": []any{
					map[string]any{
						"delivery_id":       "dlv_123",
						"recipient_address": "agent-deck/coder",
						"lease_token":       "lease_123",
						"subject":           "delegate",
						"content_type":      "text/plain",
						"body":              "body",
					},
				},
				"has_more": true,
			},
		},
		{
			name: "compact listed delivery",
			got: CompactListedDelivery(ListedDelivery{
				DeliveryID:           "dlv_123",
				RecipientAddress:     "agent-deck/coder",
				ForwardedFromAddress: &forwardedFromAddress,
				Subject:              "delegate",
				ContentType:          "text/plain",
				State:                "queued",
				VisibleAt:            "2026-04-03T09:21:00Z",
			}),
			want: map[string]any{
				"delivery_id":            "dlv_123",
				"recipient_address":      "agent-deck/coder",
				"forwarded_from_address": "agent/source",
				"subject":                "delegate",
				"content_type":           "text/plain",
			},
		},
		{
			name: "compact group listed message",
			got: CompactGroupListedMessage(GroupListedMessage{
				MessageID:            "msg_group",
				ForwardedFromAddress: &forwardedFromAddress,
				GroupID:              "grp_123",
				GroupAddress:         "group/reviewers",
				Person:               "alice",
				MessageCreatedAt:     "2026-04-03T09:20:00Z",
				Subject:              "review",
				ContentType:          "text/plain",
				Read:                 true,
				FirstReadAt:          &firstReadAt,
				ReadCount:            2,
				EligibleCount:        3,
			}),
			want: map[string]any{
				"message_id":             "msg_group",
				"forwarded_from_address": "agent/source",
				"group_id":               "grp_123",
				"group_address":          "group/reviewers",
				"person":                 "alice",
				"message_created_at":     "2026-04-03T09:20:00Z",
				"subject":                "review",
				"content_type":           "text/plain",
				"read":                   true,
				"first_read_at":          firstReadAt,
				"read_count":             float64(2),
				"eligible_count":         float64(3),
			},
		},
		{
			name: "compact group received message",
			got: CompactGroupReceivedMessage(GroupReceivedMessage{
				MessageID:            "msg_group",
				ForwardedFromAddress: &forwardedFromAddress,
				GroupID:              "grp_123",
				GroupAddress:         "group/reviewers",
				Person:               "alice",
				MessageCreatedAt:     "2026-04-03T09:20:00Z",
				Subject:              "review",
				ContentType:          "text/plain",
				Body:                 "body",
				ReadCount:            2,
				EligibleCount:        3,
				FirstReadAt:          firstReadAt,
			}),
			want: map[string]any{
				"message_id":             "msg_group",
				"forwarded_from_address": "agent/source",
				"group_id":               "grp_123",
				"group_address":          "group/reviewers",
				"person":                 "alice",
				"message_created_at":     "2026-04-03T09:20:00Z",
				"subject":                "review",
				"content_type":           "text/plain",
				"body":                   "body",
				"read_count":             float64(2),
				"eligible_count":         float64(3),
				"first_read_at":          firstReadAt,
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := jsonMap(t, tc.got)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("projection = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func jsonMap(t *testing.T, value any) map[string]any {
	t.Helper()

	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	return out
}
