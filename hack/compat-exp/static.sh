#!/usr/bin/env bash
#
# The static-check results crossrun reads (docs/compat-experiment.md section 1,
# metric S): for each chart of a chart list and each mesh world (W1, W3), render
# the chart exactly as observe.sh installs it and run staticcheck over it.
#
# Usage:
#   hack/compat-exp/static.sh CHARTS_FILE OUT [CHART_ID...]
#
#   CHARTS_FILE  charts-exp.json (select.sh), or a flat chart list (an array,
#                such as the plan's charts.json). Without ids, the primary
#                charts: `charts` of charts-exp.json, or the entries of an
#                array that are not fixes. Reserves are rendered only by id.
#   OUT          the file crossrun's manifest names as `static`: a JSON list of
#                {chart, world, check, applicable, flagged}, one row per chart,
#                world and check. OUT.meta.json beside it records the helm
#                version, the pinned one, and the chart versions rendered.
#
# What "exactly as observe.sh installs" means: hack/compat-pilot/render-args.sh
# is sourced by both. The release name (the chart id), the namespace (pilot), the
# pinned --version, --repo, the values arguments and helm's own config and cache
# directories come from there, so they cannot differ. Before rendering, this
# script checks that observe.sh still builds its install from that file, and
# the helm binary against the version the workflow pins (HELM_VERSION in
# .github/workflows/compat-exp.yml); a different helm is an error unless
# STATIC_ALLOW_HELM_MISMATCH=1, and the meta file says so either way.
#
# Rendering differs from installing in what `helm template` cannot know (cluster
# capabilities, lookup); the same render is used for W1 and W3, and the world
# only changes which checks apply and how staticcheck reads the pod templates.
#
# applicable: staticcheck's own "applies" for the world, and the world is in the
# `worlds` of the cause key whose staticCheck is that check (causes.json). A check
# that does not apply is written applicable false, flagged false; crossrun treats
# that as "not applicable", never as "not flagged" (crossrun/INTERPRETATIONS.md 12).
#
# Environment: STATICCHECK (a staticcheck binary; default: go build of
# hack/compat-exp/staticcheck), MMC_STATE (helm's config and cache, as in
# observe.sh; default $PWD/.mmc-state).
# Exit codes: 0 written, 1 a chart did not render or a check did not run (no
# output is written), 2 usage or a refused input.
set -uo pipefail

charts_file="${1:?usage: static.sh CHARTS_FILE OUT [CHART_ID...]}"
out="${2:?usage: static.sh CHARTS_FILE OUT [CHART_ID...]}"
shift 2

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
pilot="$root/hack/compat-pilot"
causes_file="${STATIC_CAUSES:-$here/causes.json}"
workflow="${STATIC_WORKFLOW:-$root/.github/workflows/compat-exp.yml}"

for t in jq helm; do command -v "$t" >/dev/null || { echo "static.sh: $t is needed" >&2; exit 2; }; done
[[ -f "$charts_file" ]] || { echo "static.sh: no $charts_file" >&2; exit 2; }
for id in "$@"; do
  [[ "$id" =~ ^[a-z0-9]([a-z0-9-]{0,51}[a-z0-9])?$ ]] || { echo "static.sh: '$id' is not a chart id" >&2; exit 2; }
done

# --- no drift from observe.sh ---------------------------------------------------

# shellcheck source=../compat-pilot/render-args.sh
source "$pilot/render-args.sh"
# shellcheck disable=SC2016 # patterns, not expansions
if ! grep -q 'source "\$here/render-args.sh"' "$pilot/observe.sh" ||
  ! grep -q 'mmc_load_spec "\$spec" "\$id"' "$pilot/observe.sh" ||
  ! grep -q '"\${mmc_target\[@\]}"' "$pilot/observe.sh"; then
  echo "static.sh: observe.sh no longer builds its install from render-args.sh, so a render here could differ from the install; refusing" >&2
  exit 1
fi

state="${MMC_STATE:-$PWD/.mmc-state}"
mmc_helm_env "$state"

