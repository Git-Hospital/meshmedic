#!/usr/bin/env bash
#
# Tests for hack/compat-exp/guard.sh. The guard runs against a throwaway git
# repository built here (GUARD_ROOT), with a tag standing in for
# compat-exp-freeze. No network, no Actions.
#
#   hack/compat-exp/test/guard_test.sh
#
# The ok-or-fail one-liners are deliberate: ok and fail always succeed.
# shellcheck disable=SC2015,SC2016,SC2012
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../../.." && pwd)"
guard="$repo/hack/compat-exp/guard.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fails=0
fail() { echo "FAIL: $*" >&2; fails=$((fails + 1)); }
ok() { echo "ok: $*"; }
eq() { # name got want
  if [[ "$2" == "$3" ]]; then ok "$1"; else fail "$1: got '$2', want '$3'"; fi
}

export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.test GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.test

sha() { shasum -a 256 | cut -d' ' -f1; }

# mklist N: a charts-exp.json with N charts ("c1".."cN"), 2 reserves, and the
# hash select.sh would store.
mklist() {
  jq -n --argjson n "$1" '{schema: 1, installDeadlineSeconds: 600,
    charts: [range(1; $n + 1) as $i | {id: "c\($i)", chart: "c\($i)", version: "1.0.0", repo: "https://example.test/\($i)", deadline: "600s"}],
    reserves: [{id: "r1", chart: "r1", version: "1.0.0", deadline: "600s"}, {id: "r2", chart: "oci://example.test/r2", version: "2.0.0", deadline: "600s"}]}' |
    jq -S . >"$2.body"
  jq -S --arg h "$(jq -S -c . "$2.body" | sha)" '. + {sha256: $h}' "$2.body" >"$2"
  rm -f "$2.body"
}

# newrepo DIR: the frozen instrument, small stand-ins, committed and tagged.
newrepo() {
  local d="$1"
  mkdir -p "$d" && git -C "$d" init -q -b main
  mkdir -p "$d/hack/compat-exp/frame" "$d/hack/compat-exp/patches" "$d/hack/compat-exp/fixes" "$d/hack/compat-exp/ignore" \
    "$d/hack/compat-pilot/difftable" "$d/.github/workflows"
  echo "package main" >"$d/hack/compat-exp/frame/main.go"
  for f in qualify.sh select.sh guard.sh candidates.json; do echo "$f v1" >"$d/hack/compat-exp/$f"; done
  echo '[{"id":"c1","chart":"c1","version":"1.0.0","repo":"https://example.test/1"},{"id":"c2","chart":"c2","version":"1.0.0"}]' >"$d/hack/compat-exp/candidates-charts.json"
  jq -n '[range(1; 10) as $i | {key: "cause-\($i)", anchor: "a", mechanism: "m"}]' >"$d/hack/compat-exp/causes.json"
  echo "observe v1" >"$d/hack/compat-pilot/observe.sh"
  echo "world v1" >"$d/hack/compat-pilot/world.sh"
  echo "package main" >"$d/hack/compat-pilot/difftable/main.go"
  echo "name: compat-exp" >"$d/.github/workflows/compat-exp.yml"
  echo "module x" >"$d/go.mod"
  echo "" >"$d/go.sum"
  echo "a patch" >"$d/hack/compat-exp/patches/c1-fix.patch"
  echo '[{"chart":"c1","world":"W3","key":"k","reason":"r"}]' >"$d/hack/compat-exp/ignore/c1.json"
  git -C "$d" add -A && git -C "$d" commit -q -m "instrument"
}
freeze() { git -C "$1" tag compat-exp-freeze; }
commit() { git -C "$1" add -A && git -C "$1" commit -q -m "${2:-change}"; }

# g REPO MODE [VAR=VALUE ...]: run the guard `check`, print its stderr; rc in $rc.
rc=0
err=
g() {
  local d="$1" m="$2"
  shift 2
  rc=0
  err="$(env -i PATH="$PATH" HOME="$HOME" GUARD_ROOT="$d" GUARD_MODE="$m" GUARD_ISTIO="${ISTIO:-1.31.3}" ISTIO_PRIMARY=1.31.3 "$@" "$guard" "${CMD:-check}" ${OUT:+"$OUT"} 2>&1 >"${STDOUT:-/dev/null}")" || rc=$?
}
passes() { # name
  if [[ $rc -eq 0 ]]; then ok "$1"; else fail "$1: exit $rc: $err"; fi
}
refuses() { # name needle
  if [[ $rc -eq 1 && "$err" == *"$2"* ]]; then ok "$1"; else fail "$1: exit $rc, wanted a refusal naming '$2': $err"; fi
}

