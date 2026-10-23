package main

import (
	"context"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tsb-service/internal/api/graphql/resolver"
	"tsb-service/internal/api/hubrisewire"
	"tsb-service/internal/modules/hubrise"
	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/shared/audit"
	"tsb-service/pkg/db"
	"tsb-service/pkg/email/scaleway"
)

// initHubrise installs the HubRise integration when it is configured on this deployment
// (HUBRISE_CLIENT_ID, HUBRISE_CLIENT_SECRET and HUBRISE_TOKEN_KEY all set) and returns it, or
// returns nil: the shop then runs exactly as a restaurant without HubRise, with no HubRise route,
// hook, observer or worker. A configuration that is wrong never stops the shop; it is logged as an
// error and the integration stays off.
//
// The returned runtime has its routes mounted on api; the caller runs its workers with Run and
// calls Stop when the server is down.
func initHubrise(api *gin.RouterGroup, r *resolver.Resolver, pool *db.DBPool, recorder *audit.Recorder, dashboardURL string) *hubrisewire.Runtime {
	cfg, err := hubrise.LoadConfig(os.Getenv)
	if err != nil {
		zap.L().Error("hubrise: invalid configuration, the integration stays off", zap.Error(err))
		return nil
	}
	if !cfg.Available() {
		if missing := cfg.Missing(); len(missing) < 3 {
			zap.L().Warn("hubrise: incomplete configuration, the integration stays off", zap.Strings("missing", missing))
		}
		return nil
	}
	mod, err := hubrise.NewModule(cfg, pool.Admin, nil)
	if err != nil {
		zap.L().Error("hubrise: cannot build the module, the integration stays off", zap.Error(err))
		return nil
	}
	opts := hubrisewire.Options{
		Module: mod, Resolver: r, APIBaseURL: os.Getenv("API_BASE_URL"), Audit: recorder,
		// The OAuth return lands on the HubRise page of the dashboard (the browser language redirect
		// of the dashboard adds the locale).
		DashboardURL: strings.TrimRight(dashboardURL, "/") + "/hubrise",
	}
	if to := os.Getenv("ASSISTANT_ALERT_EMAIL"); to != "" {
		opts.Alerter = emailAlerter{to: to}
	}
	rt, err := hubrisewire.Install(opts)
	if err != nil {
		zap.L().Error("hubrise: cannot install the integration, it stays off", zap.Error(err))
		return nil
	}
	rt.Register(api)
	zap.L().Info("hubrise: integration available", zap.Stringer("config", cfg))
	return rt
}

// emailAlerter emails the owner (ASSISTANT_ALERT_EMAIL) about a HubRise problem.
type emailAlerter struct{ to string }

var _ application.Alerter = emailAlerter{}

func (a emailAlerter) Alert(_ context.Context, subject, body string) error {
	return scaleway.SendOpsAlertEmail(a.to, subject, body)
}
