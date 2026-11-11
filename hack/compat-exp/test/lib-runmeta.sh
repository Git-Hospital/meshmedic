#!/usr/bin/env bash
#
# Sourced by analyze_test.sh and e2e_test.sh. Writes the run metadata file the way
# the workflow does: the "run metadata" step of the guard job in
# .github/workflows/compat-exp.yml is taken out of the workflow file and run, so
# the analysis side is tested against the workflow's real output, not a copy.
#
#   gen_run_metadata WORKFLOW CLONE OUT MODE ISTIO HEADSHA RUNID ATTEMPT [CHARTS_FILE [FIXES_FILE [CHART_IDS [FIX_ID]]]]
#
# CLONE is a git clone of the instrument repository (with the freeze tag) whose
# working directory the step runs in; OUT is the file written (run-metadata.json).
# The values the runner supplies from github.* (workflow ref, event name) are the
# ones a real dispatch of Git-Hospital/meshmedic has.

gen_run_metadata() {
  local wf="$1" clone="$2" out="$3" mode="$4" istio="$5" head="$6" id="$7" att="$8"
  local script
  script="$(yq -o=json '.' "$wf" | jq -r '.jobs.guard.steps[] | select(.name == "run metadata") | .run')" || return 1
  [[ -n "$script" ]] || { echo "gen_run_metadata: no run metadata step in $wf" >&2; return 1; }
  # the default chart list of qualify mode may not exist in a minimal fixture repository
  [[ -e "$clone/hack/compat-exp/candidates-charts.json" ]] || echo '[]' >"$clone/hack/compat-exp/candidates-charts.json"
  rm -rf "$clone/run-metadata"
  (
    cd "$clone" &&
      MD_MODE="$mode" MD_ISTIO="$istio" MD_CHARTS_FILE="${9:-}" MD_FIXES_FILE="${10:-}" MD_CHART_IDS="${11:-}" MD_FIX_ID="${12:-}" \
        MD_HEAD_SHA="$head" MD_WORKFLOW_REF="Git-Hospital/meshmedic/.github/workflows/compat-exp.yml@refs/heads/main" \
        MD_EVENT_NAME=workflow_dispatch MD_RUN_ID="$id" MD_RUN_ATTEMPT="$att" bash -c "$script"
  ) >/dev/null || return 1
  mkdir -p "$(dirname "$out")"
  cp "$clone/run-metadata/run-metadata.json" "$out"
}

# wf_step WORKFLOW JOB STEP-NAME: the run: script of a workflow step, to be run with bash -c
# in a directory laid out like the runner's workspace, with the step's env: set by the caller.
wf_step() {
  yq -o=json '.' "$1" | jq -r --arg j "$2" --arg s "$3" '.jobs[$j].steps[] | select(.name == $s) | .run'
}
