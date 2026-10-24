package main

import (
	"fmt"
	"sort"
	"strings"
)

// Record is one run of one chart in one world, as observe.sh writes it. Only
// the fields the comparison needs are decoded; the rest stays in the file as
// evidence.
type Record struct {
	World        string            `json:"world"`
	Chart        string            `json:"chart"`
	ChartVersion string            `json:"chartVersion"`
	Run          int               `json:"run"`
	FixOf        string            `json:"fixOf"`
	Environment  map[string]string `json:"environment"`
	Obs          map[string]string `json:"obs"`
	Analyze      []struct {
		Code string `json:"code"`
	} `json:"analyze"`
	Ports []struct {
		Svc         string `json:"svc"`
		Port        int    `json:"port"`
		Name        string `json:"name"`
		AppProtocol string `json:"appProtocol"`
	} `json:"ports"`
	Workloads []struct {
		Workload   string   `json:"workload"`
		Reasons    []string `json:"reasons"`
		Containers []struct {
			Image  string `json:"image"`
			Reason string `json:"reason"`
		} `json:"containers"`
	} `json:"workloads"`
	PullFailures []struct {
		Owner string `json:"owner"`
		Image string `json:"image"`
	} `json:"pullFailures"`
}

// pullFailure names the image a run could not pull, if any. Such a run
// measured the registry, not the chart: the second pilot run found argo-cd's
// redis image failing to pull from ecr-public.aws.com in whichever worlds the
// registry happened to refuse, W0 included, and since a world's three runs
// share one cluster and one runner, the failure repeated 3 of 3 and passed for
// a finding.
//
// pullFailures covers every pod, Job pods included. Records from before it
// existed carry containers or only the pod's reasons, so those count too.
func pullFailure(r Record) string {
	if len(r.PullFailures) > 0 {
		return r.PullFailures[0].Image
	}
	pull := func(reason string) bool { return reason == "ErrImagePull" || reason == "ImagePullBackOff" }
	for _, w := range r.Workloads {
		for _, c := range w.Containers {
			if pull(c.Reason) {
				return c.Image
			}
		}
		for _, reason := range w.Reasons {
			if pull(reason) {
				return w.Workload
			}
		}
	}
	return ""
}

// usable drops the runs that measured something other than the chart, and
// says why for each.
func usable(world string, rs []Record) ([]Record, []string) {
	var out []Record
	var dropped []string
	for _, r := range rs {
		if img := pullFailure(r); img != "" {
			dropped = append(dropped, fmt.Sprintf("%s run %d: image pull failed (%s)", world, r.Run, img))
			continue
		}
		out = append(out, r)
	}
	return out, dropped
}

// absent is the value of a key a run did not produce. A hook Job that W0
// deletes on success and W3 leaves hanging must compare as a difference, so a
// missing key is a value, not a reason to skip.
const absent = "absent"

// Difference is one observation whose value in a mesh world is not the value
// W0 produced.
type Difference struct {
	Key    string
	Oracle string   // the value every W0 run agreed on
	Seen   []string // the value in each run of the mesh world, in run order
	Misses int      // runs whose value differs from Oracle
}

// Consistent reports whether every run of the mesh world differed. Only a
// consistent difference is a finding; one that comes and goes is reported,
// but as flaky, because three runs that disagree with each other are not
// evidence of anything about the chart.
func (d Difference) Consistent() bool { return d.Misses == len(d.Seen) }

// Comparison is one chart in one mesh world, against W0.
type Comparison struct {
	Chart, World string
	Runs         int
	Findings     []Difference // consistent differences
	Flaky        []Difference
	Analyze      []string // istioctl analyze codes seen in any run
	Dropped      []string // runs left out, and why
	Undecided    string   // why no verdict was possible, if none was
}

// Verdict is the one-cell summary for the table.
func (c Comparison) Verdict() string {
	switch {
	case c.Undecided != "":
		return "undecided"
	case len(c.Findings) > 0:
		return fmt.Sprintf("differs (%d)", len(c.Findings))
	case len(c.Flaky) > 0:
		return fmt.Sprintf("flaky (%d)", len(c.Flaky))
	default:
		return "same"
	}
}

