package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type reminderSelector struct {
	Addresses  []string            `json:"addresses,omitempty"`
	GroupViews []reminderGroupView `json:"group_views,omitempty"`
}

type reminderGroupViewInput struct {
	GroupAddress string `json:"group_address"`
	AsPerson     string `json:"as_person"`
}

type reminderGroupView struct {
	GroupAddress string `json:"group_address"`
	AsPerson     string `json:"as_person"`
}

type reminderRouteConfig struct {
	Address string `json:"address"`
	Manager string `json:"manager"`
	Target  string `json:"target"`
}

type reminderPolicy struct {
	OlderThan    time.Duration `json:"-"`
	OlderThanRaw string        `json:"older_than"`
	ActivePush   bool          `json:"active_push,omitempty"`
}

type reminderRuntime struct {
	LastCheckedAt      string `json:"last_checked_at,omitempty"`
	LastMatchedAt      string `json:"last_matched_at,omitempty"`
	LastStaleCount     int    `json:"last_stale_count"`
	LastClaimableCount int    `json:"last_claimable_count"`
	LastOldestEligible string `json:"last_oldest_eligible_at,omitempty"`
	PendingSince       string `json:"pending_since,omitempty"`
	LastSuppressedAt   string `json:"last_suppressed_at,omitempty"`
	LastNotifiedAt     string `json:"last_notified_at,omitempty"`
}

type reminderSubscription struct {
	Key      string              `json:"subscription_key"`
	Selector reminderSelector    `json:"selector"`
	Route    reminderRouteConfig `json:"route"`
	Policy   reminderPolicy      `json:"policy"`
	Runtime  reminderRuntime     `json:"runtime"`
}

type staleAddress struct {
	Address          string `json:"address"`
	Person           string `json:"person,omitempty"`
	OldestEligibleAt string `json:"oldest_eligible_at"`
	ClaimableCount   int    `json:"claimable_count"`
}

type reminderCurrentState struct {
	Stale             bool   `json:"stale"`
	StaleAddressCount int    `json:"stale_address_count"`
	ClaimableCount    int    `json:"claimable_count"`
	OldestEligibleAt  string `json:"oldest_eligible_at,omitempty"`
}

type reminderStatusEntry struct {
	Subscription reminderSubscription `json:"subscription"`
	Current      reminderCurrentState `json:"current"`
}

type passiveReminderSubscription struct {
	SubscriptionKey    string              `json:"subscription_key"`
	SelectorAddresses  []string            `json:"selector_addresses,omitempty"`
	SelectorGroupViews []reminderGroupView `json:"selector_group_views,omitempty"`
	Route              string              `json:"route"`
	OlderThan          string              `json:"older_than"`
	StaleAddressCount  int                 `json:"stale_address_count"`
	ClaimableCount     int                 `json:"claimable_count"`
	OldestEligibleAt   string              `json:"oldest_eligible_at,omitempty"`
}

type passiveReminderPayload struct {
	ConfiguredCount int                           `json:"configured_count"`
	StaleCount      int                           `json:"stale_count"`
	Subscriptions   []passiveReminderSubscription `json:"subscriptions,omitempty"`
}

type reminderManagerDeps struct {
	now                func() time.Time
	confirmDelay       time.Duration
	listStaleAddresses func(context.Context, []string, string, reminderPolicy) ([]staleAddress, error)
	probeRoute         func(context.Context, notificationRoute) notificationProbe
	notifyRoute        func(context.Context, notificationEvent) notificationOutcome
}

type reminderManager struct {
	mu            sync.Mutex
	subscriptions map[string]reminderSubscription
	deps          reminderManagerDeps
}

func newReminderManager(deps reminderManagerDeps) *reminderManager {
	return &reminderManager{
		subscriptions: map[string]reminderSubscription{},
		deps:          deps,
	}
}

