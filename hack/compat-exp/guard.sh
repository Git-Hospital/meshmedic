#!/usr/bin/env bash
#
# The guard of .github/workflows/compat-exp.yml: everything that must be true
# before a runner creates a cluster for the compat experiment
# (docs/compat-experiment.md sections 4, 5 and 6, work item 8). It lives here,
# not in the workflow YAML, so it can be tested without Actions
# (hack/compat-exp/test/guard_test.sh).
#
# Usage:
#   guard.sh check          validate the inputs and the repository; exit 0 or 1
#   guard.sh freeze [SHA]   only the freeze: the tag exists, HEAD (or the full commit
#                           SHA) is the tag or a descendant, and every frozen file is
#                           byte-identical to the tag's (for HEAD: in the working tree
#                           too, with no untracked file among them). analyze.sh runs
#                           this, from the tag's copy of this script, on HEAD and on
#                           every workflow run's head commit. No other input is read.
#   guard.sh plan OUTDIR    check again, then write OUTDIR/charts.json (the
#                           chart list observe.sh and qualify.sh read) and
#                           OUTDIR/matrix.json, and print key=value lines for
#                           $GITHUB_OUTPUT: matrix, count, mode, kind, istio,
#                           fix_id, cell_worlds, ignore
#
# Inputs come from the environment, never from the command line, so the
# workflow passes them through `env:` and nothing is interpolated into a script:
#   GUARD_MODE          acceptance | qualify | observe | certify | recheck
#   GUARD_CHARTS_FILE   chart list path under hack/compat-exp/ (default by mode)
#   GUARD_FIXES_FILE    fix entries path under hack/compat-exp/ (optional)
#   GUARD_CHART_IDS     space separated ids (optional)
#   GUARD_FIX_ID        the fix entry to certify (certify only)
#   GUARD_ISTIO         the Istio version chosen
#   ISTIO_PRIMARY       the primary pinned version (the workflow's own)
#   GUARD_ROOT          repository root (default: git toplevel)
#   FREEZE_TAG          default compat-exp-freeze
#   MAX_MATRIX          default 256 (GitHub's limit per run)
#
# What it enforces:
#  1. Every input matches a strict pattern before it is used.
#  2. Istio: every mode except recheck runs on ISTIO_PRIMARY; recheck runs on a
#     plain x.y.z version, so the placeholder left in the workflow before the
#     freeze fails.
#  3. Every mode except acceptance needs the tag, HEAD at the tag or a
#     descendant of it, and every frozen instrument file byte-identical to the
#     tag's version (and no untracked file among them).
#  4. The chart list: its stored sha256 matches its canonical content (jq -S -c
#     of the file without the sha256 key), it names the qualification run it was
#     selected from (observe, certify, recheck), and at most one commit since
#     the tag touched it (HEAD's ancestry only: analyze.sh's rerun of select.sh on
#     the qualification run's records is what binds the sample). In qualify mode the list is the candidate list, which must be
#     identical to the tag's.
#  5. Every fix entry: fixOf names a chart in the frozen list (charts and
#     reserves), its cause is a key of causes.json or a new key with a complete
#     record in ledger.json, it pins the same chart,
#     version and repo, its values are --set/--set-string/--set-json pairs only
#     (no flag that runs code, such as --post-renderer), its patch is a file under
#     hack/compat-exp/patches/, its cellWorlds (certify) are mesh worlds and its
#     ignore file sits under hack/compat-exp/ignore/.
#  6. The matrix (selected charts x worlds) has at most MAX_MATRIX jobs.
#
# What it cannot check: that the workflow file that is running is the one it was
# meant to be (a descendant can change this very file, which check 3 catches
# only if the guard job still runs), that the tag was not moved (use a tag
# protection rule), and the content of a patch beyond its location (observe.sh
# applies it with fuzz 0, bounds it to 40 changed lines and lints it).
set -uo pipefail

die() {
  echo "guard: $*" >&2
  exit 1
}

cmd="${1:-}"
case "$cmd" in
  check | plan | freeze) ;;
  *) echo "usage: guard.sh check | guard.sh plan OUTDIR | guard.sh freeze [SHA]" >&2; exit 2 ;;
esac
outdir="${2:-}"
[[ "$cmd" != plan || -n "$outdir" ]] || { echo "usage: guard.sh plan OUTDIR" >&2; exit 2; }

