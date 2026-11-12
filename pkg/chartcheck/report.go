// Package chartcheck is a static check that tells a Helm chart maintainer what
// in a rendered chart is likely to break under Istio, before any cluster
// exists. It reads Kubernetes manifests (the output of helm template) and
// applies a fixed list of rules, each tied to a passage of the Istio
// documentation or, where none exists, saying so.
//
// The findings are candidates found by reading manifests. They are not
// failures observed in a cluster, and an empty result is not a guarantee. The
// package is deterministic: it reads no clock, no network and no cluster, and
// the same input gives byte-identical output.
package chartcheck

import (
	"fmt"
	"sort"
)

// SchemaVersion is the version of the JSON report. It changes when a field is
// removed or changes meaning; adding a field does not change it.
const SchemaVersion = 1

// Options selects what to check.
type Options struct {
	// Modes are the mesh modes to check; each rule runs only for the modes it
	// applies to. Empty means both.
	Modes []Mode
	// NativeSidecars says whether sidecar proxies are native sidecars unless a
	// pod opts out. The default of Istio 1.31 on Kubernetes 1.33 or newer is
	// true.
	NativeSidecars bool
}

// Finding is one candidate: something in the manifests that is likely to break
// under Istio in the given mode.
type Finding struct {
	ID       string   `json:"id"`
	Check    string   `json:"check"`
	Mode     Mode     `json:"mode"`
	Resource Resource `json:"resource"`
	// Trigger is the field, and its value, that made the check fire.
	Trigger Evidence `json:"trigger"`
	// Reason is what the check saw, specific to this resource.
	Reason string `json:"reason"`
	// Effect is what will happen under Istio, in one sentence.
	Effect string `json:"effect"`
	// Docs are the Istio documentation passages the rule rests on; empty when
	// no page states the requirement, and DocsNote then says so.
	Docs     []Doc  `json:"docs"`
	DocsNote string `json:"docsNote,omitempty"`
	Fix      string `json:"fix"`
	Confirm  string `json:"confirm"`
	// Evidence lists every field the check read to decide.
	Evidence []Evidence `json:"evidence"`
}

// CheckRun records that a check ran, so a reader can tell a check that found
// nothing from a check that did not apply.
type CheckRun struct {
	ID       string `json:"id"`
	Check    string `json:"check"`
	Mode     Mode   `json:"mode"`
	Findings int    `json:"findings"`
}

// Examined counts what the checks were able to look at.
type Examined struct {
	Objects      int `json:"objects"`
	PodTemplates int `json:"podTemplates"`
}

// Report is the result of one run. Input is for the caller to fill in.
type Report struct {
	SchemaVersion  int        `json:"schemaVersion"`
	Tool           string     `json:"tool"`
	IstioVersion   string     `json:"istioVersion"`
	Input          string     `json:"input"`
	Namespace      string     `json:"namespace"`
	Modes          []Mode     `json:"modes"`
	NativeSidecars bool       `json:"nativeSidecars"`
	Examined       Examined   `json:"examined"`
	Checks         []CheckRun `json:"checks"`
	Findings       []Finding  `json:"findings"`
	Candidates     int        `json:"candidates"`
	Notice         string     `json:"notice"`
	BlindSpots     []string   `json:"blindSpots"`
}

// Notice is printed with every report, clean or not.
const Notice = "These are candidates found by reading manifests, not failures observed in a cluster. " +
	"A clean result is not a guarantee that the chart works under Istio."

