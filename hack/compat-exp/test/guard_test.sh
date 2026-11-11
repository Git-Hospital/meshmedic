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
    qualification: {run: "7001", candidatesSha256: ("a" * 64), recordsSha256: ("b" * 64)},
    charts: [range(1; $n + 1) as $i | {id: "c\($i)", chart: "c\($i)", version: "1.0.0", repo: "https://example.test/\($i)", deadline: "600s"}],
    reserves: [{id: "r1", chart: "r1", version: "1.0.0", deadline: "600s"}, {id: "r2", chart: "oci://example.test/r2", version: "2.0.0", deadline: "600s"}]}' |
    jq -S . >"$2.body"
  jq -S --arg h "$(jq -S -c . "$2.body" | sha)" '. + {sha256: $h}' "$2.body" >"$2"
  rm -f "$2.body"
}

# newrepo DIR: the frozen instrument, small stand-ins, committed and tagged.
newrepo() {
  local d="$1" f
  mkdir -p "$d" && git -C "$d" init -q -b main
  mkdir -p "$d/hack/compat-exp/frame" "$d/hack/compat-exp/patches" "$d/hack/compat-exp/fixes" "$d/hack/compat-exp/ignore" \
    "$d/hack/compat-exp/staticcheck" "$d/hack/compat-exp/ledger" "$d/hack/compat-exp/crossrun" \
    "$d/hack/compat-pilot/difftable" "$d/.github/workflows" "$d/docs"
  for f in frame/main.go frame/exclusions.json staticcheck/main.go ledger/ledger.go crossrun/main.go; do echo "$f v1" >"$d/hack/compat-exp/$f"; done
  for f in qualify.sh select.sh static.sh analyze.sh acceptance-check.sh acceptance-expected.json guard.sh classification-prompt.md candidates.json; do echo "$f v1" >"$d/hack/compat-exp/$f"; done
  echo '[{"id":"c1","chart":"c1","version":"1.0.0","repo":"https://example.test/1"},{"id":"c2","chart":"c2","version":"1.0.0"}]' >"$d/hack/compat-exp/candidates-charts.json"
  # the real cause list, the contract of the guard, ledger and crossrun
  cp "$repo/hack/compat-exp/causes.json" "$d/hack/compat-exp/causes.json"
  echo '{"schema":1,"rows":[],"newKeys":[]}' >"$d/hack/compat-exp/ledger.json"
  for f in observe.sh render-args.sh world.sh; do echo "$f v1" >"$d/hack/compat-pilot/$f"; done
  echo "package main" >"$d/hack/compat-pilot/difftable/main.go"
  echo "name: compat-exp" >"$d/.github/workflows/compat-exp.yml"
  echo "the pre-registration" >"$d/docs/compat-experiment.md"
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
  {"id":"redis-fix","fixOf":"redis","cause":"ambient-np","chart":"oci://example.test/redis","version":"1.0.0","values":["--set-json","a=[1]"]},
  {"id":"redis-fix-patch","fixOf":"redis","cause":"ambient-np","chart":"oci://example.test/redis","version":"1.0.0","patch":"hack/compat-exp/patches/c1-fix.patch"}
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

# --- guard.sh freeze: the freeze alone, for analyze.sh ----------------------------------

d="$tmp/fz"
newrepo "$d"
pre="$(git -C "$d" rev-parse HEAD)"
CMD=freeze g "$d" observe
refuses "freeze without the tag" "does not exist"
echo "second" >"$d/hack/compat-exp/classification-prompt.md"; commit "$d" "before the tag"
freeze "$d"
tagsha="$(git -C "$d" rev-parse HEAD)"
CMD=freeze g "$d" observe
passes "freeze at the tag"
echo "data" >"$d/hack/compat-exp/ledger.json"; commit "$d" "data only"
CMD=freeze g "$d" observe
passes "freeze at a descendant that changes data only"
child="$(git -C "$d" rev-parse HEAD)"
CMD=freeze OUT="$tagsha" g "$d" observe
passes "freeze of a commit: the tag itself"
CMD=freeze OUT="$child" g "$d" observe
passes "freeze of a commit: a descendant with unchanged frozen files"
CMD=freeze OUT="$pre" g "$d" observe
refuses "freeze of a commit before the tag" "not tag compat-exp-freeze or a descendant"
CMD=freeze OUT="HEAD" g "$d" observe
refuses "freeze of a commit needs a full sha" "not a full commit sha"
CMD=freeze OUT="$(printf 'a%.0s' $(seq 1 40))" g "$d" observe
refuses "freeze of an unknown commit" "not in this repository"
echo "tampered" >>"$d/hack/compat-exp/crossrun/main.go"
CMD=freeze g "$d" observe
refuses "freeze: an uncommitted change to a frozen file" "frozen instrument files differ"
commit "$d" "change crossrun"
CMD=freeze g "$d" observe
refuses "freeze: a committed change to a frozen file" "frozen instrument files differ"
CMD=freeze OUT="$(git -C "$d" rev-parse HEAD)" g "$d" observe
refuses "freeze of a commit that changed a frozen file" "frozen instrument files at"
CMD=freeze OUT="$child" g "$d" observe
passes "freeze of the earlier commit is still fine"
git -C "$d" reset -q --hard "$child"
echo x >"$d/hack/compat-exp/crossrun/extra.go"
CMD=freeze g "$d" observe
refuses "freeze: an untracked file among the frozen paths" "untracked files"
rm "$d/hack/compat-exp/crossrun/extra.go"

# R7: the acceptance check and its expected verdicts are part of the frozen instrument
for f in acceptance-check.sh acceptance-expected.json; do
  echo "edited after the tag" >>"$d/hack/compat-exp/$f"
  CMD=freeze g "$d" observe
  refuses "freeze: a changed $f" "frozen instrument files differ"
  git -C "$d" checkout -q -- "hack/compat-exp/$f"
done

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

# the freeze: every file of the instrument (docs/compat-experiment.md section 5 and the
# scripts and tools added since), edited after the tag, committed or not
for f in hack/compat-exp/frame/main.go hack/compat-exp/frame/exclusions.json hack/compat-exp/qualify.sh hack/compat-exp/select.sh \
  hack/compat-exp/static.sh hack/compat-exp/analyze.sh hack/compat-exp/guard.sh hack/compat-exp/causes.json \
  hack/compat-exp/classification-prompt.md hack/compat-exp/staticcheck/main.go hack/compat-exp/ledger/ledger.go \
  hack/compat-exp/crossrun/main.go hack/compat-exp/candidates.json \
  hack/compat-pilot/observe.sh hack/compat-pilot/render-args.sh hack/compat-pilot/world.sh hack/compat-pilot/difftable/main.go \
  .github/workflows/compat-exp.yml docs/compat-experiment.md go.mod; do
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
# an instrument file deleted after the tag is a difference too
d="$tmp/deleted"
cp -R "$r" "$d"
git -C "$d" rm -q hack/compat-exp/analyze.sh
commit "$d" "analyze.sh deleted"
g "$d" observe
refuses "a frozen file deleted after the tag" "differ from tag"
# a freeze that lacks part of the instrument is incomplete
for f in hack/compat-exp/static.sh hack/compat-exp/analyze.sh hack/compat-exp/classification-prompt.md docs/compat-experiment.md \
  hack/compat-exp/crossrun hack/compat-exp/ledger hack/compat-exp/staticcheck hack/compat-exp/frame/exclusions.json; do
  d="$tmp/incomplete-$(tr '/.' '__' <<<"$f")"
  newrepo "$d"
  git -C "$d" rm -rq "$f"
  git -C "$d" commit -q -m "without $f"
  freeze "$d"
  mklist 3 "$d/hack/compat-exp/charts-exp.json"
  commit "$d" list
  g "$d" observe
  refuses "a tag without $f" "freeze is incomplete"
done
# a file added to a frozen tool is a difference
d="$tmp/newtool"
cp -R "$r" "$d"
echo "package main" >"$d/hack/compat-exp/crossrun/extra.go"
commit "$d" "a crossrun file added after the freeze"
g "$d" observe
refuses "a file added to a frozen tool after the tag" "differ from tag"
d="$tmp/untracked"
cp -R "$r" "$d"
echo x >"$d/hack/compat-exp/frame/extra.go"
g "$d" observe
refuses "untracked file among frozen paths" "untracked"
# data may change: fixes, patches, ledger-like files
d="$tmp/data"
cp -R "$r" "$d"
echo "another patch" >"$d/hack/compat-exp/patches/new.patch"
jq '.rows += [{"chart":"c1","world":"W3","key":"k","status":"unresolved"}]' "$d/hack/compat-exp/ledger.json" >"$d/x" && mv "$d/x" "$d/hack/compat-exp/ledger.json"
echo '[]' >"$d/hack/compat-exp/fixes/new.json"
echo '[]' >"$d/hack/compat-exp/ignore/new.json"
commit "$d" "data"
g "$d" observe
passes "a patch, a ledger row, fix entries and ignore lists are data, not frozen"
d="$tmp/data2"
cp -R "$r" "$d"
echo "another patch" >"$d/hack/compat-exp/patches/new.patch"
g "$d" observe
passes "an uncommitted patch is data too"
d="$tmp/data3"
cp -R "$r" "$d"
jq '.newKeys += [{"key":"nk"}]' "$d/hack/compat-exp/ledger.json" >"$d/x" && mv "$d/x" "$d/hack/compat-exp/ledger.json"
g "$d" observe
passes "a ledger edit (even an incomplete new key) is not a freeze matter"

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
# F6: a frozen list names the qualification run it was selected from
d="$tmp/noqual"
cp -R "$r" "$d"
jq -S 'del(.qualification) | del(.sha256)' "$d/hack/compat-exp/charts-exp.json" >"$d/x"
jq -S --arg h "$(jq -S -c . "$d/x" | sha)" '. + {sha256: $h}' "$d/x" >"$d/hack/compat-exp/charts-exp.json" && rm "$d/x"
commit "$d" "list without a qualification run"
for m in observe recheck; do
  g "$d" "$m" GUARD_CHART_IDS="c1"
  refuses "$m: a list without the qualification run id is refused" "no qualification run id"
done
fx0="$d/hack/compat-exp/fixes"
mkdir -p "$fx0"
echo '[{"id":"c1-fix","fixOf":"c1","cause":"protocol","chart":"c1","version":"1.0.0","repo":"https://example.test/1","values":["--set","a=b"],"cellWorlds":["W3"]}]' >"$fx0/f.json"
commit "$d" "fix"
g "$d" certify GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json GUARD_FIX_ID=c1-fix
refuses "certify: a list without the qualification run id is refused" "no qualification run id"
jq -S 'del(.sha256) | .qualification.run = "12;id"' "$r/hack/compat-exp/charts-exp.json" >"$d/x"
jq -S --arg h "$(jq -S -c . "$d/x" | sha)" '. + {sha256: $h}' "$d/x" >"$d/hack/compat-exp/charts-exp.json" && rm "$d/x"
commit "$d" "bad run id"
g "$d" observe
refuses "a qualification run id that is not digits is refused" "no qualification run id"
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
base='"fixOf":"c1","cause":"protocol","chart":"c1","version":"1.0.0","repo":"https://example.test/1"'
fx "[{\"id\":\"c1-fix\",$base,\"values\":[\"--set\",\"a=b\"]}]"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json GUARD_CHART_IDS="c1 c1-fix"
passes "a values fix of a listed chart"
fx "[{\"id\":\"c1-fix\",$base,\"patch\":\"hack/compat-exp/patches/c1-fix.patch\"}]"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json GUARD_CHART_IDS="c1 c1-fix"
passes "a patch fix under patches/"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json GUARD_CHART_IDS="c1-fix"
refuses "a fix without its base in the selection" "without their base"
fx "[{\"id\":\"zz-fix\",\"fixOf\":\"zz\",\"cause\":\"protocol\",\"chart\":\"c1\",\"version\":\"1.0.0\",\"values\":[\"--set\",\"a=b\"]}]"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json
refuses "fixOf outside the frozen list" "not in the frozen chart list"
fx "[{\"id\":\"r1-fix\",\"fixOf\":\"r1\",\"cause\":\"protocol\",\"chart\":\"r1\",\"version\":\"1.0.0\",\"values\":[\"--set\",\"a=b\"]}]"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json GUARD_CHART_IDS="r1 r1-fix"
passes "fixOf may name a reserve"
fx "[{\"id\":\"c1-fix\",\"fixOf\":\"c1\",\"cause\":\"made-up\",\"chart\":\"c1\",\"version\":\"1.0.0\",\"repo\":\"https://example.test/1\",\"values\":[\"--set\",\"a=b\"]}]"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json
refuses "cause outside causes.json" "neither a key of causes.json"
fx "[{\"id\":\"c1-fix\",\"fixOf\":\"c1\",\"chart\":\"c1\",\"version\":\"1.0.0\",\"repo\":\"https://example.test/1\",\"values\":[\"--set\",\"a=b\"]}]"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json
refuses "missing cause" "neither a key of causes.json"
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
fx "[{\"id\":\"c1-fix\",\"fixOf\":\"c1\",\"cause\":\"protocol\",\"chart\":\"c9\",\"version\":\"1.0.0\",\"values\":[\"--set\",\"a=b\"]}]"
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

# causes.json: the real file passes (every test above runs on a copy of it); other shapes do not exist
d="$tmp/causes-array"
newrepo "$d"
jq '.keys' "$d/hack/compat-exp/causes.json" >"$d/x" && mv "$d/x" "$d/hack/compat-exp/causes.json"
commit "$d" "array shape"
freeze "$d"
mklist 3 "$d/hack/compat-exp/charts-exp.json"
commit "$d" list
g "$d" observe
refuses "causes.json as a bare array is not read" "causes.json"
d="$tmp/causes-wrapped"
newrepo "$d"
jq '{causes: .keys}' "$d/hack/compat-exp/causes.json" >"$d/x" && mv "$d/x" "$d/hack/compat-exp/causes.json"
commit "$d" "causes wrapper"
freeze "$d"
mklist 3 "$d/hack/compat-exp/charts-exp.json"
commit "$d" list
g "$d" observe
refuses "causes.json as {causes: [...]} is not read" "causes.json"

# a fix entry's cause: a key of causes.json, or a new key with a complete record in the ledger
mk_nk() { # JSON of the newKeys array
  jq --argjson n "$1" '.newKeys = $n' "$r2/hack/compat-exp/ledger.json" >"$r2/x" && mv "$r2/x" "$r2/hack/compat-exp/ledger.json"
  commit "$r2" "ledger newKeys"
}
nk_fix="[{\"id\":\"c1-fix\",\"fixOf\":\"c1\",\"cause\":\"brand-new\",\"chart\":\"c1\",\"version\":\"1.0.0\",\"repo\":\"https://example.test/1\",\"values\":[\"--set\",\"a=b\"]}]"
fx "$nk_fix"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json
refuses "a new key that is not in the ledger" "neither a key of causes.json nor a new key"
full='{"key":"brand-new","anchor":"https://istio.io/x","certifiedFix":"c1-fix","reproduction":"hack/compat-exp/repro/x.sh","reviewer":{"verdict":"admit","run":"123"}}'
mk_nk "[$full]"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json
passes "a new key with a complete record in the ledger"
for missing in anchor certifiedFix reproduction reviewer; do
  mk_nk "[$(jq -c "del(.$missing)" <<<"$full")]"
  g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json
  refuses "a new key without $missing" "neither a key of causes.json nor a new key"
done
mk_nk "[$(jq -c '.reviewer.verdict = "merge"' <<<"$full")]"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json
refuses "a new key the reviewer did not admit" "neither a key of causes.json nor a new key"
mk_nk "[$(jq -c '.reviewer.run = ""' <<<"$full")]"
g "$r2" observe GUARD_FIXES_FILE=hack/compat-exp/fixes/f.json
refuses "a new key whose reviewer run is empty" "neither a key of causes.json nor a new key"
mk_nk '[]'

if [[ $fails -ne 0 ]]; then echo "$fails failure(s)" >&2; exit 1; fi
echo "all guard tests passed"