// Oracle is what W0 says about a chart: the value of every key that all W0
// runs agreed on, and the keys they did not agree on.
type Oracle struct {
	Values   map[string]string
	Unstable []string
	Runs     int
}

// ignored reports whether a key carries no information in this comparison.
//
// An HTTP probe of a port that W0 did not answer over HTTP measures the probe:
// a sniffing sidecar turns the request into a 502 on a MySQL port, and that
// says nothing about MySQL.
//
// The passive probe on a port a sidecar treats as HTTP measures the probe too.
// The first pilot run found it on podinfo's gRPC port: an HTTP/2 server sends
// its SETTINGS frame as soon as a connection opens, so W0 "greets", while a
// sidecar proxying HTTP/2 opens nothing upstream until the client sends a
// request. Every real HTTP/2 client speaks first, so the silence breaks
// nothing. Only sidecars do this: ztunnel in W3 carries bytes and does not
// parse HTTP, so there a greeting that stops arriving is still a difference.
func ignored(key, oracle, world string, httpPorts map[string]bool) bool {
	if strings.HasSuffix(key, ":http") && oracle == "none" {
		return true
	}
	if sidecarWorlds[world] && strings.HasSuffix(key, ":passive") && httpPorts[strings.TrimSuffix(key, ":passive")] {
		return true
	}
	return false
}

var sidecarWorlds = map[string]bool{"W1": true, "W2": true}

// httpFamily is the set of port protocols Istio proxies as HTTP, selected by
// the port name's prefix or by appProtocol.
var httpFamily = map[string]bool{
	"http": true, "http2": true, "grpc": true, "grpc-web": true,
	"h2c": true, "kubernetes.io/h2c": true,
}

// httpPorts maps "port:<svc>:<port>" to true for every port, in any record,
// declared as an HTTP-family protocol.
func httpPorts(runs ...[]Record) map[string]bool {
	out := map[string]bool{}
	for _, rs := range runs {
		for _, r := range rs {
			for _, p := range r.Ports {
				prefix, _, _ := strings.Cut(p.Name, "-")
				if httpFamily[p.AppProtocol] || httpFamily[prefix] {
					out[fmt.Sprintf("port:%s:%d", p.Svc, p.Port)] = true
				}
			}
		}
	}
	return out
}

func value(r Record, key string) string {
	if v, ok := r.Obs[key]; ok {
		return v
	}
	return absent
}

