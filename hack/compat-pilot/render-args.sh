#!/usr/bin/env bash
# Sourced, not run. What a chart entry means for helm, in one place, so that
# observe.sh (which installs) and hack/compat-exp/static.sh (which renders the
# same release for the static checks) cannot drift apart: the namespace, the
# release name, the pinned version, the repository and the values.
#
#   source hack/compat-pilot/render-args.sh
#   mmc_load_spec SPEC_JSON ID      # SPEC_JSON: one entry of a chart list
#
# sets: mmc_ns (the release namespace), chart, version, repo, deadline, fixof,
# release (the id, or the id of the chart a fix entry repairs), values (the
# helm arguments of the entry, an array), mmc_target (--namespace NS --version
# VERSION, an array) and mmc_repo_args (--repo REPO, or empty).
# shellcheck shell=bash disable=SC2034 # the variables are the interface

mmc_ns=pilot

mmc_load_spec() {
  local spec="$1" id="$2" v
  chart="$(jq -r .chart <<<"$spec")"
  version="$(jq -r .version <<<"$spec")"
  repo="$(jq -r '.repo // empty' <<<"$spec")"
  deadline="$(jq -r '.deadline // "8m"' <<<"$spec")"
  fixof="$(jq -r '.fixOf // empty' <<<"$spec")"
  # A candidate fix installs under the release name of the chart it fixes, so
  # every resource, and so every observation key, is named the same and the two
  # can be compared key by key.
  release="${fixof:-$id}"
  # A read loop rather than mapfile, which the bash 3.2 macOS ships lacks.
  values=()
  while IFS= read -r v; do values+=("$v"); done < <(jq -r '.values[]?' <<<"$spec")
  mmc_target=(--namespace "$mmc_ns" --version "$version")
  mmc_repo_args=()
  [[ -z "$repo" ]] || mmc_repo_args=(--repo "$repo")
}

# mmc_helm_env STATE: helm gets its own config and cache, as in observe.sh: a
# repository list left on the operator's machine changes how `--repo` resolves,
# and has been seen to make it fail outright on a stale cache entry.
mmc_helm_env() {
  export HELM_CONFIG_HOME="$1/helm/config" HELM_CACHE_HOME="$1/helm/cache"
  export HELM_REPOSITORY_CONFIG="$HELM_CONFIG_HOME/repositories.yaml"
  export HELM_REPOSITORY_CACHE="$HELM_CACHE_HOME/repository"
}
