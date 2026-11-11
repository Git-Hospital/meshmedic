# frame: the sampling frame of the compat experiment

`frame` turns Artifact Hub's star ranking into the candidate list, so nobody
picks charts by hand. The specification is section 4 of
[docs/compat-experiment.md](../../../docs/compat-experiment.md). Standard
library only.

## Use

```console
# the only command that uses the network: read-only GETs against artifacthub.io
$ go run ./hack/compat-exp/frame fetch -out /path/to/frame-raw

# offline: verifies SHA256SUMS first, then applies the rules
$ go run ./hack/compat-exp/frame build -in /path/to/frame-raw -out candidates.json

# candidates.json to the entry shape hack/compat-pilot/observe.sh reads
$ go run ./hack/compat-exp/frame charts -in candidates.json -out charts-exp-candidates.json
```

`fetch` stores, byte for byte, the five search pages (`kind=0&sort=stars&limit=60`,
offsets 0 to 240, the top 300 Helm packages) and, for every package that is not
deprecated, `GET /api/v1/packages/helm/<repository>/<name>`. It requests
sequentially, one second apart (`-pause`), with a 30 s timeout (`-timeout`),
and stops with the URL and status on anything but 200. The output directory
must be empty, so a stored set always comes from one pass. `SHA256SUMS` lists
every stored file.

`build` never touches the network. It refuses a directory whose files do not
match `SHA256SUMS`, or that holds a file the sums do not list.

## Rules, in the order build applies them

1. Walk the ranking: stars descending; ties keep the order Artifact Hub
   returned, then repository name, then package name. A response that is not
   sorted by stars is an error.
2. Skip deprecated packages.
3. Skip library charts: the package field `data.type` is `library`.
4. Skip CNIs, service meshes and CRD-only charts by `exclusions.json`.
5. Publisher: the GitHub owner (lower-cased) in the first source link that
   points at github.com, else the Artifact Hub repository name. A source link
   is a package link named source, sources, chart source or helm chart source.
6. Pin the version: the highest version, by semantic-version precedence
   (1.10.0 above 1.9.0), among the package detail's `available_versions` that
   has no pre-release part (no `-` suffix; `+build` metadata is fine). Artifact
   Hub's `prerelease` flag is ignored. When the newest listed version was a
   pre-release that got passed over, it is recorded as `skippedPrerelease`. A
   package with no stable version is skipped as `no-stable-version`. The
   detail's `content_url` describes the newest version, so it is not copied.
7. Skip a chart when its publisher already has 2 candidates. Skipped charts
   never use up a publisher's places.

Every skipped package is listed with its rank and reason. `candidates.json`
also records the sha256 of the input set (the hash of `SHA256SUMS`) and of
`exclusions.json`. The same stored input gives byte-identical output.
`build` keeps every survivor of the top 300 in rank order; taking the first 50
plus 15 reserves is the freeze step.

`charts` is a separate subcommand, not a second output of `build`. Entries
carry `id`, `repo` (classic repositories only), `chart`, `version` and `why`,
with no values: defaults only. `id` is the package name, or
`<repository>-<name>` when two candidates share a name.

## exclusions.json

A small committed data file, applied in file order, first match wins. Each
entry has a `class` (`cni`, `mesh`, `crd-only`), a `reason` that is copied into
`candidates.json`, and one or more match fields, all of which must match:
`repository`, `name`, `name_suffix`, `keyword` (an Artifact Hub keyword,
case-insensitive). Prefer a keyword, suffix or repository rule over a chart
name. The file is part of the frozen instrument.

CRD-only is the weakest rule: Artifact Hub has no field for it, so it relies on
the `-crds` naming convention and the `crds` keyword. A CRD-only chart with
neither is not caught here.

## Tests

`go test ./hack/compat-exp/frame` runs on `testdata/store`, real API responses
trimmed to the fields frame reads (no maintainer data) plus a few synthetic
packages in the repository `example-repo`. `UPDATE_GOLDEN=1` rewrites
`testdata/candidates.golden.json` after review.
