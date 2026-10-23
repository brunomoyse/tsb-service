// Package application holds the use cases of the HubRise integration: the order push (enqueue,
// outbox worker, reconciler) and the services that later tasks add. It depends on the domain ports
// and on pkg/hubrise only, never on another business module.
package application

import (
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
)

// statusPairs is the local to HubRise mapping of section 7.1 of the plan, in the outbound direction.
// PENDING and FAILED have no entry on purpose: they are never pushed as a status change.
var statusPairs = []domain.StatusPair{
	{Local: domain.LocalConfirmed, Remote: string(hubrise.StatusAccepted)},
	{Local: domain.LocalPreparing, Remote: string(hubrise.StatusInPreparation)},
	{Local: domain.LocalAwaitingPickUp, Remote: string(hubrise.StatusAwaitingCollection)},
	{Local: domain.LocalOutForDelivery, Remote: string(hubrise.StatusInDelivery)},
	{Local: domain.LocalPickedUp, Remote: string(hubrise.StatusCompleted)},
	{Local: domain.LocalDelivered, Remote: string(hubrise.StatusCompleted)},
	{Local: domain.LocalCancelled, Remote: string(hubrise.StatusCancelled)},
}

// StatusPairs returns the local to HubRise status mapping, for queries that compare the two sides.
func StatusPairs() []domain.StatusPair { return append([]domain.StatusPair(nil), statusPairs...) }

// RemoteStatusFor returns the HubRise status that mirrors a local order status, or "" when the
// status is never pushed (PENDING, FAILED, unknown values).
func RemoteStatusFor(local string) hubrise.OrderStatus {
	for _, p := range statusPairs {
		if p.Local == local {
			return hubrise.OrderStatus(p.Remote)
		}
	}
	return ""
}

// initialRemoteStatus is the status an order is created with at HubRise: the mirror of its current
// local status, "new" for an order nobody has touched yet. "" means the order must not be created
// (it is failed, or its status is unknown).
func initialRemoteStatus(local string) hubrise.OrderStatus {
	if local == domain.LocalPending {
		return hubrise.StatusNew
	}
	return RemoteStatusFor(local)
}

// LocalStatusFor is the inbound direction of the mapping: the local status a HubRise status asks
// for on an order of the given type, "" when it asks for none. new and received change nothing,
// awaiting_collection means nothing for a delivery (a "ready for pickup" e-mail would be wrong),
// delivery_failed is never applied by itself, and the deprecated awaiting_shipment reads as
// awaiting_collection. rejected and cancelled both cancel.
func LocalStatusFor(remote hubrise.OrderStatus, orderType string) string {
	switch remote.Canonical() {
	case hubrise.StatusAccepted:
		return domain.LocalConfirmed
	case hubrise.StatusInPreparation:
		return domain.LocalPreparing
	case hubrise.StatusAwaitingCollection:
		if orderType == domain.TypePickUp {
			return domain.LocalAwaitingPickUp
		}
	case hubrise.StatusInDelivery:
		return domain.LocalOutForDelivery
	case hubrise.StatusCompleted:
		if orderType == domain.TypeDelivery {
			return domain.LocalDelivered
		}
		return domain.LocalPickedUp
	case hubrise.StatusCancelled, hubrise.StatusRejected:
		return domain.LocalCancelled
	}
	return ""
}