# --- before the tag ---------------------------------------------------------------

r="$tmp/pre"
newrepo "$r"
cat >"$r/hack/compat-exp/acceptance-charts.json" <<'JSONEND'
[
  {"id":"redis","chart":"oci://example.test/redis","version":"1.0.0"},
  {"id":"podinfo","chart":"podinfo","version":"6.0.0","repo":"https://example.test/p"},
  {"id":"redis-fix","fixOf":"redis","cause":"cause-1","chart":"oci://example.test/redis","version":"1.0.0","values":["--set-json","a=[1]"]},
  {"id":"redis-fix-patch","fixOf":"redis","cause":"cause-1","chart":"oci://example.test/redis","version":"1.0.0","patch":"hack/compat-exp/patches/c1-fix.patch"}
]
JSONEND
commit "$r"
STDOUT="$tmp/pre.out" CMD=plan OUT="$tmp/pre-plan" g "$r" acceptance
passes "acceptance runs before the tag exists"
eq "acceptance: 4 charts x 3 worlds" "$(grep '^count=' "$tmp/pre.out")" "count=12"
eq "acceptance: effective list holds the fixes" "$(jq 'map(.id) | length' "$tmp/pre-plan/charts.json")" 4
eq "acceptance: matrix worlds" "$(jq -c '[.include[].world] | unique' "$tmp/pre-plan/matrix.json")" '["W0","W1","W3"]'
for m in qualify observe certify recheck; do
  g "$r" "$m" GUARD_FIX_ID="$([[ $m == certify ]] && echo redis-fix || true)"
  refuses "$m refuses without the tag" "does not exist"
done
ISTIO=1.32.0 g "$r" acceptance
refuses "acceptance runs on the primary version only" "runs on Istio"
g "$r" acceptance GUARD_CHARTS_FILE=hack/compat-exp/charts-exp.json
refuses "acceptance takes only its own list" "acceptance runs"

# --- the real acceptance list passes the real guard --------------------------------

rc=0
err="$(env -i PATH="$PATH" HOME="$HOME" GUARD_ROOT="$repo" GUARD_MODE=acceptance GUARD_ISTIO=1.31.3 ISTIO_PRIMARY=1.31.3 "$guard" check 2>&1)" || rc=$?
passes "the repository's acceptance list passes"
eq "acceptance: 12 charts, 36 jobs" "$(grep -o '12 charts, 36 jobs' <<<"$err")" "12 charts, 36 jobs"

# --- after the tag -----------------------------------------------------------------

r="$tmp/frozen"
newrepo "$r"
freeze "$r"
mklist 5 "$r/hack/compat-exp/charts-exp.json"
commit "$r" "list"
g "$r" observe
passes "observe at a descendant with a valid list"
STDOUT="$tmp/o.out" CMD=plan OUT="$tmp/o-plan" g "$r" observe
eq "observe: default selection is the 5 primary charts, not the reserves" "$(jq -c '[.include[].chart] | unique' "$tmp/o-plan/matrix.json")" '["c1","c2","c3","c4","c5"]'
eq "observe: effective list keeps the reserves" "$(jq 'map(.id) | length' "$tmp/o-plan/charts.json")" 7
g "$r" observe GUARD_CHART_IDS="c1 r2"
passes "a reserve may be named"
g "$r" observe GUARD_CHART_IDS="c1 zz"
refuses "an id outside the list is refused" "ids not in the chart list"
g "$r" observe GUARD_CHART_IDS='c1; rm -rf /'
refuses "shell metacharacters in ids" "chart ids may hold only"
g "$r" observe GUARD_CHART_IDS='c1 $(id)'
refuses "command substitution in ids" "chart ids may hold only"
g "$r" observe GUARD_CHARTS_FILE=../../etc/passwd
refuses "charts file path traversal" "not a plain path"
g "$r" observe GUARD_CHARTS_FILE=hack/compat-exp/../compat-exp/charts-exp.json
refuses "dot segment in the charts file path" "dot segment"
g "$r" observe GUARD_CHARTS_FILE='hack/compat-exp/charts-exp.json;id'
refuses "metacharacter in the charts file path" "not a plain path"
g "$r" observe GUARD_FIXES_FILE=hack/compat-exp/other.json
refuses "fixes file outside fixes/" "outside hack/compat-exp/fixes/"

