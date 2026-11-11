package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// evidence is one field a reader can look up in the rendered manifest.
type evidence struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	Value string `json:"value"`
}

// finding is one thing a check found, with every field it read to decide.
type finding struct {
	Subject  string     `json:"subject"`
	Reason   string     `json:"reason"`
	Evidence []evidence `json:"evidence"`
}

type options struct {
	World string
	// NativeSidecars is the world's default for sidecar mode: on in the pinned
	// Istio 1.31.3 on Kubernetes 1.36 (ENABLE_NATIVE_SIDECARS auto).
	NativeSidecars bool
}

// check is one static check. Rule is the sentence stored next to the check
// id in causes.json; causes_test.go holds the two equal.
type check struct {
	ID     string
	Key    string
	Rule   string
	Worlds []string
	Run    func(*manifest, options) []finding
}

// checks is in the order of the cause keys in causes.json.
var checks = []check{
	{
		ID:     "np-hbone-probe",
		Key:    "ambient-np",
		Worlds: []string{"W3"},
		Rule: "In W3, a pod in the mesh that a NetworkPolicy isolates for ingress is flagged when no ingress rule of the policies selecting it admits TCP 15008, or when a kubelet probe port of the pod is not admitted from 169.254.7.127. " +
			"A rule admits a port when its ports list is empty or has a TCP entry that omits the port, names it, names a container port with that number, or is a range covering it; it admits 169.254.7.127 when its from list is empty or has an ipBlock whose cidr covers the address and whose except list does not.",
		Run: checkNetworkPolicy,
	},
	{
		ID:     "protocol-declared",
		Key:    "protocol",
		Worlds: []string{"W1"},
		Rule: "In W1, a TCP port of a Service that selects a pod in the mesh is flagged when its number is one of the server-first ports Istio documents (25, 53, 3306, 27017) and its appProtocol, else its name prefix, declares http, http2, grpc, grpc-web or http_proxy. " +
			"A pod in the mesh is flagged when two of its Service ports target the same container port with different declared protocols (classes http, http2, tls, tcp; undeclared ports are not compared).",
		Run: checkProtocol,
	},
	{
		ID:     "uid-1337",
		Key:    "uid-1337",
		Worlds: []string{"W1"},
		Rule:   "In W1, a container or init container of a pod in the mesh is flagged when its effective runAsUser (the container's securityContext, else the pod's) is 1337.",
		Run:    checkUID,
	},
	{
		ID:     "reserved-port",
		Key:    "reserved-port",
		Worlds: []string{"W1", "W3"},
		Rule:   "A pod in the mesh is flagged when a container declares, or a Service selecting it targets, a TCP port Istio uses inside the pod: in W1 15000, 15001, 15002, 15004, 15006, 15008, 15020, 15021, 15053 or 15090 (the sidecar's ports), in W3 15001, 15006 or 15008 (ztunnel's in-pod listeners).",
		Run:    checkReservedPort,
	},
	{
		ID:     "classic-sidecar-startup",
		Key:    "startup-order",
		Worlds: []string{"W1"},
		Rule:   "In W1, a pod in the mesh whose proxy is a classic sidecar (pod annotation sidecar.istio.io/nativeSidecar set to false, or native sidecars off in the world) is flagged when it has init containers, or when its proxy.istio.io/config annotation does not set holdApplicationUntilProxyStarts to true.",
		Run:    checkStartup,
	},
	{
		ID:     "classic-sidecar-job",
		Key:    "job-lifecycle",
		Worlds: []string{"W1"},
		Rule:   "In W1, a Job, a CronJob, or a bare Pod with restartPolicy Never or OnFailure, whose pod is in the mesh, is flagged when its proxy is a classic sidecar (pod annotation sidecar.istio.io/nativeSidecar set to false, or native sidecars off in the world).",
		Run:    checkJob,
	},
	{
		ID:     "webhook-backend-in-mesh",
		Key:    "apiserver-caller",
		Worlds: []string{"W1", "W3"},
		Rule: "A webhook of a ValidatingWebhookConfiguration or MutatingWebhookConfiguration, an APIService, or a CustomResourceDefinition conversion webhook is flagged when its service reference names a Service port the chart renders whose Service selects a pod in the mesh of the world. " +
			"In W1 it is not flagged when the pod's traffic.sidecar.istio.io/excludeInboundPorts or includeInboundPorts annotation keeps the target port out of the sidecar.",
		Run: checkWebhook,
	},
	{
		ID:     "l7-without-waypoint",
		Key:    "ambient-l7",
		Worlds: []string{"W3"},
		Rule: "In W3, when the chart renders no Gateway of gatewayClassName istio-waypoint, an AuthorizationPolicy without targetRef or targetRefs is flagged when a rule uses an attribute outside the L4 list on Istio's ambient L4 policy page. " +
			"So is a VirtualService bound to the mesh (gateways empty or containing mesh) with http routes, and an HTTPRoute or GRPCRoute with a Service parentRef.",
		Run: checkL7,
	},
}

