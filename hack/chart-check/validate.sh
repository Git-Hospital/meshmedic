#!/usr/bin/env bash
#
# Runs `meshmedic chart check` over the charts in charts.json (the ten charts of
# the compat pilot, pinned, with the values the pilot installed them with, plus
# the one-rule redis fix and Istio's gateway chart, which is expected clean) and prints, per chart, which rules flagged what in
# each mode. The observed side of docs/chart-check.md's validation table comes
# from docs/compat-pilot.md and cannot be reproduced here: it needs clusters.
#
#   hack/chart-check/validate.sh [OUTDIR]
#
# Needs helm and jq, and network access for helm to fetch the public charts.
# Charts are pulled with helm's own config and cache directories under OUTDIR,
# so a repository list on this machine cannot change what is fetched. OUTDIR
# (default: a new temporary directory) keeps the chart archives and one JSON
# report per chart.
#
# Namespace and release name follow the pilot: namespace "pilot", release name
# the chart id.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
out="${1:-$(mktemp -d)}"
mkdir -p "$out/helm/config" "$out/helm/cache" "$out/charts" "$out/reports"
export HELM_CONFIG_HOME="$out/helm/config" HELM_CACHE_HOME="$out/helm/cache"
export HELM_REPOSITORY_CONFIG="$out/helm/config/repositories.yaml" HELM_REPOSITORY_CACHE="$out/helm/cache/repository"

for t in helm jq go; do command -v "$t" >/dev/null || { echo "validate.sh: $t is needed" >&2; exit 2; }; done
(cd "$root" && go build -o "$out/meshmedic" ./cmd/meshmedic) || exit 2
echo "helm $(helm version --short), output in $out" >&2

jq -c '.[]' "$here/charts.json" | while IFS= read -r spec; do
  id="$(jq -r .id <<<"$spec")"
  chart="$(jq -r .chart <<<"$spec")"
  version="$(jq -r .version <<<"$spec")"
  repo="$(jq -r '.repo // empty' <<<"$spec")"
  args=()
  while IFS= read -r a; do args+=("$a"); done < <(jq -r '.args[]' <<<"$spec")

  mkdir -p "$out/charts/$id"
  pullargs=(pull "$chart" --version "$version" --destination "$out/charts/$id")
  [[ -z "$repo" ]] || pullargs+=(--repo "$repo")
  if ! helm "${pullargs[@]}" >/dev/null 2>"$out/reports/$id.pull.err"; then
    printf '%s\t%s\tpull failed: %s\n' "$id" "$version" "$(tail -1 "$out/reports/$id.pull.err")"
    continue
  fi
  archive="$(ls "$out"/charts/"$id"/*.tgz)"
  "$out/meshmedic" chart check "$archive" ${args[@]+"${args[@]}"} --namespace pilot --release-name "${id%-fixed}" \
    --format json >"$out/reports/$id.json" 2>"$out/reports/$id.err"
  code=$?
  if [[ $code -eq 2 ]]; then
    printf '%s\t%s\texit 2: %s\n' "$id" "$version" "$(head -1 "$out/reports/$id.err")"
    continue
  fi
  n="$(jq -r '[.findings[] | "\(.mode) \(.id) \(.check) \(.level)"] | group_by(.) | map("\(.[0]) x\(length)") | if length == 0 then "none" else join("; ") end' "$out/reports/$id.json")"
  # An entry marked "expect": "clean" is a chart known not to be broken by the
  # mesh (Istio's own gateway chart, whose istio-proxy container with image
  # "auto" is an injection placeholder); a candidate on it is a false positive.
  verdict=""
  if [[ "$(jq -r '.expect // empty' <<<"$spec")" == clean && $code -ne 0 ]]; then
    verdict="  UNEXPECTED: expected clean"
  fi
  printf '%s\t%s\texit %s\t%s%s\n' "$id" "$version" "$code" "$n" "$verdict"
done
