package mailbox

import (
	"context"
	"time"
)

// Operations exposes the typed mailbox behaviors shared by the CLI and MCP
// adapters without routing through CLI argv/stdout.
type Operations struct {
	store *Store
}

func NewOperations(store *Store) *Operations {
	return &Operations{store: store}
}

func (o *Operations) Send(ctx context.Context, params SendParams) (SendResult, error) {
	return o.store.Send(ctx, params)
}

func (o *Operations) List(ctx context.Context, params ListParams) ([]ListedDelivery, error) {
	return o.store.List(ctx, params)
}

func (o *Operations) ListGroupMessages(ctx context.Context, params GroupListParams) ([]GroupListedMessage, error) {
	return o.store.ListGroupMessages(ctx, params)
}

func (o *Operations) ListStaleAddresses(ctx context.Context, params StaleAddressesParams) ([]StaleAddress, error) {
	return o.store.ListStaleAddresses(ctx, params)
}

func (o *Operations) ReceiveBatch(ctx context.Context, params ReceiveBatchParams) (ReceiveResult, error) {
	return o.store.ReceiveBatch(ctx, params)
}

func (o *Operations) Wait(ctx context.Context, params WaitParams) (ListedDelivery, error) {
	return o.store.Wait(ctx, params)
}

func (o *Operations) ReadMessages(ctx context.Context, messageIDs []string) ([]ReadMessage, error) {
	return o.store.ReadMessages(ctx, messageIDs)
}

func (o *Operations) ReadLatestDeliveries(ctx context.Context, addresses []string, state string, limit int) ([]ReadDelivery, bool, error) {
	return o.store.ReadLatestDeliveries(ctx, addresses, state, limit)
}

func (o *Operations) ReadDeliveries(ctx context.Context, deliveryIDs []string) ([]ReadDelivery, error) {
	return o.store.ReadDeliveries(ctx, deliveryIDs)
}

func (o *Operations) Ack(ctx context.Context, deliveryID, leaseToken string) (DeliveryTransitionResult, error) {
	return o.store.Ack(ctx, deliveryID, leaseToken)
}

func (o *Operations) Renew(ctx context.Context, deliveryID, leaseToken string, extendBy time.Duration) (LeaseRenewResult, error) {
	return o.store.Renew(ctx, deliveryID, leaseToken, extendBy)
}

func (o *Operations) Release(ctx context.Context, deliveryID, leaseToken string) (DeliveryTransitionResult, error) {
	return o.store.Release(ctx, deliveryID, leaseToken)
}

func (o *Operations) Defer(ctx context.Context, deliveryID, leaseToken string, until time.Time) (DeliveryTransitionResult, error) {
	return o.store.Defer(ctx, deliveryID, leaseToken, until)
}

func (o *Operations) Fail(ctx context.Context, deliveryID, leaseToken, reason string) (DeliveryTransitionResult, error) {
	return o.store.Fail(ctx, deliveryID, leaseToken, reason)
}
