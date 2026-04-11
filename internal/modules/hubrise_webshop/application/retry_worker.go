package application

import (
	"context"
	"time"

	"go.uber.org/zap"

	"tsb-service/internal/modules/hubrise_webshop/domain"
)

// RetryWorkerInterval is how often the worker polls Postgres for
// retryable orders. Each tick runs a single bounded batch.
const RetryWorkerInterval = 60 * time.Second

// RetryBatchSize caps the number of orders retried per tick to avoid
// synchronous HTTP storms against HubRise during recovery windows.
const RetryBatchSize = 20

// RetryWorker is the background goroutine that drives the HubRise
// order push retry queue. It sleeps on a ticker, calls
// ListRetryable to select ready-to-retry orders, and replays each
// one through OrderPusher.PushOrder.
//
// The backoff logic lives in SQL (see
// OrderPushRepository.ListRetryable) so concurrent workers —
// hypothetically, though we only run one today — would not
// double-attempt the same order within a window.
type RetryWorker struct {
	pushRepo domain.OrderPushRepository
	pusher   *OrderPusher
	loader   OrderLoader
}

// NewRetryWorker wires the worker.
func NewRetryWorker(
	pushRepo domain.OrderPushRepository,
	pusher *OrderPusher,
	loader OrderLoader,
) *RetryWorker {
	return &RetryWorker{
		pushRepo: pushRepo,
		pusher:   pusher,
		loader:   loader,
	}
}

// Run blocks until ctx is cancelled. Intended to be spawned as a
// goroutine from main.go, with ctx wired to the app's shutdown signal.
func (w *RetryWorker) Run(ctx context.Context) {
	logger := zap.L().With(zap.String("worker", "hubrise_retry"))
	logger.Info("retry worker started",
		zap.Duration("interval", RetryWorkerInterval),
		zap.Int("batch_size", RetryBatchSize),
		zap.Int("max_attempts", MaxRetryAttempts))

	ticker := time.NewTicker(RetryWorkerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("retry worker stopped")
			return
		case <-ticker.C:
			w.tick(ctx, logger)
		}
	}
}

func (w *RetryWorker) tick(ctx context.Context, logger *zap.Logger) {
	ids, err := w.pushRepo.ListRetryable(ctx, MaxRetryAttempts, RetryBatchSize)
	if err != nil {
		logger.Error("list retryable orders failed", zap.Error(err))
		return
	}
	if len(ids) == 0 {
		return
	}

	logger.Info("retrying orders", zap.Int("count", len(ids)))
	for _, id := range ids {
		if _, err := w.pusher.PushOrder(ctx, w.loader, id); err != nil {
			logger.Warn("retry still failing",
				zap.String("order_id", id.String()),
				zap.Error(err))
		}
	}
}
