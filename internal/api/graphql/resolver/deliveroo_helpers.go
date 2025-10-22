package resolver

import (
	"time"
	"tsb-service/internal/api/graphql/model"
	"tsb-service/services/deliveroo"

	"github.com/google/uuid"
)

// Helper functions for converting between Deliveroo types and GraphQL model types

func convertDeliverooOrderToPlatformOrder(order *deliveroo.Order, source model.OrderSource) *model.PlatformOrder {
	platformOrder := &model.PlatformOrder{
		Source:               source,
		PlatformOrderID:      uuid.MustParse(order.ID),
		OrderNumber:          order.OrderNumber,
		DisplayID:            order.DisplayID,
		LocationID:           order.LocationID,
		BrandID:              order.BrandID,
		Status:               convertDeliverooStatusToPlatformStatus(order.Status),
		FulfillmentType:      convertDeliverooFulfillmentType(order.FulfillmentType),
		OrderNotes:           stringPtr(order.OrderNotes),
		CutleryNotes:         stringPtr(order.CutleryNotes),
		Asap:                 order.ASAP,
		PrepareFor:           order.PrepareFor,
		StartPreparingAt:     order.StartPreparingAt,
		ConfirmAt:            order.ConfirmAt,
		TableNumber:          stringPtr(order.TableNumber),
		Subtotal:             convertMonetaryAmount(&order.Subtotal),
		TotalPrice:           convertMonetaryAmount(&order.TotalPrice),
		PartnerOrderSubtotal: convertMonetaryAmount(&order.PartnerOrderSubtotal),
		PartnerOrderTotal:    convertMonetaryAmount(&order.PartnerOrderTotal),
		OfferDiscount:        convertMonetaryAmount(&order.OfferDiscount),
		CashDue:              convertMonetaryAmount(&order.CashDue),
		BagFee:               convertMonetaryAmount(&order.BagFee),
		Surcharge:            convertMonetaryAmount(&order.Surcharge),
		Items:                convertDeliverooOrderItems(order.Items),
		Delivery:             convertDeliverooDeliveryDetails(order.Delivery),
		Customer:             convertDeliverooCustomer(order.Customer),
		StatusLog:            convertDeliverooStatusLog(order.StatusLog),
		Promotions:           convertDeliverooPromotions(order.Promotions),
		IsTabletless:         order.IsTabletless,
	}
	return platformOrder
}

func convertDeliverooStatusToPlatformStatus(status deliveroo.OrderStatus) model.PlatformOrderStatus {
	switch status {
	case deliveroo.OrderStatusPending:
		return model.PlatformOrderStatusPending
	case deliveroo.OrderStatusPlaced:
		return model.PlatformOrderStatusPlaced
	case deliveroo.OrderStatusAccepted:
		return model.PlatformOrderStatusAccepted
	case deliveroo.OrderStatusConfirmed:
		return model.PlatformOrderStatusConfirmed
	case deliveroo.OrderStatusRejected:
		return model.PlatformOrderStatusRejected
	case deliveroo.OrderStatusCanceled:
		return model.PlatformOrderStatusCanceled
	case deliveroo.OrderStatusDelivered:
		return model.PlatformOrderStatusDelivered
	default:
		return model.PlatformOrderStatusPending
	}
}

func convertDeliverooFulfillmentType(ft deliveroo.FulfillmentType) model.FulfillmentType {
	switch ft {
	case deliveroo.FulfillmentDeliveroo:
		return model.FulfillmentTypePlatformDelivery
	case deliveroo.FulfillmentRestaurant:
		return model.FulfillmentTypeRestaurantDelivery
	case deliveroo.FulfillmentCustomer:
		return model.FulfillmentTypeCustomerPickup
	case deliveroo.FulfillmentTableService:
		return model.FulfillmentTypeTableService
	case deliveroo.FulfillmentAutonomous:
		return model.FulfillmentTypeAutonomous
	default:
		return model.FulfillmentTypePlatformDelivery
	}
}

