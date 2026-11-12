package chartcheck

// The documentation each rule rests on. Quotes are the rendered page text of
// the Istio documentation, copied word for word (markup removed, typographic
// apostrophes written as ASCII), and were checked against the 1.31 and 1.32
// documentation on 2026-11-11. The URLs use the /latest/ path, which moves
// with Istio releases; the heading says where on the page to look.

var docsNetworkPolicy = []Doc{
	{
		URL:     "https://istio.io/latest/docs/ambient/usage/networkpolicy/",
		Heading: "Ambient traffic overlay and Kubernetes NetworkPolicy",
		Quote:   "However, NetworkPolicy is enforced on the host, outside the pod. This means that if you have preexisting NetworkPolicy in place that, for example, will deny list inbound traffic to an ambient pod on every port but 443, you will have to add an exception to that NetworkPolicy for port 15008.",
	},
}

// docsProbe is the page on kubelet probes. What it says depends on the CNI,
// and the note that rests on it says so.
var docsProbe = []Doc{
	{
		URL:     "https://istio.io/latest/docs/ambient/usage/networkpolicy/",
		Heading: "Ambient, health probes, and Kubernetes NetworkPolicy",
		Quote:   "Various CNI implementations solve this in different ways and seek to either work around the problem by silently excluding kubelet health probes from normal policy enforcement, or configuring policy exceptions for them.",
	},
	{
		URL:     "https://istio.io/latest/docs/ambient/usage/networkpolicy/",
		Heading: "Ambient, health probes, and Kubernetes NetworkPolicy",
		Quote:   "Note: If your workload, namespace, or cluster enforces Kubernetes NetworkPolicy, you must allow both the IPv4 and IPv6 addresses used by ambient mode. Depending on your CNI, packets with these addresses may otherwise be blocked, which will cause application pod health probes to fail once they join the ambient mesh.",
	},
	{
		URL:     "https://istio.io/latest/docs/ambient/usage/networkpolicy/",
		Heading: "Ambient, health probes, and Kubernetes NetworkPolicy",
		Quote:   "Depending on your CNI, kubelet probes and link-local addresses may be ignored by this policy, or be blocked by it:",
	},
}

var docsProtocol = []Doc{
	{
		URL:     "https://istio.io/latest/docs/ops/deployment/application-requirements/",
		Heading: "Server First Protocols",
		Quote:   "Both of these features work by inspecting the initial bytes of a connection to determine the protocol, which is incompatible with server first protocols. In order to support these cases, follow the Explicit protocol selection steps to declare the protocol of the application as TCP.",
	},
	{
		URL:     "https://istio.io/latest/docs/ops/deployment/application-requirements/",
		Heading: "Server First Protocols",
		Quote:   "The following ports are known to commonly carry server first protocols, and are automatically assumed to be TCP:",
	},
	{
		URL:     "https://istio.io/latest/docs/ops/deployment/application-requirements/",
		Heading: "Pod requirements",
		Quote:   "If a pod belongs to multiple Kubernetes services, the services cannot use the same port number for different protocols, for instance HTTP and TCP.",
	},
	{
		URL:     "https://istio.io/latest/docs/ops/configuration/traffic-management/protocol-selection/",
		Heading: "Explicit protocol selection",
		Quote:   "If both are defined, appProtocol takes precedence over the port name.",
	},
}

var docsUID = []Doc{
	{
		URL:     "https://istio.io/latest/docs/ops/deployment/application-requirements/",
		Heading: "Pod requirements",
		Quote:   "Application UIDs: Ensure your pods do not run applications as a user with the user ID (UID) value of 1337 because 1337 is reserved for the sidecar proxy.",
	},
}

var docsReservedPort = []Doc{
	{
		URL:     "https://istio.io/latest/docs/ops/deployment/application-requirements/",
		Heading: "Ports used by Istio",
		Quote:   "To avoid port conflicts with sidecars, applications should not use any of the ports used by Envoy.",
	},
	{
		URL:     "https://istio.io/latest/docs/ambient/architecture/traffic-redirection/",
		Heading: "Istio's in-pod traffic redirection model",
		Quote:   "it should establish local proxy listening ports inside the pod's network namespace (on ports 15008, 15006, and 15001)",
	},
}

var docsStartup = []Doc{
	{
		URL:     "https://istio.io/latest/docs/ops/common-problems/injection/",
		Heading: "Pod or containers start with network issues if istio-proxy is not ready",
		Quote:   "Many applications execute commands or checks during startup, which require network connectivity. This can cause application containers to hang or restart if the istio-proxy sidecar container is not ready.",
	},
	{
		URL:     "https://istio.io/latest/blog/2023/native-sidecars/",
		Heading: "Sidecar woes",
		Quote:   "InitContainers, which run before Istio's container starts, cannot access the network.",
	},
	{
		URL:     "https://istio.io/latest/docs/reference/config/annotations/",
		Heading: "sidecar.istio.io/nativeSidecar",
		Quote:   "Specifies if the istio-proxy sidecar should be injected as a native sidecar or not. Takes precedence over the ENABLE_NATIVE_SIDECARS environment variable.",
	},
}

var docsJob = []Doc{
	{
		URL:     "https://istio.io/latest/blog/2023/native-sidecars/",
		Heading: "Sidecar woes",
		Quote:   "If an application container intentionally exits (typically from usage in a Job), Istio's container will still run and keep the pod running indefinitely.",
	},
	{
		URL:     "https://istio.io/latest/docs/reference/config/annotations/",
		Heading: "sidecar.istio.io/nativeSidecar",
		Quote:   "Specifies if the istio-proxy sidecar should be injected as a native sidecar or not. Takes precedence over the ENABLE_NATIVE_SIDECARS environment variable.",
	},
}

var docsL7 = []Doc{
	{
		URL:     "https://istio.io/latest/docs/ambient/usage/l7-features/",
		Heading: "Considerations for authorization policies",
		Quote:   "The ztunnel cannot enforce L7 policies. If a policy with rules matching L7 attributes is targeted with a workload selector (rather than attached with a targetRef), such that it is enforced by a ztunnel, it will fail safe by becoming a DENY policy.",
	},
	{
		URL:     "https://istio.io/latest/docs/ambient/usage/l7-features/",
		Heading: "Traffic routing",
		Quote:   "With a waypoint proxy deployed, you can use the following traffic route types:",
	},
	{
		URL:     "https://istio.io/latest/docs/ambient/usage/l4-policy/",
		Heading: "Allowed policy attributes",
		Quote:   "This list of attributes determines whether a policy is considered L4-only:",
	},
}
