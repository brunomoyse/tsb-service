// Package domain holds the entities and ports of the HubRise integration: the connection of this
// installation to a HubRise location, the links between HubRise catalog entries and local rows, the
// durable outbox, the inbound event store and the order links. It has no external dependencies
// besides ids and decimals; repositories are implemented in the infrastructure package.
//
// HubRise is an additive layer: nothing here is on the critical path of placing an order, paying
// or serving the menu (see docs/hubrise/PLAN.md).
package domain
