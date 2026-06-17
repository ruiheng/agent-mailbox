package mailbox

import (
	"context"
	"fmt"
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

func (o *Operations) ListGroups(ctx context.Context) ([]GroupRecord, error) {
	return o.store.ListGroups(ctx)
}

func (o *Operations) ListGroupTranscript(ctx context.Context, params GroupTranscriptParams) ([]GroupTranscriptMessage, error) {
	return o.store.ListGroupTranscript(ctx, params)
}

func (o *Operations) WaitGroupMessage(ctx context.Context, params GroupWaitParams) (GroupListedMessage, error) {
	return o.store.WaitGroupMessage(ctx, params)
}

func (o *Operations) ReceiveGroupMessage(ctx context.Context, params GroupReceiveParams) (GroupReceivedMessage, error) {
	return o.store.ReceiveGroupMessage(ctx, params)
}

func (o *Operations) CreateGroup(ctx context.Context, groupAddress string) (GroupRecord, error) {
	return o.store.CreateGroup(ctx, groupAddress)
}

func (o *Operations) AddGroupMember(ctx context.Context, groupAddress, person string) (GroupMembershipRecord, error) {
	return o.store.AddGroupMember(ctx, groupAddress, person)
}

func (o *Operations) RemoveGroupMember(ctx context.Context, groupAddress, person string) (GroupMembershipRecord, error) {
	return o.store.RemoveGroupMember(ctx, groupAddress, person)
}

func (o *Operations) ListGroupMembers(ctx context.Context, groupAddress string) ([]GroupMembershipRecord, error) {
	return o.store.ListGroupMembers(ctx, groupAddress)
}

func (o *Operations) AddGroupNotificationSubscriber(ctx context.Context, groupAddress, notifyAddress, person string) (GroupNotificationSubscriberRecord, error) {
	return o.store.AddGroupNotificationSubscriber(ctx, groupAddress, notifyAddress, person)
}

func (o *Operations) RemoveGroupNotificationSubscriber(ctx context.Context, groupAddress, notifyAddress string) (GroupNotificationSubscriberRecord, error) {
	return o.store.RemoveGroupNotificationSubscriber(ctx, groupAddress, notifyAddress)
}

func (o *Operations) ListGroupNotificationSubscribers(ctx context.Context, groupAddress string) ([]GroupNotificationSubscriberRecord, error) {
	return o.store.ListGroupNotificationSubscribers(ctx, groupAddress)
}

func (o *Operations) InspectAddress(ctx context.Context, address string) (AddressInspection, error) {
	return o.store.InspectAddress(ctx, address)
}

func (o *Operations) ListStaleAddresses(ctx context.Context, params StaleAddressesParams) ([]StaleAddress, error) {
	return o.store.ListStaleAddresses(ctx, params)
}

func (o *Operations) ListClaimableAddresses(ctx context.Context, addresses []string) ([]ClaimableAddress, error) {
	return o.store.ListClaimableAddresses(ctx, addresses)
}

func (o *Operations) ReceiveBatch(ctx context.Context, params ReceiveBatchParams) (ReceiveResult, error) {
	return o.store.ReceiveBatch(ctx, params)
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

	return o.store.receiveBatchWithLeasePolicy(ctx, addresses, maxMessages, receiveLeasePolicy{LeaseTTL: ttl})
}

func (o *Operations) Wait(ctx context.Context, params WaitParams) (ListedDelivery, error) {
	return o.store.Wait(ctx, params)
}

func (o *Operations) HasVisibleDelivery(ctx context.Context, params WaitParams) (bool, error) {
	addresses, err := normalizeAddresses(params.Address, params.Addresses, "--for")
	if err != nil {
		return false, err
	}
	return o.store.hasClaimableDelivery(ctx, addresses)
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

func (o *Operations) Undefer(ctx context.Context, deliveryID string) (DeliveryTransitionResult, error) {
	return o.store.Undefer(ctx, deliveryID)
}

func (o *Operations) Fail(ctx context.Context, deliveryID, leaseToken, reason string) (DeliveryTransitionResult, error) {
	return o.store.Fail(ctx, deliveryID, leaseToken, reason)
}