func (m *reminderManager) subscribe(ctx context.Context, input mailboxReminderSubscribeInput) (reminderSubscription, bool, error) {
	subscription, err := buildReminderSubscription(input.Addresses, input.GroupViews, input.Route, input.OlderThan, input.ActivePush)
	if err != nil {
		return reminderSubscription{}, false, err
	}

	staleEntries, err := m.listStaleEntries(ctx, subscription.Selector, subscription.Policy)
	if err != nil {
		return reminderSubscription{}, false, err
	}
	subscription.Runtime = nextReminderRuntime(reminderRuntime{}, m.now(), staleEntries)

	existed := m.setSubscription(subscription)
	return subscription, existed, nil
}

func (m *reminderManager) unsubscribe(input mailboxReminderUnsubscribeInput) (string, bool, error) {
	subscriptionKey, err := reminderSubscriptionKey(input.Addresses, input.GroupViews, input.Route)
	if err != nil {
		return "", false, err
	}
	return subscriptionKey, m.deleteSubscription(subscriptionKey), nil
}

func (m *reminderManager) status(ctx context.Context) ([]reminderStatusEntry, error) {
	subscriptions := m.snapshot()
	entries := make([]reminderStatusEntry, 0, len(subscriptions))
	now := m.now()
	updatedRuntime := make(map[string]reminderRuntime, len(subscriptions))

	for _, subscription := range subscriptions {
		staleEntries, err := m.listStaleEntries(ctx, subscription.Selector, subscription.Policy)
		if err != nil {
			return nil, err
		}
		subscription.Runtime = nextReminderRuntime(subscription.Runtime, now, staleEntries)
		updatedRuntime[subscription.Key] = subscription.Runtime
		entries = append(entries, reminderStatusEntry{
			Subscription: subscription,
			Current:      reminderCurrentSummary(staleEntries),
		})
	}

	m.updateRuntime(updatedRuntime)
	return entries, nil
}

func (m *reminderManager) passivePayload(ctx context.Context) (map[string]any, error) {
	subscriptions := m.snapshot()
	if len(subscriptions) == 0 {
		return nil, nil
	}

	now := m.now()
	updatedRuntime := make(map[string]reminderRuntime, len(subscriptions))
	staleSubscriptions := make([]passiveReminderSubscription, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		staleEntries, err := m.listStaleEntries(ctx, subscription.Selector, subscription.Policy)
		if err != nil {
			return nil, err
		}
		subscription.Runtime = nextReminderRuntime(subscription.Runtime, now, staleEntries)
		updatedRuntime[subscription.Key] = subscription.Runtime
		if len(staleEntries) == 0 {
			continue
		}
		summary := reminderCurrentSummary(staleEntries)
		staleSubscriptions = append(staleSubscriptions, passiveReminderSubscription{
			SubscriptionKey:    subscription.Key,
			SelectorAddresses:  append([]string(nil), subscription.Selector.Addresses...),
			SelectorGroupViews: append([]reminderGroupView(nil), subscription.Selector.GroupViews...),
			Route:              subscription.Route.Address,
			OlderThan:          subscription.Policy.OlderThanRaw,
			StaleAddressCount:  summary.StaleAddressCount,
			ClaimableCount:     summary.ClaimableCount,
			OldestEligibleAt:   summary.OldestEligibleAt,
		})
	}

	m.updateRuntime(updatedRuntime)
	sort.Slice(staleSubscriptions, func(i, j int) bool {
		left := staleSubscriptions[i]
		right := staleSubscriptions[j]
		if left.OldestEligibleAt != right.OldestEligibleAt {
			if left.OldestEligibleAt == "" {
				return false
			}
			if right.OldestEligibleAt == "" {
				return true
			}
			return left.OldestEligibleAt < right.OldestEligibleAt
		}
		if left.Route != right.Route {
			return left.Route < right.Route
		}
		return left.SubscriptionKey < right.SubscriptionKey
	})

	payload := passiveReminderPayload{
		ConfiguredCount: len(subscriptions),
		StaleCount:      len(staleSubscriptions),
		Subscriptions:   staleSubscriptions,
	}
	return structToMap(payload)
}