command -v jq >/dev/null || die "jq is needed"
root="${GUARD_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null)}"
[[ -n "$root" && -d "$root" ]] || die "no repository root"
tag="${FREEZE_TAG:-compat-exp-freeze}"
max_matrix="${MAX_MATRIX:-256}"
mode="${GUARD_MODE:-}"
istio="${GUARD_ISTIO:-}"
primary="${ISTIO_PRIMARY:-}"
fix_id="${GUARD_FIX_ID:-}"
chart_ids="${GUARD_CHART_IDS:-}"
fixes_in="${GUARD_FIXES_FILE:-}"
charts_in="${GUARD_CHARTS_FILE:-}"

git_() { git -C "$root" "$@"; }

# --- patterns ---------------------------------------------------------------------

re_semver='^[0-9]+\.[0-9]+\.[0-9]+$'
re_id='^[a-z0-9]([a-z0-9-]{0,51}[a-z0-9])?$'
re_ids='^[a-z0-9 -]*$'
re_path='^hack/compat-exp/[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$'

safe_path() { # path prefix suffix-regex label
  local p="$1" prefix="$2" suffix="$3" label="$4"
  [[ "$p" =~ $re_path ]] || die "$label '$p' is not a plain path under hack/compat-exp/"
  [[ "/$p/" != *"/../"* && "/$p/" != *"/./"* ]] || die "$label '$p' has a dot segment"
  [[ "$p" == "$prefix"* ]] || die "$label '$p' is outside $prefix"
  [[ "$p" =~ $suffix ]] || die "$label '$p' has the wrong file type"
  [[ -f "$root/$p" && ! -L "$root/$p" ]] || die "$label '$p' is not a regular file"
}

# Paths whose bytes must equal the tag's: the whole instrument (docs/compat-experiment.md
# section 5). Data stays outside it: ledger.json, patches/, fix entries (fixes/, ignore/) and
# the frozen chart list, which has its own rule below (one commit since the tag).
# .github/workflows is frozen as a whole: a workflow file added or changed after the tag
# (one that names itself compat-exp and uploads its own metadata) fails the freeze.
frozen=(
  hack/compat-exp/frame
  hack/compat-exp/frame/exclusions.json
  hack/compat-exp/qualify.sh
  hack/compat-exp/select.sh
  hack/compat-exp/static.sh
  hack/compat-exp/analyze.sh
  hack/compat-exp/acceptance-check.sh
  hack/compat-exp/acceptance-expected.json
  hack/compat-exp/guard.sh
  hack/compat-exp/causes.json
  hack/compat-exp/classification-prompt.md
  hack/compat-exp/staticcheck
  hack/compat-exp/ledger
  hack/compat-exp/crossrun
  hack/compat-exp/candidates.json
  hack/compat-exp/candidates-charts.json
  hack/compat-pilot/observe.sh
  hack/compat-pilot/render-args.sh
  hack/compat-pilot/world.sh
  hack/compat-pilot/difftable
  .github/workflows
  .github/workflows/compat-exp.yml
  docs/compat-experiment.md
  go.mod
  go.sum
)
# Must exist in the tag, or the freeze is incomplete. candidates.json and
# candidates-charts.json are checked where they are used (qualify mode).
required_at_tag=(
  hack/compat-exp/frame hack/compat-exp/frame/exclusions.json
  hack/compat-exp/qualify.sh hack/compat-exp/select.sh hack/compat-exp/static.sh hack/compat-exp/analyze.sh
  hack/compat-exp/acceptance-check.sh hack/compat-exp/acceptance-expected.json
  hack/compat-exp/guard.sh hack/compat-exp/causes.json hack/compat-exp/classification-prompt.md
  hack/compat-exp/staticcheck hack/compat-exp/ledger hack/compat-exp/crossrun
  hack/compat-pilot/observe.sh hack/compat-pilot/render-args.sh hack/compat-pilot/world.sh hack/compat-pilot/difftable
  .github/workflows/compat-exp.yml docs/compat-experiment.md
)


