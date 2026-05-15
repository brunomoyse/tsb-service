package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type customerStatsIn struct {
	StartDate *time.Time `json:"startDate,omitempty" jsonschema:"inclusive lower bound on order date"`
	EndDate   *time.Time `json:"endDate,omitempty"`
	OrderType *string    `json:"orderType,omitempty" jsonschema:"either 'delivery' or 'pickup'"`
	MinOrders *int       `json:"minOrders,omitempty" jsonschema:"only include customers with at least this many orders"`
}

type customerStatsRowOut struct {
	UserID             string     `json:"userId"`
	FirstName          string     `json:"firstName"`
	LastName           string     `json:"lastName"`
	Email              string     `json:"email"`
	PhoneNumber        *string    `json:"phoneNumber,omitempty"`
	RegisteredAt       time.Time  `json:"registeredAt"`
	TotalOrders        int        `json:"totalOrders"`
	TotalAmount        string     `json:"totalAmount"`
	AverageOrderAmount string     `json:"averageOrderAmount"`
	FirstOrderDate     time.Time  `json:"firstOrderDate"`
	LastOrderDate      time.Time  `json:"lastOrderDate"`
	PreferredOrderType string     `json:"preferredOrderType"`
	DeliveryCount      int        `json:"deliveryCount"`
	PickupCount        int        `json:"pickupCount"`
}

type customerStatsSummaryOut struct {
	TotalCustomers    int    `json:"totalCustomers"`
	TotalRevenue      string `json:"totalRevenue"`
	AverageOrderValue string `json:"averageOrderValue"`
	TotalOrders       int    `json:"totalOrders"`
}

type customerStatsOut struct {
	Summary   customerStatsSummaryOut `json:"summary"`
	Customers []customerStatsRowOut   `json:"customers"`
}

func registerAnalyticsTools(s *mcpsdk.Server, deps Deps) {
	mcpsdk.AddTool(s,
		&mcpsdk.Tool{
			Name:        "get_customer_stats",
			Description: "Get aggregated customer analytics: total revenue, average order value, and per-customer breakdown including delivery vs pickup split.",
		},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, args customerStatsIn) (*mcpsdk.CallToolResult, customerStatsOut, error) {
			var orderType *string
			if args.OrderType != nil {
				ot := strings.ToUpper(strings.TrimSpace(*args.OrderType))
				if ot != "DELIVERY" && ot != "PICKUP" {
					return errorResult("orderType must be 'delivery' or 'pickup'"), customerStatsOut{}, nil
				}
				orderType = &ot
			}

			rows, err := deps.Order.GetCustomerStats(ctx, args.StartDate, args.EndDate, orderType, args.MinOrders)
			if err != nil {
				return errorResult(fmt.Sprintf("fetch stats: %v", err)), customerStatsOut{}, nil
			}

			out := customerStatsOut{Customers: make([]customerStatsRowOut, len(rows))}
			totalRevenue := decimal.Zero
			totalOrders := 0

			for i, r := range rows {
				preferred := "PICKUP"
				if r.DeliveryCount > r.PickupCount {
					preferred = "DELIVERY"
				}
				out.Customers[i] = customerStatsRowOut{
					UserID:             r.UserID.String(),
					FirstName:          r.FirstName,
					LastName:           r.LastName,
					Email:              r.Email,
					PhoneNumber:        r.PhoneNumber,
					RegisteredAt:       r.RegisteredAt,
					TotalOrders:        r.TotalOrders,
					TotalAmount:        r.TotalAmount.StringFixed(2),
					AverageOrderAmount: r.AverageAmount.StringFixed(2),
					FirstOrderDate:     r.FirstOrderDate,
					LastOrderDate:      r.LastOrderDate,
					PreferredOrderType: preferred,
					DeliveryCount:      r.DeliveryCount,
					PickupCount:        r.PickupCount,
				}
				totalRevenue = totalRevenue.Add(r.TotalAmount)
				totalOrders += r.TotalOrders
			}

			avgOrderValue := decimal.Zero
			if totalOrders > 0 {
				avgOrderValue = totalRevenue.Div(decimal.NewFromInt(int64(totalOrders)))
			}

			out.Summary = customerStatsSummaryOut{
				TotalCustomers:    len(out.Customers),
				TotalRevenue:      totalRevenue.StringFixed(2),
				AverageOrderValue: avgOrderValue.StringFixed(2),
				TotalOrders:       totalOrders,
			}
			return nil, out, nil
		},
	)
}
