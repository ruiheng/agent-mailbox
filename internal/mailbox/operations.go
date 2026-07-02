package mailbox

import (
	"context"
	"fmt"
	"time"
)

// Operations adds cross-adapter policy on top of Store.
type Operations struct {
	*Store
}

func NewOperations(store *Store) *Operations {
	return &Operations{Store: store}
}

func (o *Operations) ReceiveBatchWithLeaseTTL(ctx context.Context, params ReceiveBatchParams, ttl time.Duration) (ReceiveResult, error) {
	addresses, err := normalizeAddresses(params.Address, params.Addresses, "--for")
	if err != nil {
		return ReceiveResult{}, err
	}

	maxMessages := params.Max
	if maxMessages < 1 || maxMessages > maxReceiveBatchSize {
		return ReceiveResult{}, fmt.Errorf("--max must be between 1 and %d", maxReceiveBatchSize)
	}

	return o.Store.receiveBatchWithLeasePolicy(ctx, addresses, maxMessages, receiveLeasePolicy{LeaseTTL: ttl})
}

func (o *Operations) HasVisibleDelivery(ctx context.Context, params WaitParams) (bool, error) {
	addresses, err := normalizeAddresses(params.Address, params.Addresses, "--for")
	if err != nil {
		return false, err
	}
	return o.Store.hasClaimableDelivery(ctx, addresses)
}