# istio
ISTIO=recheck-version-undecided g "$r" observe
refuses "observe refuses the recheck option" "runs on Istio 1.31.3 only"
ISTIO=recheck-version-undecided g "$r" recheck GUARD_CHART_IDS=c1
refuses "recheck refuses the freeze placeholder" "decided at the freeze"
ISTIO=1.32.0 g "$r" recheck GUARD_CHART_IDS=c1
passes "recheck accepts a decided version"
ISTIO=1.32.0 g "$r" recheck
refuses "recheck needs chart_ids" "needs chart_ids"
ISTIO=1.32.0 g "$r" observe
refuses "observe refuses the recheck version" "runs on Istio 1.31.3 only"
ISTIO='1.31.3; id' g "$r" recheck GUARD_CHART_IDS=c1
refuses "istio must be x.y.z" "decided at the freeze"
g "$r" bogus
refuses "unknown mode" "is not one of"

# the freeze: every frozen file
for f in hack/compat-exp/qualify.sh hack/compat-exp/select.sh hack/compat-exp/guard.sh hack/compat-exp/causes.json \
  hack/compat-pilot/observe.sh hack/compat-pilot/world.sh hack/compat-pilot/difftable/main.go \
  hack/compat-exp/frame/main.go .github/workflows/compat-exp.yml go.mod hack/compat-exp/candidates.json; do
  d="$tmp/mod-$(tr '/.' '__' <<<"$f")"
  cp -R "$r" "$d"
  echo "# edited" >>"$d/$f"
  commit "$d" "edit $f"
  g "$d" observe
  refuses "frozen file edited in a descendant: $f" "differ from tag"
  # the same edit, uncommitted
  d2="$tmp/dirty-$(tr '/.' '__' <<<"$f")"
  cp -R "$r" "$d2"
  echo "# edited" >>"$d2/$f"
  g "$d2" observe
  refuses "frozen file edited in the working tree: $f" "differ from tag"
done
# new tools appear after the freeze: frozen from then on, and a new one is a difference
d="$tmp/newtool"
cp -R "$r" "$d"
mkdir -p "$d/hack/compat-exp/crossrun"
echo "package main" >"$d/hack/compat-exp/crossrun/main.go"
commit "$d" "crossrun added after the freeze"
g "$d" observe
refuses "a frozen tool added after the tag" "differ from tag"
d="$tmp/untracked"
cp -R "$r" "$d"
echo x >"$d/hack/compat-exp/frame/extra.go"
g "$d" observe
refuses "untracked file among frozen paths" "untracked"
# data may change: fixes, patches, ledger-like files
d="$tmp/data"
cp -R "$r" "$d"
echo "another patch" >"$d/hack/compat-exp/patches/new.patch"
mkdir -p "$d/hack/compat-exp/ledger-data"
echo '{}' >"$d/hack/compat-exp/ledger-data/row.json"
commit "$d" "data"
g "$d" observe
passes "patches and data are not frozen"

# HEAD must descend from the tag
d="$tmp/side"
newrepo "$d"
git -C "$d" checkout -q -b old
freeze "$d"
git -C "$d" checkout -q -b elsewhere HEAD~0
git -C "$d" reset -q --hard HEAD
# a commit that does not contain the tag: orphan branch with the same files
git -C "$d" checkout -q --orphan unrelated
git -C "$d" add -A && git -C "$d" commit -q -m "unrelated history"
mklist 3 "$d/hack/compat-exp/charts-exp.json"
commit "$d" "list"
g "$d" observe
refuses "HEAD that is not a descendant of the tag" "not tag compat-exp-freeze or a descendant"

# --- the chart list -------------------------------------------------------------------

d="$tmp/hash"
cp -R "$r" "$d"
jq '.charts[0].version = "9.9.9"' "$d/hack/compat-exp/charts-exp.json" >"$d/x" && mv "$d/x" "$d/hack/compat-exp/charts-exp.json"
commit "$d" "edit the list without the hash"
g "$d" observe
refuses "hash mismatch" "does not match its content"
d="$tmp/nohash"
cp -R "$r" "$d"
jq 'del(.sha256)' "$d/hack/compat-exp/charts-exp.json" >"$d/x" && mv "$d/x" "$d/hack/compat-exp/charts-exp.json"
commit "$d" "drop hash"
g "$d" observe
refuses "list without a hash" "must be a charts-exp.json"
d="$tmp/twice"
cp -R "$r" "$d"
mklist 6 "$d/hack/compat-exp/charts-exp.json"
commit "$d" "second commit on the list"
g "$d" observe
refuses "a list rewritten after it was committed" "committed once"