func applies(c check, world string) bool {
	for _, w := range c.Worlds {
		if w == world {
			return true
		}
	}
	return false
}

func podEvidence(p *podTemplate) evidence {
	return evidence{Kind: p.Owner.Kind, Name: p.Owner.Name, Path: p.path("metadata.labels"), Value: labelString(p.Labels)}
}

func labelString(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + m[k]
	}
	return strings.Join(parts, ",")
}

// --- ambient-np ----------------------------------------------------------------

const (
	hbonePort    = 15008
	probeSource  = "169.254.7.127"
	probeSrcCIDR = probeSource + "/32"
)

type npRule struct {
	np   *object
	idx  int
	rule map[string]any
}

// isolatesIngress: policyTypes absent means Ingress is always included.
func isolatesIngress(np *object) bool {
	spec := mp(np.Raw["spec"])
	pt, ok := spec["policyTypes"]
	if !ok || pt == nil {
		return true
	}
	for _, t := range slice(pt) {
		if str(t) == "Ingress" {
			return true
		}
	}
	return false
}

func ruleAdmitsPort(r map[string]any, port int, cps []containerPort) bool {
	ports, ok := r["ports"]
	if !ok || ports == nil || len(slice(ports)) == 0 {
		return true
	}
	for _, e := range slice(ports) {
		em := mp(e)
		if protoOr(em["protocol"]) != "TCP" {
			continue
		}
		pv, has := em["port"]
		if !has || pv == nil {
			return true
		}
		n, ok := resolvePort(pv, cps)
		if !ok {
			continue
		}
		if end, ok := num(em["endPort"]); ok {
			if n <= port && port <= end {
				return true
			}
			continue
		}
		if n == port {
			return true
		}
	}
	return false
}

func ruleAdmitsSource(r map[string]any, ip string) bool {
	from, ok := r["from"]
	if !ok || from == nil || len(slice(from)) == 0 {
		return true
	}
	for _, peer := range slice(from) {
		ib := mp(mp(peer)["ipBlock"])
		if ib == nil || !cidrContains(str(ib["cidr"]), ip) {
			continue
		}
		excluded := false
		for _, ex := range slice(ib["except"]) {
			if cidrContains(str(ex), ip) {
				excluded = true
			}
		}
		if !excluded {
			return true
		}
	}
	return false
}

func ruleSummary(r map[string]any) string {
	var ports []string
	for _, e := range slice(r["ports"]) {
		em := mp(e)
		p := str(em["port"])
		if p == "" {
			p = "any"
		}
		if end := str(em["endPort"]); end != "" {
			p += "-" + end
		}
		ports = append(ports, p+"/"+protoOr(em["protocol"]))
	}
	ps := "any port"
	if len(ports) > 0 {
		ps = strings.Join(ports, ",")
	}
	var peers []string
	for _, peer := range slice(r["from"]) {
		pm := mp(peer)
		switch {
		case pm["ipBlock"] != nil:
			peers = append(peers, "ipBlock "+str(mp(pm["ipBlock"])["cidr"]))
		case pm["podSelector"] != nil && pm["namespaceSelector"] != nil:
			peers = append(peers, "namespaceSelector+podSelector")
		case pm["podSelector"] != nil:
			peers = append(peers, "podSelector")
		case pm["namespaceSelector"] != nil:
			peers = append(peers, "namespaceSelector")
		}
	}
	fs := "any source"
	if len(peers) > 0 {
		fs = strings.Join(peers, ",")
	}
	return ps + " from " + fs
}

type probePort struct {
	Port      int
	Container string
	Path      string
}

