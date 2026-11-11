#!/usr/bin/env bash
#
# Tests for hack/compat-exp/static.sh with a stub helm that "renders" fixture
# manifests (the staticcheck testdata) and the real staticcheck tool. No cluster,
# no network.
#
#   hack/compat-exp/test/static_test.sh
#
# shellcheck disable=SC2015,SC2016
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/../../.." && pwd)"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT

pass=0
fail=0
ok() { pass=$((pass + 1)); echo "ok   $1"; }
bad() { fail=$((fail + 1)); echo "FAIL $1"; }
eq() { if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1: got '$2', want '$3'"; fi; }

fx="$repo/hack/compat-exp/staticcheck/testdata"
(cd "$repo" && go build -o "$T/staticcheck" ./hack/compat-exp/staticcheck) || { echo "staticcheck did not build" >&2; exit 1; }

mkdir -p "$T/bin" "$T/stub/render" "$T/state"
cat >"$T/bin/helm" <<'STUB'
#!/usr/bin/env bash
echo "helm $*" >>"$STUB_DIR/helm.log"
case "$1" in
  version) echo "${STUB_HELM_VERSION:-v4.0.4+gabcdef}" ;;
  template)
    f="$STUB_DIR/render/$2.yaml"
    if [[ ! -f "$f" ]]; then echo "Error: chart not found" >&2; exit 1; fi
    cat "$f" ;;
  install) exit 0 ;;
esac
exit 0
STUB
chmod +x "$T/bin/helm"
export STUB_DIR="$T/stub"

cp "$fx/np-redis-default.yaml" "$T/stub/render/badnp.yaml"
cp "$fx/np-redis-fixed.yaml" "$T/stub/render/goodnp.yaml"
cp "$fx/np-redis-fixed.yaml" "$T/stub/render/withvals.yaml"
cp "$fx/np-redis-default.yaml" "$T/stub/render/spare.yaml"

list="$T/charts-exp.json"
cat >"$list" <<'JSON'
{"schema":1,"charts":[
 {"id":"badnp","chart":"badnp","version":"1.2.3","repo":"https://example.test/repo","publisher":"p1","qualified":true},
 {"id":"goodnp","chart":"oci://example.test/goodnp","version":"2.0.0","publisher":"p2","qualified":true},
 {"id":"withvals","chart":"withvals","version":"3.0.0","values":["--set","a=b","--set-string","c=d"],"publisher":"p3","qualified":true}],
 "reserves":[{"id":"spare","chart":"spare","version":"1.0.0","publisher":"p4","qualified":true}]}
JSON

run_static() { # args...; rc in $rc
  env PATH="$T/bin:$PATH" STATICCHECK="$T/staticcheck" MMC_STATE="$T/state" "$@" >"$T/stdout" 2>"$T/stderr"
  rc=$?
}

# --- rows ----------------------------------------------------------------------------

run_static "$repo/hack/compat-exp/static.sh" "$list" "$T/out.json"
eq "exit 0" "$rc" 0
nchecks="$(jq '.keys | map(select(.staticCheck != null)) | length' "$repo/hack/compat-exp/causes.json")"
eq "one row per primary chart, world and check (reserves only by id)" "$(jq length "$T/out.json")" $((3 * 2 * nchecks))
row() { jq -c --arg c "$1" --arg w "$2" --arg k "$3" '.[] | select(.chart == $c and .world == $w and .check == $k) | {applicable, flagged}' "$T/out.json"; }
eq "np check flags the unfixed policy in W3" "$(row badnp W3 np-hbone-probe)" '{"applicable":true,"flagged":true}'
eq "np check does not flag the fixed policy" "$(row goodnp W3 np-hbone-probe)" '{"applicable":true,"flagged":false}'
eq "np check is not applicable to W1, and says so" "$(row badnp W1 np-hbone-probe)" '{"applicable":false,"flagged":false}'
eq "protocol check is not applicable to W3" "$(row badnp W3 protocol-declared)" '{"applicable":false,"flagged":false}'
eq "protocol check applies in W1, not flagged" "$(row badnp W1 protocol-declared)" '{"applicable":true,"flagged":false}'
eq "every row names a check id of causes.json" "$(jq --slurpfile c "$repo/hack/compat-exp/causes.json" '[.[].check] | unique - ([$c[0].keys[].staticCheck | select(. != null)]) | length' "$T/out.json")" 0
eq "no row for the key without a check (bind-address)" "$(jq '[.[] | select(.check == "bind-address")] | length' "$T/out.json")" 0
eq "meta: helm version and pin" "$(jq -c '{helmVersion, helmPinned, helmMatchesPinned}' "$T/out.json.meta.json")" '{"helmVersion":"v4.0.4","helmPinned":"v4.0.4","helmMatchesPinned":true}'
eq "meta: chart versions" "$(jq -c '[.charts[] | .version]' "$T/out.json.meta.json")" '["1.2.3","2.0.0","3.0.0"]'

