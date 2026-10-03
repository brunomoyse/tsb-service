// Command tsb-mcp is the MCP server that lets the owner's chat agent run the
// TSB dashboard: it exposes narrow tools over stdio (default) or Streamable
// HTTP, and a separate internal API to apply or reject pending changes.
package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"tsb-service/internal/mcp/actions"
	"tsb-service/internal/mcp/changes"
	"tsb-service/internal/mcp/config"
	"tsb-service/internal/mcp/internalapi"
	"tsb-service/internal/mcp/tools"
	"tsb-service/internal/mcp/upstream"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "tsb-mcp:", err)
		os.Exit(1)
	}
}

func run() error {
	httpAddr := flag.String("http", "", "serve MCP over Streamable HTTP on this address (e.g. :8080) instead of stdio")
	internalAddr := flag.String("internal", "", "serve the internal apply/reject API on this address (e.g. :8081)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if *httpAddr != "" && cfg.MCPAuthToken == "" {
		return errors.New("configuration: MCP_AUTH_TOKEN is required with --http")
	}

	// Logs always go to stderr: stdout carries the stdio MCP transport.
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := changes.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	sa := upstream.ServiceAccount{Issuer: cfg.ZitadelIssuer, InternalURL: cfg.ZitadelInternalURL, ClientID: cfg.ZitadelClientID,
		ClientSecret: cfg.ZitadelClientSecret, ProjectID: cfg.ZitadelProjectID}
	up := upstream.New(cfg.UpstreamBaseURL, sa.TokenSource(context.WithoutCancel(ctx)), log.With("component", "upstream"))

	svc := actions.NewService(&actions.Env{Up: up, Loc: cfg.Location, PriceMaxPct: cfg.PriceMaxChangePct}, store, cfg.PendingTTL, log.With("component", "actions"))

	server := mcp.NewServer(&mcp.Implementation{Name: "tsb-mcp", Title: "Tokyo Sushi Bar dashboard", Version: version},
		&mcp.ServerOptions{Instructions: tools.Instructions, Logger: log.With("component", "mcp")})
	tools.Register(server, &tools.Deps{Svc: svc, Up: up, Loc: cfg.Location, Log: log.With("component", "tools")})

	errc := make(chan error, 2)
	var servers []*http.Server

	if *internalAddr != "" {
		srv := newHTTPServer(*internalAddr, internalapi.Handler(svc, cfg.InternalAPIToken, cfg.Location, log.With("component", "internalapi")))
		servers = append(servers, srv)
		go func() {
			log.Info("internal API listening", "addr", *internalAddr)
			errc <- serve(srv)
		}()
	} else {
		log.Warn("internal API disabled: pending changes cannot be applied (start with --internal :8081)")
	}

	if *httpAddr != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
		mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Logger: log.With("component", "mcp-http")})
		mux.Handle("/mcp", auth.RequireBearerToken(staticToken(cfg.MCPAuthToken), &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(mcpHandler))
		srv := newHTTPServer(*httpAddr, mux)
		servers = append(servers, srv)
		go func() {
			log.Info("MCP Streamable HTTP listening", "addr", *httpAddr, "path", "/mcp", "version", version)
			errc <- serve(srv)
		}()
	} else {
		go func() {
			log.Info("MCP stdio transport ready", "version", version)
			errc <- server.Run(ctx, &mcp.StdioTransport{})
		}()
	}

	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(shutdownCtx)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	log.Info("stopped")
	return nil
}

func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
}

func serve(s *http.Server) error {
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// staticToken verifies the MCP_AUTH_TOKEN bearer token in constant time.
func staticToken(secret string) auth.TokenVerifier {
	return func(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		if subtle.ConstantTimeCompare([]byte(token), []byte(secret)) != 1 {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{UserID: "agent"}, nil
	}
}