// keys is the union of observation keys over a set of runs, sorted so the
// report is stable.
func keys(runs ...[]Record) []string {
	set := map[string]bool{}
	for _, rs := range runs {
		for _, r := range rs {
			for k := range r.Obs {
				set[k] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// BuildOracle keeps only what W0 agreed on with itself. A chart that is flaky
// without a mesh cannot be blamed on the mesh for being flaky with one.
func BuildOracle(w0 []Record, universe []string) Oracle {
	o := Oracle{Values: map[string]string{}, Runs: len(w0)}
	for _, k := range universe {
		first := value(w0[0], k)
		stable := true
		for _, r := range w0[1:] {
			if value(r, k) != first {
				stable = false
				break
			}
		}
		if stable {
			o.Values[k] = first
		} else {
			o.Unstable = append(o.Unstable, k)
		}
	}
	return o
}

// Compare holds one mesh world against W0. minRuns is the bar for a verdict:
// fewer runs than that on either side means the comparison is undecided, not
// that the chart is compatible.
func Compare(chart, world string, w0, wn []Record, minRuns int) (Comparison, Oracle) {
	if minRuns < 1 {
		minRuns = 1
	}
	c := Comparison{Chart: chart, World: world}
	w0, d0 := usable(oracleWorld, w0)
	wn, dn := usable(world, wn)
	c.Dropped = append(d0, dn...)
	c.Runs = len(wn)
	if len(w0) < minRuns {
		c.Undecided = fmt.Sprintf("%s has %d of %d runs", oracleWorld, len(w0), minRuns)
		return c, Oracle{}
	}
	if len(wn) < minRuns {
		c.Undecided = fmt.Sprintf("%s has %d of %d runs", world, len(wn), minRuns)
		return c, Oracle{}
	}
	sort.Slice(wn, func(i, j int) bool { return wn[i].Run < wn[j].Run })

	universe := keys(w0, wn)
	oracle := BuildOracle(w0, universe)
	skip := httpPorts(w0, wn)
	for _, k := range universe {
		want, ok := oracle.Values[k]
		if !ok || ignored(k, want, world, skip) {
			continue
		}
		d := Difference{Key: k, Oracle: want}
		for _, r := range wn {
			got := value(r, k)
			d.Seen = append(d.Seen, got)
			if got != want {
				d.Misses++
			}
		}
		switch {
		case d.Misses == 0:
		case d.Consistent():
			c.Findings = append(c.Findings, d)
		default:
			c.Flaky = append(c.Flaky, d)
		}
	}

	codes := map[string]bool{}
	for _, r := range wn {
		for _, a := range r.Analyze {
			codes[a.Code] = true
		}
	}
	for code := range codes {
		c.Analyze = append(c.Analyze, code)
	}
	sort.Strings(c.Analyze)
	return c, oracle
}

// Certificate is the verdict on a candidate fix (ADR 0001, section 4): a chart
// entry whose fixOf names the chart it repairs, installed under the same
// release name so that its observations line up key by key.
type Certificate struct {
	Fix, Base string
	Cleared   []string // mesh worlds where the base differed and the fix does not
	Problems  []string // why it is not certified
}

// Certified needs at least one cleared world and nothing else wrong. A fix for
// a chart that never differed has nothing to prove.
func (c Certificate) Certified() bool { return len(c.Problems) == 0 && len(c.Cleared) > 0 }

// Certify holds a fix to three conditions: in every mesh world it behaves like
// its own W0 (so the finding is gone, 3 of 3, and nothing new broke), with no
// flaky keys either; and its W0 behaves like the base chart's W0, because a
// fix that changes the chart without a mesh is a different chart.
func Certify(fix, base string, fixCmp, baseCmp map[string]Comparison, fixW0, baseW0 []Record, minRuns int) Certificate {
	c := Certificate{Fix: fix, Base: base}
	for _, w := range meshWorlds {
		f, b := fixCmp[w], baseCmp[w]
		switch {
		case f.Undecided != "":
			c.Problems = append(c.Problems, w+" undecided: "+f.Undecided)
		case len(f.Findings) > 0:
			c.Problems = append(c.Problems, fmt.Sprintf("%s still differs from %s: %s", w, oracleWorld, keyList(f.Findings)))
		case len(f.Flaky) > 0:
			c.Problems = append(c.Problems, fmt.Sprintf("%s flaky: %s", w, keyList(f.Flaky)))
		case len(b.Findings) > 0:
			c.Cleared = append(c.Cleared, w)
		}
	}
	w0, _ := Compare(fix, oracleWorld, baseW0, fixW0, minRuns)
	switch {
	case w0.Undecided != "":
		c.Problems = append(c.Problems, oracleWorld+" comparison with the base undecided: "+w0.Undecided)
	case len(w0.Findings) > 0:
		c.Problems = append(c.Problems, "changes the chart in "+oracleWorld+": "+keyList(w0.Findings))
	case len(w0.Flaky) > 0:
		c.Problems = append(c.Problems, "flaky against the chart in "+oracleWorld+": "+keyList(w0.Flaky))
	}
	if len(c.Cleared) == 0 && len(c.Problems) == 0 {
		c.Problems = append(c.Problems, base+" has no finding for this fix to clear")
	}
	return c
}

func keyList(ds []Difference) string {
	ks := make([]string, len(ds))
	for i, d := range ds {
		ks[i] = d.Key
	}
	return strings.Join(ks, ", ")
}
