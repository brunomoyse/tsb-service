#!/usr/bin/env bash
# Self-test of main-green.sh against canned GitHub responses: it must accept a
# fully tested commit and reject each way a commit can be untested.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

run() { # name status conclusion event id
  printf '{"databaseId":%s,"workflowName":"%s","status":"%s","conclusion":"%s","event":"%s","createdAt":"2026-10-24T10:00:00Z","url":"u%s"}' "$5" "$1" "$2" "$3" "$4" "$5"
}
fixture() { # case-name runs-json jobs-json
  mkdir -p "$tmp/$1"
  printf '[%s]' "$2" > "$tmp/$1/runs.json"
  printf '%s' "$3" > "$tmp/$1/jobs-1.json"
}
noise="$(run 'CI watch' in_progress '' workflow_run 2),$(run 'Release (tag + deploy)' in_progress '' workflow_dispatch 3),$(run 'Security scan' completed failure schedule 4)"
tests_ok='[{"name":"tests / Run Tests","status":"completed","conclusion":"success"},{"name":"Build Test Image","status":"completed","conclusion":"success"}]'

fixture green "$(run 'Build and Deploy' completed success push 1),$noise" "$tests_ok"
fixture no-gate-run "$(run 'Other' completed success push 1),$noise" "$tests_ok"
fixture no-runs "$noise" "$tests_ok"
fixture pending "$(run 'Build and Deploy' in_progress '' push 1),$noise" "$tests_ok"
fixture failed-run "$(run 'Build and Deploy' completed failure push 1),$noise" "$tests_ok"
fixture tests-failed "$(run 'Build and Deploy' completed success push 1),$noise" '[{"name":"tests / Run Tests","status":"completed","conclusion":"failure"}]'
fixture tests-missing "$(run 'Build and Deploy' completed success push 1),$noise" '[{"name":"Build Test Image","status":"completed","conclusion":"success"}]'
fixture tests-all-skipped "$(run 'Build and Deploy' completed success push 1),$noise" '[{"name":"tests / Run Tests","status":"completed","conclusion":"skipped"}]'
fixture other-workflow-red "$(run 'Build and Deploy' completed success push 1),$(run 'CodeQL' completed failure dynamic 5),$noise" "$tests_ok"

fail=0
expect() { # case expected-exit
  set +e
  MAIN_GREEN_FIXTURES="$tmp/$1" bash "$here/main-green.sh" abc123 >"$tmp/out" 2>&1
  rc=$?
  set -e
  if [ "$rc" -ne "$2" ]; then
    echo "FAIL $1: expected exit $2, got $rc"; cat "$tmp/out"; fail=1
  else
    echo "ok   $1"
  fi
}
expect green 0
for c in no-gate-run no-runs pending failed-run tests-failed tests-missing tests-all-skipped other-workflow-red; do expect "$c" 1; done
exit "$fail"
