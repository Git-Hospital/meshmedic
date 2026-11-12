package chartcheck

import (
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// object is one document of the rendered chart.
type object struct {
	Kind      string
	Name      string
	Namespace string
	Group     string // the part of apiVersion before the slash, "" for core
	Raw       map[string]any
}

// clusterScoped lists the kinds that have no namespace, so that the release
// namespace is not made up for them.
var clusterScoped = map[string]bool{
	"APIService": true, "ClusterRole": true, "ClusterRoleBinding": true, "CustomResourceDefinition": true,
	"GatewayClass": true, "IngressClass": true, "MutatingWebhookConfiguration": true, "Namespace": true,
	"Node": true, "PersistentVolume": true, "PriorityClass": true, "StorageClass": true,
	"ValidatingWebhookConfiguration": true,
}

func (o *object) ref() string { return o.Kind + "/" + o.Name }

// podTemplate is a pod spec the chart renders, with the object that owns it.
type podTemplate struct {
	Owner       *object
	Path        string // path of the template inside the owner, "" for a bare Pod
	Labels      map[string]string
	Annotations map[string]string
	Spec        map[string]any
	// RunToCompletion is true for Jobs, CronJobs and bare Pods whose
	// restartPolicy is Never or OnFailure: pods that are meant to end.
	RunToCompletion bool
}

func (p *podTemplate) path(rest string) string {
	if p.Path == "" {
		return rest
	}
	return p.Path + "." + rest
}

// DocIssue names a document of the input that could not be read, or that was
// read with a leniency, and says why. Source, Kind and Name are recovered from
// the text when it has them.
type DocIssue struct {
	// Index counts the documents with content, from 1.
	Index int `json:"index"`
	// Line is the line of the input where the document starts.
	Line   int    `json:"line"`
	Source string `json:"source,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Name   string `json:"name,omitempty"`
	Reason string `json:"reason"`
}

// Manifest is a rendered chart: the objects of a multi-document YAML stream
// and the pod templates found inside them.
type Manifest struct {
	namespace string // the release namespace, for objects that do not name one
	objects   []*object
	pods      []*podTemplate
	unread    []DocIssue
	dupKeys   []DocIssue
}

// Objects returns how many Kubernetes objects the manifest holds.
func (m *Manifest) Objects() int { return len(m.objects) }

// PodTemplates returns how many pod templates (Deployments, StatefulSets,
// DaemonSets, ReplicaSets, Jobs, CronJobs and bare Pods) the manifest holds.
func (m *Manifest) PodTemplates() int { return len(m.pods) }

// Unread returns the documents that could not be read.
func (m *Manifest) Unread() []DocIssue { return m.unread }

// Parse reads the multi-document YAML that helm template writes. Empty
// documents and documents without a kind are skipped. namespace is used for
// objects that name none, as the release namespace would be.
//
// A document that cannot be read does not stop the others: it is kept in
// Unread with the reason, and the caller must treat the result as incomplete.
// A duplicate mapping key is not a failure: helm and the API server accept it
// and use the last value, and so does Parse, which records it in the report.
// The error is for a failure to read r.
func Parse(r io.Reader, namespace string) (*Manifest, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	m := &Manifest{namespace: namespace}
	index := 0
	for _, ch := range splitDocuments(string(data)) {
		if !hasContent(ch.text) {
			continue
		}
		index++
		issue := DocIssue{Index: index, Line: ch.line}
		issue.Source, issue.Kind, issue.Name = recoverIdentity(ch.text)
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(ch.text), &node); err != nil {
			issue.Reason = absoluteLines(strings.TrimPrefix(err.Error(), "yaml: "), ch.line)
			m.unread = append(m.unread, issue)
			continue
		}
		var dups []string
		doc, ok := nodeToAny(&node, &dups, ch.line-1).(map[string]any)
		if !ok {
			issue.Reason = "the document is not a mapping"
			m.unread = append(m.unread, issue)
			continue
		}
		if len(dups) > 0 {
			d := issue
			d.Reason = dups[0] + "; the last value is used, as helm and the API server do"
			if len(dups) > 1 {
				d.Reason += fmt.Sprintf(" (%d duplicate keys in all)", len(dups))
			}
			m.dupKeys = append(m.dupKeys, d)
		}
		kind := str(doc["kind"])
		if kind == "" {
			continue
		}
		if kind == "List" {
			for _, it := range slice(doc["items"]) {
				if im, ok := it.(map[string]any); ok {
					m.add(im)
				}
			}
			continue
		}
		m.add(doc)
	}
	return m, nil
}

type docChunk struct {
	line int // 1-based line of the input where the chunk starts
	text string
}

// splitDocuments cuts the stream at lines that are a document separator
// ("---" alone, or followed by a space). It is a line split, not a YAML
// parser: yaml.v3 does the parsing of each piece, so one bad piece cannot
// take the others with it.
func splitDocuments(data string) []docChunk {
	var out []docChunk
	var cur []string
	start := 1
	flush := func() {
		out = append(out, docChunk{line: start, text: strings.Join(cur, "\n") + "\n"})
		cur = nil
	}
	for i, line := range strings.Split(data, "\n") {
		l := strings.TrimRight(line, "\r")
		if l == "---" || strings.HasPrefix(l, "--- ") {
			flush()
			start = i + 2
			if l != "---" {
				cur = append(cur, strings.TrimPrefix(l, "--- "))
				start = i + 1
			}
			continue
		}
		cur = append(cur, l)
	}
	flush()
	return out
}

func hasContent(text string) bool {
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			return true
		}
	}
	return false
}

var (
	sourceRE = regexp.MustCompile(`(?m)^# Source: (\S+)`)
	kindRE   = regexp.MustCompile(`(?m)^kind:\s*["']?([A-Za-z0-9]+)`)
	nameRE   = regexp.MustCompile(`(?m)^  name:\s*["']?([A-Za-z0-9._-]+)`)
	lineRE   = regexp.MustCompile(`line (\d+)`)
)

// recoverIdentity reads what it can from the text of a document that may not
// parse: helm's "# Source:" comment, and the first top-level kind and
// metadata name.
func recoverIdentity(text string) (source, kind, name string) {
	if g := sourceRE.FindStringSubmatch(text); g != nil {
		source = g[1]
	}
	if g := kindRE.FindStringSubmatch(text); g != nil {
		kind = g[1]
	}
	if g := nameRE.FindStringSubmatch(text); g != nil {
		name = g[1]
	}
	return
}

// absoluteLines rewrites the line numbers of a yaml.v3 message, which count
// from the start of the piece, to count from the start of the input.
func absoluteLines(msg string, start int) string {
	return lineRE.ReplaceAllStringFunc(msg, func(s string) string {
		n, _ := strconv.Atoi(strings.TrimPrefix(s, "line "))
		return fmt.Sprintf("line %d", n+start-1)
	})
}

// nodeToAny turns a parsed node into the plain maps, slices and scalars the
// checks read. yaml.v3 refuses to decode a mapping with a duplicate key into a
// map; this walk does not, and the later key wins. Each duplicate is appended
// to dups, with line numbers shifted by off.
func nodeToAny(n *yaml.Node, dups *[]string, off int) any {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil
		}
		return nodeToAny(n.Content[0], dups, off)
	case yaml.AliasNode:
		return nodeToAny(n.Alias, dups, off)
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			out = append(out, nodeToAny(c, dups, off))
		}
		return out
	case yaml.MappingNode:
		out := map[string]any{}
		seen := map[string]int{}
		var merges []map[string]any
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Tag == "!!merge" {
				switch vv := nodeToAny(v, dups, off).(type) {
				case map[string]any:
					merges = append(merges, vv)
				case []any:
					for _, e := range vv {
						if em, ok := e.(map[string]any); ok {
							merges = append(merges, em)
						}
					}
				}
				continue
			}
			key := k.Value
			if first, dup := seen[key]; dup {
				*dups = append(*dups, fmt.Sprintf("mapping key %q already defined at line %d (again at line %d)", key, first+off, k.Line+off))
			} else {
				seen[key] = k.Line
			}
			out[key] = nodeToAny(v, dups, off)
		}
		// Merged keys are defaults: the document's own keys win.
		for _, mm := range merges {
			for k, v := range mm {
				if _, set := seen[k]; !set {
					out[k] = v
				}
			}
		}
		return out
	default:
		var v any
		if err := n.Decode(&v); err != nil {
			return n.Value
		}
		return v
	}
}

