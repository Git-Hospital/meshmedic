package chartcheck

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Mode is the Istio data-plane mode a check is about.
type Mode string

const (
	// Sidecar is the classic data plane: an Envoy proxy in every pod.
	Sidecar Mode = "sidecar"
	// Ambient is the sidecar-less data plane: a ztunnel on every node, and
	// optional waypoint proxies.
	Ambient Mode = "ambient"
)

// Resource names a Kubernetes object.
type Resource struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

func (o *object) resource() Resource {
	return Resource{Kind: o.Kind, Namespace: o.Namespace, Name: o.Name}
}

// Evidence is one field a reader can look up in the rendered manifest.
type Evidence struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	Value string `json:"value"`
}

// finding is one thing a check found, before the rule's own text is added.
type finding struct {
	Resource Resource
	Reason   string
	// Note marks a finding that depends on something the manifests cannot
	// show (the CNI), so it is reported but is not a candidate.
	Note bool
	// Trigger is the field whose value made the check fire.
	Trigger  Evidence
	Evidence []Evidence
}

type options struct {
	Mode Mode
	// NativeSidecars says whether the proxy is a native sidecar unless a pod
	// opts out. It is on in Istio 1.31 on Kubernetes 1.33 or newer
	// (ENABLE_NATIVE_SIDECARS=auto), which is why the two classic-sidecar
	// checks only fire for pods that opt out by default.
	NativeSidecars bool
}