func convertMonetaryAmount(amount *deliveroo.MonetaryAmount) *model.MonetaryAmount {
	if amount == nil {
		return nil
	}
	return &model.MonetaryAmount{
		Fractional:   amount.Fractional,
		CurrencyCode: amount.CurrencyCode,
	}
}

func convertMonetaryAmountPtr(amount *deliveroo.MonetaryAmount) *model.MonetaryAmount {
	if amount == nil {
		return nil
	}
	return convertMonetaryAmount(amount)
}

func convertDeliverooOrderItems(items []deliveroo.OrderItem) []*model.PlatformOrderItem {
	result := make([]*model.PlatformOrderItem, len(items))
	for i, item := range items {
		result[i] = &model.PlatformOrderItem{
			PosItemID:       item.PosItemID,
			Name:            item.Name,
			OperationalName: item.OperationalName,
			UnitPrice:       convertMonetaryAmount(&item.UnitPrice),
			TotalPrice:      convertMonetaryAmount(&item.TotalPrice),
			MenuUnitPrice:   convertMonetaryAmount(&item.MenuUnitPrice),
			Quantity:        item.Quantity,
			Modifiers:       convertDeliverooOrderItems(item.Modifiers),
			DiscountAmount:  convertMonetaryAmount(&item.DiscountAmount),
		}
	}
	return result
}

func convertDeliverooDeliveryDetails(delivery *deliveroo.DeliveryDetails) *model.PlatformDeliveryDetails {
	if delivery == nil {
		return nil
	}
	return &model.PlatformDeliveryDetails{
		DeliveryFee:         convertMonetaryAmount(&delivery.DeliveryFee),
		Address:             convertDeliverooAddress(delivery.Address),
		EstimatedDeliveryAt: delivery.EstimatedDeliveryAt,
	}
}

func convertDeliverooAddress(address *deliveroo.DeliveryAddress) *model.PlatformAddress {
	if address == nil {
		return nil
	}
	return &model.PlatformAddress{
		Street:       address.Street,
		Number:       address.Number,
		PostalCode:   address.PostalCode,
		City:         address.City,
		AddressLine1: address.AddressLine1,
		AddressLine2: stringPtr(address.AddressLine2),
		Latitude:     address.Latitude,
		Longitude:    address.Longitude,
	}
}

func convertDeliverooCustomer(customer *deliveroo.CustomerDetails) *model.PlatformCustomer {
	if customer == nil {
		return nil
	}
	return &model.PlatformCustomer{
		FirstName:            stringPtr(customer.FirstName),
		ContactNumber:        stringPtr(customer.ContactNumber),
		ContactAccessCode:    stringPtr(customer.ContactAccessCode),
		OrderFrequencyAtSite: stringPtr(customer.OrderFrequencyAtSite),
	}
}

func convertDeliverooStatusLog(logs []deliveroo.StatusLogItem) []*model.PlatformStatusLogItem {
	result := make([]*model.PlatformStatusLogItem, len(logs))
	for i, log := range logs {
		result[i] = &model.PlatformStatusLogItem{
			At:     log.At,
			Status: convertDeliverooStatusToPlatformStatus(log.Status),
		}
	}
	return result
}

func convertDeliverooPromotions(promotions []deliveroo.Promotion) []*model.PlatformPromotion {
	result := make([]*model.PlatformPromotion, len(promotions))
	for i, promo := range promotions {
		result[i] = &model.PlatformPromotion{
			ID:     uuid.MustParse(promo.ID),
			Name:   promo.Name,
			Amount: convertMonetaryAmount(&promo.Amount),
		}
	}
	return result
}