func probePorts(p *podTemplate) []probePort {
	var out []probePort
	all := p.ports()
	for _, c := range p.containers("containers") {
		var cps []containerPort
		for _, cp := range all {
			if strings.HasPrefix(cp.Path, c.Path+".") {
				cps = append(cps, cp)
			}
		}
		for _, kind := range []string{"livenessProbe", "readinessProbe", "startupProbe"} {
			pr := mp(c.Raw[kind])
			for _, h := range []string{"httpGet", "tcpSocket", "grpc"} {
				hm := mp(pr[h])
				if hm == nil {
					continue
				}
				// A probe resolves a named port against its own container.
				n, ok := resolvePort(hm["port"], cps)
				if !ok {
					continue
				}
				out = append(out, probePort{Port: n, Container: c.Name, Path: fmt.Sprintf("%s.%s.%s.port", c.Path, kind, h)})
			}
		}
	}
	return out
}

func checkNetworkPolicy(m *manifest, o options) []finding {
	var out []finding
	nps := m.byKind("NetworkPolicy")
	for _, p := range m.Pods {
		if !inMesh(o.World, p) {
			continue
		}
		var rules []npRule
		var selecting []*object
		for _, np := range nps {
			if np.Namespace != p.Owner.Namespace || !isolatesIngress(np) {
				continue
			}
			if !matchSelector(mp(mp(np.Raw["spec"])["podSelector"]), p.Labels) {
				continue
			}
			selecting = append(selecting, np)
			for i, r := range slice(mp(np.Raw["spec"])["ingress"]) {
				rules = append(rules, npRule{np: np, idx: i, rule: mp(r)})
			}
		}
		if len(selecting) == 0 {
			continue
		}
		ev := []evidence{podEvidence(p)}
		for _, np := range selecting {
			ev = append(ev, evidence{Kind: "NetworkPolicy", Name: np.Name, Path: "spec.podSelector", Value: "selects this pod; isolates ingress"})
			if len(slice(mp(np.Raw["spec"])["ingress"])) == 0 {
				ev = append(ev, evidence{Kind: "NetworkPolicy", Name: np.Name, Path: "spec.ingress", Value: "no rules"})
			}
		}
		for _, r := range rules {
			ev = append(ev, evidence{Kind: "NetworkPolicy", Name: r.np.Name, Path: fmt.Sprintf("spec.ingress[%d]", r.idx), Value: ruleSummary(r.rule)})
		}
		cps := p.ports()
		admits := func(port int, src string) bool {
			for _, r := range rules {
				if ruleAdmitsPort(r.rule, port, cps) && (src == "" || ruleAdmitsSource(r.rule, src)) {
					return true
				}
			}
			return false
		}
		subject := p.Owner.ref()
		if !admits(hbonePort, "") {
			out = append(out, finding{Subject: subject, Reason: "no ingress rule admits TCP 15008 (HBONE)", Evidence: ev})
		}
		seen := map[int]bool{}
		for _, pp := range probePorts(p) {
			if seen[pp.Port] || admits(pp.Port, probeSource) {
				continue
			}
			seen[pp.Port] = true
			pev := append(append([]evidence{}, ev...), evidence{Kind: p.Owner.Kind, Name: p.Owner.Name, Path: pp.Path, Value: strconv.Itoa(pp.Port)})
			out = append(out, finding{Subject: subject, Reason: fmt.Sprintf("kubelet probe port %d is not admitted from %s", pp.Port, probeSrcCIDR), Evidence: pev})
		}
	}
	return out
}

// --- protocol --------------------------------------------------------------------

// istioProtocol follows ConvertProtocol in istio/istio 1.31.3,
// pkg/config/kube/conversion.go, and protocol.Parse in pkg/config/protocol.
// It returns "" for a port Istio leaves to automatic detection.
func istioProtocol(port int, name, proto string, appProto *string) string {
	if proto == "UDP" {
		return "udp"
	}
	n := name
	if appProto != nil {
		n = *appProto
		switch n {
		case "kubernetes.io/h2c":
			return "http2"
		case "kubernetes.io/ws":
			return "http"
		case "kubernetes.io/wss":
			return "https"
		}
	}
	if len(n) >= len("grpc-web") && strings.EqualFold(n[:len("grpc-web")], "grpc-web") {
		return "grpc-web"
	}
	if i := strings.IndexByte(n, '-'); i >= 0 {
		n = n[:i]
	}
	switch p := strings.ToLower(n); p {
	case "tcp", "udp", "grpc", "grpc-web", "http", "http_proxy", "http2", "https", "tls", "mongo", "redis", "mysql":
		return p
	}
	switch port {
	case 25, 53, 3306, 27017:
		return "tcp"
	}
	return ""
}

