#!/usr/bin/env bash
# Boot smoke test of a built image, run by CI before the image is promoted to
# :latest or :production. Needs a Docker daemon able to run the image's
# platform and nothing else (no secret, no external service).
#
#   scripts/ci-smoke-image.sh api <image>   pili-api image
#   scripts/ci-smoke-image.sh mcp <image>   pili-mcp image
#
# api: starts a throwaway Postgres (same major as production), applies every
#      migration with the image's own tsb-migrate, then boots the real server
#      in APP_ENV=production against it and waits for GET /api/v1/up. Zitadel
#      is replaced by a static OIDC discovery document served by python http.server
#      (startup is fatal when discovery fails; the server never validates a
#      token during the smoke test).
# mcp: boots tsb-mcp over HTTP with dummy configuration and waits for /healthz.
set -euo pipefail

mode="${1:?usage: ci-smoke-image.sh api|mcp <image>}"
image="${2:?usage: ci-smoke-image.sh api|mcp <image>}"
pg_image="${SMOKE_PG_IMAGE:-postgres:18-alpine}"
oidc_image="${SMOKE_OIDC_IMAGE:-python:3.13-alpine}"
timeout_s="${SMOKE_TIMEOUT:-90}"

id="smoke-$$-$RANDOM"
net="$id-net"
cleanup() {
  local rc=$?
  if [ "$rc" -ne 0 ]; then
    for c in "$id-api" "$id-mcp" "$id-oidc" "$id-pg"; do
      if docker inspect "$c" >/dev/null 2>&1; then
        echo "::group::logs of $c"
        docker logs "$c" 2>&1 | tail -n 60 || true
        echo "::endgroup::"
      fi
    done
  fi
  docker rm -f "$id-api" "$id-mcp" "$id-oidc" "$id-pg" >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
  exit "$rc"
}
trap cleanup EXIT

docker network create "$net" >/dev/null

# Polls "$@" until it succeeds or the timeout expires; fails early when the
# container named by $WAIT_CONTAINER is no longer running.
wait_for() {
  local deadline=$((SECONDS + timeout_s))
  until "$@" >/dev/null 2>&1; do
    if [ -n "${WAIT_CONTAINER:-}" ] && [ "$(docker inspect -f '{{.State.Running}}' "$WAIT_CONTAINER" 2>/dev/null)" != "true" ]; then
      echo "::error::container $WAIT_CONTAINER exited before becoming ready"
      return 1
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "::error::not ready after ${timeout_s}s: $*"
      return 1
    fi
    sleep 1
  done
}

