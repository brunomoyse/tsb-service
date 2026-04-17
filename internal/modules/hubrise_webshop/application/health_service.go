package application

import (
	"context"
	"fmt"
	"time"

	"tsb-service/internal/modules/hubrise_webshop/domain"
)

// HealthStatus summarises the HubRise integration posture.
type HealthStatus string

const (
	HealthOK       HealthStatus = "ok"
	HealthDegraded HealthStatus = "degraded"
	HealthDown     HealthStatus = "down"
)

// Thresholds below are tuned for a single-location TSB; revisit if
// throughput grows or if operators complain about false positives.
const (
	healthFailedMinAttempts = 5                // count only orders with attempts >= 5
	healthStuckPendingAge   = 2 * time.Minute  // stuck-pending threshold
	healthDownThreshold     = 5                // failed + stuck count that flips to "down"
	healthDegradedThreshold = 1                // any failure is a degradation
	healthStaleSuccessAge   = 15 * time.Minute // 15-min success gap → degraded
)

// HealthSnapshot is the payload returned by the health endpoint and
// consumed by both the cron CLI and the dashboard banner.
type HealthSnapshot struct {
	Status                  HealthStatus `json:"status"`
	OrdersFailedCount       int          `json:"orders_failed_count"`
	OrdersStuckPendingCount int          `json:"orders_stuck_pending_count"`
	LastSuccessfulPushAge   *int         `json:"last_successful_push_age_seconds"`
	CatalogLastPushStatus   *string      `json:"catalog_last_push_status"`
	CatalogLastPushAge      *int         `json:"catalog_last_push_age_seconds"`
	GeneratedAt             time.Time    `json:"generated_at"`
	Reasons                 []string     `json:"reasons,omitempty"`
}

// HealthService aggregates the 4 signals the HubRise plan tracks:
// failed orders, stuck pending orders, last successful push age, and
// the catalog sync state.
type HealthService struct {
	pushRepo domain.OrderPushRepository
	syncRepo domain.CatalogSyncStateRepository
}

func NewHealthService(
	pushRepo domain.OrderPushRepository,
	syncRepo domain.CatalogSyncStateRepository,
) *HealthService {
	return &HealthService{pushRepo: pushRepo, syncRepo: syncRepo}
}

// Check runs the three database queries + one sync-state lookup and
// computes the overall status with a set of human-readable reasons.
// Never returns an error — on partial query failure, the snapshot
// contains whatever signals succeeded and the status is computed
// with the rest (best-effort).
func (s *HealthService) Check(ctx context.Context) (*HealthSnapshot, error) {
	snap := &HealthSnapshot{GeneratedAt: time.Now()}

	failed, err := s.pushRepo.CountFailed(ctx, healthFailedMinAttempts)
	if err == nil {
		snap.OrdersFailedCount = failed
	}

	stuck, err := s.pushRepo.CountStuckPending(ctx, healthStuckPendingAge)
	if err == nil {
		snap.OrdersStuckPendingCount = stuck
	}

	lastAge, _ := s.pushRepo.LastSuccessfulPushAgeSeconds(ctx)
	snap.LastSuccessfulPushAge = lastAge

	if syncState, err := s.syncRepo.Get(ctx, ClientName); err == nil && syncState != nil {
		if syncState.LastPushStatus != nil {
			copyStatus := *syncState.LastPushStatus
			snap.CatalogLastPushStatus = &copyStatus
		}
		if syncState.LastPushedAt != nil {
			age := int(time.Since(*syncState.LastPushedAt).Seconds())
			snap.CatalogLastPushAge = &age
		}
	}

	// Overall status computation — order matters: down wins over degraded.
	switch {
	case snap.OrdersFailedCount >= healthDownThreshold:
		snap.Status = HealthDown
		snap.Reasons = append(snap.Reasons,
			fmt.Sprintf("%d orders failed >=%d times", snap.OrdersFailedCount, healthFailedMinAttempts))
	case snap.OrdersStuckPendingCount >= healthDownThreshold:
		snap.Status = HealthDown
		snap.Reasons = append(snap.Reasons,
			fmt.Sprintf("%d orders stuck pending >%s", snap.OrdersStuckPendingCount, healthStuckPendingAge))
	case snap.OrdersFailedCount >= healthDegradedThreshold:
		snap.Status = HealthDegraded
		snap.Reasons = append(snap.Reasons,
			fmt.Sprintf("%d failed orders", snap.OrdersFailedCount))
	case snap.OrdersStuckPendingCount >= healthDegradedThreshold:
		snap.Status = HealthDegraded
		snap.Reasons = append(snap.Reasons,
			fmt.Sprintf("%d stuck pending orders", snap.OrdersStuckPendingCount))
	case lastAge != nil && *lastAge > int(healthStaleSuccessAge.Seconds()):
		snap.Status = HealthDegraded
		snap.Reasons = append(snap.Reasons,
			fmt.Sprintf("no successful push in %s", healthStaleSuccessAge))
	default:
		snap.Status = HealthOK
	}

	if snap.CatalogLastPushStatus != nil && *snap.CatalogLastPushStatus == "failed" && snap.Status == HealthOK {
		snap.Status = HealthDegraded
		snap.Reasons = append(snap.Reasons, "catalog last push failed")
	}

	return snap, nil
}