// protocolClass groups protocols by what the sidecar does with them, as the
// table on Istio's protocol selection page does.
func protocolClass(p string) string {
	switch p {
	case "http", "http_proxy":
		return "http"
	case "http2", "grpc", "grpc-web":
		return "http2"
	case "https", "tls":
		return "tls"
	case "tcp", "mongo", "redis", "mysql":
		return "tcp"
	}
	return p
}

var serverFirstPorts = map[int]bool{25: true, 53: true, 3306: true, 27017: true}

func declSource(sp servicePort) string {
	if sp.AppProtocol != nil {
		return "appProtocol=" + *sp.AppProtocol
	}
	return "name=" + sp.Name
}

func checkProtocol(m *manifest, o options) []finding {
	var out []finding
	svcs := m.byKind("Service")
	flaggedPort := map[string]bool{}
	for _, p := range m.Pods {
		if !inMesh(o.World, p) {
			continue
		}
		byTarget := map[int][]servicePort{}
		for _, svc := range svcs {
			if !svc.selects(p) {
				continue
			}
			for _, sp := range servicePorts(svc, p) {
				if sp.Protocol != "TCP" {
					continue
				}
				proto := istioProtocol(sp.Port, sp.Name, sp.Protocol, sp.AppProtocol)
				if c := protocolClass(proto); serverFirstPorts[sp.Port] && (c == "http" || c == "http2") {
					k := svc.Name + "/" + sp.path()
					if !flaggedPort[k] {
						flaggedPort[k] = true
						out = append(out, finding{
							Subject: svc.ref(),
							Reason:  fmt.Sprintf("port %d is a documented server-first port and is declared %s", sp.Port, proto),
							Evidence: []evidence{
								{Kind: "Service", Name: svc.Name, Path: sp.path(), Value: fmt.Sprintf("port=%d %s", sp.Port, declSource(sp))},
								podEvidence(p),
							},
						})
					}
				}
				if proto != "" && sp.Target != 0 {
					byTarget[sp.Target] = append(byTarget[sp.Target], sp)
				}
			}
		}
		targets := make([]int, 0, len(byTarget))
		for t := range byTarget {
			targets = append(targets, t)
		}
		sort.Ints(targets)
		for _, t := range targets {
			classes := map[string]bool{}
			var ev []evidence
			for _, sp := range byTarget[t] {
				proto := istioProtocol(sp.Port, sp.Name, sp.Protocol, sp.AppProtocol)
				classes[protocolClass(proto)] = true
				ev = append(ev, evidence{Kind: "Service", Name: sp.Svc.Name, Path: sp.path(), Value: fmt.Sprintf("targetPort=%d %s class=%s", t, declSource(sp), protocolClass(proto))})
			}
			if len(classes) > 1 {
				ev = append(ev, podEvidence(p))
				out = append(out, finding{Subject: p.Owner.ref(), Reason: fmt.Sprintf("container port %d is declared with different protocols by its Services", t), Evidence: ev})
			}
		}
	}
	return out
}

// --- uid-1337 --------------------------------------------------------------------

func checkUID(m *manifest, o options) []finding {
	var out []finding
	for _, p := range m.Pods {
		if !inMesh(o.World, p) {
			continue
		}
		podUID, podHas := num(mp(p.Spec["securityContext"])["runAsUser"])
		for _, field := range []string{"initContainers", "containers"} {
			for _, c := range p.containers(field) {
				uid, has := num(mp(c.Raw["securityContext"])["runAsUser"])
				path := c.Path + ".securityContext.runAsUser"
				if !has {
					uid, has = podUID, podHas
					path = p.path("spec.securityContext.runAsUser")
				}
				if has && uid == 1337 {
					out = append(out, finding{
						Subject:  p.Owner.ref(),
						Reason:   fmt.Sprintf("container %s runs as UID 1337", c.Name),
						Evidence: []evidence{{Kind: p.Owner.Kind, Name: p.Owner.Name, Path: path, Value: "1337"}},
					})
				}
			}
		}
	}
	return out
}