func (m *Manifest) add(doc map[string]any) {
	meta := mp(doc["metadata"])
	o := &object{
		Kind:      str(doc["kind"]),
		Name:      str(meta["name"]),
		Namespace: str(meta["namespace"]),
		Raw:       doc,
	}
	if o.Namespace == "" && !clusterScoped[o.Kind] {
		o.Namespace = m.namespace
	}
	if av := str(doc["apiVersion"]); strings.Contains(av, "/") {
		o.Group = av[:strings.Index(av, "/")]
	}
	m.objects = append(m.objects, o)

	spec := mp(doc["spec"])
	switch o.Kind {
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job":
		m.addPod(o, "spec.template", mp(spec["template"]), o.Kind == "Job")
	case "CronJob":
		js := mp(mp(spec["jobTemplate"])["spec"])
		m.addPod(o, "spec.jobTemplate.spec.template", mp(js["template"]), true)
	case "Pod":
		rp := str(spec["restartPolicy"])
		m.addPod(o, "", doc, rp == "Never" || rp == "OnFailure")
	}
}

func (m *Manifest) addPod(o *object, path string, tmpl map[string]any, rtc bool) {
	if tmpl == nil {
		return
	}
	meta := mp(tmpl["metadata"])
	m.pods = append(m.pods, &podTemplate{
		Owner:           o,
		Path:            path,
		Labels:          strMap(meta["labels"]),
		Annotations:     strMap(meta["annotations"]),
		Spec:            mp(tmpl["spec"]),
		RunToCompletion: rtc,
	})
}

