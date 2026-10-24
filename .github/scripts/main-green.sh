#!/usr/bin/env bash
# Exits 0 only when a commit is fully green: every CI run on it finished
# successfully AND the test jobs of the "Build and Deploy" push run really ran
# and passed. Used by "Release (tag + deploy)" and "Nightly dependency release"
# so that nothing is tagged (and so deployed to production) from a commit that
# was never tested.
#
#   bash .github/scripts/main-green.sh <commit-sha>
#
# Environment:
#   GH_TOKEN, GH_REPO   used by the gh CLI (needs actions: read)
#   GATE_WORKFLOW       workflow that calls the test workflow (default "Build and Deploy")
#   GATE_JOB_PREFIX     name prefix of the called test jobs (default "tests / ")
#   MAIN_GREEN_FIXTURES directory with runs.json and jobs-<run id>.json to judge
#                       instead of calling GitHub (used by the self-test)
set -euo pipefail

sha="${1:?usage: main-green.sh <commit-sha>}"
gate_workflow="${GATE_WORKFLOW:-Build and Deploy}"
job_prefix="${GATE_JOB_PREFIX:-tests / }"

runs_for() {
  if [ -n "${MAIN_GREEN_FIXTURES:-}" ]; then
    cat "$MAIN_GREEN_FIXTURES/runs.json"
  else
    gh run list --commit "$sha" --limit 100 \
      --json databaseId,workflowName,status,conclusion,event,createdAt,url
  fi
}

jobs_for() {
  if [ -n "${MAIN_GREEN_FIXTURES:-}" ]; then
    cat "$MAIN_GREEN_FIXTURES/jobs-$1.json"
  else
    gh api --paginate "repos/$GH_REPO/actions/runs/$1/jobs?per_page=100" --jq '.jobs[] | {name, status, conclusion}' | jq -s '.'
  fi
}

# Scheduled and manual runs (daily scan, rollbacks, the release itself) say
# nothing about the commit; bot housekeeping runs are not CI. Of the remaining
# runs, only the newest per workflow and event counts, so a green re-trigger
# replaces a red one.
runs=$(runs_for | jq '
  [ .[]
    | select(.event != "schedule" and .event != "workflow_dispatch")
    | select(.workflowName | IN("CI watch", "Nightly dependency release", "Release (tag + deploy)", "Dependabot Updates") | not)
  ]
  | group_by([.workflowName, .event])
  | map(max_by(.createdAt))')

problems=()

if [ "$(jq 'length' <<<"$runs")" -eq 0 ]; then
  problems+=("no CI run found for $sha")
fi

while IFS= read -r line; do
  [ -n "$line" ] && problems+=("$line")
done < <(jq -r '.[] | select((.status != "completed") or ((.conclusion // "") | IN("success", "skipped", "neutral") | not)) | "\(.workflowName) (\(.event)): \(.status) \(.conclusion // "-") \(.url)"' <<<"$runs")

gate_ids=$(jq -r --arg w "$gate_workflow" '.[] | select(.workflowName == $w and .event == "push") | .databaseId' <<<"$runs")
if [ -z "$gate_ids" ]; then
  problems+=("no \"$gate_workflow\" push run for $sha: the tests did not run on this commit")
else
  for id in $gate_ids; do
    jobs=$(jobs_for "$id")
    tests=$(jq --arg p "$job_prefix" '[.[] | select(.name | ascii_downcase | startswith($p | ascii_downcase))]' <<<"$jobs")
    if [ "$(jq 'length' <<<"$tests")" -eq 0 ]; then
      problems+=("run $id has no test job (\"$job_prefix...\"): the test workflow is not wired in")
    elif ! jq -e 'all(.conclusion == "success" or .conclusion == "skipped")' <<<"$tests" >/dev/null; then
      problems+=("run $id has test jobs that did not pass: $(jq -r '[.[] | select(.conclusion != "success" and .conclusion != "skipped") | "\(.name)=\(.conclusion // .status)"] | join(", ")' <<<"$tests")")
    elif ! jq -e 'any(.conclusion == "success")' <<<"$tests" >/dev/null; then
      problems+=("run $id: every test job was skipped, nothing was tested")
    fi
  done
fi

if [ "${#problems[@]}" -gt 0 ]; then
  echo "Commit $sha is not green:"
  printf '  - %s\n' "${problems[@]}"
  exit 1
fi
echo "Commit $sha is green: all CI runs passed and the test jobs ran."