# matrix limit: 85 charts x 3 worlds = 255 passes, 86 x 3 = 258 does not
d="$tmp/m85"
newrepo "$d"
freeze "$d"
mklist 85 "$d/hack/compat-exp/charts-exp.json"
commit "$d" list
g "$d" observe
passes "255 jobs are allowed"
eq "255 jobs: count" "$(STDOUT="$tmp/m.out" CMD=plan OUT="$tmp/m-plan" g "$d" observe; grep '^count=' "$tmp/m.out")" "count=255"
d="$tmp/m86"
newrepo "$d"
freeze "$d"
mklist 86 "$d/hack/compat-exp/charts-exp.json"
commit "$d" list
g "$d" observe
refuses "258 jobs are refused" "258 jobs, over the limit of 256"
g "$d" observe GUARD_CHART_IDS="c1 c2"
passes "a smaller selection of the same list fits"
CMD=plan OUT="$tmp/m86-plan" g "$d" observe GUARD_CHART_IDS="c1 c2"
passes "plan on the smaller selection"
eq "qualify mode has one world per chart" "$(STDOUT="$tmp/q.out" CMD=plan OUT="$tmp/q-plan" g "$d" qualify; grep '^count=' "$tmp/q.out")" "count=2"
eq "qualify kind" "$(grep '^kind=' "$tmp/q.out")" "kind=qualify"
eq "qualify: matrix world" "$(jq -c '[.include[].world] | unique' "$tmp/q-plan/matrix.json")" '["W0"]'

# qualify: the candidate list must equal the tag's
d="$tmp/qmod"
newrepo "$d"
freeze "$d"
echo '[{"id":"zzz","chart":"zzz","version":"1.0.0"}]' >"$d/hack/compat-exp/candidates-charts.json"
commit "$d" "change the candidates"
g "$d" qualify
refuses "candidate list changed after the freeze" "differ from tag"

# --- fix entries -----------------------------------------------------------------------

fx() { # JSON array of fix entries -> fixes file; runs the guard in certify or observe
  printf '%s' "$1" >"$r2/hack/compat-exp/fixes/f.json"
  commit "$r2" "fixes"
}
r2="$tmp/fixrepo"
newrepo "$r2"
freeze "$r2"
mklist 5 "$r2/hack/compat-exp/charts-exp.json"
commit "$r2" list
base='"fixOf":"c1","cause":"cause-2","chart":"c1","version":"1.0.0","repo":"https://example.test/1"'
fx "[{\"id\":\"c1-fix\",$base,\"values\":[\"--set\",\"a=b\"]}]"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json GUARD_CHART_IDS="c1 c1-fix"
passes "a values fix of a listed chart"
fx "[{\"id\":\"c1-fix\",$base,\"patch\":\"hack/compat-exp/patches/c1-fix.patch\"}]"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json GUARD_CHART_IDS="c1 c1-fix"
passes "a patch fix under patches/"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json GUARD_CHART_IDS="c1-fix"
refuses "a fix without its base in the selection" "without their base"
fx "[{\"id\":\"zz-fix\",\"fixOf\":\"zz\",\"cause\":\"cause-2\",\"chart\":\"c1\",\"version\":\"1.0.0\",\"values\":[\"--set\",\"a=b\"]}]"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json
refuses "fixOf outside the frozen list" "not in the frozen chart list"
fx "[{\"id\":\"r1-fix\",\"fixOf\":\"r1\",\"cause\":\"cause-2\",\"chart\":\"r1\",\"version\":\"1.0.0\",\"values\":[\"--set\",\"a=b\"]}]"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json GUARD_CHART_IDS="r1 r1-fix"
passes "fixOf may name a reserve"
fx "[{\"id\":\"c1-fix\",\"fixOf\":\"c1\",\"cause\":\"made-up\",\"chart\":\"c1\",\"version\":\"1.0.0\",\"repo\":\"https://example.test/1\",\"values\":[\"--set\",\"a=b\"]}]"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json
refuses "cause outside causes.json" "not a key of causes.json"
fx "[{\"id\":\"c1-fix\",\"fixOf\":\"c1\",\"chart\":\"c1\",\"version\":\"1.0.0\",\"repo\":\"https://example.test/1\",\"values\":[\"--set\",\"a=b\"]}]"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json
refuses "missing cause" "not a key of causes.json"
for bad in '/etc/passwd' 'hack/compat-exp/patches/../../go.mod' 'hack/compat-exp/frame/main.go' 'hack/compat-pilot/world.sh' 'hack/compat-exp/patches/x.sh'; do
  fx "[{\"id\":\"c1-fix\",$base,\"patch\":\"$bad\"}]"
  g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json
  refuses "patch outside patches/: $bad" "patch is not a file under hack/compat-exp/patches/"