func (m *reminderManager) processActiveSubscriptions(ctx context.Context) error {
	subscriptions := m.snapshot()
	if len(subscriptions) == 0 {
		return nil
	}

	now := m.now()
	updatedRuntime := make(map[string]reminderRuntime, len(subscriptions))
	for _, subscription := range subscriptions {
		if !subscription.Policy.ActivePush {
			continue
		}
		staleEntries, err := m.listStaleEntries(ctx, subscription.Selector, subscription.Policy)
		if err != nil {
			continue
		}
		updatedRuntime[subscription.Key] = m.processActiveSubscription(ctx, subscription, now, staleEntries)
	}
	m.updateRuntime(updatedRuntime)
	return nil
}

func (m *reminderManager) processActiveSubscription(ctx context.Context, subscription reminderSubscription, now time.Time, staleEntries []staleAddress) reminderRuntime {
	runtime := nextReminderRuntime(subscription.Runtime, now, staleEntries)
	if len(staleEntries) == 0 {
		runtime.PendingSince = ""
		return runtime
	}

	route := notificationRoute{
		Manager: subscription.Route.Manager,
		Target:  subscription.Route.Target,
	}
	probe := m.deps.probeRoute(ctx, route)
	if !probe.Wakeable {
		runtime.PendingSince = ""
		if probe.Status == "suppressed_local_activity" {
			runtime.LastSuppressedAt = now.Format(time.RFC3339)
		}
		return runtime
	}

	if inReminderCooldown(runtime.LastNotifiedAt, now, subscription.Policy.cooldown()) {
		return runtime
	}

	if runtime.PendingSince == "" {
		runtime.PendingSince = now.Format(time.RFC3339)
		return runtime
	}

	pendingSince, err := time.Parse(time.RFC3339, runtime.PendingSince)
	if err != nil {
		runtime.PendingSince = now.Format(time.RFC3339)
		return runtime
	}
	if now.Sub(pendingSince) < m.deps.confirmDelay {
		return runtime
	}

	outcome := m.deps.notifyRoute(ctx, notificationEvent{
		Kind:  notificationStaleUnread,
		Route: route,
	})
	if notificationOutcomeDelivered(outcome) {
		runtime.PendingSince = ""
		runtime.LastNotifiedAt = now.Format(time.RFC3339)
	}
	return runtime
}

func (m *reminderManager) snapshot() []reminderSubscription {
	m.mu.Lock()
	defer m.mu.Unlock()

	subscriptions := make([]reminderSubscription, 0, len(m.subscriptions))
	for _, subscription := range m.subscriptions {
		subscriptions = append(subscriptions, cloneReminderSubscription(subscription))
	}
	sort.Slice(subscriptions, func(i, j int) bool {
		return subscriptions[i].Key < subscriptions[j].Key
	})
	return subscriptions
}

func (m *reminderManager) setSubscription(subscription reminderSubscription) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, existed := m.subscriptions[subscription.Key]
	m.subscriptions[subscription.Key] = subscription
	return existed
}

func (m *reminderManager) deleteSubscription(subscriptionKey string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, existed := m.subscriptions[subscriptionKey]
	delete(m.subscriptions, subscriptionKey)
	return existed
}

func (m *reminderManager) subscriptionRuntime(subscriptionKey string) (reminderRuntime, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	subscription, ok := m.subscriptions[subscriptionKey]
	if !ok {
		return reminderRuntime{}, false
	}
	return subscription.Runtime, true
}

