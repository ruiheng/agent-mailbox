package mcpserver

import (
	"errors"
	"strings"
	"sync"

	"github.com/ruiheng/agent-mailbox/internal/mailbox"
)

type activeLease struct {
	DeliveryID     string
	LeaseToken     string
	LeaseExpiresAt string
}

type leaseRenewalFailure struct {
	DeliveryID string
	Cause      error
	Definitive bool
}

func (e *leaseRenewalFailure) Error() string {
	return "lease ownership is no longer guaranteed after renewal failure"
}

func (e *leaseRenewalFailure) Unwrap() error {
	return e.Cause
}

type activeLeaseManager struct {
	mu        sync.Mutex
	leases    map[string]activeLease
	failures  map[string]leaseRenewalFailure
	lastError error
}

func newActiveLeaseManager() *activeLeaseManager {
	return &activeLeaseManager{
		leases:   map[string]activeLease{},
		failures: map[string]leaseRenewalFailure{},
	}
}

func (m *activeLeaseManager) trackReceive(result mailbox.ReceiveResult) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, message := range result.Messages {
		m.leases[message.DeliveryID] = activeLease{
			DeliveryID:     message.DeliveryID,
			LeaseToken:     message.LeaseToken,
			LeaseExpiresAt: message.LeaseExpiresAt,
		}
		delete(m.failures, message.DeliveryID)
	}
}

func (m *activeLeaseManager) updateRenewed(result mailbox.LeaseRenewResult) {
	m.mu.Lock()
	defer m.mu.Unlock()

	lease, ok := m.leases[result.DeliveryID]
	if !ok {
		return
	}
	lease.LeaseToken = result.LeaseToken
	lease.LeaseExpiresAt = result.LeaseExpiresAt
	m.leases[result.DeliveryID] = lease
	delete(m.failures, result.DeliveryID)
	if len(m.failures) == 0 {
		m.lastError = nil
	}
}

func (m *activeLeaseManager) remove(deliveryID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.leases, deliveryID)
	delete(m.failures, deliveryID)
}

func (m *activeLeaseManager) snapshot() []activeLease {
	m.mu.Lock()
	defer m.mu.Unlock()

	leases := make([]activeLease, 0, len(m.leases))
	for _, lease := range m.leases {
		leases = append(leases, lease)
	}
	return leases
}

func (m *activeLeaseManager) hasTrackedLeases() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.leases) > 0
}

func (m *activeLeaseManager) markRenewalFailure(lease activeLease, cause error) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	definitive := renewalFailureDefinitive(cause)
	if definitive {
		delete(m.leases, lease.DeliveryID)
	}
	failure := &leaseRenewalFailure{
		DeliveryID: lease.DeliveryID,
		Cause:      cause,
		Definitive: definitive,
	}
	m.failures[lease.DeliveryID] = *failure
	m.lastError = failure
	return failure
}

func (m *activeLeaseManager) terminalMutationAllowed(deliveryID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	failure, ok := m.failures[deliveryID]
	if !ok {
		return nil
	}
	if failure.Definitive {
		return &failure
	}
	return nil
}

func (m *activeLeaseManager) lastRenewalError() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastError
}

func isLeaseRenewalFailure(err error) bool {
	var target *leaseRenewalFailure
	return errors.As(err, &target)
}

func renewalFailureDefinitive(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, mailbox.ErrLeaseExpired) || errors.Is(err, mailbox.ErrLeaseTokenMismatch) {
		return true
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "not found") ||
		strings.Contains(text, "want leased") ||
		strings.Contains(text, "changed while renewing")
}