# check_freeze [SHA] [WHY]: the tag exists, TARGET (HEAD with the working tree, or
# the commit SHA) is the tag or a descendant, the instrument is complete at the
# tag, and every frozen file equals the tag's.
check_freeze() {
  local target="${1:-}" why="${2:-}" tagc
  tagc="$(git_ rev-parse -q --verify "refs/tags/$tag^{commit}")" ||
    die "tag $tag does not exist${why:+; $why}"
  if [[ -n "$target" ]]; then
    [[ "$target" =~ ^[0-9a-f]{40}$ ]] || die "'$target' is not a full commit sha"
    git_ cat-file -e "$target^{commit}" 2>/dev/null || die "commit $target is not in this repository (git fetch it first)"
    git_ merge-base --is-ancestor "$tagc" "$target" || die "commit $target is not tag $tag or a descendant of it"
  else
    git_ merge-base --is-ancestor "$tagc" HEAD || die "HEAD is not tag $tag or a descendant of it"
  fi
  local p
  for p in "${required_at_tag[@]}"; do
    git_ cat-file -e "$tagc:$p" 2>/dev/null || die "$p is not in tag $tag; the freeze is incomplete"
  done
  if [[ -n "$target" ]]; then
    git_ diff --quiet "$tagc" "$target" -- "${frozen[@]}" ||
      die "frozen instrument files at $target differ from tag $tag: $(git_ diff --name-only "$tagc" "$target" -- "${frozen[@]}" | head -5 | tr '\n' ' ')"
  else
    git_ diff --quiet "$tagc" -- "${frozen[@]}" ||
      die "frozen instrument files differ from tag $tag: $(git_ diff --name-only "$tagc" -- "${frozen[@]}" | head -5 | tr '\n' ' ')"
    [[ -z "$(git_ ls-files --others --exclude-standard -- "${frozen[@]}")" ]] ||
      die "untracked files among the frozen instrument paths"
  fi
}

if [[ "$cmd" == freeze ]]; then
  [[ -z "$outdir" || "$outdir" != -* ]] || { echo "usage: guard.sh freeze [SHA]" >&2; exit 2; }
  check_freeze "$outdir" ""
  echo "guard: freeze ok: ${outdir:-HEAD} descends from $tag, frozen files unchanged" >&2
  exit 0
fi

# --- 1. inputs -----------------------------------------------------------------------

case "$mode" in
  acceptance | qualify | observe | certify | recheck) ;;
  *) die "mode '$mode' is not one of acceptance, qualify, observe, certify, recheck" ;;
esac
[[ "$primary" =~ $re_semver ]] || die "ISTIO_PRIMARY '$primary' is not x.y.z"
if [[ "$mode" == recheck ]]; then
  [[ "$istio" =~ $re_semver ]] ||
    die "recheck needs the recheck version decided at the freeze, but the workflow still offers '$istio'; edit the marked line in compat-exp.yml in the freeze commit"
else
  [[ "$istio" == "$primary" ]] || die "mode $mode runs on Istio $primary only, not '$istio'"
fi
[[ "$chart_ids" =~ $re_ids ]] || die "chart ids may hold only a-z, 0-9, '-' and spaces"
if [[ -n "$fix_id" ]]; then [[ "$fix_id" =~ $re_id ]] || die "fix id '$fix_id' is not a plain id"; fi
[[ "$mode" != certify || -n "$fix_id" ]] || die "certify needs fix_id"
[[ "$mode" == certify || -z "$fix_id" ]] || die "fix_id is for certify only"

# The tag first: without it nothing else is worth reporting.
if [[ "$mode" != acceptance ]]; then
  git_ rev-parse -q --verify "refs/tags/$tag^{commit}" >/dev/null ||
    die "tag $tag does not exist; mode $mode refuses to run before the freeze (only acceptance may)"
fi

case "$mode" in
  acceptance)
    [[ -z "$charts_in" || "$charts_in" == hack/compat-exp/acceptance-charts.json ]] ||
      die "acceptance runs hack/compat-exp/acceptance-charts.json only"
    charts_in=hack/compat-exp/acceptance-charts.json
    ;;
  qualify) charts_in="${charts_in:-hack/compat-exp/candidates-charts.json}" ;;
  *) charts_in="${charts_in:-hack/compat-exp/charts-exp.json}" ;;
esac
safe_path "$charts_in" hack/compat-exp/ '\.json$' "chart list"
if [[ -n "$fixes_in" ]]; then
  [[ "$mode" != qualify && "$mode" != acceptance ]] || die "$mode takes no fixes file"
  safe_path "$fixes_in" hack/compat-exp/fixes/ '\.json$' "fixes file"
fi

# --- 3. the freeze -----------------------------------------------------------------------

if [[ "$mode" != acceptance ]]; then
  check_freeze "" "mode $mode refuses to run before the freeze (only acceptance may)"
fi

# --- 4. the chart list ----------------------------------------------------------------------

sha() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum | cut -d' ' -f1; else shasum -a 256 | cut -d' ' -f1; fi
}
chart_file="$root/$charts_in"
jq -e . "$chart_file" >/dev/null 2>&1 || die "$charts_in is not JSON"

