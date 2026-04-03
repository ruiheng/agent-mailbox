package mailbox

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

type WatchParams struct {
	Address   string
	Addresses []string
	State     string
	Timeout   time.Duration
}

type WaitParams struct {
	Address   string
	Addresses []string
	Timeout   time.Duration
}

type GroupWaitParams struct {
	Address string
	Person  string
	Timeout time.Duration
}

func (s *Store) Wait(ctx context.Context, params WaitParams) (ListedDelivery, error) {
	addresses, err := normalizeAddresses(params.Address, params.Addresses, "--for")
	if err != nil {
		return ListedDelivery{}, err
	}

	var deadline time.Time
	if params.Timeout > 0 {
		deadline = time.Now().Add(params.Timeout)
	}

	delay := initialPollDelay
	for {
		attemptCtx := ctx
		cancel := func() {}
		if !deadline.IsZero() {
			attemptCtx, cancel = context.WithDeadline(ctx, deadline)
		}
		delivery, err := s.waitOnce(attemptCtx, addresses)
		cancel()
		if err == nil {
			return delivery, nil
		}
		if !deadline.IsZero() && errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return ListedDelivery{}, ErrNoMessage
		}
		if !errors.Is(err, ErrNoMessage) {
			return ListedDelivery{}, err
		}
		if !deadline.IsZero() {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return ListedDelivery{}, ErrNoMessage
			}
			if delay > remaining {
				delay = remaining
			}
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ListedDelivery{}, ctx.Err()
		case <-timer.C:
		}

		if delay < maxPollDelay {
			delay *= 2
			if delay > maxPollDelay {
				delay = maxPollDelay
			}
		}
	}
}

func (s *Store) Watch(ctx context.Context, params WatchParams, emit func(ListedDelivery) error) error {
	addresses, err := normalizeAddresses(params.Address, params.Addresses, "--for")
	if err != nil {
		return err
	}

	state := strings.TrimSpace(params.State)
	emitted := make(map[string]string)
	idleDeadline := time.Time{}
	if params.Timeout > 0 {
		idleDeadline = time.Now().Add(params.Timeout)
	}

	delay := initialPollDelay
	availability := s.availability()
	for {
		scope, err := availability.resolvePersonal(ctx, s.readDB, addresses)
		if err != nil {
			return err
		}

		deliveries, err := availability.listPersonalDeliveries(ctx, s.readDB, scope, state, formatTimestamp(s.now()))
		if err != nil {
			return err
		}

		observedNewDelivery := false
		for _, delivery := range deliveries {
			fingerprint := watchFingerprint(delivery)
			if emitted[delivery.DeliveryID] == fingerprint {
				continue
			}
			if err := emit(delivery); err != nil {
				return err
			}
			emitted[delivery.DeliveryID] = fingerprint
			observedNewDelivery = true
		}

		if observedNewDelivery {
			delay = initialPollDelay
			if params.Timeout > 0 {
				idleDeadline = time.Now().Add(params.Timeout)
			}
		} else if delay < maxPollDelay {
			delay *= 2
			if delay > maxPollDelay {
				delay = maxPollDelay
			}
		}

		waitFor := delay
		if !idleDeadline.IsZero() {
			remaining := time.Until(idleDeadline)
			if remaining <= 0 {
				return nil
			}
			if waitFor > remaining {
				waitFor = remaining
			}
		}

		timer := time.NewTimer(waitFor)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Store) waitOnce(ctx context.Context, addresses []string) (ListedDelivery, error) {
	scope, err := s.availability().resolvePersonal(ctx, s.readDB, addresses)
	if err != nil {
		return ListedDelivery{}, err
	}

	deliveries, err := s.availability().listPersonalDeliveries(ctx, s.readDB, scope, "", formatTimestamp(s.now()))
	if err != nil {
		return ListedDelivery{}, err
	}
	if len(deliveries) == 0 {
		return ListedDelivery{}, ErrNoMessage
	}

	return deliveries[0], nil
}

func watchFingerprint(delivery ListedDelivery) string {
	senderEndpointID := ""
	if delivery.SenderEndpointID != nil {
		senderEndpointID = *delivery.SenderEndpointID
	}

	parts := []string{
		delivery.MessageID,
		delivery.RecipientAddress,
		delivery.RecipientEndpointID,
		senderEndpointID,
		delivery.State,
		delivery.VisibleAt,
		delivery.MessageCreatedAt,
		delivery.Subject,
		delivery.ContentType,
		delivery.SchemaVersion,
		delivery.BodyBlobRef,
		strconv.FormatInt(delivery.BodySize, 10),
		delivery.BodySHA256,
	}
	return strings.Join(parts, "\x00")
}