func podEvidence(p *podTemplate) Evidence {
	return Evidence{Kind: p.Owner.Kind, Name: p.Owner.Name, Path: p.path("metadata.labels"), Value: labelString(p.Labels)}
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

func checkNetworkPolicy(m *Manifest, o options) []finding {
	var out []finding
	nps := m.byKind("NetworkPolicy")
	for _, p := range m.pods {
		if !inMesh(o.Mode, p) {
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
		ev := []Evidence{podEvidence(p)}
		for _, np := range selecting {
			ev = append(ev, Evidence{Kind: "NetworkPolicy", Name: np.Name, Path: "spec.podSelector", Value: "selects this pod; isolates ingress"})
			if len(slice(mp(np.Raw["spec"])["ingress"])) == 0 {
				ev = append(ev, Evidence{Kind: "NetworkPolicy", Name: np.Name, Path: "spec.ingress", Value: "no rules"})
			}
		}
		for _, r := range rules {
			ev = append(ev, Evidence{Kind: "NetworkPolicy", Name: r.np.Name, Path: fmt.Sprintf("spec.ingress[%d]", r.idx), Value: ruleSummary(r.rule)})
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
		resource := p.Owner.resource()
		if !admits(hbonePort, "") {
			// The first policy that selects the pod stands for the rest; all
			// of them are in the evidence.
			trigger := Evidence{Kind: "NetworkPolicy", Name: selecting[0].Name, Path: "spec.ingress", Value: "no rules"}
			if len(rules) > 0 {
				var sums []string
				for _, r := range rules {
					sums = append(sums, ruleSummary(r.rule))
				}
				trigger.Value = strings.Join(sums, "; ")
			}
			out = append(out, finding{Resource: resource, Reason: "no ingress rule admits TCP 15008 (HBONE)", Trigger: trigger, Evidence: ev})
		}
		seen := map[int]bool{}
		for _, pp := range probePorts(p) {
			if seen[pp.Port] || admits(pp.Port, probeSource) {
				continue
			}
			seen[pp.Port] = true
			trigger := Evidence{Kind: p.Owner.Kind, Name: p.Owner.Name, Path: pp.Path, Value: strconv.Itoa(pp.Port)}
			pev := append(append([]Evidence{}, ev...), trigger)
			out = append(out, finding{Resource: resource, Reason: fmt.Sprintf("kubelet probe port %d is not admitted from %s", pp.Port, probeSrcCIDR), Note: true, Trigger: trigger, Evidence: pev})
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

func checkProtocol(m *Manifest, o options) []finding {
	var out []finding
	svcs := m.byKind("Service")
	flaggedPort := map[string]bool{}
	for _, p := range m.pods {
		if !inMesh(o.Mode, p) {
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
						trigger := Evidence{Kind: "Service", Name: svc.Name, Path: sp.path(), Value: fmt.Sprintf("port=%d %s", sp.Port, declSource(sp))}
						out = append(out, finding{
							Resource: svc.resource(),
							Reason:   fmt.Sprintf("port %d is a documented server-first port and is declared %s", sp.Port, proto),
							Trigger:  trigger,
							Evidence: []Evidence{trigger, podEvidence(p)},
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
			var ev []Evidence
			for _, sp := range byTarget[t] {
				proto := istioProtocol(sp.Port, sp.Name, sp.Protocol, sp.AppProtocol)
				classes[protocolClass(proto)] = true
				ev = append(ev, Evidence{Kind: "Service", Name: sp.Svc.Name, Path: sp.path(), Value: fmt.Sprintf("targetPort=%d %s class=%s", t, declSource(sp), protocolClass(proto))})
			}
			if len(classes) > 1 {
				ev = append(ev, podEvidence(p))
				out = append(out, finding{Resource: p.Owner.resource(), Reason: fmt.Sprintf("container port %d is declared with different protocols by its Services", t), Trigger: ev[0], Evidence: ev})
			}
		}
	}
	return out
}

// --- uid-1337 --------------------------------------------------------------------

func checkUID(m *Manifest, o options) []finding {
	var out []finding
	for _, p := range m.pods {
		if !inMesh(o.Mode, p) {
			continue
		}
		podUID, podHas := num(mp(p.Spec["securityContext"])["runAsUser"])
		for _, field := range []string{"initContainers", "containers"} {
			for _, c := range p.containers(field) {
				if c.isPlaceholder() && p.gatewayTemplate() {
					// Replaced at injection; see isPlaceholder.
					continue
				}
				uid, has := num(mp(c.Raw["securityContext"])["runAsUser"])
				path := c.Path + ".securityContext.runAsUser"
				if !has {
					uid, has = podUID, podHas
					path = p.path("spec.securityContext.runAsUser")
				}
				if has && uid == 1337 {
					trigger := Evidence{Kind: p.Owner.Kind, Name: p.Owner.Name, Path: path, Value: "1337"}
					out = append(out, finding{
						Resource: p.Owner.resource(),
						Reason:   fmt.Sprintf("container %s runs as UID 1337", c.Name),
						Trigger:  trigger,
						Evidence: []Evidence{trigger},
					})
				}
			}
		}
	}
	return out
}

// --- reserved-port ---------------------------------------------------------------

var reservedPorts = map[Mode]map[int]bool{
	// application-requirements, "Ports used by Istio", the sidecar's table.
	Sidecar: {15000: true, 15001: true, 15002: true, 15004: true, 15006: true, 15008: true, 15020: true, 15021: true, 15053: true, 15090: true},
	// ambient traffic-redirection, "Istio's in-pod traffic redirection model".
	Ambient: {15001: true, 15006: true, 15008: true},
}

func checkReservedPort(m *Manifest, o options) []finding {
	var out []finding
	res := reservedPorts[o.Mode]
	svcs := m.byKind("Service")
	for _, p := range m.pods {
		if !inMesh(o.Mode, p) {
			continue
		}
		hits := map[int][]Evidence{}
		ph := p.gatewayPlaceholder()
		for _, cp := range p.ports() {
			if ph != "" && strings.HasPrefix(cp.Path, ph+".") {
				continue
			}
			if cp.Protocol == "TCP" && res[cp.Number] {
				hits[cp.Number] = append(hits[cp.Number], Evidence{Kind: p.Owner.Kind, Name: p.Owner.Name, Path: cp.Path, Value: strconv.Itoa(cp.Number)})
			}
		}
		for _, svc := range svcs {
			// In a pod that is only a gateway, every Service port reaches the
			// injected proxy itself (the gateway chart's status port 15021).
			// With an application container beside it, they may reach that.
			if !svc.selects(p) || p.pureGateway() {
				continue
			}
			for _, sp := range servicePorts(svc, p) {
				if sp.Protocol == "TCP" && res[sp.Target] {
					hits[sp.Target] = append(hits[sp.Target], Evidence{Kind: "Service", Name: svc.Name, Path: sp.path() + ".targetPort", Value: strconv.Itoa(sp.Target)})
				}
			}
		}
		ports := make([]int, 0, len(hits))
		for n := range hits {
			ports = append(ports, n)
		}
		sort.Ints(ports)
		for _, n := range ports {
			out = append(out, finding{Resource: p.Owner.resource(), Reason: fmt.Sprintf("port %d is used by Istio inside the pod", n), Trigger: hits[n][0], Evidence: hits[n]})
		}
	}
	return out
}

// --- startup-order and job-lifecycle -----------------------------------------------

// classicSidecar says whether the pod's proxy would be a regular container
// rather than a native sidecar, and why.
func classicSidecar(p *podTemplate, o options) (bool, Evidence) {
	if v, ok := p.Annotations["sidecar.istio.io/nativeSidecar"]; ok {
		return v == "false", Evidence{Kind: p.Owner.Kind, Name: p.Owner.Name, Path: p.path("metadata.annotations.sidecar.istio.io/nativeSidecar"), Value: v}
	}
	return !o.NativeSidecars, Evidence{Kind: "Option", Name: "native-sidecars", Path: "nativeSidecars", Value: strconv.FormatBool(o.NativeSidecars)}
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

func checkStartup(m *Manifest, o options) []finding {
	var out []finding
	for _, p := range m.pods {
		if !inMesh(o.Mode, p) {
			continue
		}
		classic, why := classicSidecar(p, o)
		if !classic || p.pureGateway() {
			continue
		}
		inits := p.containers("initContainers")
		hold := holdsApplication(p)
		if len(inits) == 0 && hold {
			continue
		}
		ev := []Evidence{why}
		var reasons []string
		if len(inits) > 0 {
			ev = append(ev, Evidence{Kind: p.Owner.Kind, Name: p.Owner.Name, Path: p.path("spec.initContainers"), Value: strconv.Itoa(len(inits)) + " init containers"})
			reasons = append(reasons, "init containers start before a classic sidecar")
		}
		if !hold {
			ev = append(ev, Evidence{Kind: p.Owner.Kind, Name: p.Owner.Name, Path: p.path("metadata.annotations.proxy.istio.io/config"), Value: p.Annotations["proxy.istio.io/config"]})
			reasons = append(reasons, "holdApplicationUntilProxyStarts not set")
		}
		out = append(out, finding{Resource: p.Owner.resource(), Reason: strings.Join(reasons, "; "), Trigger: ev[1], Evidence: ev})
	}
	return out
}

func checkJob(m *Manifest, o options) []finding {
	var out []finding
	for _, p := range m.pods {
		if !p.RunToCompletion || !inMesh(o.Mode, p) {
			continue
		}
		classic, why := classicSidecar(p, o)
		if !classic || p.pureGateway() {
			continue
		}
		out = append(out, finding{Resource: p.Owner.resource(), Reason: "pod meant to end gets a classic sidecar that keeps running", Trigger: why, Evidence: []Evidence{why}})
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

func webhookRefs(m *Manifest) []serviceRef {
	var out []serviceRef
	for _, o := range m.objects {
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

func checkWebhook(m *Manifest, o options) []finding {
	var out []finding
	seen := map[string]int{}
	svcs := m.byKind("Service")
	for _, ref := range webhookRefs(m) {
		ns := ref.Namespace
		if ns == "" {
			ns = m.namespace
		}
		for _, svc := range svcs {
			if svc.Name != ref.Name || svc.Namespace != ns {
				continue
			}
			for _, p := range m.pods {
				if !svc.selects(p) || !inMesh(o.Mode, p) {
					continue
				}
				for _, sp := range servicePorts(svc, p) {
					if sp.Port != ref.Port || sp.Target == 0 {
						continue
					}
					if o.Mode == Sidecar {
						if bypassesSidecar(p, sp.Target) {
							continue
						}
					}
					trigger := Evidence{Kind: ref.Owner.Kind, Name: ref.Owner.Name, Path: ref.Path, Value: fmt.Sprintf("%s/%s:%d", ns, ref.Name, ref.Port)}
					reason := fmt.Sprintf("the API server calls %s:%d, whose pod %s is in the mesh", svc.Name, ref.Port, p.Owner.ref())
					// An operator chart lists the same backend in several
					// webhooks of one configuration (cloudnative-pg has nine).
					// They share a cause and a fix, so they are one finding
					// that lists every webhook in its evidence.
					key := ref.Owner.ref() + "|" + reason
					if i, ok := seen[key]; ok {
						out[i].Evidence = append(out[i].Evidence, trigger)
						continue
					}
					seen[key] = len(out)
					out = append(out, finding{
						Resource: ref.Owner.resource(),
						Reason:   reason,
						Trigger:  trigger,
						Evidence: []Evidence{
							trigger,
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

func l7Attributes(spec map[string]any) []Evidence {
	var out []Evidence
	for i, r := range slice(spec["rules"]) {
		rm := mp(r)
		for j, f := range slice(rm["from"]) {
			for _, k := range sortedKeys(mp(mp(f)["source"])) {
				if !l4Source[k] {
					out = append(out, Evidence{Path: fmt.Sprintf("spec.rules[%d].from[%d].source.%s", i, j, k), Value: k})
				}
			}
		}
		for j, t := range slice(rm["to"]) {
			for _, k := range sortedKeys(mp(mp(t)["operation"])) {
				if !l4Operation[k] {
					out = append(out, Evidence{Path: fmt.Sprintf("spec.rules[%d].to[%d].operation.%s", i, j, k), Value: k})
				}
			}
		}
		for j, w := range slice(rm["when"]) {
			if k := str(mp(w)["key"]); !l4When[k] {
				out = append(out, Evidence{Path: fmt.Sprintf("spec.rules[%d].when[%d].key", i, j), Value: k})
			}
		}
	}
	return out
}

func checkL7(m *Manifest, o options) []finding {
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
		out = append(out, finding{Resource: ap.resource(), Reason: "L7 attributes in a policy ztunnel enforces, and no waypoint", Trigger: ev[0], Evidence: ev})
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
			trigger := Evidence{Kind: vs.Kind, Name: vs.Name, Path: "spec.http", Value: strconv.Itoa(len(slice(spec["http"]))) + " routes"}
			out = append(out, finding{Resource: vs.resource(), Reason: "mesh VirtualService with http routes, and no waypoint", Trigger: trigger, Evidence: []Evidence{
				{Kind: vs.Kind, Name: vs.Name, Path: "spec.gateways", Value: fmt.Sprint(slice(spec["gateways"]))},
				trigger,
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
				trigger := Evidence{Kind: rt.Kind, Name: rt.Name, Path: fmt.Sprintf("spec.parentRefs[%d]", i), Value: "Service/" + str(pm["name"])}
				out = append(out, finding{Resource: rt.resource(), Reason: "route attached to a Service (mesh), and no waypoint", Trigger: trigger, Evidence: []Evidence{trigger}})
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