func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func convertGraphQLPrepStageToDeliveroo(stage model.PrepStage) deliveroo.PrepStage {
	switch stage {
	case model.PrepStageInKitchen:
		return deliveroo.PrepStageInKitchen
	case model.PrepStageReadyForCollectionSoon:
		return deliveroo.PrepStageReadyForCollectionSoon
	case model.PrepStageReadyForCollection:
		return deliveroo.PrepStageReadyForCollection
	case model.PrepStageCollected:
		return deliveroo.PrepStageCollected
	default:
		return deliveroo.PrepStageInKitchen
	}
}

func convertGraphQLAvailabilityStatusToDeliveroo(status model.ItemAvailabilityStatus) deliveroo.UnavailabilityStatus {
	switch status {
	case model.ItemAvailabilityStatusAvailable:
		return deliveroo.StatusAvailable
	case model.ItemAvailabilityStatusUnavailable:
		return deliveroo.StatusUnavailable
	case model.ItemAvailabilityStatusHidden:
		return deliveroo.StatusHidden
	default:
		return deliveroo.StatusAvailable
	}
}

func convertDeliverooUnavailabilityList(response *deliveroo.GetItemUnavailabilitiesResponse) *model.ItemUnavailabilityList {
	unavailableIDs := make([]uuid.UUID, len(response.UnavailableIDs))
	for i, id := range response.UnavailableIDs {
		unavailableIDs[i] = uuid.MustParse(id)
	}

	hiddenIDs := make([]uuid.UUID, len(response.HiddenIDs))
	for i, id := range response.HiddenIDs {
		hiddenIDs[i] = uuid.MustParse(id)
	}

	return &model.ItemUnavailabilityList{
		UnavailableIds: unavailableIDs,
		HiddenIds:      hiddenIDs,
	}
}

func convertServicePreviewToGraphQL(preview *deliveroo.MenuSyncPreview) *model.MenuSyncPreview {
	result := &model.MenuSyncPreview{
		ToCreate: make([]*model.ProductToCreate, len(preview.ToCreate)),
		ToUpdate: make([]*model.ProductToUpdate, len(preview.ToUpdate)),
		ToDelete: make([]*model.ProductToDelete, len(preview.ToDelete)),
	}

	for i, item := range preview.ToCreate {
		result.ToCreate[i] = &model.ProductToCreate{
			Name:        item.Name,
			Price:       item.Price,
			Description: item.Description,
			Category:    item.Category,
			IsAvailable: item.IsAvailable,
			IsVisible:   item.IsVisible,
		}
	}

	// Convert items to update
	for i, item := range preview.ToUpdate {
		result.ToUpdate[i] = &model.ProductToUpdate{
			ID:                  item.ID,
			Name:                item.Name,
			CurrentPrice:        item.CurrentPrice,
			NewPrice:            item.NewPrice,
			CurrentDescription:  item.CurrentDescription,
			NewDescription:      item.NewDescription,
			CurrentAvailability: item.CurrentAvailability,
			NewAvailability:     item.NewAvailability,
			CurrentVisibility:   item.CurrentVisibility,
			NewVisibility:       item.NewVisibility,
		}
	}

	// Convert items to delete
	for i, item := range preview.ToDelete {
		result.ToDelete[i] = &model.ProductToDelete{
			ID:     item.ID,
			Name:   item.Name,
			Reason: item.Reason,
		}
	}

	return result
}

func convertGraphQLPlatformStatusToDeliveroo(status model.PlatformOrderStatus) string {
	switch status {
	case model.PlatformOrderStatusPending:
		return "pending"
	case model.PlatformOrderStatusPlaced:
		return "placed"
	case model.PlatformOrderStatusAccepted:
		return "accepted"
	case model.PlatformOrderStatusConfirmed:
		return "confirmed"
	case model.PlatformOrderStatusRejected:
		return "rejected"
	case model.PlatformOrderStatusCanceled:
		return "canceled"
	case model.PlatformOrderStatusDelivered:
		return "delivered"
	default:
		return "pending"
	}
}