# Targets for the tsb-mcp server (cmd/tsb-mcp, internal/mcp). The backend
# itself keeps its commands in CLAUDE.md / README.md.

GOLANGCI_LINT ?= golangci-lint
MCP_IMAGE     ?= ghcr.io/brunomoyse/tsb-mcp:dev
MCP_ENV       ?= .env.mcp

.PHONY: mcp-build mcp-test mcp-lint mcp-run mcp-run-http mcp-docker

mcp-build:
	CGO_ENABLED=0 go build -trimpath -o bin/tsb-mcp ./cmd/tsb-mcp

mcp-test:
	go test -race ./internal/mcp/... ./cmd/tsb-mcp/...

mcp-lint:
	$(GOLANGCI_LINT) run --timeout=5m ./internal/mcp/... ./cmd/tsb-mcp/...

# stdio transport (for the MCP Inspector or a local agent). Reads $(MCP_ENV).
mcp-run: mcp-build
	set -a; . ./$(MCP_ENV); set +a; ./bin/tsb-mcp --internal :8081

# Streamable HTTP on :8080 + internal API on :8081.
mcp-run-http: mcp-build
	set -a; . ./$(MCP_ENV); set +a; ./bin/tsb-mcp --http :8080 --internal :8081

mcp-docker:
	docker build -f Dockerfile.mcp -t $(MCP_IMAGE) .