// --- reserved-port ---------------------------------------------------------------

var reservedPorts = map[string]map[int]bool{
	// application-requirements, "Ports used by Istio", the sidecar's table.
	"W1": {15000: true, 15001: true, 15002: true, 15004: true, 15006: true, 15008: true, 15020: true, 15021: true, 15053: true, 15090: true},
	// ambient traffic-redirection, "Istio's in-pod traffic redirection model".
	"W3": {15001: true, 15006: true, 15008: true},
}

func checkReservedPort(m *manifest, o options) []finding {
	var out []finding
	res := reservedPorts[o.World]
	svcs := m.byKind("Service")
	for _, p := range m.Pods {
		if !inMesh(o.World, p) {
			continue
		}
		hits := map[int][]evidence{}
		for _, cp := range p.ports() {
			if cp.Protocol == "TCP" && res[cp.Number] {
				hits[cp.Number] = append(hits[cp.Number], evidence{Kind: p.Owner.Kind, Name: p.Owner.Name, Path: cp.Path, Value: strconv.Itoa(cp.Number)})
			}
		}
		for _, svc := range svcs {
			if !svc.selects(p) {
				continue
			}
			for _, sp := range servicePorts(svc, p) {
				if sp.Protocol == "TCP" && res[sp.Target] {
					hits[sp.Target] = append(hits[sp.Target], evidence{Kind: "Service", Name: svc.Name, Path: sp.path() + ".targetPort", Value: strconv.Itoa(sp.Target)})
				}
			}
		}
		ports := make([]int, 0, len(hits))
		for n := range hits {
			ports = append(ports, n)
		}
		sort.Ints(ports)
		for _, n := range ports {
			out = append(out, finding{Subject: p.Owner.ref(), Reason: fmt.Sprintf("port %d is used by Istio inside the pod", n), Evidence: hits[n]})
		}
	}
	return out
}

// --- startup-order and job-lifecycle -----------------------------------------------

// classicSidecar says whether the pod's proxy would be a regular container
// rather than a native sidecar, and why.
func classicSidecar(p *podTemplate, o options) (bool, evidence) {
	if v, ok := p.Annotations["sidecar.istio.io/nativeSidecar"]; ok {
		return v == "false", evidence{Kind: p.Owner.Kind, Name: p.Owner.Name, Path: p.path("metadata.annotations.sidecar.istio.io/nativeSidecar"), Value: v}
	}
	return !o.NativeSidecars, evidence{Kind: "World", Name: o.World, Path: "nativeSidecars", Value: strconv.FormatBool(o.NativeSidecars)}
}

func holdsApplication(p *podTemplate) bool {
	raw, ok := p.Annotations["proxy.istio.io/config"]
	if !ok {
		return false
	}
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		return false
	}
	b, _ := cfg["holdApplicationUntilProxyStarts"].(bool)
	return b
}

func checkStartup(m *manifest, o options) []finding {
	var out []finding
	for _, p := range m.Pods {
		if !inMesh(o.World, p) {
			continue
		}
		classic, why := classicSidecar(p, o)
		if !classic {
			continue
		}
		inits := p.containers("initContainers")
		hold := holdsApplication(p)
		if len(inits) == 0 && hold {
			continue
		}
		ev := []evidence{why}
		var reasons []string
		if len(inits) > 0 {
			ev = append(ev, evidence{Kind: p.Owner.Kind, Name: p.Owner.Name, Path: p.path("spec.initContainers"), Value: strconv.Itoa(len(inits)) + " init containers"})
			reasons = append(reasons, "init containers start before a classic sidecar")
		}
		if !hold {
			ev = append(ev, evidence{Kind: p.Owner.Kind, Name: p.Owner.Name, Path: p.path("metadata.annotations.proxy.istio.io/config"), Value: p.Annotations["proxy.istio.io/config"]})
			reasons = append(reasons, "holdApplicationUntilProxyStarts not set")
		}
		out = append(out, finding{Subject: p.Owner.ref(), Reason: strings.Join(reasons, "; "), Evidence: ev})
	}
	return out
}

