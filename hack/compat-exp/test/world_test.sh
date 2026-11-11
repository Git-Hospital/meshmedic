#!/usr/bin/env bash
#
# Tests for the registry mirror in hack/compat-pilot/world.sh. No kind, no
# cluster: the dry mode prints the generated kind command and config, and a
# full run against stub kind, kubectl, istioctl and date compares the mirror-off
# behaviour with origin/main's world.sh, call for call and byte for byte.
#
#   hack/compat-exp/test/world_test.sh
#
# The mirror has not been exercised on a real cluster by this test.
# The ok-or-fail one-liners are deliberate: ok and fail always succeed.
# shellcheck disable=SC2015,SC2001
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../../.." && pwd)"
world_sh="$repo/hack/compat-pilot/world.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fails=0
fail() {
  echo "FAIL: $*" >&2
  fails=$((fails + 1))
}
ok() { echo "ok: $*"; }

assert_contains() { # name text needle
  if grep -qF -- "$3" <<<"$2"; then ok "$1"; else fail "$1: missing: $3"; fi
}
assert_lacks() { # name text needle
  if grep -qF -- "$3" <<<"$2"; then fail "$1: unexpected: $3"; else ok "$1"; fi
}

# --- stubs -------------------------------------------------------------------
bin="$tmp/bin"
mkdir -p "$bin"
cat >"$bin/kind" <<'EOS'
#!/usr/bin/env bash
echo "kind $*" >>"$STUB_LOG"
if [[ "$1 $2" == "get clusters" ]]; then exit 0; fi
prev=""
for a in "$@"; do
  if [[ "$prev" == "--config" ]]; then
    echo "--- config $(basename "$a")" >>"$STUB_LOG"
    cat "$a" >>"$STUB_LOG"
  fi
  prev="$a"
done
EOS
cat >"$bin/kubectl" <<'EOS'
#!/usr/bin/env bash
echo "kubectl $*" >>"$STUB_LOG"
case "$*" in
  "version -o json") echo '{"serverVersion":{"gitVersion":"v1.36.4"}}' ;;
  *"get deploy istiod"*) echo '{"spec":{"template":{"spec":{"containers":[{"env":[]}]}}}}' ;;
