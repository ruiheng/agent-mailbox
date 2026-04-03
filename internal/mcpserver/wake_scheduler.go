package mcpserver

import (
	"context"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ruiheng/agent-mailbox/internal/mailbox"
)

type WakeChannelName string

const (
	WakeChannelAgentDeck       WakeChannelName = "agent_deck"
	WakeHintMCPResourceUpdated WakeChannelName = "mcp_resource_updated"
)

type WakeTarget struct {
	Channel WakeChannelName
	Target  string
}

type wakeScope struct {
	ScopeID          string
	MailboxAddresses []string
	WakeTargets      []WakeTarget
}

type wakeSnapshot struct {
	ScopeID                   string
	HasPendingVisibleDelivery bool
	PendingSince              string
	BoundAddresses            []string
	WakeTargets               []WakeTarget
	QueuedVisibleCount        int
}

type visibleMailboxState struct {
	QueuedVisibleCount int
	OldestEligibleAt   string
}

type wakeRuntime struct {
	ScopeID           string
	PendingSince      string
	LastAnyWakeAt     string
	LastWakeByChannel map[WakeChannelName]string
}

type wakeSchedulerState struct {
	mu       sync.Mutex
	runtimes map[string]wakeRuntime
}

type wakeChannelConfig struct {
	Category     string
	InitialDelay time.Duration
	Cooldown     time.Duration
}

func newWakeSchedulerState() *wakeSchedulerState {
	return &wakeSchedulerState{
		runtimes: map[string]wakeRuntime{},
	}
}

func (s *Service) startWakeSchedulerLoop() {
	if s.disableWakeScheduler {
		return
	}
	scope, err := s.currentLocalWakeScope()
	if err != nil || scope == nil {
		return
	}
	s.wakeSchedulerLoopOnce.Do(func() {
		go s.runWakeSchedulerLoop()
	})
}

func (s *Service) runWakeSchedulerLoop() {
	_ = s.processWakeScheduler(context.Background())
	ticker := time.NewTicker(s.wakePollInterval)
	defer ticker.Stop()

	for range ticker.C {
		_ = s.processWakeScheduler(context.Background())
	}
}

func (s *Service) processWakeScheduler(ctx context.Context) error {
	scope, err := s.currentLocalWakeScope()
	if err != nil {
		return err
	}
	if scope == nil {
		s.wakeSchedulerState.reset()
		return nil
	}

	snapshot, err := s.currentWakeSnapshot(ctx, *scope)
	if err != nil {
		return err
	}
	if !snapshot.HasPendingVisibleDelivery {
		s.wakeSchedulerState.reset()
		return nil
	}

	now := s.now().UTC()
	runtime := s.wakeSchedulerState.runtimeForScope(scope.ScopeID, snapshot.PendingSince)
	if s.tryWakeChannel(ctx, snapshot, runtime, now, WakeHintMCPResourceUpdated) {
		return nil
	}
	if s.tryWakeChannel(ctx, snapshot, runtime, now, WakeChannelAgentDeck) {
		return nil
	}

	s.wakeSchedulerState.pruneExcept([]string{scope.ScopeID})
	return nil
}

func (s *Service) tryWakeChannel(ctx context.Context, snapshot wakeSnapshot, runtime wakeRuntime, now time.Time, channel WakeChannelName) bool {
	config := wakeChannelConfigs[channel]
	if allowed, reason := allowWakeAttempt(snapshot, runtime, now, channel, config); !allowed {
		logWakeSuppressed(snapshot, runtime, now, channel, config.Category, "", reason)
		return false
	}

	switch channel {
	case WakeHintMCPResourceUpdated:
		if !s.overviewSubscriptions.hasSubscribers() {
			logWakeSuppressed(snapshot, runtime, now, channel, config.Category, mailboxOverviewURI, "unavailable:no_subscribers")
			return false
		}
		outcome := notificationOutcome{Status: "sent", Scheme: string(channel)}
		s.emitMailboxOverviewUpdatedBestEffort(ctx)
		logWakeAttempt(snapshot, now, channel, config.Category, mailboxOverviewURI, outcome)
		s.wakeSchedulerState.markDelivered(snapshot.ScopeID, channel, now, snapshot.PendingSince)
		return true
	case WakeChannelAgentDeck:
		for _, target := range snapshotTargetsForChannel(snapshot, channel) {
			route := notificationRoute{
				Manager: "agent-deck",
				Target:  target.Target,
			}
			probe := s.notifications.probeRoute(ctx, route)
			if !probe.Wakeable {
				logWakeSuppressed(snapshot, runtime, now, channel, config.Category, target.Target, "unavailable:"+probe.Status)
				continue
			}
			outcome := s.notifications.notifyRoute(ctx, notificationEvent{
				Kind:  notificationFallbackWake,
				Route: route,
			})
			logWakeAttempt(snapshot, now, channel, config.Category, target.Target, outcome)
			if notificationOutcomeDelivered(outcome) {
				s.wakeSchedulerState.markDelivered(snapshot.ScopeID, channel, now, snapshot.PendingSince)
			}
			return true
		}
		logWakeSuppressed(snapshot, runtime, now, channel, config.Category, "", "unavailable:no_wakeable_target")
		return false
	default:
		return false
	}
}

