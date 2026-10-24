#!/usr/bin/env bash
# Post-deploy smoke test of a live Pili site, run by the deploy workflows right after
# `helm upgrade` (a failure makes the job `helm rollback` the release).
#
#   scripts/smoke.sh [--skip-unset] <check> <base-url> [<check> <base-url> ...]
#
# --skip-unset: a check whose URL is empty (a repository variable that is not set) is skipped with
# a ::warning:: instead of failing. The deploy workflows use it: a missing variable must not roll
# back a healthy release, it only means the release is not verified.
#
# Checks:
#   api        <API_BASE_URL>   GET <url>/up answers 2xx, and the public GraphQL query
#                               { productCategories { id } } at <url>/graphql lists categories
#                               (the API reaches its database and serves the menu).
#                               <url> is the API root, e.g. https://example.be/api/v1
#   up         <API_BASE_URL>   Only GET <url>/up (a server whose database may be empty, e.g. the test
#                               server).
#   shop       <SITE_URL>       GET <url>/fr answers 2xx with the server-rendered Nuxt payload.
#   dashboard  <DASHBOARD_URL>  GET <url>/healthz answers 2xx.
#
# Every check is retried (new pods, ingress and Cloudflare need a moment to settle):
#   SMOKE_ATTEMPTS  tries per check                   (default 10)
#   SMOKE_DELAY     seconds between two tries         (default 15)
#   SMOKE_TIMEOUT   seconds allowed to each request   (default 20)
#
# Exit status: 0 every check passed, 1 a check failed, 2 bad usage or missing tool.
set -uo pipefail

attempts="${SMOKE_ATTEMPTS:-10}"
delay="${SMOKE_DELAY:-15}"
timeout="${SMOKE_TIMEOUT:-20}"

usage() {
  sed -n '2,/^set -uo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//' >&2
  exit 2
}

skip_unset=0
if [ "${1:-}" = "--skip-unset" ]; then
  skip_unset=1
  shift
fi

for tool in curl jq; do
  command -v "$tool" >/dev/null 2>&1 || { echo "smoke: $tool is required" >&2; exit 2; }
done

[ "$#" -ge 2 ] && [ $(($# % 2)) -eq 0 ] || usage

# fetch <curl args...>: the body on stdout, non-zero on a transport error or a non-2xx answer.
fetch() {
  curl -fsS --max-time "$timeout" "$@"
}

check_api() {
  local base="$1"
  fetch -o /dev/null "$base/up" || return 1
  fetch -X POST "$base/graphql" \
    -H 'content-type: application/json' \
    -d '{"query":"{ productCategories { id } }"}' \
    | jq -e '.data.productCategories | length > 0' >/dev/null
}

check_up() {
  fetch -o /dev/null "$1/up"
}

check_shop() {
  # The query string skips any cached copy of the page.
  fetch "$1/fr?smoke=${GITHUB_RUN_ID:-$$}" | grep -q '__NUXT_DATA__'
}

check_dashboard() {
  fetch -o /dev/null "$1/healthz"
}

run_check() {
  case "$1" in
    api) check_api "$2" ;;
    up) check_up "$2" ;;
    shop) check_shop "$2" ;;
    dashboard) check_dashboard "$2" ;;
  esac
}

failed=0
while [ "$#" -gt 0 ]; do
  name="$1"
  url="${2%/}"
  shift 2
  case "$name" in
    api | up | shop | dashboard) ;;
    *) echo "smoke: unknown check '$name'" >&2; usage ;;
  esac
  if [ -z "$url" ] && [ "$skip_unset" -eq 1 ]; then
    echo "::warning::smoke: the URL of the '$name' check is not set (repository variable missing): skipped, not verified"
    continue
  fi
  case "$url" in
    http://* | https://*) ;;
    *) echo "smoke: '$url' is not an http(s) URL (is the repository variable set?)" >&2; exit 2 ;;
  esac

  passed=0
  for i in $(seq 1 "$attempts"); do
    if run_check "$name" "$url"; then
      echo "smoke: $name $url passed (attempt $i/$attempts)"
      passed=1
      break
    fi
    echo "smoke: $name $url failed (attempt $i/$attempts)"
    if [ "$i" -lt "$attempts" ]; then sleep "$delay"; fi
  done
  if [ "$passed" -ne 1 ]; then
    echo "smoke: $name $url FAILED after $attempts attempts" >&2
    failed=1
  fi
done

exit "$failed"
