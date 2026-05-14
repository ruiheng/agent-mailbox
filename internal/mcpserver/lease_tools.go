package mcpserver

import (
	"context"
	"time"

	"github.com/ruiheng/agent-mailbox/internal/mailbox"
)

const (
	leaseRenewRetryDelay    = 50 * time.Millisecond
	leaseRenewRetryAttempts = 3
)

func (s *Service) startLeaseRenewLoop() {
	if s.disableLeaseRenewLoop || !s.activeLeases.hasTrackedLeases() {
		return
	}
	s.leaseRenewLoopOnce.Do(func() {
		go s.runLeaseRenewLoop()
	})
}

func (s *Service) runLeaseRenewLoop() {
	ticker := time.NewTicker(s.leaseRenewInterval)
	defer ticker.Stop()

	for range ticker.C {
		s.processLeaseRenewals(context.Background())
	}
}

func (s *Service) processLeaseRenewals(ctx context.Context) error {
	leases := s.activeLeases.snapshot()
	if len(leases) == 0 {
		return nil
	}

	var firstErr error
	for _, lease := range leases {
		renewed, err := s.renewLeaseWithRetry(ctx, lease)
		if err != nil {
			recorded := s.activeLeases.markRenewalFailure(lease, err)
			if firstErr == nil {
				firstErr = recorded
			}
			continue
		}
		s.activeLeases.updateRenewed(renewed)
	}
	return firstErr
}

func (s *Service) renewLeaseWithRetry(ctx context.Context, lease activeLease) (mailbox.LeaseRenewResult, error) {
	var lastErr error
	for attempt := 0; attempt < leaseRenewRetryAttempts; attempt++ {
		renewed, err := withMailboxService(ctx, s.mailboxServices, func(service mailboxService) (mailbox.LeaseRenewResult, error) {
			return service.Renew(ctx, lease.DeliveryID, lease.LeaseToken, s.mcpLeaseTTL)
		})
		if err == nil {
			return renewed, nil
		}
		lastErr = err
		if renewalFailureDefinitive(err) || attempt == leaseRenewRetryAttempts-1 {
			break
		}
		if err := waitForLeaseRenewRetry(ctx, leaseRenewRetryDelay); err != nil {
			return mailbox.LeaseRenewResult{}, err
		}
	}
	return mailbox.LeaseRenewResult{}, lastErr
}

func waitForLeaseRenewRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