case "$mode" in
  acceptance | qualify)
    jq -e 'type == "array"' "$chart_file" >/dev/null || die "$charts_in must be an array of chart entries"
    if [[ "$mode" == qualify ]]; then
      git_ cat-file -e "refs/tags/$tag^{commit}:$charts_in" 2>/dev/null || die "$charts_in is not in tag $tag"
      git_ diff --quiet "refs/tags/$tag^{commit}" -- "$charts_in" || die "$charts_in differs from tag $tag"
    fi
    entries_json="$(jq -c '.' "$chart_file")"
    frozen_json="$(jq -c '[.[] | select(.fixOf == null)]' "$chart_file")"
    default_ids="$(jq -c '[.[].id]' "$chart_file")"
    ;;
  *)
    jq -e 'type == "object" and (.charts | type == "array") and (.reserves | type == "array") and (.sha256 | type == "string")' \
      "$chart_file" >/dev/null || die "$charts_in must be a charts-exp.json (charts, reserves, sha256)"
    # The qualification run id (select.sh writes it with the hashes of the
    # candidates and the records it chose from). Without it the list could have
    # been chosen after data existed. This check only demands that the origin
    # is stated; what protects the sample is analyze.sh, which downloads that
    # run's records, reruns select.sh on them and refuses to go on unless the
    # result is byte-identical to this list. The one-commit rule below counts
    # commits on HEAD's ancestry only, so it cannot see a list that was
    # committed on another branch first.
    jq -e '(.qualification | type == "object") and (.qualification.run | type == "string" and test("^[0-9]{1,20}$"))
        and (.qualification.candidatesSha256 | type == "string" and test("^[0-9a-f]{64}$"))
        and (.qualification.recordsSha256 | type == "string" and test("^[0-9a-f]{64}$"))' "$chart_file" >/dev/null ||
      die "$charts_in has no qualification run id (qualification.run, candidatesSha256, recordsSha256): a frozen list names the qualification run it was selected from"
    stored="$(jq -r .sha256 "$chart_file")"
    actual="$(jq -S -c 'del(.sha256)' "$chart_file" | sha)"
    [[ "$stored" == "$actual" ]] ||
      die "$charts_in: stored sha256 ${stored:0:12}... does not match its content ${actual:0:12}..."
    touched="$(git_ rev-list --count "refs/tags/$tag^{commit}..HEAD" -- "$charts_in")"
    [[ "$touched" -le 1 ]] || die "$charts_in was changed by $touched commits since the tag; the list is committed once"
    frozen_json="$(jq -c '.charts + .reserves' "$chart_file")"
    entries_json="$frozen_json"
    default_ids="$(jq -c '[.charts[].id]' "$chart_file")"
    ;;
esac

# --- 5. entries and fixes ------------------------------------------------------------------------