func (s *Service) currentLocalWakeScope() (*wakeScope, error) {
	if err := s.sessions.tryAutoBindCurrentSession(ctxOrBackground(nil)); err != nil {
		return nil, err
	}
	snapshot := s.sessions.snapshotState()
	if len(snapshot.BoundAddresses) == 0 {
		return nil, nil
	}
	targets, err := resolveWakeTargets(snapshot.BoundAddresses)
	if err != nil {
		return nil, err
	}
	return &wakeScope{
		ScopeID:          localWakeScopeID(snapshot),
		MailboxAddresses: append([]string(nil), snapshot.BoundAddresses...),
		WakeTargets:      targets,
	}, nil
}

func directWakeScopeForAddress(address string) (*wakeScope, string, error) {
	target, ok, scheme, err := wakeTargetForAddress(address)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, scheme, nil
	}
	return &wakeScope{
		ScopeID:          "direct/" + strings.TrimSpace(address),
		MailboxAddresses: []string{strings.TrimSpace(address)},
		WakeTargets:      []WakeTarget{target},
	}, scheme, nil
}

func localWakeScopeID(snapshot stateSnapshot) string {
	switch {
	case strings.TrimSpace(snapshot.DetectedAgentDeckSession) != "":
		return "local/agent-deck/" + strings.TrimSpace(snapshot.DetectedAgentDeckSession)
	case strings.TrimSpace(snapshot.DetectedAgentSession) != "":
		return "local/agent/" + strings.TrimSpace(snapshot.DetectedAgentSession)
	default:
		addresses := append([]string(nil), snapshot.BoundAddresses...)
		sort.Strings(addresses)
		return "local/bound/" + strings.Join(addresses, ",")
	}
}

func resolveWakeTargets(addresses []string) ([]WakeTarget, error) {
	targets := make([]WakeTarget, 0, len(addresses))
	seen := map[string]bool{}
	for _, address := range addresses {
		target, ok, _, err := wakeTargetForAddress(address)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		key := string(target.Channel) + "\x00" + target.Target
		if seen[key] {
			continue
		}
		seen[key] = true
		targets = append(targets, target)
	}
	return targets, nil
}

func wakeTargetForAddress(address string) (WakeTarget, bool, string, error) {
	parsed, err := parseAddress(address)
	if err != nil {
		return WakeTarget{}, false, "", err
	}
	switch parsed.Scheme {
	case "agent-deck":
		return WakeTarget{Channel: WakeChannelAgentDeck, Target: parsed.ID}, true, parsed.Scheme, nil
	default:
		return WakeTarget{}, false, parsed.Scheme, nil
	}
}

func (s *Service) currentWakeSnapshot(ctx context.Context, scope wakeScope) (wakeSnapshot, error) {
	visible, err := s.visibleMailboxSnapshot(ctx, scope.MailboxAddresses)
	if err != nil {
		return wakeSnapshot{}, err
	}
	return wakeSnapshot{
		ScopeID:                   scope.ScopeID,
		HasPendingVisibleDelivery: visible.QueuedVisibleCount > 0,
		PendingSince:              visible.OldestEligibleAt,
		BoundAddresses:            append([]string(nil), scope.MailboxAddresses...),
		WakeTargets:               append([]WakeTarget(nil), scope.WakeTargets...),
		QueuedVisibleCount:        visible.QueuedVisibleCount,
	}, nil
}

func (s *Service) visibleMailboxSnapshot(ctx context.Context, addresses []string) (visibleMailboxState, error) {
	if len(addresses) == 0 {
		return visibleMailboxState{}, nil
	}
	return withMailboxService(ctx, s.mailboxServices, func(service mailboxService) (visibleMailboxState, error) {
		state := visibleMailboxState{}
		for _, address := range addresses {
			deliveries, err := service.List(ctx, mailbox.ListParams{Address: address})
			if err != nil {
				return visibleMailboxState{}, err
			}
			state.QueuedVisibleCount += len(deliveries)
			for _, delivery := range deliveries {
				if state.OldestEligibleAt == "" || delivery.VisibleAt < state.OldestEligibleAt {
					state.OldestEligibleAt = delivery.VisibleAt
				}
			}
		}
		return state, nil
	})
}

func snapshotTargetsForChannel(snapshot wakeSnapshot, channel WakeChannelName) []WakeTarget {
	targets := make([]WakeTarget, 0, len(snapshot.WakeTargets))
	for _, target := range snapshot.WakeTargets {
		if target.Channel == channel {
			targets = append(targets, target)
		}
	}
	return targets
}

