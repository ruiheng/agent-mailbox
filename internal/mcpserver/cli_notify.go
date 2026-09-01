package mcpserver

import (
	"context"
	"strings"

	"github.com/ruiheng/waypost/internal/waypost"
)

// NotifyWaypostSend performs the same best-effort post-send wakeup used by the
// MCP waypost_send tool. The durable send must already have completed.
func NotifyWaypostSend(ctx context.Context, store *waypost.Store, request waypost.SendNotificationRequest) waypost.SendNotificationOutcome {
	return notifyWaypostSendWithOptions(ctx, store, request, Options{})
}

func notifyWaypostSendWithOptions(ctx context.Context, service any, request waypost.SendNotificationRequest, options Options) waypost.SendNotificationOutcome {
	notifier := NewService(options)
	defer notifier.Close()

	// A standalone CLI invocation has no persistent MCP binding state. Treat an
	// explicit sender as local and suppress auto-binding probes during notify.
	notifier.state.autoBindAttempted = true
	if fromAddress := strings.TrimSpace(request.Params.FromAddress); fromAddress != "" {
		notifier.state.boundAddresses = []string{fromAddress}
	}

	outcome := notifier.notifyWaypostSend(ctx, waypostSendInput{
		ToAddress:   request.Params.ToAddress,
		FromAddress: request.Params.FromAddress,
		AsPerson:    request.Params.AsPerson,
		Subject:     request.Params.Subject,
		Body:        string(request.Params.Body),
		ContentType: request.Params.ContentType,
		Group:       request.Params.Group,
	}, request.Result, service)

	return waypost.SendNotificationOutcome{
		Status: outcome.Status,
		Scheme: outcome.Scheme,
		Detail: outcome.Detail,
		Err:    outcome.Err,
	}
}
