package mcpserver

import (
	"errors"
	"sync"

	"github.com/ruiheng/agent-mailbox/internal/mailbox"
)

type activeLease struct {
	DeliveryID       string
	RecipientAddress string
	LeaseToken       string
	LeaseExpiresAt   string
	Subject          string
	ContentType      string
	ClaimedAt        string
	LastRenewedAt    string
	Status           string
	TerminalAt       string
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
	history   map[string]activeLease
	failures  map[string]leaseRenewalFailure
	lastError error
}

func newActiveLeaseManager() *activeLeaseManager {
	return &activeLeaseManager{
		leases:   map[string]activeLease{},
		history:  map[string]activeLease{},
		failures: map[string]leaseRenewalFailure{},
	}
}

func (m *activeLeaseManager) trackReceive(result mailbox.ReceiveResult, claimedAt string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, message := range result.Messages {
		lease := activeLease{
			DeliveryID:       message.DeliveryID,
			RecipientAddress: message.RecipientAddress,
			LeaseToken:       message.LeaseToken,
			LeaseExpiresAt:   message.LeaseExpiresAt,
			Subject:          message.Subject,
			ContentType:      message.ContentType,
			ClaimedAt:        claimedAt,
			Status:           "active",
		}
		m.leases[message.DeliveryID] = lease
		m.history[message.DeliveryID] = lease
		delete(m.failures, message.DeliveryID)
	}
}

func (m *activeLeaseManager) updateRenewed(result mailbox.LeaseRenewResult, renewedAt string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	lease, ok := m.leases[result.DeliveryID]
	if !ok {
		return
	}
	lease.LeaseToken = result.LeaseToken
	lease.LeaseExpiresAt = result.LeaseExpiresAt
	lease.LastRenewedAt = renewedAt
	m.leases[result.DeliveryID] = lease
	if history, ok := m.history[result.DeliveryID]; ok {
		history.LeaseToken = result.LeaseToken
		history.LeaseExpiresAt = result.LeaseExpiresAt
		history.LastRenewedAt = renewedAt
		m.history[result.DeliveryID] = history
	}
	delete(m.failures, result.DeliveryID)
	if len(m.failures) == 0 {
		m.lastError = nil
	}
}

func (m *activeLeaseManager) markTerminal(deliveryID, status, terminalAt string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.leases, deliveryID)
	delete(m.failures, deliveryID)
	if history, ok := m.history[deliveryID]; ok {
		history.Status = status
		history.TerminalAt = terminalAt
		m.history[deliveryID] = history
	}
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

func (m *activeLeaseManager) historySnapshot(includeTerminal bool) []activeLease {
	m.mu.Lock()
	defer m.mu.Unlock()

	leases := make([]activeLease, 0, len(m.history))
	for _, lease := range m.history {
		if !includeTerminal && lease.Status != "active" {
			continue
		}
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
		if history, ok := m.history[lease.DeliveryID]; ok {
			history.Status = "renewal_failed"
			m.history[lease.DeliveryID] = history
		}
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
	return errors.Is(err, mailbox.ErrLeaseExpired) ||
		errors.Is(err, mailbox.ErrLeaseNotFound) ||
		errors.Is(err, mailbox.ErrLeaseNotLeased) ||
		errors.Is(err, mailbox.ErrLeaseRenewChanged) ||
		errors.Is(err, mailbox.ErrLeaseTokenMismatch)
}