esac
EOS
cat >"$bin/istioctl" <<'EOS'
#!/usr/bin/env bash
echo "istioctl $*" >>"$STUB_LOG"
if [[ "$1" == version ]]; then echo "{\"clientVersion\":{\"version\":\"$ISTIO_VERSION\"}}"; fi
EOS
cat >"$bin/date" <<'EOS'
#!/usr/bin/env bash
echo 2026-01-02T03:04:05Z
EOS
chmod +x "$bin"/*
export ISTIO_VERSION=1.31.3
unset MMC_REGISTRY_MIRROR MMC_DRY_RUN KIND_NODE_IMAGE MMC_GATEWAY_API

dry() { # world mirror-value (empty = unset)
  local w="$1" m="$2"
  if [[ -n "$m" ]]; then
    MMC_DRY_RUN=1 MMC_REGISTRY_MIRROR="$m" MMC_STATE=/state PATH="$bin:$PATH" "$world_sh" "$w"
  else
    MMC_DRY_RUN=1 MMC_STATE=/state PATH="$bin:$PATH" "$world_sh" "$w"
  fi
}

# --- 1. dry mode, every world, mirror on and off -----------------------------
ref_on=""
for w in W0 W1 W2 W3 W4; do
  lc="$(tr '[:upper:]' '[:lower:]' <<<"$w")"
  on="$(dry "$w" 1)"
  off="$(dry "$w" 0)"
  unset_out="$(dry "$w" "")"

  assert_contains "$w on: kind command names the config" "$on" "--config /state/mmc-$lc.kind.yaml"
  assert_contains "$w on: config_path set" "$on" 'config_path = "/etc/containerd/certs.d"'
  assert_contains "$w on: docker.io mount" "$on" "containerPath: /etc/containerd/certs.d/docker.io"
  assert_contains "$w on: mount is read-only" "$on" "readOnly: true"
  assert_contains "$w on: mirror first" "$on" '[host."https://mirror.gcr.io"]'
  assert_contains "$w on: upstream fallback" "$on" 'server = "https://registry-1.docker.io"'
  mirror_line="$(grep -n 'mirror.gcr.io' <<<"$on" | head -1 | cut -d: -f1)"
  server_line="$(grep -n '^server' <<<"$on" | head -1 | cut -d: -f1)"
  [[ -n "$mirror_line" && -n "$server_line" ]] && ok "$w on: both endpoints present" || fail "$w on: endpoints"

  assert_lacks "$w off: no config file" "$off" "--config"
  assert_lacks "$w off: no mirror text" "$off" "mirror.gcr.io"
  [[ "$off" == "$unset_out" ]] && ok "$w: unset equals 0" || fail "$w: unset differs from 0"
  [[ "$off" == "kind create cluster --name mmc-$lc --kubeconfig /state/mmc-$lc.kubeconfig --wait 180s" ]] &&
    ok "$w off: exact pilot kind command" || fail "$w off: kind command is: $off"

  # Identical in every world apart from the cluster name.
  norm="$(sed "s/mmc-$lc/mmc-X/g" <<<"$on")"
  if [[ -z "$ref_on" ]]; then ref_on="$norm"; elif [[ "$norm" == "$ref_on" ]]; then ok "$w on: same config as W0"; else fail "$w on: differs from W0"; fi
done

out="$(MMC_DRY_RUN=1 MMC_REGISTRY_MIRROR=1 MMC_STATE=/state KIND_NODE_IMAGE=kindest/node:vX PATH="$bin:$PATH" "$world_sh" W0)"
assert_contains "node image flag kept after the config flag" "$out" "--config /state/mmc-w0.kind.yaml --image kindest/node:vX --wait 180s"

if MMC_REGISTRY_MIRROR=yes MMC_DRY_RUN=1 MMC_STATE=/state PATH="$bin:$PATH" "$world_sh" W0 >/dev/null 2>&1; then
  fail "bad switch value accepted"
else ok "bad switch value refused"; fi

# --- 2. mirror off against origin/main, full run with stubs -----------------
base_ref="${BASE_REF:-origin/main}"
if git -C "$repo" cat-file -e "$base_ref:hack/compat-pilot/world.sh" 2>/dev/null; then
  git -C "$repo" show "$base_ref:hack/compat-pilot/world.sh" >"$tmp/world_base.sh"
  chmod +x "$tmp/world_base.sh"
  for w in W0 W1 W3; do
    for variant in base new; do
      st="$tmp/state-$variant-$w"
      export STUB_LOG="$tmp/log-$variant-$w"
      : >"$STUB_LOG"
      script="$tmp/world_base.sh"
      [[ "$variant" == new ]] && script="$world_sh"
      (cd "$tmp" && MMC_STATE="$st" PATH="$bin:$PATH" "$script" "$w" >"$tmp/stdout-$variant-$w")
    done
    lc="$(tr '[:upper:]' '[:lower:]' <<<"$w")"
    # The state directory differs between the two runs; mask it.
    for variant in base new; do
      sed "s#$tmp/state-$variant-$w#STATE#g" "$tmp/log-$variant-$w" >"$tmp/log-$variant-$w.n"
      sed "s#$tmp/state-$variant-$w#STATE#g" "$tmp/stdout-$variant-$w" >"$tmp/stdout-$variant-$w.n"
    done
    cmp -s "$tmp/log-base-$w.n" "$tmp/log-new-$w.n" && ok "$w off: stub call log identical to $base_ref" ||
      { fail "$w off: stub call log differs from $base_ref"; diff "$tmp/log-base-$w.n" "$tmp/log-new-$w.n" >&2 || true; }
    cmp -s "$tmp/stdout-base-$w.n" "$tmp/stdout-new-$w.n" && ok "$w off: stdout identical" || fail "$w off: stdout differs"
    cmp -s "$tmp/state-base-$w/mmc-$lc.world" "$tmp/state-new-$w/mmc-$lc.world" && ok "$w off: world file byte-identical" ||
      fail "$w off: world file differs"
    # Nothing new in the state directory.
    [[ "$(cd "$tmp/state-base-$w" && find . | sort)" == "$(cd "$tmp/state-new-$w" && find . | sort)" ]] &&
      ok "$w off: same files in the state directory" || fail "$w off: state directory differs"
  done
else
  echo "SKIP: $base_ref has no hack/compat-pilot/world.sh; comparison against the base not run" >&2
  fails=$((fails + 1))
fi

# --- 3. mirror on, full run: files and world file ----------------------------
for w in W0 W3; do
  lc="$(tr '[:upper:]' '[:lower:]' <<<"$w")"
  st="$tmp/state-on-$w"
  export STUB_LOG="$tmp/log-on-$w"
  : >"$STUB_LOG"
  (cd "$tmp" && MMC_REGISTRY_MIRROR=1 MMC_STATE="$st" PATH="$bin:$PATH" "$world_sh" "$w" >/dev/null)
  grep -q '^registry_mirror=docker.io:https://mirror.gcr.io,https://registry-1.docker.io$' "$st/mmc-$lc.world" &&
    ok "$w on: world file records the endpoints" || fail "$w on: world file lacks registry_mirror"
  [[ -f "$st/registry-mirror/docker.io/hosts.toml" ]] && ok "$w on: hosts.toml written" || fail "$w on: hosts.toml missing"
  grep -qF "hostPath: $st/registry-mirror/docker.io" "$tmp/log-on-$w" && ok "$w on: config mounts the written directory" ||
    fail "$w on: config does not mount the written directory"
  grep -q "^kind create cluster .*--config $st/mmc-$lc.kind.yaml" "$tmp/log-on-$w" && ok "$w on: kind got --config" || fail "$w on: no --config"
done

if [[ $fails -gt 0 ]]; then
  echo "$fails failure(s)" >&2
  exit 1
fi
echo "all world mirror tests passed"
