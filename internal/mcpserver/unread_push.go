package mcpserver

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/ruiheng/agent-mailbox/internal/mailbox"
)

type unreadPushRuntime struct {
	lastNotifiedAt string
}

type unreadPushState struct {
	mu       sync.Mutex
	runtimes map[string]unreadPushRuntime
}

func newUnreadPushState() *unreadPushState {
	return &unreadPushState{
		runtimes: map[string]unreadPushRuntime{},
	}
}

func (s *Service) startUnreadPushLoop() {
	if s.disableUnreadPushLoop {
		return
	}
	if len(s.currentUnreadPushScopes()) == 0 {
		return
	}
	s.unreadPushLoopOnce.Do(func() {
		go s.runUnreadPushLoop()
	})
}

func (s *Service) runUnreadPushLoop() {
	_ = s.processUnreadPush(context.Background())
	ticker := time.NewTicker(s.unreadPushPollInterval)
	defer ticker.Stop()

	for range ticker.C {
		_ = s.processUnreadPush(context.Background())
	}
}

func (s *Service) processUnreadPush(ctx context.Context) error {
	scopes := s.currentUnreadPushScopes()
	if len(scopes) == 0 {
		s.unreadPushState.reset()
		return nil
	}

	now := s.now().UTC()
	activeKeys := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		activeKeys = append(activeKeys, scope.key)
		stale, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) ([]mailbox.StaleAddress, error) {
			return service.ListStaleAddresses(ctx, mailbox.StaleAddressesParams{
				Addresses: []string{scope.address},
				OlderThan: s.unreadPushOlderThan,
			})
		})
		if err != nil {
			return err
		}
		if len(stale) == 0 {
			continue
		}

		probe := s.notifications.probeRoute(ctx, scope.route)
		if !probe.Wakeable {
			continue
		}

		runtime := s.unreadPushState.runtimeForKey(scope.key)
		if inUnreadPushCooldown(runtime.lastNotifiedAt, now, s.unreadPushCooldown) {
			continue
		}

		// This is a later recovery wakeup for stale unread mail on a bound inbox.
		// It is intentionally independent from any send-time notify_message opt-out.
		outcome := s.notifications.notifyRoute(ctx, notificationEvent{
			Kind:  notificationStaleUnread,
			Route: scope.route,
		})
		if notificationOutcomeDelivered(outcome) {
			s.unreadPushState.markNotified(scope.key, now)
		}
	}
	s.unreadPushState.pruneExcept(activeKeys)
	return nil
}

type unreadPushScope struct {
	address string
	route   notificationRoute
	key     string
}

func (s *Service) currentUnreadPushScopes() []unreadPushScope {
	snapshot := s.sessions.snapshotState()
	scopes := make([]unreadPushScope, 0, len(snapshot.BoundAddresses))
	for _, address := range snapshot.BoundAddresses {
		route, ok := unreadPushRouteForAddress(address)
		if !ok {
			continue
		}
		scopes = append(scopes, unreadPushScope{
			address: address,
			route:   route,
			key:     route.Manager + "/" + route.Target,
		})
	}
	return scopes
}

func unreadPushRouteForAddress(address string) (notificationRoute, bool) {
	parsed, err := parseAddress(address)
	if err != nil || parsed.Scheme != "agent-deck" {
		return notificationRoute{}, false
	}
	return notificationRoute{
		Manager: parsed.Scheme,
		Target:  parsed.ID,
	}, true
}

func (s *unreadPushState) runtimeForKey(scopeKey string) unreadPushRuntime {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtimes[scopeKey]
}

func (s *unreadPushState) markNotified(scopeKey string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runtime := s.runtimes[scopeKey]
	runtime.lastNotifiedAt = now.Format(time.RFC3339)
	s.runtimes[scopeKey] = runtime
}

func (s *unreadPushState) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtimes = map[string]unreadPushRuntime{}
}

func (s *unreadPushState) pruneExcept(activeKeys []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := make(map[string]bool, len(activeKeys))
	for _, key := range activeKeys {
		keep[key] = true
	}
	for key := range s.runtimes {
		if !keep[key] {
			delete(s.runtimes, key)
		}
	}
}

func inUnreadPushCooldown(lastNotifiedAt string, now time.Time, cooldown time.Duration) bool {
	if cooldown <= 0 || strings.TrimSpace(lastNotifiedAt) == "" {
		return false
	}
	notifiedAt, err := time.Parse(time.RFC3339, lastNotifiedAt)
	if err != nil {
		return false
	}
	return now.Sub(notifiedAt) < cooldown
}

func notificationOutcomeDelivered(outcome notificationOutcome) bool {
	return strings.TrimSpace(outcome.Status) == "sent"
}
