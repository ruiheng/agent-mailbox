package mcpserver

import (
	"context"
	"strings"
)

type notificationRoute struct {
	Manager string
	Target  string
}

type notificationEvent struct {
	Kind                 string
	Route                notificationRoute
	Subject              string
	Body                 string
	DisableNotifyMessage *bool
}

type notificationOutcome struct {
	Status string
	Scheme string
	Err    error
}

type notificationProbe struct {
	Status   string
	Scheme   string
	Wakeable bool
}

type managerNotifier interface {
	Name() string
	Probe(ctx context.Context, route notificationRoute) notificationProbe
	Notify(ctx context.Context, event notificationEvent) notificationOutcome
}

type notificationManager struct {
	notifiers map[string]managerNotifier
	sessions  *sessionManager
}

type agentDeckNotifier struct {
	runner   Runner
	sessions *sessionManager
}

func newNotificationManager(runner Runner, sessions *sessionManager) *notificationManager {
	manager := &notificationManager{
		notifiers: map[string]managerNotifier{},
		sessions:  sessions,
	}
	manager.notifiers["agent-deck"] = agentDeckNotifier{
		runner:   runner,
		sessions: sessions,
	}
	return manager
}

func (m *notificationManager) notifyMailboxSend(ctx context.Context, input mailboxSendInput) notificationOutcome {
	if m.sessions.isLocalAddress(ctx, input.ToAddress) {
		return notificationOutcome{Status: "skipped_local"}
	}

	scope, scheme, err := directWakeScopeForAddress(input.ToAddress)
	if err != nil {
		return notificationOutcome{Status: "failed", Err: err}
	}
	if scope == nil {
		return notificationOutcome{Status: "unsupported", Scheme: scheme}
	}

	return m.notifyDirectWakeScope(ctx, *scope, input)
}

func (m *notificationManager) notifyDirectWakeScope(ctx context.Context, scope wakeScope, input mailboxSendInput) notificationOutcome {
	outcome := notificationOutcome{Status: "unsupported"}
	targets := scope.targetsForChannel(WakeChannelAgentDeck)
	if len(targets) == 0 {
		return outcome
	}
	if wakeNotifyDisabled(input.DisableNotifyMessage) {
		return notificationOutcome{
			Status: "skipped_disabled",
			Scheme: "agent-deck",
		}
	}
	for _, target := range targets {
		route := notificationRoute{
			Manager: "agent-deck",
			Target:  target.Target,
		}
		probe := m.probeRoute(ctx, route)
		if !probe.Wakeable {
			outcome = notificationOutcome{
				Status: probe.Status,
				Scheme: probe.Scheme,
			}
			continue
		}

		outcome = m.notifyRoute(ctx, notificationEvent{
			Kind:                 notificationDelivery,
			Route:                route,
			Subject:              input.Subject,
			Body:                 input.Body,
			DisableNotifyMessage: input.DisableNotifyMessage,
		})
		if notificationOutcomeDelivered(outcome) {
			return outcome
		}
	}
	return outcome
}

func (m *notificationManager) notifyRoute(ctx context.Context, event notificationEvent) notificationOutcome {
	notifier, ok := m.notifiers[event.Route.Manager]
	if !ok {
		return notificationOutcome{
			Status: "unsupported",
			Scheme: event.Route.Manager,
		}
	}
	outcome := notifier.Notify(ctx, event)
	if outcome.Scheme == "" {
		outcome.Scheme = notifier.Name()
	}
	return outcome
}

func (m *notificationManager) probeRoute(ctx context.Context, route notificationRoute) notificationProbe {
	notifier, ok := m.notifiers[route.Manager]
	if !ok {
		return notificationProbe{
			Status: "unsupported",
			Scheme: route.Manager,
		}
	}
	probe := notifier.Probe(ctx, route)
	if probe.Scheme == "" {
		probe.Scheme = notifier.Name()
	}
	return probe
}

func (n agentDeckNotifier) Name() string {
	return "agent-deck"
}

func (n agentDeckNotifier) Probe(ctx context.Context, route notificationRoute) notificationProbe {
	targetSession, err := n.sessions.resolveSessionShowBestEffort(ctx, route.Target)
	if err != nil {
		return notificationProbe{
			Status: "failed",
			Scheme: n.Name(),
		}
	}
	if targetSession == nil {
		return notificationProbe{
			Status: "not_found",
			Scheme: n.Name(),
		}
	}

	switch strings.TrimSpace(targetSession.Status) {
	case "waiting", "idle":
		return notificationProbe{
			Status:   "wakeable",
			Scheme:   n.Name(),
			Wakeable: true,
		}
	default:
		return notificationProbe{
			Status: "suppressed_local_activity",
			Scheme: n.Name(),
		}
	}
}

func (n agentDeckNotifier) Notify(ctx context.Context, event notificationEvent) notificationOutcome {
	if event.Kind != notificationDelivery && event.Kind != notificationFallbackWake {
		return notificationOutcome{
			Status: "unsupported",
			Scheme: n.Name(),
		}
	}
	if wakeNotifyDisabled(event.DisableNotifyMessage) {
		return notificationOutcome{
			Status: "skipped_disabled",
			Scheme: n.Name(),
		}
	}
	notifyMessage := resolveWakeNotifyMessage(event.DisableNotifyMessage, defaultNotifyMessage)
	if notifyMessage == "" {
		return notificationOutcome{
			Status: "skipped_disabled",
			Scheme: n.Name(),
		}
	}

	_, err := runCommand(ctx, n.runner, []string{
		"agent-deck", "session", "send", "--no-wait", event.Route.Target, notifyMessage,
	}, runOptions{timeout: syncCmdTimeout})
	if err != nil {
		return notificationOutcome{
			Status: "failed",
			Scheme: n.Name(),
			Err:    err,
		}
	}

	return notificationOutcome{
		Status: "sent",
		Scheme: n.Name(),
	}
}

func notificationOutcomeDelivered(outcome notificationOutcome) bool {
	return strings.TrimSpace(outcome.Status) == "sent"
}

func wakeNotifyDisabled(disableNotifyMessage *bool) bool {
	return disableNotifyMessage != nil && *disableNotifyMessage
}

func resolveWakeNotifyMessage(disableNotifyMessage *bool, defaultMessage string) string {
	if wakeNotifyDisabled(disableNotifyMessage) {
		return ""
	}
	return defaultMessage
}

func ensureCheckAgentMailHint(message, defaultMessage string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return defaultMessage
	}
	if strings.Contains(trimmed, "check-agent-mail") {
		return trimmed
	}
	return trimmed + "\nWhen a wakeup message arrives, use the 'check-agent-mail' skill and execute its requested action."
}

func ensureMailboxRecoveryHint(message string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return mailboxRecoveryHint
	}
	if strings.Contains(trimmed, "mailbox_read") && strings.Contains(trimmed, "acked") {
		return trimmed
	}
	return trimmed + "\n" + mailboxRecoveryHint
}

func ensureReceiverWorkflowHint(message, defaultMessage, targetLabel string) string {
	withWakeHint := ensureCheckAgentMailHint(message, defaultMessage)
	if !isWorkerSessionLabel(targetLabel) {
		return withWakeHint
	}
	return ensureMailboxRecoveryHint(withWakeHint)
}

func isWorkerSessionLabel(label string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(label))
	return trimmed == "coder" || strings.HasPrefix(trimmed, "coder-") ||
		trimmed == "reviewer" || strings.HasPrefix(trimmed, "reviewer-") ||
		trimmed == "architect" || strings.HasPrefix(trimmed, "architect-")
}