func (m *reminderManager) updateRuntime(runtimeByKey map[string]reminderRuntime) {
	if len(runtimeByKey) == 0 {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for key, runtime := range runtimeByKey {
		subscription, ok := m.subscriptions[key]
		if !ok {
			continue
		}
		subscription.Runtime = runtime
		m.subscriptions[key] = subscription
	}
}

func (m *reminderManager) listStaleEntries(ctx context.Context, selector reminderSelector, policy reminderPolicy) ([]staleAddress, error) {
	staleEntries := make([]staleAddress, 0, len(selector.Addresses)+len(selector.GroupViews))
	if len(selector.Addresses) > 0 {
		personalEntries, err := m.listStaleAddresses(ctx, selector.Addresses, "", policy)
		if err != nil {
			return nil, err
		}
		staleEntries = append(staleEntries, personalEntries...)
	}
	for _, groupView := range selector.GroupViews {
		groupEntries, err := m.listStaleAddresses(ctx, []string{groupView.GroupAddress}, groupView.AsPerson, policy)
		if err != nil {
			return nil, err
		}
		staleEntries = append(staleEntries, groupEntries...)
	}
	sort.Slice(staleEntries, func(i, j int) bool {
		if staleEntries[i].OldestEligibleAt != staleEntries[j].OldestEligibleAt {
			return staleEntries[i].OldestEligibleAt < staleEntries[j].OldestEligibleAt
		}
		if staleEntries[i].Address != staleEntries[j].Address {
			return staleEntries[i].Address < staleEntries[j].Address
		}
		return staleEntries[i].Person < staleEntries[j].Person
	})
	return staleEntries, nil
}

func (m *reminderManager) listStaleAddresses(ctx context.Context, addresses []string, person string, policy reminderPolicy) ([]staleAddress, error) {
	return m.deps.listStaleAddresses(ctx, addresses, person, policy)
}

func (m *reminderManager) now() time.Time {
	return m.deps.now().UTC()
}

func buildReminderSubscription(addresses []string, groupViews []reminderGroupViewInput, route, olderThan string, activePush bool) (reminderSubscription, error) {
	selector, err := normalizeReminderSelector(addresses, groupViews)
	if err != nil {
		return reminderSubscription{}, err
	}
	routeConfig, err := normalizeReminderRoute(route)
	if err != nil {
		return reminderSubscription{}, err
	}
	policy, err := normalizeReminderPolicy(olderThan, activePush)
	if err != nil {
		return reminderSubscription{}, err
	}
	return reminderSubscription{
		Key:      reminderKey(selector, routeConfig.Address),
		Selector: selector,
		Route:    routeConfig,
		Policy:   policy,
	}, nil
}

func reminderSubscriptionKey(addresses []string, groupViews []reminderGroupViewInput, route string) (string, error) {
	selector, err := normalizeReminderSelector(addresses, groupViews)
	if err != nil {
		return "", err
	}
	routeConfig, err := normalizeReminderRoute(route)
	if err != nil {
		return "", err
	}
	return reminderKey(selector, routeConfig.Address), nil
}

func normalizeReminderSelector(addresses []string, groupViews []reminderGroupViewInput) (reminderSelector, error) {
	normalizedAddresses := dedupe(addresses)
	if len(normalizedAddresses) > 0 {
		sort.Strings(normalizedAddresses)
	}
	normalizedGroupViews, err := normalizeReminderGroupViews(groupViews)
	if err != nil {
		return reminderSelector{}, err
	}
	if len(normalizedAddresses) == 0 && len(normalizedGroupViews) == 0 {
		return reminderSelector{}, errors.New("mailbox reminder requires at least one selector")
	}
	return reminderSelector{
		Addresses:  normalizedAddresses,
		GroupViews: normalizedGroupViews,
	}, nil
}

func normalizeReminderGroupViews(groupViews []reminderGroupViewInput) ([]reminderGroupView, error) {
	if len(groupViews) == 0 {
		return nil, nil
	}
	normalized := make([]reminderGroupView, 0, len(groupViews))
	seen := map[string]bool{}
	for _, groupView := range groupViews {
		groupAddress := strings.TrimSpace(groupView.GroupAddress)
		asPerson := strings.TrimSpace(groupView.AsPerson)
		if groupAddress == "" {
			return nil, errors.New("mailbox reminder group view requires group_address")
		}
		if asPerson == "" {
			return nil, errors.New("mailbox reminder group view requires as_person")
		}
		key := groupAddress + "\x00" + asPerson
		if seen[key] {
			continue
		}
		seen[key] = true
		normalized = append(normalized, reminderGroupView{
			GroupAddress: groupAddress,
			AsPerson:     asPerson,
		})
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].GroupAddress != normalized[j].GroupAddress {
			return normalized[i].GroupAddress < normalized[j].GroupAddress
		}
		return normalized[i].AsPerson < normalized[j].AsPerson
	})
	return normalized, nil
}