func checkJob(m *manifest, o options) []finding {
	var out []finding
	for _, p := range m.Pods {
		if !p.RunToCompletion || !inMesh(o.World, p) {
			continue
		}
		classic, why := classicSidecar(p, o)
		if !classic {
			continue
		}
		out = append(out, finding{Subject: p.Owner.ref(), Reason: "pod meant to end gets a classic sidecar that keeps running", Evidence: []evidence{why}})
	}
	return out
}

// --- apiserver-caller ----------------------------------------------------------------

type serviceRef struct {
	Owner     *object
	Path      string
	Namespace string
	Name      string
	Port      int
}

func refOf(o *object, path string, svc map[string]any) (serviceRef, bool) {
	if svc == nil || str(svc["name"]) == "" {
		return serviceRef{}, false
	}
	port := 443
	if n, ok := num(svc["port"]); ok {
		port = n
	}
	return serviceRef{Owner: o, Path: path, Namespace: str(svc["namespace"]), Name: str(svc["name"]), Port: port}, true
}

func webhookRefs(m *manifest) []serviceRef {
	var out []serviceRef
	for _, o := range m.Objects {
		switch o.Kind {
		case "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration":
			for i, w := range slice(o.Raw["webhooks"]) {
				path := fmt.Sprintf("webhooks[%d].clientConfig.service", i)
				if r, ok := refOf(o, path, mp(mp(mp(w)["clientConfig"])["service"])); ok {
					out = append(out, r)
				}
			}
		case "APIService":
			if r, ok := refOf(o, "spec.service", mp(mp(o.Raw["spec"])["service"])); ok {
				out = append(out, r)
			}
		case "CustomResourceDefinition":
			conv := mp(mp(o.Raw["spec"])["conversion"])
			if str(conv["strategy"]) == "Webhook" {
				if r, ok := refOf(o, "spec.conversion.webhook.clientConfig.service", mp(mp(mp(conv["webhook"])["clientConfig"])["service"])); ok {
					out = append(out, r)
				}
			}
		}
	}
	return out
}

// portListed reads a comma-separated port list; "*" means every port only
// where the annotation allows it (includeInboundPorts).
func portListed(list string, port int, star bool) bool {
	for _, s := range strings.Split(list, ",") {
		s = strings.TrimSpace(s)
		if (star && s == "*") || s == strconv.Itoa(port) {
			return true
		}
	}
	return false
}

// bypassesSidecar: traffic.sidecar.istio.io annotations that keep an inbound
// port out of the sidecar's redirection.
func bypassesSidecar(p *podTemplate, port int) bool {
	if ex, ok := p.Annotations["traffic.sidecar.istio.io/excludeInboundPorts"]; ok && portListed(ex, port, false) {
		return true
	}
	if in, ok := p.Annotations["traffic.sidecar.istio.io/includeInboundPorts"]; ok && !portListed(in, port, true) {
		return true
	}
	return false
}

func checkWebhook(m *manifest, o options) []finding {
	var out []finding
	svcs := m.byKind("Service")
	for _, ref := range webhookRefs(m) {
		ns := ref.Namespace
		if ns == "" {
			ns = m.Namespace
		}
		for _, svc := range svcs {
			if svc.Name != ref.Name || svc.Namespace != ns {
				continue
			}
			for _, p := range m.Pods {
				if !svc.selects(p) || !inMesh(o.World, p) {
					continue
				}
				for _, sp := range servicePorts(svc, p) {
					if sp.Port != ref.Port || sp.Target == 0 {
						continue
					}
					if o.World == "W1" {
						if bypassesSidecar(p, sp.Target) {
							continue
						}
					}
					out = append(out, finding{
						Subject: ref.Owner.ref(),
						Reason:  fmt.Sprintf("the API server calls %s:%d, whose pod %s is in the mesh", svc.Name, ref.Port, p.Owner.ref()),
						Evidence: []evidence{
							{Kind: ref.Owner.Kind, Name: ref.Owner.Name, Path: ref.Path, Value: fmt.Sprintf("%s/%s:%d", ns, ref.Name, ref.Port)},
							{Kind: "Service", Name: svc.Name, Path: sp.path(), Value: fmt.Sprintf("port=%d targetPort=%d selector=%s", sp.Port, sp.Target, labelString(strMap(mp(svc.Raw["spec"])["selector"])))},
							podEvidence(p),
						},
					})
				}
			}
		}
	}
	return out
}