smoke_api() {
  docker run -d --name "$id-pg" --network "$net" \
    -e POSTGRES_PASSWORD=smoke -e POSTGRES_DB=tsb "$pg_image" >/dev/null
  WAIT_CONTAINER="$id-pg" wait_for docker exec "$id-pg" pg_isready -U postgres -d tsb
  # The first pg_isready answer can come from the init-time server that
  # restarts once; require a real query on the final server.
  WAIT_CONTAINER="$id-pg" wait_for docker exec "$id-pg" psql -U postgres -d tsb -c 'select 1'

  local db_env=(-e DB_HOST="$id-pg" -e DB_PORT=5432 -e DB_USERNAME=postgres -e DB_PASSWORD=smoke
    -e DB_DATABASE=tsb -e DB_SSL_MODE=disable)

  echo "== tsb-migrate -cmd=up"
  docker run --rm --network "$net" "${db_env[@]}" "$image" ./tsb-migrate -cmd=up >/dev/null 2>&1
  echo "== tsb-migrate -cmd=status"
  docker run --rm --network "$net" "${db_env[@]}" "$image" ./tsb-migrate -cmd=status 2>&1 | tail -n 3

  # Static OIDC discovery document served by a throwaway Python web server.
  local issuer="https://smoke-issuer.invalid"
  docker run -d --name "$id-oidc" --network "$net" --entrypoint sh "$oidc_image" -c '
    mkdir -p /tmp/www/.well-known
    cat > /tmp/www/.well-known/openid-configuration <<EOF
{"issuer":"'"$issuer"'","authorization_endpoint":"'"$issuer"'/authorize","token_endpoint":"'"$issuer"'/token","jwks_uri":"'"$issuer"'/keys","userinfo_endpoint":"'"$issuer"'/userinfo","introspection_endpoint":"'"$issuer"'/introspect","id_token_signing_alg_values_supported":["RS256"],"response_types_supported":["code"],"subject_types_supported":["public"]}
EOF
    echo "{\"keys\":[]}" > /tmp/www/keys
    exec python -m http.server 8080 --directory /tmp/www' >/dev/null

  WAIT_CONTAINER="$id-oidc" wait_for docker exec "$id-oidc" wget -qO- http://127.0.0.1:8080/.well-known/openid-configuration

  echo "== boot ./tsb-service (APP_ENV=production)"
  docker run -d --name "$id-api" --network "$net" "${db_env[@]}" \
    -e APP_ENV=production -e LOG_FORMAT=json \
    -e MOLLIE_API_TOKEN=test_smoke -e MOLLIE_TESTING=true \
    -e ZITADEL_ISSUER="$issuer" -e ZITADEL_INTERNAL_URL="http://$id-oidc:8080" \
    -e ZITADEL_CLIENT_ID=smoke-client -e ZITADEL_ADMIN_CLIENT_IDS=smoke-admin \
    -e TURNSTILE_SECRET_KEY=smoke -e POS_JWT_SECRET=smoke-smoke-smoke-smoke-smoke-smoke-smoke \
    -e SMTP_HOST="$id-oidc" -e SCW_SENDER_EMAIL=smoke@example.com \
    -e GOOGLE_MAPS_API_KEY=smoke -e APP_BASE_URL=https://smoke.example.com -e APP_DASHBOARD_URL=https://dash.example.com \
    "$image" >/dev/null
  WAIT_CONTAINER="$id-api" wait_for docker exec "$id-api" wget -qO- http://localhost:8080/api/v1/up
  echo "GET /api/v1/up answered."

  # A page of real GraphQL proves the schema, resolvers and migrated tables
  # line up (an empty menu is fine, an error is not).
  local body
  body=$(docker exec "$id-api" wget -qO- --header 'Content-Type: application/json' \
    --post-data '{"query":"{ productCategories { id } }"}' http://localhost:8080/api/v1/graphql)
  if ! grep -q '"data"' <<<"$body" || grep -q '"errors"' <<<"$body"; then
    echo "::error::GraphQL query failed on the booted image: $body"
    return 1
  fi
  echo "GraphQL answered: $body"
}

smoke_mcp() {
  docker run -d --name "$id-mcp" --network "$net" -p 127.0.0.1::8080 \
    -e UPSTREAM_BASE_URL=http://upstream.invalid/api/v1 \
    -e ZITADEL_ISSUER=https://smoke-issuer.invalid -e ZITADEL_CLIENT_ID=smoke \
    -e ZITADEL_CLIENT_SECRET=smoke -e ZITADEL_PROJECT_ID=smoke \
    -e MCP_AUTH_TOKEN=smoke -e INTERNAL_API_TOKEN=smoke \
    "$image" >/dev/null
  local port
  port=$(docker port "$id-mcp" 8080/tcp | head -n1 | sed 's/.*://')
  WAIT_CONTAINER="$id-mcp" wait_for curl -fsS "http://127.0.0.1:$port/healthz"
  echo "GET /healthz answered."
}

case "$mode" in
  api) smoke_api ;;
  mcp) smoke_mcp ;;
  *) echo "unknown mode: $mode" >&2; exit 2 ;;
esac
echo "Smoke test passed for $image ($mode)."