func normalizeReminderRoute(route string) (reminderRouteConfig, error) {
	trimmed := strings.TrimSpace(route)
	if trimmed == "" {
		return reminderRouteConfig{}, errors.New("mailbox reminder requires route")
	}
	parsed, err := parseAddress(trimmed)
	if err != nil {
		return reminderRouteConfig{}, err
	}
	return reminderRouteConfig{
		Address: parsed.Scheme + "/" + parsed.ID,
		Manager: parsed.Scheme,
		Target:  parsed.ID,
	}, nil
}

func normalizeReminderPolicy(olderThan string, activePush bool) (reminderPolicy, error) {
	trimmed := strings.TrimSpace(olderThan)
	if trimmed == "" {
		return reminderPolicy{}, errors.New("mailbox reminder requires older_than")
	}
	duration, err := time.ParseDuration(trimmed)
	if err != nil {
		return reminderPolicy{}, fmt.Errorf("invalid older_than duration: %w", err)
	}
	if duration <= 0 {
		return reminderPolicy{}, errors.New("older_than must be greater than zero")
	}
	return reminderPolicy{
		OlderThan:    duration,
		OlderThanRaw: trimmed,
		ActivePush:   activePush,
	}, nil
}

func (p reminderPolicy) cooldown() time.Duration {
	return p.OlderThan
}

func reminderKey(selector reminderSelector, route string) string {
	parts := []string{route, "addresses"}
	parts = append(parts, selector.Addresses...)
	parts = append(parts, "groups")
	for _, groupView := range selector.GroupViews {
		parts = append(parts, groupView.GroupAddress, groupView.AsPerson)
	}
	return strings.Join(parts, "\x00")
}

func cloneReminderSubscription(subscription reminderSubscription) reminderSubscription {
	cloned := subscription
	cloned.Selector.Addresses = append([]string(nil), subscription.Selector.Addresses...)
	cloned.Selector.GroupViews = append([]reminderGroupView(nil), subscription.Selector.GroupViews...)
	return cloned
}

func nextReminderRuntime(previous reminderRuntime, checkedAt time.Time, staleEntries []staleAddress) reminderRuntime {
	runtime := previous
	runtime.LastCheckedAt = checkedAt.Format(time.RFC3339)

	summary := reminderCurrentSummary(staleEntries)
	runtime.LastStaleCount = summary.StaleAddressCount
	runtime.LastClaimableCount = summary.ClaimableCount
	runtime.LastOldestEligible = summary.OldestEligibleAt
	if summary.Stale {
		runtime.LastMatchedAt = checkedAt.Format(time.RFC3339)
	}
	return runtime
}

func reminderCurrentSummary(staleEntries []staleAddress) reminderCurrentState {
	summary := reminderCurrentState{
		Stale:             len(staleEntries) > 0,
		StaleAddressCount: len(staleEntries),
	}
	for _, entry := range staleEntries {
		summary.ClaimableCount += entry.ClaimableCount
		if summary.OldestEligibleAt == "" || (entry.OldestEligibleAt != "" && entry.OldestEligibleAt < summary.OldestEligibleAt) {
			summary.OldestEligibleAt = entry.OldestEligibleAt
		}
	}
	return summary
}