// BlindSpots is what the checks cannot see. It is part of every report.
var BlindSpots = []string{
	"Only the rendered manifests are read. A requirement that lives in a container image (for example an image whose USER is 1337 with no runAsUser in the manifest), in application code (the address it binds, the ports it really listens on) or in the cluster is not seen.",
	"The release namespace is assumed to be enrolled in the mesh, and a pod is in the mesh unless it opts out itself. Namespace labels, PeerAuthentication, mesh-wide settings and policies from outside the chart are not read; STRICT mutual TLS is assumed where a finding depends on it.",
	"NetworkPolicy: only policies the chart renders are read, not a default-deny policy from elsewhere; the peers of an ingress rule are not evaluated for port 15008; the IPv6 probe address is not checked; the behavior of a particular CNI (Calico, Cilium) is not modelled.",
	"Nothing is run. Traffic between the chart's own pods, client-first protocols broken by the mesh, whether a webhook is ever called and its failure policy, and whether a container port is really bound are not observed.",
	"A pod that leaves the mesh on purpose (for example a Helm test pod with sidecar injection disabled) is not checked, although under STRICT mutual TLS a pod outside the mesh cannot call one inside it.",
	"helm template cannot evaluate lookup or cluster capabilities, so a real install can render something different.",
}

// Analyze applies the rules to m for each requested mode. It refuses an empty
// manifest: a run that read nothing must not look like a clean one.
func Analyze(m *Manifest, o Options) (*Report, error) {
	if m.Objects() == 0 {
		return nil, fmt.Errorf("the input holds no Kubernetes objects, so nothing was checked")
	}
	modes := o.Modes
	if len(modes) == 0 {
		modes = []Mode{Sidecar, Ambient}
	}
	for _, md := range modes {
		if md != Sidecar && md != Ambient {
			return nil, fmt.Errorf("unknown mesh mode %q", md)
		}
	}
	modes = sortedModes(modes)

	rep := &Report{
		SchemaVersion:  SchemaVersion,
		Tool:           "meshmedic chart check",
		IstioVersion:   IstioVersion,
		Namespace:      m.namespace,
		Modes:          modes,
		NativeSidecars: o.NativeSidecars,
		Examined:       Examined{Objects: m.Objects(), PodTemplates: m.PodTemplates()},
		Checks:         []CheckRun{},
		Findings:       []Finding{},
		Notice:         Notice,
		BlindSpots:     BlindSpots,
	}
	for _, r := range rules {
		for _, md := range modes {
			if !r.appliesTo(md) {
				continue
			}
			fs := r.run(m, options{Mode: md, NativeSidecars: o.NativeSidecars})
			rep.Checks = append(rep.Checks, CheckRun{ID: r.ID, Check: r.Check, Mode: md, Findings: len(fs)})
			for _, f := range fs {
				docs := r.Docs
				if docs == nil {
					docs = []Doc{}
				}
				ev := f.Evidence
				if ev == nil {
					ev = []Evidence{}
				}
				rep.Findings = append(rep.Findings, Finding{
					ID: r.ID, Check: r.Check, Mode: md, Resource: f.Resource, Trigger: f.Trigger,
					Reason: f.Reason, Effect: r.Effect, Docs: docs, DocsNote: r.DocsNote,
					Fix: r.Fix, Confirm: r.Confirm, Evidence: ev,
				})
			}
		}
	}
	sort.SliceStable(rep.Findings, func(i, j int) bool { return lessFinding(rep.Findings[i], rep.Findings[j]) })
	rep.Candidates = len(rep.Findings)
	return rep, nil
}

func sortedModes(in []Mode) []Mode {
	seen := map[Mode]bool{}
	var out []Mode
	for _, m := range in {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func lessFinding(a, b Finding) bool {
	switch {
	case a.ID != b.ID:
		return a.ID < b.ID
	case a.Mode != b.Mode:
		return a.Mode < b.Mode
	case a.Resource.Kind != b.Resource.Kind:
		return a.Resource.Kind < b.Resource.Kind
	case a.Resource.Namespace != b.Resource.Namespace:
		return a.Resource.Namespace < b.Resource.Namespace
	case a.Resource.Name != b.Resource.Name:
		return a.Resource.Name < b.Resource.Name
	case a.Reason != b.Reason:
		return a.Reason < b.Reason
	}
	return a.Trigger.Path < b.Trigger.Path
}
