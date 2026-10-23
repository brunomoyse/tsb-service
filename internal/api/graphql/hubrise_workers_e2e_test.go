package graphql_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	hubApp "tsb-service/internal/modules/hubrise/application"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// Run starts every worker of the integration. The singleton jobs announce themselves by taking
// their lease as soon as they start, so the lease table shows which of them are running: the
// reconciler, the callback poller, and the three jobs of the inventory side (the 30 minute read, the
// sold-out sweeper and the catalog drift check). The outbox drain and the callback event processor
// have no lease; the rollout and status tests only pass when they run (the order never leaves, and the
// EPOS status never arrives, without them), and the unit test of the wiring lists all five by name.
func TestHubriseRunStartsTheLeasedWorkers(t *testing.T) {
	env := setupCovEnv(t, covOptions{})
	r := env.withHubriseInstalled(t, hubrisetest.New(t))
	require.Zero(t, countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_job_leases`), "nothing runs before Run")

	r.run()

	for _, lease := range []string{
		hubApp.LeaseReconciler, hubApp.LeaseCallbackPoller,
		hubApp.LeaseInventoryPoll, hubApp.LeaseInventorySweeper, hubApp.LeaseCatalogDrift,
	} {
		require.Eventually(t, func() bool {
			return countRows(t, env.TestContext, `SELECT count(*) FROM hubrise_job_leases WHERE name = $1`, lease) == 1
		}, 10*time.Second, 20*time.Millisecond, "the worker of the lease %q never started", lease)
	}
}