pinned="$(sed -n 's/^[[:space:]]*HELM_VERSION:[[:space:]]*"\{0,1\}\([^" ]*\)"\{0,1\}[[:space:]]*$/\1/p' "$workflow" | head -1)"
[[ -n "$pinned" ]] || { echo "static.sh: no HELM_VERSION in $workflow" >&2; exit 1; }
have="$(helm version --short 2>/dev/null | head -1 | sed 's/+.*//')"
helm_ok=true
if [[ "$have" != "$pinned" ]]; then
  helm_ok=false
  if [[ "${STATIC_ALLOW_HELM_MISMATCH:-}" != 1 ]]; then
    echo "static.sh: helm is '$have', the workflow pins '$pinned'; install that version or set STATIC_ALLOW_HELM_MISMATCH=1 (the meta file records it)" >&2
    exit 1
  fi
  echo "static.sh: warning: helm is '$have', the workflow pins '$pinned'" >&2
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

sc="${STATICCHECK:-}"
if [[ -z "$sc" ]]; then
  command -v go >/dev/null || { echo "static.sh: go (or STATICCHECK) is needed" >&2; exit 2; }
  (cd "$root" && go build -o "$tmp/staticcheck" ./hack/compat-exp/staticcheck) || { echo "static.sh: staticcheck did not build" >&2; exit 1; }
  sc="$tmp/staticcheck"
fi

# --- the charts -------------------------------------------------------------------

jq -e . "$charts_file" >/dev/null 2>&1 || { echo "static.sh: $charts_file is not JSON" >&2; exit 2; }
# shellcheck disable=SC2016 # a jq program, not shell
all="$(jq -c 'if type == "object" then (.charts + (.reserves // [])) else . end' "$charts_file")" ||
  { echo "static.sh: $charts_file is neither charts-exp.json nor a chart list" >&2; exit 2; }
if [[ $# -gt 0 ]]; then
  ids=("$@")
else
  ids=()
  # shellcheck disable=SC2016
  while IFS= read -r i; do ids+=("$i"); done < <(jq -r 'if type == "object" then .charts[].id else .[] | select(.fixOf == null) | .id end' "$charts_file")
fi
[[ ${#ids[@]} -gt 0 ]] || { echo "static.sh: no charts" >&2; exit 2; }

jq -e '.keys | type == "array" and length > 0' "$causes_file" >/dev/null 2>&1 ||
  { echo "static.sh: $causes_file is not the cause list (an object with keys)" >&2; exit 2; }

worlds=(W1 W3)
failed=()
: >"$tmp/rows.jsonl"
: >"$tmp/versions.jsonl"
for id in "${ids[@]}"; do
  spec="$(jq -ce --arg id "$id" '.[] | select(.id == $id and (.fixOf == null))' <<<"$all")" || {
    echo "static.sh: no chart '$id' (fix entries are not rendered) in $charts_file" >&2
    exit 2
  }
  mmc_load_spec "$spec" "$id"
  # What observe.sh runs for this entry is
  #   helm install RELEASE CHART --namespace NS --version V --wait ... [--repo R] VALUES
  # and this is the same with `template`, without waiting.
  if ! helm template "$release" "$chart" "${mmc_target[@]}" ${mmc_repo_args[@]+"${mmc_repo_args[@]}"} \
    ${values[@]+"${values[@]}"} >"$tmp/$id.yaml" 2>"$tmp/$id.err"; then
    failed+=("$id: helm template failed: $(tail -2 "$tmp/$id.err" | tr '\n' ' ')")
    continue
  fi
  jq -c -n --arg id "$id" --arg chart "$chart" --arg version "$version" --arg release "$release" \
    '{id: $id, chart: $chart, version: $version, release: $release}' >>"$tmp/versions.jsonl"
  for w in "${worlds[@]}"; do
    if ! "$sc" -world "$w" -namespace "$mmc_ns" "$tmp/$id.yaml" >"$tmp/$id.$w.json" 2>"$tmp/$id.$w.err"; then
      failed+=("$id/$w: staticcheck failed: $(tail -2 "$tmp/$id.$w.err" | tr '\n' ' ')")
      continue
    fi
    # shellcheck disable=SC2016
    jq -c --arg chart "$id" --arg world "$w" --slurpfile causes "$causes_file" '
      .checks[] as $c
      | ([$causes[0].keys[] | select(.staticCheck == $c.id)] | first) as $k
      | if $k == null then error("check \($c.id) is not the staticCheck of any key in causes.json") else . end
      | ($c.applies and ($k.worlds | index($world) != null)) as $app
      | {chart: $chart, world: $world, check: $c.id, applicable: $app, flagged: ($app and $c.flagged)}' \
      "$tmp/$id.$w.json" >>"$tmp/rows.jsonl" 2>"$tmp/$id.$w.err" ||
      failed+=("$id/$w: $(tail -1 "$tmp/$id.$w.err")")
  done
done

if [[ ${#failed[@]} -gt 0 ]]; then
  echo "static.sh: ${#failed[@]} failure(s), nothing written:" >&2
  printf '  %s\n' "${failed[@]}" >&2
  exit 1
fi

mkdir -p "$(dirname "$out")"
jq -s . "$tmp/rows.jsonl" >"$out.tmp" && mv "$out.tmp" "$out"
jq -s --arg have "$have" --arg pinned "$pinned" --argjson ok "$helm_ok" --arg ns "$mmc_ns" \
  '{helmVersion: $have, helmPinned: $pinned, helmMatchesPinned: $ok, namespace: $ns, worlds: ["W1", "W3"], charts: .}' \
  "$tmp/versions.jsonl" >"$out.meta.json"
echo "static.sh: wrote $out: $(jq length "$out") rows for ${#ids[@]} chart(s), helm $have"
