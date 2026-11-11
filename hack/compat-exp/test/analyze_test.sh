#!/usr/bin/env bash
#
# Refusals and failures of hack/compat-exp/analyze.sh, with a stub gh. The
# successful path is in e2e_test.sh.
#
#   hack/compat-exp/test/analyze_test.sh
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

mkdir -p "$T/bin" "$T/fix"
cat >"$T/bin/gh" <<'STUB'
#!/usr/bin/env bash
echo "gh $*" >>"$GH_LOG"
[[ "$1 $2" == "run download" || "$1 $2" == "run view" ]] || exit 9
id="$3"
if [[ "$2" == download ]]; then
  while [[ $# -gt 0 ]]; do case "$1" in -D) dir="$2"; shift 2 ;; *) shift ;; esac; done
  [[ -d "$GH_FIX/$id" ]] || { echo "no such run" >&2; exit 1; }
  cp -R "$GH_FIX/$id/." "$dir/"
else
  cat "$GH_FIX/$id.view.json"
fi
STUB
chmod +x "$T/bin/gh"
export GH_LOG="$T/gh.log" GH_FIX="$T/fix"
for id in 11 12; do
  mkdir -p "$T/fix/$id/results"
  echo '{"schema":1,"oracle":"W0","worlds":["W1","W3"],"runsRequired":3,"charts":[]}' >"$T/fix/$id/report-$id.json"
  echo '{"status":"completed","conclusion":"success","startedAt":"2026-11-20T08:00:00Z","updatedAt":"2026-11-20T09:00:00Z"}' >"$T/fix/$id.view.json"
done
echo '{"schema":1,"rows":[],"newKeys":[]}' >"$T/ledger-good.json"
echo '{"schema":1,"rows":[{"chart":"x"}],"newKeys":[]}' >"$T/ledger-bad.json"
echo '{"charts":[],"reserves":[]}' >"$T/charts.json"

an() { # args...: rc, stderr text
  : >"$GH_LOG"
  env PATH="$T/bin:$PATH" CROSSRUN=/nonexistent/crossrun LEDGER="${LEDGER_BIN:-/nonexistent/ledger}" STATICCHECK=/nonexistent/sc \
    "$repo/hack/compat-exp/analyze.sh" "$@" >"$T/out" 2>"$T/err"
  rc=$?
}
gh_calls() { wc -l <"$GH_LOG" | tr -d ' '; }

for bad_id in 'abc' '10;id' '1 2' '-5' '1e5' '../x' '' '$(id)' '99999999999999999999999'; do
  an --out "$T/o1" --run1 "$bad_id" --run2 12 --charts "$T/charts.json" --ledger "$T/ledger-good.json"
  eq "run1 '$bad_id' is refused" "$rc" 2
  eq "  and gh is not called" "$(gh_calls)" 0
done
an --out "$T/o2" --run1 11 --run2 12x --charts "$T/charts.json" --ledger "$T/ledger-good.json"
eq "run2 with a letter is refused" "$rc" 2
an --out "$T/o3" --run1 11 --run2 12 --cert 1a --charts "$T/charts.json" --ledger "$T/ledger-good.json"
eq "a cert id with a letter is refused" "$rc" 2
an --out "$T/o4" --run1 11 --run2 12 --recheck '1;2' --charts "$T/charts.json" --ledger "$T/ledger-good.json"
eq "a recheck id with a metacharacter is refused" "$rc" 2
an --out "$T/o5" --run1 11 --run2 11 --charts "$T/charts.json" --ledger "$T/ledger-good.json"
eq "the same id twice is refused" "$rc" 2
an --out "$T/o6" --run1 11 --run2 12 --repo 'a b/c' --charts "$T/charts.json" --ledger "$T/ledger-good.json"
eq "a malformed --repo is refused" "$rc" 2
mkdir -p "$T/full" && touch "$T/full/x"
an --out "$T/full" --run1 11 --run2 12 --charts "$T/charts.json" --ledger "$T/ledger-good.json"
eq "an output directory that is not empty is refused" "$rc" 2
eq "  without calling gh" "$(gh_calls)" 0
an --run1 11
eq "no --out is a usage error" "$rc" 2

an --out "$T/o7" --run1 11 --run2 99 --charts "$T/charts.json" --ledger "$T/ledger-good.json"
eq "a failing gh run download stops with exit 1" "$rc" 1
grep -q "gh run download 99 failed" "$T/err" && ok "  and names the run" || bad "  and names the run"

echo '{"status":"in_progress","conclusion":"","startedAt":"2026-11-20T08:00:00Z","updatedAt":"2026-11-20T09:00:00Z"}' >"$T/fix/12.view.json"
an --out "$T/o8" --run1 11 --run2 12 --charts "$T/charts.json" --ledger "$T/ledger-good.json"
eq "a run that has not completed is refused" "$rc" 1
grep -q "not completed" "$T/err" && ok "  and says so" || bad "  and says so"
echo '{"status":"completed","startedAt":"yesterday","updatedAt":"2026-11-20T09:00:00Z"}' >"$T/fix/12.view.json"
an --out "$T/o9" --run1 11 --run2 12 --charts "$T/charts.json" --ledger "$T/ledger-good.json"
eq "a time that is not RFC 3339 is refused" "$rc" 1
echo '{"status":"completed","startedAt":"2026-11-20T08:00:00Z","updatedAt":"2026-11-20T09:00:00Z"}' >"$T/fix/12.view.json"

(cd "$repo" && go build -o "$T/ledger" ./hack/compat-exp/ledger/cmd/ledger) && LEDGER_BIN="$T/ledger"
an --out "$T/o10" --run1 11 --run2 12 --charts "$T/charts.json" --ledger "$T/ledger-bad.json"
eq "a ledger that fails its check stops before static.sh and crossrun" "$rc" 1
grep -q "ledger check failed" "$T/err" && ok "  and says so" || bad "  and says so"
[[ ! -e "$T/o10/static.json" && ! -e "$T/o10/result.json" ]] && ok "  and writes no result" || bad "  and writes no result"

an --out "$T/o11" --run1 11 --run2 12 --repo Git-Hospital/meshmedic --charts "$T/charts.json" --ledger "$T/ledger-bad.json"
grep -q -- "-R Git-Hospital/meshmedic" "$GH_LOG" && ok "--repo is passed to gh as -R" || bad "--repo is passed to gh as -R"
eq "gh was only asked to download and view" "$(grep -cv '^gh run \(download\|view\) ' "$GH_LOG")" 0

echo
echo "$pass passed, $fail failed"
[[ $fail -eq 0 ]]