// --- ambient-l7 ---------------------------------------------------------------------

// The L4 attributes of the ambient L4 policy page, "Allowed policy attributes".
var (
	l4Source    = map[string]bool{"principals": true, "notPrincipals": true, "namespaces": true, "notNamespaces": true, "ipBlocks": true, "notIpBlocks": true}
	l4Operation = map[string]bool{"ports": true, "notPorts": true}
	l4When      = map[string]bool{"source.ip": true, "source.namespace": true, "source.principal": true, "destination.ip": true, "destination.port": true}
)

func l7Attributes(spec map[string]any) []evidence {
	var out []evidence
	for i, r := range slice(spec["rules"]) {
		rm := mp(r)
		for j, f := range slice(rm["from"]) {
			for _, k := range sortedKeys(mp(mp(f)["source"])) {
				if !l4Source[k] {
					out = append(out, evidence{Path: fmt.Sprintf("spec.rules[%d].from[%d].source.%s", i, j, k), Value: k})
				}
			}
		}
		for j, t := range slice(rm["to"]) {
			for _, k := range sortedKeys(mp(mp(t)["operation"])) {
				if !l4Operation[k] {
					out = append(out, evidence{Path: fmt.Sprintf("spec.rules[%d].to[%d].operation.%s", i, j, k), Value: k})
				}
			}
		}
		for j, w := range slice(rm["when"]) {
			if k := str(mp(w)["key"]); !l4When[k] {
				out = append(out, evidence{Path: fmt.Sprintf("spec.rules[%d].when[%d].key", i, j), Value: k})
			}
		}
	}
	return out
}

func checkL7(m *manifest, o options) []finding {
	for _, g := range m.byKind("Gateway") {
		if g.Group == "gateway.networking.k8s.io" && str(mp(g.Raw["spec"])["gatewayClassName"]) == "istio-waypoint" {
			return nil
		}
	}
	var out []finding
	for _, ap := range m.byKind("AuthorizationPolicy") {
		if ap.Group != "security.istio.io" {
			continue
		}
		spec := mp(ap.Raw["spec"])
		if spec["targetRef"] != nil || len(slice(spec["targetRefs"])) > 0 {
			continue
		}
		ev := l7Attributes(spec)
		if len(ev) == 0 {
			continue
		}
		for i := range ev {
			ev[i].Kind, ev[i].Name = ap.Kind, ap.Name
		}
		out = append(out, finding{Subject: ap.ref(), Reason: "L7 attributes in a policy ztunnel enforces, and no waypoint", Evidence: ev})
	}
	for _, vs := range m.byKind("VirtualService") {
		if vs.Group != "networking.istio.io" {
			continue
		}
		spec := mp(vs.Raw["spec"])
		mesh := len(slice(spec["gateways"])) == 0
		for _, g := range slice(spec["gateways"]) {
			if str(g) == "mesh" {
				mesh = true
			}
		}
		if mesh && len(slice(spec["http"])) > 0 {
			out = append(out, finding{Subject: vs.ref(), Reason: "mesh VirtualService with http routes, and no waypoint", Evidence: []evidence{
				{Kind: vs.Kind, Name: vs.Name, Path: "spec.gateways", Value: fmt.Sprint(slice(spec["gateways"]))},
				{Kind: vs.Kind, Name: vs.Name, Path: "spec.http", Value: strconv.Itoa(len(slice(spec["http"]))) + " routes"},
			}})
		}
	}
	for _, rt := range m.byKind("HTTPRoute", "GRPCRoute") {
		if rt.Group != "gateway.networking.k8s.io" {
			continue
		}
		for i, pr := range slice(mp(rt.Raw["spec"])["parentRefs"]) {
			pm := mp(pr)
			if str(pm["kind"]) == "Service" && (str(pm["group"]) == "" || str(pm["group"]) == "core") {
				out = append(out, finding{Subject: rt.ref(), Reason: "route attached to a Service (mesh), and no waypoint", Evidence: []evidence{
					{Kind: rt.Kind, Name: rt.Name, Path: fmt.Sprintf("spec.parentRefs[%d]", i), Value: "Service/" + str(pm["name"])},
				}})
			}
		}
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