done
fx "[{\"id\":\"c1-fix\",$base,\"patch\":\"hack/compat-exp/patches/missing.patch\"}]"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json
refuses "patch file that does not exist" "not a regular file"
fx "[{\"id\":\"c1-fix\",$base,\"values\":[\"--post-renderer\",\"/tmp/x\"]}]"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json
refuses "values that run code" "values must be"
fx "[{\"id\":\"c1-fix\",$base,\"values\":[\"--set\"]}]"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json
refuses "odd values" "values must be"
fx "[{\"id\":\"c1-fix\",\"fixOf\":\"c1\",\"cause\":\"cause-2\",\"chart\":\"c9\",\"version\":\"1.0.0\",\"values\":[\"--set\",\"a=b\"]}]"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json
refuses "a fix of another chart or version" "must equal those of c1"
fx "[{\"id\":\"c1-fix\",$base}]"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json
refuses "a fix with neither values nor a patch" "needs values or a patch"
fx "[{\"id\":\"c1\",$base,\"values\":[\"--set\",\"a=b\"]}]"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json
refuses "a fix id that clashes with a chart id" "clash"

# certify
fx "[{\"id\":\"c1-fix\",$base,\"values\":[\"--set\",\"a=b\"],\"cellWorlds\":[\"W3\"],\"ignore\":\"hack/compat-exp/ignore/c1.json\"}]"
STDOUT="$tmp/c.out" CMD=plan OUT="$tmp/c-plan" g "$r2" certify GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json GUARD_FIX_ID=c1-fix
passes "certify plan"
eq "certify: cell worlds passed through" "$(grep '^cell_worlds=' "$tmp/c.out")" "cell_worlds=W3"
eq "certify: ignore passed through" "$(grep '^ignore=' "$tmp/c.out")" "ignore=hack/compat-exp/ignore/c1.json"
eq "certify: base and fix, three worlds" "$(grep '^count=' "$tmp/c.out")" "count=6"
eq "certify: the fix and its base only" "$(jq -c '[.include[].chart] | unique' "$tmp/c-plan/matrix.json")" '["c1","c1-fix"]'
g "$r2" certify GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json GUARD_FIX_ID=nope
refuses "certify an unknown fix" "not in the fixes file"
g "$r2" certify GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json
refuses "certify without fix_id" "needs fix_id"
g "$r2" observe GUARD_FIX_ID=c1-fix
refuses "fix_id outside certify" "certify only"
fx "[{\"id\":\"c1-fix\",$base,\"values\":[\"--set\",\"a=b\"],\"cellWorlds\":[\"W2\"]}]"
g "$r2" certify GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json GUARD_FIX_ID=c1-fix
refuses "cellWorlds must be mesh worlds of the experiment" "cellWorlds must be"
fx "[{\"id\":\"c1-fix\",$base,\"values\":[\"--set\",\"a=b\"],\"cellWorlds\":[\"W3\"],\"ignore\":\"hack/compat-exp/patches/c1-fix.patch\"}]"
g "$r2" certify GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json GUARD_FIX_ID=c1-fix
refuses "ignore file outside ignore/" "ignore is not a file under"

# causes.json shapes: an object with a causes array works too
d="$tmp/causes-obj"
cp -R "$r" "$d"
rm -rf "$d/.git" && git -C "$d" init -q -b main
jq '{causes: .}' "$r/hack/compat-exp/causes.json" >"$d/x" && mv "$d/x" "$d/hack/compat-exp/causes.json"
git -C "$d" add -A && git -C "$d" commit -q -m i && git -C "$d" tag compat-exp-freeze
printf '[{"id":"c1-fix","fixOf":"c1","cause":"cause-3","chart":"c1","version":"1.0.0","repo":"https://example.test/1","values":["--set","a=b"]}]' >"$d/hack/compat-exp/fixes/f.json"
commit "$d" fixes
g "$d" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json
passes "causes.json as {causes: [...]}"

if [[ $fails -ne 0 ]]; then echo "$fails failure(s)" >&2; exit 1; fi
echo "all guard tests passed"
