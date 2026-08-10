package mcpserver

import (
	"context"
	"errors"
	"strings"
	"time"
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
	Err      error
}

type managerNotifier interface {
	Name() string
	Probe(ctx context.Context, route notificationRoute) notificationProbe
	Notify(ctx context.Context, event notificationEvent) notificationOutcome
}

type notificationManager struct {
	notifiers map[string]managerNotifier
	sessions  *sessionManager
	retryWait func(context.Context, time.Duration) error
}

type agentDeckNotifier struct {
	runner   Runner
	sessions *sessionManager
}

type thurboxNotifier struct {
	runner Runner
}

func newNotificationManager(runner Runner, sessions *sessionManager) *notificationManager {
	manager := &notificationManager{
		notifiers: map[string]managerNotifier{},
		sessions:  sessions,
		retryWait: waitForNotificationRetry,
	}
	manager.notifiers["agent-deck"] = agentDeckNotifier{
		runner:   runner,
		sessions: sessions,
	}
	manager.notifiers["thurbox"] = thurboxNotifier{runner: runner}
	return manager
}

func (m *notificationManager) notifyWaypostSend(ctx context.Context, input waypostSendInput) notificationOutcome {
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

func (m *notificationManager) notifyGroupSubscribers(ctx context.Context, input waypostSendInput, notifyAddresses []string) notificationOutcome {
	if len(notifyAddresses) == 0 {
		return notificationOutcome{Status: "no_subscribers"}
	}
	if wakeNotifyDisabled(input.DisableNotifyMessage) {
		return notificationOutcome{
			Status: "skipped_disabled",
			Scheme: notificationSchemeForAddresses(notifyAddresses),
		}
	}

	attemptedCount := 0
	sentCount := 0
	schemes := map[string]bool{}
	statuses := map[string]bool{}
	var failures []error
	for _, notifyAddress := range notifyAddresses {
		if strings.TrimSpace(notifyAddress) == strings.TrimSpace(input.FromAddress) {
			continue
		}
		attemptedCount++
		scope, scheme, err := directWakeScopeForAddress(notifyAddress)
		if err != nil {
			statuses["failed"] = true
			failures = append(failures, err)
			continue
		}
		if scheme != "" {
			schemes[scheme] = true
		}
		if scope == nil {
			statuses["unsupported"] = true
			continue
		}
		candidate := m.notifyDirectWakeScope(ctx, *scope, input)
		if candidate.Scheme != "" {
			schemes[candidate.Scheme] = true
		}
		if status := strings.TrimSpace(candidate.Status); status != "" {
			statuses[status] = true
		}
		if notificationOutcomeDelivered(candidate) {
			sentCount++
		} else if candidate.Err != nil {
			failures = append(failures, candidate.Err)
		}
	}
	aggregateScheme := notificationSchemeFromSet(schemes)
	if attemptedCount > 0 && sentCount == attemptedCount {
		return notificationOutcome{Status: "sent", Scheme: aggregateScheme}
	}
	if sentCount > 0 {
		return notificationOutcome{Status: "partial_failed", Scheme: aggregateScheme, Err: errors.Join(failures...)}
	}
	if len(failures) > 0 {
		return notificationOutcome{Status: "failed", Scheme: aggregateScheme, Err: errors.Join(failures...)}
	}
	return notificationOutcome{
		Status: notificationStatusFromSet(statuses),
		Scheme: aggregateScheme,
	}
}

func notificationStatusFromSet(statuses map[string]bool) string {
	if len(statuses) == 0 {
		return "skipped_sender"
	}
	if len(statuses) > 1 {
		return "unavailable"
	}
	for status := range statuses {
		return status
	}
	return "unavailable"
}

func notificationSchemeForAddresses(addresses []string) string {
	schemes := map[string]bool{}
	for _, address := range addresses {
		parsed, err := parseAddress(address)
		if err != nil || parsed.Scheme == "" {
			continue
		}
		schemes[parsed.Scheme] = true
	}
	return notificationSchemeFromSet(schemes)
}

func notificationSchemeFromSet(schemes map[string]bool) string {
	if len(schemes) == 0 {
		return ""
	}
	if len(schemes) > 1 {
		return "mixed"
	}
	for scheme := range schemes {
		return scheme
	}
	return ""
}

func (m *notificationManager) notifyDirectWakeScope(ctx context.Context, scope wakeScope, input waypostSendInput) notificationOutcome {
	outcome := notificationOutcome{Status: "unsupported"}
	for _, target := range scope.WakeTargets {
		manager := notificationManagerForWakeChannel(target.Channel)
		if manager == "" {
			continue
		}
		if wakeNotifyDisabled(input.DisableNotifyMessage) {
			return notificationOutcome{
				Status: "skipped_disabled",
				Scheme: manager,
			}
		}
		route := notificationRoute{
			Manager: manager,
			Target:  target.Target,
		}
		outcome = m.notifyRouteWithRetry(ctx, notificationEvent{
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

var notificationRetryDelays = [...]time.Duration{
	500 * time.Millisecond,
	time.Second,
	2 * time.Second,
}

func (m *notificationManager) notifyRouteWithRetry(ctx context.Context, event notificationEvent) notificationOutcome {
	for attempt := 0; ; attempt++ {
		probe := m.probeRoute(ctx, event.Route)
		if probe.Wakeable {
			// Notify is a non-idempotent side effect. Once it has been attempted,
			// its error is ambiguous: the target may already have received the
			// message. Only retry failures that happen before Notify is called.
			return m.notifyRoute(ctx, event)
		}
		outcome := notificationOutcome{
			Status: probe.Status,
			Scheme: probe.Scheme,
			Err:    probe.Err,
		}
		if !notificationProbeRetryable(probe) || attempt == len(notificationRetryDelays) {
			return outcome
		}
		delay := notificationRetryDelays[attempt]
		wait := m.retryWait
		if wait == nil {
			wait = waitForNotificationRetry
		}
		if err := wait(ctx, delay); err != nil {
			return outcome
		}
	}
}

func notificationProbeRetryable(probe notificationProbe) bool {
	if probe.Status != "failed" {
		return false
	}
	return !errors.Is(probe.Err, context.Canceled) && !errors.Is(probe.Err, context.DeadlineExceeded)
}

func waitForNotificationRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
	probe, err := n.sessions.probeSessionShowBestEffort(ctx, route.Target)
	if err != nil {
		return notificationProbe{
			Status: "failed",
			Scheme: n.Name(),
			Err:    err,
		}
	}
	switch probe.Status {
	case sessionShowProbeUnknown:
		return notificationProbe{
			Status: "failed",
			Scheme: n.Name(),
			Err:    errors.New("agent-deck session lookup returned an unknown result"),
		}
	case sessionShowProbeNotFound:
		return notificationProbe{
			Status: "not_found",
			Scheme: n.Name(),
		}
	}
	targetSession := probe.Data
	if targetSession == nil {
		return notificationProbe{
			Status: "failed",
			Scheme: n.Name(),
			Err:    errors.New("agent-deck session lookup returned no session data"),
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

func (n thurboxNotifier) Name() string {
	return "thurbox"
}

func (n thurboxNotifier) Probe(_ context.Context, route notificationRoute) notificationProbe {
	if strings.TrimSpace(route.Target) == "" {
		return notificationProbe{
			Status: "not_found",
			Scheme: n.Name(),
		}
	}
	// Thurbox's native mailbox is intentionally not part of Waypost workflow.
	// A session-send attempt is the best available targeted wake check and is
	// performed only after the durable Waypost send has completed.
	return notificationProbe{
		Status:   "wakeable",
		Scheme:   n.Name(),
		Wakeable: true,
	}
}

func (n thurboxNotifier) Notify(ctx context.Context, event notificationEvent) notificationOutcome {
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
	if _, err := runCommand(ctx, n.runner, []string{
		"thurbox-cli", "session", "send", event.Route.Target, notifyMessage,
	}, runOptions{timeout: syncCmdTimeout}); err != nil {
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