causes=
if [[ -f "$root/hack/compat-exp/causes.json" ]]; then
  # The real shape (hack/compat-exp/causes.json): an object whose list is under
  # `keys`, each entry with a string `key`. No other shape is read.
  causes="$(jq -c '.keys | map(.key | strings)' "$root/hack/compat-exp/causes.json" 2>/dev/null)" ||
    die "causes.json is not readable: it must be an object with a list under keys"
  [[ "$(jq length <<<"$causes")" -gt 0 ]] || die "causes.json holds no cause keys"
  # A fix entry's cause may also be a new key recorded in the ledger with a
  # complete new-key record (section 3 "New key"; ledger/ledger.go NewKey): an
  # anchor, a certified fix, a reproduction and an admitting reviewer verdict
  # with its run. `ledger check` (the guard job, certify and recheck) is the
  # full check; this is the part a fix entry's cause needs.
  if [[ -f "$root/hack/compat-exp/ledger.json" ]]; then
    # shellcheck disable=SC2016 # a jq program, not shell
    newkeys="$(jq -c --argjson base "$causes" '[.newKeys[]? | .key as $k
        | select(($k | type == "string") and ($k != "") and (($base | index($k)) == null)
            and ((.anchor // "") | gsub("\\s"; "") | length > 0)
            and ((.certifiedFix // "") | gsub("\\s"; "") | length > 0)
            and ((.reproduction // "") | gsub("\\s"; "") | length > 0)
            and (.reviewer.verdict == "admit") and ((.reviewer.run // "") != ""))
        | .key]' "$root/hack/compat-exp/ledger.json" 2>/dev/null)" || die "hack/compat-exp/ledger.json is not readable"
    causes="$(jq -c -n --argjson a "$causes" --argjson b "$newkeys" '$a + $b')"
  fi
elif [[ "$mode" != acceptance ]]; then
  die "hack/compat-exp/causes.json is missing"
fi

fixes_json='[]'
if [[ -n "$fixes_in" ]]; then
  jq -e 'type == "array"' "$root/$fixes_in" >/dev/null 2>&1 || die "$fixes_in must be an array of fix entries"
  fixes_json="$(jq -c . "$root/$fixes_in")"
  [[ "$(jq 'map(select(.fixOf == null)) | length' <<<"$fixes_json")" -eq 0 ]] || die "$fixes_in holds an entry without fixOf"
fi
if [[ "$mode" == acceptance ]]; then
  fixes_json="$(jq -c '[.[] | select(.fixOf != null)]' <<<"$entries_json")"
  entries_json="$frozen_json"
fi

# One pass over every entry that may reach a helm command line. jq returns the
# first problem or the empty string.
# shellcheck disable=SC2016 # a jq program, not shell
problems="$(jq -rn --argjson base "$entries_json" --argjson fixes "$fixes_json" --argjson causes "${causes:-null}" '
  def ok_id: type == "string" and test("^[a-z0-9]([a-z0-9-]{0,51}[a-z0-9])?$");
  def ok_chart: type == "string" and test("^(oci://)?[A-Za-z0-9][A-Za-z0-9._/:-]*$") and (contains("..") | not);
  def ok_repo: type == "string" and test("^https://[A-Za-z0-9._/:-]+$") and (contains("..") | not);
  def ok_version: type == "string" and test("^[0-9A-Za-z][0-9A-Za-z._+-]*$");
  def ok_values: type == "array" and (length % 2 == 0)
      and ([to_entries[] | select(.key % 2 == 0) | .value] | all(. == "--set" or . == "--set-string" or . == "--set-json"))
      and ([to_entries[] | select(.key % 2 == 1) | .value] | all(type == "string" and (contains("\n") | not)));
  def ok_patch: type == "string" and test("^hack/compat-exp/patches/[A-Za-z0-9._-]+\\.(patch|diff)$");
  def ok_ignore: type == "string" and test("^hack/compat-exp/ignore/[A-Za-z0-9._-]+\\.json$");
  def common($e):
      (if ($e.id | ok_id | not) then "id \($e.id) is not a plain id" else empty end),
      (if ($e.chart | ok_chart | not) then "\($e.id): chart is not a plain chart reference" else empty end),
      (if ($e.version | ok_version | not) then "\($e.id): version is not a plain version" else empty end),
      (if ($e | has("repo")) and ($e.repo | ok_repo | not) then "\($e.id): repo is not a plain https URL" else empty end),
      (if ($e | has("values")) and ($e.values | ok_values | not) then "\($e.id): values must be --set, --set-string or --set-json pairs" else empty end),
      (if ($e | has("deadline")) and (($e.deadline | type) != "string" or ($e.deadline | test("^[0-9]+[sm]$") | not)) then "\($e.id): deadline is not <n>s or <n>m" else empty end),
      (if ($e | has("patch")) and ($e.patch | ok_patch | not) then "\($e.id): patch is not a file under hack/compat-exp/patches/" else empty end);
  ($base | map(.id)) as $ids
  | (if ($ids | length) != ($ids | unique | length) then "duplicate ids in the chart list" else empty end),
    ($base[] | common(.)),
    ($fixes[] | . as $f
       | common($f),
         (if ($ids | index($f.fixOf)) == null then "\($f.id): fixOf \($f.fixOf) is not in the frozen chart list" else empty end),
         (if $causes != null and (($causes | index($f.cause)) == null) then "\($f.id): cause \($f.cause) is neither a key of causes.json nor a new key recorded completely in ledger.json" else empty end),
         (($base[] | select(.id == $f.fixOf)) as $b
            | if $b.chart != $f.chart or $b.version != $f.version or ($b.repo // "") != ($f.repo // "")
              then "\($f.id): chart, version and repo must equal those of \($f.fixOf)" else empty end),
         (if ($f | has("values") | not) and ($f | has("patch") | not) then "\($f.id): a fix needs values or a patch" else empty end),
         (if ($f | has("cellWorlds")) and (($f.cellWorlds | type) != "array" or ($f.cellWorlds | length) == 0
              or ($f.cellWorlds | all(. == "W1" or . == "W3") | not)) then "\($f.id): cellWorlds must be a non-empty list of W1, W3" else empty end),
         (if ($f | has("ignore")) and ($f.ignore | ok_ignore | not) then "\($f.id): ignore is not a file under hack/compat-exp/ignore/" else empty end)),
    ($fixes | map(.id) | . as $fids
       | if ($fids | length) != ($fids | unique | length) or ([$fids[] | select(. as $i | $ids | index($i))] | length) > 0
         then "fix ids are not unique or clash with chart ids" else empty end)
' 2>&1 | head -5)"
[[ -z "$problems" ]] || die "$(tr '\n' ';' <<<"$problems")"

# Files named by fix entries must exist as regular files.
while IFS= read -r p; do
  [[ -z "$p" ]] && continue
  [[ -f "$root/$p" && ! -L "$root/$p" ]] || die "'$p' named by a fix entry is not a regular file"
done < <(jq -r '.[] | (.patch // empty), (.ignore // empty)' <<<"$fixes_json")

# --- 2/6. selection and matrix ------------------------------------------------------------------

all_json="$(jq -c -n --argjson a "$entries_json" --argjson f "$fixes_json" '$a + $f')"
if [[ "$mode" == certify ]]; then
  fix_entry="$(jq -c --arg id "$fix_id" '.[] | select(.id == $id)' <<<"$fixes_json")"
  [[ -n "$fix_entry" ]] || die "fix '$fix_id' is not in the fixes file"
  [[ "$(jq -r '(.cellWorlds | length) > 0' <<<"$fix_entry")" == true ]] || die "fix '$fix_id' has no cellWorlds"
  selected="$(jq -c '[.fixOf, .id]' <<<"$fix_entry")"
else
  if [[ -n "$chart_ids" ]]; then
    selected="$(jq -c -n --arg s "$chart_ids" '$s | split(" ") | map(select(. != ""))')"
  else
    [[ "$mode" != recheck ]] || die "recheck needs chart_ids"
    selected="$default_ids"
  fi
  unknown="$(jq -rn --argjson sel "$selected" --argjson all "$all_json" '$sel - ($all | map(.id)) | join(" ")')"
  [[ -z "$unknown" ]] || die "ids not in the chart list or fixes: $unknown"
  bad="$(jq -rn --argjson sel "$selected" --argjson fixes "$fixes_json" \
    '[$fixes[] | select(.id as $i | $sel | index($i)) | select(.fixOf as $b | $sel | index($b) | not) | .id] | join(" ")')"
  [[ -z "$bad" ]] || die "fix(es) selected without their base chart: $bad"
fi
[[ "$(jq length <<<"$selected")" -gt 0 ]] || die "no charts selected"
[[ "$(jq 'length == (unique | length)' <<<"$selected")" == true ]] || die "an id is selected twice"

if [[ "$mode" == qualify ]]; then worlds='["W0"]'; else worlds='["W0","W1","W3"]'; fi
matrix="$(jq -c -n --argjson sel "$selected" --argjson w "$worlds" '{include: [$sel[] as $c | $w[] as $x | {chart: $c, world: $x}]}')"
count="$(jq '.include | length' <<<"$matrix")"
[[ "$count" -le "$max_matrix" ]] ||
  die "the matrix would have $count jobs, over the limit of $max_matrix per run (docs/compat-experiment.md section 6); select fewer charts"

echo "guard: ok: mode $mode, $(jq length <<<"$selected") charts, $count jobs, Istio $istio" >&2
[[ "$cmd" == plan ]] || exit 0

# --- plan output -----------------------------------------------------------------------------------

mkdir -p "$outdir" || die "cannot create $outdir"
jq -c -n --argjson a "$all_json" '$a' | jq . >"$outdir/charts.json"
jq . <<<"$matrix" >"$outdir/matrix.json"
cell_worlds=
ignore=
if [[ "$mode" == certify ]]; then
  cell_worlds="$(jq -r '.cellWorlds | join(",")' <<<"$fix_entry")"
  ignore="$(jq -r '.ignore // ""' <<<"$fix_entry")"
fi
kind=observe
[[ "$mode" != qualify ]] || kind=qualify
echo "matrix=$matrix"
echo "count=$count"
echo "mode=$mode"
echo "kind=$kind"
echo "istio=$istio"
echo "fix_id=$fix_id"
echo "cell_worlds=$cell_worlds"
echo "ignore=$ignore"
