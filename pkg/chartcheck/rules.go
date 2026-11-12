package chartcheck

// IstioVersion is the Istio release the rules were written against. The
// documentation quotes were checked against its documentation and the next
// minor's, and the sidecar defaults (native sidecars) are those of this release.
const IstioVersion = "1.31"

// Doc is a passage of the Istio documentation a rule rests on.
type Doc struct {
	URL     string `json:"url"`
	Heading string `json:"heading"`
	Quote   string `json:"quote"`
}

// Rule is one check with the text that explains its findings. IDs are stable:
// an ID is never reused for another check, and a retired check leaves its ID
// unused.
type Rule struct {
	// ID is the stable finding id, MM-CHART-NNN.
	ID string `json:"id"`
	// Check is the short name of the check.
	Check string `json:"check"`
	// Modes lists the mesh modes the check applies to.
	Modes []Mode `json:"modes"`
	// Summary says exactly what the check reads and when it fires.
	Summary string `json:"summary"`
	// Effect is what happens under Istio, in one plain sentence.
	Effect string `json:"effect"`
	// Fix is generic manifest-level guidance: it names no value of any
	// particular chart, because a rendered manifest cannot show them.
	Fix string `json:"fix"`
	// Confirm says how to see in a cluster whether the candidate is real.
	Confirm string `json:"confirm"`
	// Docs are the documentation passages the rule rests on, the first being
	// the main one. Empty when no page states the requirement.
	Docs []Doc `json:"docs"`
	// DocsNote says plainly why Docs is empty.
	DocsNote string `json:"docsNote,omitempty"`

	run func(*Manifest, options) []finding
}

func (r Rule) appliesTo(m Mode) bool {
	for _, x := range r.Modes {
		if x == m {
			return true
		}
	}
	return false
}

// Rules returns the checks in id order.
func Rules() []Rule {
	out := make([]Rule, len(rules))
	copy(out, rules)
	return out
}

