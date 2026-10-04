#!/usr/bin/env bash
# Statement-coverage gate for tsb-service.
#
#   scripts/coverage-check.sh [threshold-percent]     (default 0: report only)
#
# Runs the suite with cross-package coverage (-coverpkg=./internal/...,./pkg/...) over
# ./internal/... ./pkg/..., drops the files that are not hand-written production code from the
# profile, prints the total and exits non-zero when it is below the threshold.
#
# Excluded: gqlgen generated code, test support code and everything under cmd/.
# Extra arguments after the threshold are passed to `go test` (e.g. -run, -p 2).
set -euo pipefail

threshold="${1:-0}"
shift || true

cd "$(dirname "$0")/.."

profile="$(mktemp "${TMPDIR:-/tmp}/tsb-cover.XXXXXX")"
filtered="$(mktemp "${TMPDIR:-/tmp}/tsb-cover-filtered.XXXXXX")"
trap 'rm -f "$profile" "$filtered"' EXIT

go test -race -coverpkg=./internal/...,./pkg/... -coverprofile="$profile" "$@" ./internal/... ./pkg/...

# Profile lines look like: tsb-service/internal/x/y.go:12.3,15.2 3 1 (first line is "mode: ...").
exclude='^tsb-service/(internal/api/graphql/generated\.go|internal/api/graphql/model/model_gen\.go|internal/api/graphql/testhelpers/|internal/mcp/fakeupstream/|cmd/)'
awk -v re="$exclude" 'NR == 1 || $0 !~ re' "$profile" > "$filtered"

# Statement-weighted total over the filtered profile. Blocks listed several times (one per test
# binary) count once, as covered when any binary covered them. This is what `go tool cover` does.
total="$(go tool cover -func="$filtered" | awk '/^total:/ {gsub("%", "", $3); print $3}')"

echo "coverage (excluding generated, test support and cmd/): ${total}%  (threshold ${threshold}%)"

if awk -v t="$total" -v min="$threshold" 'BEGIN { exit !(t + 0 < min + 0) }'; then
  echo "FAIL: coverage ${total}% is below the required ${threshold}%" >&2
  exit 1
fi