run_static "$repo/hack/compat-exp/static.sh" "$list" "$T/out2.json" spare badnp
eq "a reserve may be named" "$(jq -c '[.[].chart] | unique' "$T/out2.json")" '["badnp","spare"]'
run_static "$repo/hack/compat-exp/static.sh" "$list" "$T/out3.json" nope
eq "an unknown id is refused" "$rc" 2

# a flat list (the plan's charts.json): fix entries are not rendered
jq -c '.charts + .reserves + [{"id":"badnp-fix","fixOf":"badnp","cause":"ambient-np","chart":"badnp","version":"1.2.3","values":["--set","x=1"]}]' "$list" >"$T/flat.json"
run_static "$repo/hack/compat-exp/static.sh" "$T/flat.json" "$T/out4.json"
eq "flat list: exit 0" "$rc" 0
eq "flat list: charts of the list, no fix entry" "$(jq -c '[.[].chart] | unique' "$T/out4.json")" '["badnp","goodnp","spare","withvals"]'
run_static "$repo/hack/compat-exp/static.sh" "$T/flat.json" "$T/out5.json" badnp-fix
eq "a fix entry by id is refused" "$rc" 2

# --- the render is the install -----------------------------------------------------------

# Run observe.sh for the same entries with a helm stub that logs the install, then compare
# the two command lines: same release, chart, namespace, version, repo and values.
cat >"$T/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
a=" $* "
case "$a" in
  *" config get-contexts "*) echo "kind-mmc-w0" ;;
  *" get "*" -o json "*) echo '{"items":[]}' ;;
  *" get --raw "*) echo "Error from server (NotFound)" >&2; exit 1 ;;
  *" apply "*) cat >/dev/null ;;