func (s wakeScope) targetsForChannel(channel WakeChannelName) []WakeTarget {
	targets := make([]WakeTarget, 0, len(s.WakeTargets))
	for _, target := range s.WakeTargets {
		if target.Channel == channel {
			targets = append(targets, target)
		}
	}
	return targets
}

var wakeChannelConfigs = map[WakeChannelName]wakeChannelConfig{
	WakeHintMCPResourceUpdated: {
		Category:     "local_hint",
		InitialDelay: defaultMCPHintInitialDelay,
		Cooldown:     defaultMCPHintCooldown,
	},
	WakeChannelAgentDeck: {
		Category:     "targeted_wake",
		InitialDelay: defaultAgentDeckInitialDelay,
		Cooldown:     defaultAgentDeckCooldown,
	},
}

func allowWakeAttempt(snapshot wakeSnapshot, runtime wakeRuntime, now time.Time, channel WakeChannelName, config wakeChannelConfig) (bool, string) {
	if snapshot.PendingSince == "" {
		return false, "suppressed:pending_since_missing"
	}
	pendingSince, err := time.Parse(time.RFC3339Nano, snapshot.PendingSince)
	if err != nil {
		return false, "suppressed:pending_since_invalid"
	}
	if now.Before(pendingSince.Add(config.InitialDelay)) {
		return false, "suppressed:initial_delay"
	}
	if inWakeCooldown(runtime.LastAnyWakeAt, now, defaultWakeInterChannelGap) {
		return false, "suppressed:inter_channel_gap"
	}
	if inWakeCooldown(runtime.LastWakeByChannel[channel], now, config.Cooldown) {
		return false, "suppressed:channel_cooldown"
	}
	return true, ""
}

func inWakeCooldown(lastWakeAt string, now time.Time, cooldown time.Duration) bool {
	if cooldown <= 0 || strings.TrimSpace(lastWakeAt) == "" {
		return false
	}
	wakeAt, err := time.Parse(time.RFC3339Nano, lastWakeAt)
	if err != nil {
		return false
	}
	return now.Sub(wakeAt) < cooldown
}

func logWakeSuppressed(snapshot wakeSnapshot, runtime wakeRuntime, now time.Time, channel WakeChannelName, category, target, reason string) {
	log.Printf(
		"mcpserver wake_suppressed scope_id=%s channel=%s category=%s target=%s reason=%s pending_since=%s last_any_wake_at=%s last_channel_wake_at=%s addresses=%q now=%s",
		snapshot.ScopeID,
		channel,
		category,
		target,
		reason,
		snapshot.PendingSince,
		runtime.LastAnyWakeAt,
		runtime.LastWakeByChannel[channel],
		strings.Join(snapshot.BoundAddresses, ","),
		now.Format(time.RFC3339Nano),
	)
}

func logWakeAttempt(snapshot wakeSnapshot, now time.Time, channel WakeChannelName, category, target string, outcome notificationOutcome) {
	errText := ""
	if outcome.Err != nil {
		errText = outcome.Err.Error()
	}
	log.Printf(
		"mcpserver wake_attempt scope_id=%s channel=%s category=%s target=%s status=%s scheme=%s pending_since=%s addresses=%q now=%s err=%q",
		snapshot.ScopeID,
		channel,
		category,
		target,
		outcome.Status,
		outcome.Scheme,
		snapshot.PendingSince,
		strings.Join(snapshot.BoundAddresses, ","),
		now.Format(time.RFC3339Nano),
		errText,
	)
}

func (s *wakeSchedulerState) runtimeForScope(scopeID, pendingSince string) wakeRuntime {
	s.mu.Lock()
	defer s.mu.Unlock()
	runtime := s.runtimes[scopeID]
	runtime.ScopeID = scopeID
	runtime.PendingSince = pendingSince
	if runtime.LastWakeByChannel == nil {
		runtime.LastWakeByChannel = map[WakeChannelName]string{}
	}
	s.runtimes[scopeID] = runtime
	return runtime
}

func (s *wakeSchedulerState) markDelivered(scopeID string, channel WakeChannelName, now time.Time, pendingSince string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runtime := s.runtimes[scopeID]
	runtime.ScopeID = scopeID
	runtime.PendingSince = pendingSince
	if runtime.LastWakeByChannel == nil {
		runtime.LastWakeByChannel = map[WakeChannelName]string{}
	}
	timestamp := now.Format(time.RFC3339Nano)
	runtime.LastAnyWakeAt = timestamp
	runtime.LastWakeByChannel[channel] = timestamp
	s.runtimes[scopeID] = runtime
}

func (s *wakeSchedulerState) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtimes = map[string]wakeRuntime{}
}

func (s *wakeSchedulerState) pruneExcept(activeScopeIDs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := make(map[string]bool, len(activeScopeIDs))
	for _, scopeID := range activeScopeIDs {
		keep[scopeID] = true
	}
	for scopeID := range s.runtimes {
		if !keep[scopeID] {
			delete(s.runtimes, scopeID)
		}
	}
}

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	return context.Background()
}