func (m *Manifest) byKind(kinds ...string) []*object {
	var out []*object
	for _, o := range m.objects {
		for _, k := range kinds {
			if o.Kind == k {
				out = append(out, o)
				break
			}
		}
	}
	return out
}

// inMesh says whether a pod from this template would be in the mesh in the
// given mode. The checks assume the release namespace is enrolled (labelled
// istio-injection=enabled for sidecars, istio.io/dataplane-mode=ambient for
// ambient), because a rendered chart cannot show namespace labels. A pod leaves
// the mesh by its own opt-out, and pods on the host network are never enrolled.
func inMesh(mode Mode, p *podTemplate) bool {
	if b, ok := p.Spec["hostNetwork"].(bool); ok && b {
		return false
	}
	switch mode {
	case Sidecar:
		// The label takes precedence over the deprecated annotation.
		if v, ok := p.Labels["sidecar.istio.io/inject"]; ok {
			return v != "false"
		}
		return p.Annotations["sidecar.istio.io/inject"] != "false"
	case Ambient:
		return p.Labels["istio.io/dataplane-mode"] != "none"
	}
	return false
}

// container is one entry of containers or initContainers.
type container struct {
	Name string
	Path string // full path from the owner object
	Raw  map[string]any
}

func (p *podTemplate) containers(field string) []container {
	var out []container
	for i, c := range slice(p.Spec[field]) {
		cm := mp(c)
		out = append(out, container{
			Name: str(cm["name"]),
			Path: p.path(fmt.Sprintf("spec.%s[%d]", field, i)),
			Raw:  cm,
		})
	}
	return out
}

// containerPort is a declared port of one of the pod's containers.
type containerPort struct {
	Name     string
	Number   int
	Protocol string
	Path     string
}

func (p *podTemplate) ports() []containerPort {
	var out []containerPort
	for _, c := range p.containers("containers") {
		for j, pt := range slice(c.Raw["ports"]) {
			pm := mp(pt)
			n, ok := num(pm["containerPort"])
			if !ok {
				continue
			}
			out = append(out, containerPort{
				Name:     str(pm["name"]),
				Number:   n,
				Protocol: protoOr(pm["protocol"]),
				Path:     fmt.Sprintf("%s.ports[%d].containerPort", c.Path, j),
			})
		}
	}
	return out
}

// resolvePort turns an IntOrString port into a number, looking a name up in
// the given container ports. ok is false for a name no port carries.
func resolvePort(v any, ports []containerPort) (int, bool) {
	if n, ok := num(v); ok {
		return n, true
	}
	name := str(v)
	if name == "" {
		return 0, false
	}
	for _, cp := range ports {
		if cp.Name == name {
			return cp.Number, true
		}
	}
	return 0, false
}

// selects reports whether a Service's selector picks this pod template.
func (svc *object) selects(p *podTemplate) bool {
	if svc.Namespace != p.Owner.Namespace {
		return false
	}
	spec := mp(svc.Raw["spec"])
	if str(spec["type"]) == "ExternalName" {
		return false
	}
	sel := strMap(spec["selector"])
	if len(sel) == 0 {
		return false
	}
	for k, v := range sel {
		if p.Labels[k] != v {
			return false
		}
	}
	return true
}