esac
exit 0
STUB
cat >"$T/bin/istioctl" <<'STUB'
#!/usr/bin/env bash
echo '[]'
STUB
chmod +x "$T/bin/kubectl" "$T/bin/istioctl"
: >"$T/state/mmc-w0.kubeconfig"
printf 'istio=1.31.3\nkubernetes=1.35\n' >"$T/state/mmc-w0.world"
for id in badnp goodnp withvals; do
  : >"$T/stub/helm.log"
  env PATH="$T/bin:$PATH" CHARTS_FILE="$T/flat.json" MMC_STATE="$T/state" MMC_SETTLE=0 MMC_LATE=0 \
    bash "$repo/hack/compat-pilot/observe.sh" W0 "$id" 1 "$T/obs" >/dev/null 2>&1
  inst="$(grep '^helm install ' "$T/stub/helm.log" | head -1)"
  run_static "$repo/hack/compat-exp/static.sh" "$T/flat.json" "$T/o-$id.json" "$id"
  tmpl="$(grep '^helm template ' "$T/stub/helm.log" | tail -1)"
  # drop the verb and the wait flags the install adds; what is left must be identical
  norm_i="$(sed -e 's/^helm install //' -e 's/ --wait --wait-for-jobs --timeout [^ ]*//' <<<"$inst")"
  # --repo sits after the wait flags in install, before the values; compare as sorted words
  # of the part up to the first values flag, plus the values in order
  words() { tr ' ' '\n' <<<"$1" | grep -v '^$'; }
  eq "release, chart, namespace and version are the same: $id" "$(awk '{print $3, $4, $5, $6, $7, $8}' <<<"$inst")" "$(awk '{print $3, $4, $5, $6, $7, $8}' <<<"$tmpl")"
  eq "values are the same, in order: $id" "$(sed -n 's/.*\(--set.*\)$/\1/p' <<<"$inst")" "$(sed -n 's/.*\(--set.*\)$/\1/p' <<<"$tmpl")"
  eq "render and install take the same arguments: $id" "$(words "$norm_i" | sed 's/^template$//' | sort | tr '\n' ' ')" "$(words "${tmpl#helm template }" | sort | tr '\n' ' ')"
done

# --- failures -----------------------------------------------------------------------------

jq '.charts += [{"id":"norender","chart":"norender","version":"1.0.0","publisher":"p","qualified":true}]' "$list" >"$T/bad.json"
run_static "$repo/hack/compat-exp/static.sh" "$T/bad.json" "$T/outbad.json"
eq "a chart that does not render fails the run" "$rc" 1
[[ ! -e "$T/outbad.json" ]] && ok "and writes no file" || bad "and writes no file"
grep -q "chart norender (worlds W1, W3): helm template failed" "$T/stderr" && ok "and names the chart and the worlds" || bad "and names the chart and the worlds"
grep -q "rendered at qualification" "$T/stderr" && ok "and says a listed chart that fails now is an error" || bad "and says a listed chart that fails now is an error"

# the overrides of the cause list and the workflow belong to analyze.sh's test mode only
echo '{"keys":[{"key":"x","worlds":["W1"],"staticCheck":null}]}' >"$T/other-causes.json"
run_static env STATIC_CAUSES="$T/other-causes.json" "$repo/hack/compat-exp/static.sh" "$list" "$T/outc.json"
eq "STATIC_CAUSES without MMC_ANALYZE_TEST=1 is refused" "$rc" 2
grep -q "MMC_ANALYZE_TEST" "$T/stderr" && ok "and says why" || bad "and says why"
run_static env STATIC_WORKFLOW="$T/none.yml" "$repo/hack/compat-exp/static.sh" "$list" "$T/outc.json"
eq "STATIC_WORKFLOW without MMC_ANALYZE_TEST=1 is refused" "$rc" 2
run_static env MMC_ANALYZE_TEST=1 STATIC_CAUSES="$T/other-causes.json" "$repo/hack/compat-exp/static.sh" "$list" "$T/outc.json"
[[ $rc -ne 2 ]] && ok "with MMC_ANALYZE_TEST=1 the override is read" || bad "with MMC_ANALYZE_TEST=1 the override is read"

run_static env STUB_HELM_VERSION=v4.0.5 "$repo/hack/compat-exp/static.sh" "$list" "$T/outh.json"
eq "a helm other than the pinned one is refused" "$rc" 1
[[ ! -e "$T/outh.json" ]] && ok "no file for a wrong helm" || bad "no file for a wrong helm"
run_static env STUB_HELM_VERSION=v4.0.5 STATIC_ALLOW_HELM_MISMATCH=1 "$repo/hack/compat-exp/static.sh" "$list" "$T/outh.json"
eq "unless the override is set" "$rc" 0
eq "and then the meta file says so" "$(jq .helmMatchesPinned "$T/outh.json.meta.json")" false

# drift: an observe.sh that no longer builds its install from render-args.sh
mkdir -p "$T/fake/hack/compat-exp" "$T/fake/hack/compat-pilot" "$T/fake/.github/workflows"
cp "$repo/hack/compat-exp/static.sh" "$repo/hack/compat-exp/causes.json" "$T/fake/hack/compat-exp/"
cp "$repo/hack/compat-pilot/render-args.sh" "$T/fake/hack/compat-pilot/"
cp "$repo/.github/workflows/compat-exp.yml" "$T/fake/.github/workflows/"
sed 's/"\${mmc_target\[@\]}"/--namespace pilot --version "$version"/' "$repo/hack/compat-pilot/observe.sh" >"$T/fake/hack/compat-pilot/observe.sh"
run_static "$T/fake/hack/compat-exp/static.sh" "$list" "$T/outd.json"
eq "an observe.sh with its own install arguments is refused" "$rc" 1
grep -q "render-args.sh" "$T/stderr" && ok "and says why" || bad "and says why"
cp "$repo/hack/compat-pilot/observe.sh" "$T/fake/hack/compat-pilot/observe.sh"
run_static "$T/fake/hack/compat-exp/static.sh" "$list" "$T/outd.json"
eq "the real observe.sh is accepted in the same layout" "$rc" 0

echo
echo "$pass passed, $fail failed"
[[ $fail -eq 0 ]]