var rules = []Rule{
	{
		ID:    "MM-CHART-001",
		Check: "np-hbone-probe",
		Modes: []Mode{Ambient},
		Summary: "A pod in the mesh that a NetworkPolicy isolates for ingress is flagged when no ingress rule of the policies selecting it admits TCP 15008, or when a kubelet probe port of the pod is not admitted from 169.254.7.127. " +
			"A rule admits a port when its ports list is empty or has a TCP entry that omits the port, names it, names a container port with that number, or is a range covering it; it admits 169.254.7.127 when its from list is empty or has an ipBlock whose cidr covers the address and whose except list does not.",
		Effect:  "Ambient carries traffic between meshed pods over TCP port 15008 (HBONE) and sends kubelet probes from 169.254.7.127, and the node enforces this NetworkPolicy on both, so a policy that admits neither drops pod-to-pod traffic or fails the pod's probes.",
		Fix:     "Add an ingress rule to the NetworkPolicy that admits TCP 15008 (ports: [{port: 15008, protocol: TCP}]) and, for a probe finding, one that admits the probed port from ipBlock 169.254.7.127/32. If the chart has a value for extra ingress rules, put the rule there instead of patching the template.",
		Confirm: "Install the chart in a throwaway ambient cluster and watch the traffic between its own pods (replication, clustering): pods that never become Ready or keep restarting, and connection errors toward port 15008 in the ztunnel logs, confirm it. Install it without the mesh as the comparison.",
		Docs:    docsNetworkPolicy,
		run:     checkNetworkPolicy,
	},
	{
		ID:    "MM-CHART-002",
		Check: "protocol-declared",
		Modes: []Mode{Sidecar},
		Summary: "A TCP port of a Service that selects a pod in the mesh is flagged when its number is one of the server-first ports Istio documents (25, 53, 3306, 27017) and its appProtocol, else its name prefix, declares http, http2, grpc, grpc-web or http_proxy. " +
			"A pod in the mesh is flagged when two of its Service ports target the same container port with different declared protocols (classes http, http2, tls, tcp; undeclared ports are not compared).",
		Effect:  "The sidecar treats the port as the protocol the Service declares, so a server-first protocol (one where the server speaks first, like MySQL) never gets its greeting through an HTTP listener, and one container port cannot be served as two protocols.",
		Fix:     "Declare the protocol the port really carries: set appProtocol: tcp on the Service port (or name it with a tcp- prefix) for a server-first port, and give every Service that targets one container port the same protocol.",
		Confirm: "From a meshed client in a sidecar cluster, connect to the Service port and check that the server's first bytes arrive (for example the MySQL handshake); a client that hangs or times out confirms it. `istioctl analyze` also reports port naming problems.",
		Docs:    docsProtocol,
		run:     checkProtocol,
	},
	{
		ID:      "MM-CHART-003",
		Check:   "uid-1337",
		Modes:   []Mode{Sidecar},
		Summary: "A container or init container of a pod in the mesh is flagged when its effective runAsUser (the container's securityContext, else the pod's) is 1337.",
		Effect:  "User ID 1337 is the sidecar proxy's own, and the sidecar's traffic redirection lets that user's traffic skip the proxy, so this container's traffic leaves the mesh unproxied.",
		Fix:     "Run the container as another user ID: change runAsUser (in the container's or the pod's securityContext) and make sure the files the image needs are readable by that user.",
		Confirm: "In a sidecar cluster, run `kubectl exec POD -c CONTAINER -- id -u` to see the user, and look for the container's outbound requests in the sidecar's access log or metrics: traffic that is missing there bypassed the proxy.",
		Docs:    docsUID,
		run:     checkUID,
	},
	{
		ID:      "MM-CHART-004",
		Check:   "reserved-port",
		Modes:   []Mode{Sidecar, Ambient},
		Summary: "A pod in the mesh is flagged when a container declares, or a Service selecting it targets, a TCP port Istio uses inside the pod: for sidecar 15000, 15001, 15002, 15004, 15006, 15008, 15020, 15021, 15053 or 15090 (the sidecar's ports), for ambient 15001, 15006 or 15008 (ztunnel's in-pod listeners).",
		Effect:  "The mesh proxy listens on this port inside the pod, so an application that binds it competes with the proxy and either fails to start or has its traffic taken over.",
		Fix:     "Move the application to a port outside Istio's list and change the Service targetPort and any probe that names it to match. A containerPort the application never listens on can simply be removed.",
		Confirm: "Start the pod in a cluster in that mode and read the application container's log for bind errors such as address already in use, or see whether the pod stays unready.",
		Docs:    docsReservedPort,
		run:     checkReservedPort,
	},
	{
		ID:      "MM-CHART-005",
		Check:   "classic-sidecar-startup",
		Modes:   []Mode{Sidecar},
		Summary: "A pod in the mesh whose proxy is a classic sidecar (pod annotation sidecar.istio.io/nativeSidecar set to false, or native sidecars off by --native-sidecars=false) is flagged when it has init containers, or when its proxy.istio.io/config annotation does not set holdApplicationUntilProxyStarts to true.",
		Effect:  "A classic sidecar starts after the init containers and alongside the application, so anything they do over the network at startup can hang or fail until the proxy is ready.",
		Fix:     "Use native sidecars, the default in Istio 1.31 on Kubernetes 1.33 or newer, by removing the sidecar.istio.io/nativeSidecar: \"false\" annotation. Where classic sidecars must stay, set the pod annotation proxy.istio.io/config to '{\"holdApplicationUntilProxyStarts\": true}' for the application container; init containers that need the network still need native sidecars.",
		Confirm: "Start the pod in a sidecar cluster and read the init container and application logs of the first start for connection refused or timeout errors; the same pod without the mesh is the comparison.",
		Docs:    docsStartup,
		run:     checkStartup,
	},
	{
		ID:      "MM-CHART-006",
		Check:   "classic-sidecar-job",
		Modes:   []Mode{Sidecar},
		Summary: "A Job, a CronJob, or a bare Pod with restartPolicy Never or OnFailure, whose pod is in the mesh, is flagged when its proxy is a classic sidecar (pod annotation sidecar.istio.io/nativeSidecar set to false, or native sidecars off by --native-sidecars=false).",
		Effect:  "A classic sidecar keeps running after the Job's own container exits, so the pod never completes and the Job never finishes.",
		Fix:     "Remove the sidecar.istio.io/nativeSidecar: \"false\" annotation so the proxy is a native sidecar that ends with the Job. If the pod needs no mesh, label it sidecar.istio.io/inject: \"false\"; otherwise have the Job call the proxy's /quitquitquit endpoint on port 15020 as its last step.",
		Confirm: "Run the Job in a sidecar cluster and watch `kubectl get pods`: the application container shows Completed while istio-proxy keeps running and the Job never reaches Complete.",
		Docs:    docsJob,
		run:     checkJob,
	},
	{
		ID:    "MM-CHART-007",
		Check: "webhook-backend-in-mesh",
		Modes: []Mode{Sidecar, Ambient},
		Summary: "A webhook of a ValidatingWebhookConfiguration or MutatingWebhookConfiguration, an APIService, or a CustomResourceDefinition conversion webhook is flagged when its service reference names a Service port the chart renders whose Service selects a pod in the mesh. " +
			"For sidecar it is not flagged when the pod's traffic.sidecar.istio.io/excludeInboundPorts or includeInboundPorts annotation keeps the target port out of the sidecar.",
		Effect:   "The Kubernetes API server is outside the mesh and calls this backend without mTLS, so where the namespace enforces STRICT mutual TLS the call can be refused and the requests this webhook or API serves fail or are skipped, depending on its failure policy.",
		Fix:      "Keep the backend's port out of mTLS enforcement. For sidecar, annotate the pod traffic.sidecar.istio.io/excludeInboundPorts with the target port, or add a PeerAuthentication with a portLevelMtls PERMISSIVE entry for it. For ambient, or when the backend needs no mesh, label the pod istio.io/dataplane-mode: none (ambient) or sidecar.istio.io/inject: \"false\" (sidecar). Look for a chart value that adds pod annotations or labels to the component.",
		Confirm:  "In a cluster with STRICT mTLS, run an operation the webhook covers (`kubectl apply --dry-run=server` on a matching object) and read the error, which names the webhook and the failed call. For an APIService, `kubectl get apiservice` shows Available False.",
		DocsNote: "No Istio documentation page states this requirement for the API server. The nearest statement is general, not about webhooks: \"When STRICT mutual TLS is enabled, non-Istio workloads cannot communicate to Istio services, as they will not have a valid Istio client certificate.\" (https://istio.io/latest/about/faq/security/). The API server is a caller outside the mesh; that it is refused is an inference from the statement, not a documented behavior.",
		run:      checkWebhook,
	},
	{
		ID:    "MM-CHART-008",
		Check: "l7-without-waypoint",
		Modes: []Mode{Ambient},
		Summary: "When the chart renders no Gateway of gatewayClassName istio-waypoint, an AuthorizationPolicy without targetRef or targetRefs is flagged when a rule uses an attribute outside the L4 list on Istio's ambient L4 policy page. " +
			"So is a VirtualService bound to the mesh (gateways empty or containing mesh) with http routes, and an HTTPRoute or GRPCRoute with a Service parentRef.",
		Effect:  "Ztunnel handles layer 4 only, so without a waypoint proxy an authorization policy with layer 7 attributes that is selected by workload fails safe by becoming a DENY policy, and HTTP routing rules are not applied.",
		Fix:     "Deploy a waypoint (a Gateway with gatewayClassName: istio-waypoint) for the namespace or Service and attach the policy with targetRefs and the route through its parentRefs; or reduce the policy to layer 4 attributes (principals, namespaces, ipBlocks, ports).",
		Confirm: "Check with `kubectl get gateway -A` whether a waypoint exists for the workload, then send the kind of request the policy or route should affect and see whether it is denied or routed as intended.",
		Docs:    docsL7,
		run:     checkL7,
	},
}