// servicePort is one port of a Service, with its target resolved against
// one selected pod template.
type servicePort struct {
	Svc         *object
	Index       int
	Name        string
	Port        int
	Protocol    string
	AppProtocol *string
	Target      int // 0 when a named target port resolves to nothing
}

func (sp servicePort) path() string { return fmt.Sprintf("spec.ports[%d]", sp.Index) }

func servicePorts(svc *object, p *podTemplate) []servicePort {
	var out []servicePort
	cps := p.ports()
	for i, raw := range slice(mp(svc.Raw["spec"])["ports"]) {
		pm := mp(raw)
		n, ok := num(pm["port"])
		if !ok {
			continue
		}
		sp := servicePort{Svc: svc, Index: i, Name: str(pm["name"]), Port: n, Protocol: protoOr(pm["protocol"])}
		if ap, ok := pm["appProtocol"]; ok && ap != nil {
			s := str(ap)
			sp.AppProtocol = &s
		}
		if tp, ok := pm["targetPort"]; ok && tp != nil {
			sp.Target, _ = resolvePort(tp, cps)
		} else {
			sp.Target = n
		}
		out = append(out, sp)
	}
	return out
}

// matchSelector evaluates a metav1.LabelSelector. An empty selector matches
// everything; a nil one (absent) is treated the same, as NetworkPolicy does.
func matchSelector(sel map[string]any, labels map[string]string) bool {
	for k, v := range strMap(sel["matchLabels"]) {
		if lv, ok := labels[k]; !ok || lv != v {
			return false
		}
	}
	for _, e := range slice(sel["matchExpressions"]) {
		em := mp(e)
		key := str(em["key"])
		lv, has := labels[key]
		var vals []string
		for _, v := range slice(em["values"]) {
			vals = append(vals, str(v))
		}
		in := false
		for _, v := range vals {
			if v == lv {
				in = true
			}
		}
		switch str(em["operator"]) {
		case "In":
			if !has || !in {
				return false
			}
		case "NotIn":
			if has && in {
				return false
			}
		case "Exists":
			if !has {
				return false
			}
		case "DoesNotExist":
			if has {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func cidrContains(cidr, ip string) bool {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	return n.Contains(net.ParseIP(ip))
}

// --- untyped YAML helpers ---------------------------------------------------

func mp(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func slice(v any) []any {
	s, _ := v.([]any)
	return s
}

func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

func strMap(v any) map[string]string {
	out := map[string]string{}
	for k, val := range mp(v) {
		out[k] = str(val)
	}
	return out
}

// num reads an integer. A numeric string counts as a number: the API server
// would refuse it as a port name, so a chart that installs never has one.
func num(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case uint64:
		return int(t), true
	case float64:
		return int(t), true
	case string:
		n, err := strconv.Atoi(t)
		return n, err == nil
	}
	return 0, false
}

func protoOr(v any) string {
	if s := str(v); s != "" {
		return s
	}
	return "TCP"
}

// The gateway injection convention, from Istio's gateway installation page: a
// gateway Deployment lists a container named istio-proxy whose image is the
// literal "auto"; the injector replaces it with the proxy at pod creation,
// usually with the pod annotation inject.istio.io/templates: gateway and the
// label sidecar.istio.io/inject: "true". Only a container with both that name
// and that image is the placeholder; one merely named istio-proxy with a real
// image runs what it says.
const (
	placeholderName  = "istio-proxy"
	placeholderImage = "auto"
)

func (c container) isPlaceholder() bool {
	return c.Name == placeholderName && str(c.Raw["image"]) == placeholderImage
}

// placeholder returns the path of the pod's injection placeholder container,
// or "" when it has none.
func (p *podTemplate) placeholder() string {
	for _, c := range p.containers("containers") {
		if c.isPlaceholder() {
			return c.Path
		}
	}
	return ""
}

// gatewayTemplate says whether the pod asks for the gateway injection
// template, which adds no separate sidecar: the placeholder is the proxy.
func (p *podTemplate) gatewayTemplate() bool {
	for _, t := range strings.Split(p.Annotations["inject.istio.io/templates"], ",") {
		if strings.TrimSpace(t) == "gateway" {
			return true
		}
	}
	return false
}
