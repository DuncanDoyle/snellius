package kgateway

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/policy"
)

// Precedence annotation keys kgateway orders policies by (Phase 8 design §5.5).
const (
	PolicyWeightAnnotation            = "kgateway.dev/policy-weight"
	InheritedPolicyPriorityAnnotation = "kgateway.dev/inherited-policy-priority"
)

// summaryName is the kind and name of the synthetic ancestor kgateway writes a
// missing targetRef under (pluginsdk/reporter.PolicyStatusSummaryAncestorName, v2.5).
const summaryName = "StatusSummary"

// PolicyInterpreter returns the kgateway pack's reading of its own policy kinds
// (Phase 8 design §8). It reads nothing about any other group.
func PolicyInterpreter() policy.Interpreter { return interpreter{} }

type interpreter struct{}

// policyKinds are the kgateway kinds in the policy category.
var policyKinds = func() map[objects.GroupKind]bool {
	out := map[objects.GroupKind]bool{}
	for _, d := range Kinds() {
		if d.Category == kinds.CategoryPolicy {
			out[objects.GroupKind{Group: d.Group, Kind: d.Kind}] = true
		}
	}
	return out
}()

func (interpreter) Handles(gk objects.GroupKind) bool { return policyKinds[gk] }

// featureLabels name the spec sections of TrafficPolicy, ListenerPolicy, HTTPListenerPolicy
// and BackendConfigPolicy (api/v1alpha1/kgateway, v2.3 to v2.5). Unlisted keys show as written.
var featureLabels = map[string]string{ //nolint:gosec // spec keys and labels, no credentials
	// TrafficPolicy
	"transformation": "Transformations", "extProc": "External processing", "extAuth": "External auth",
	"rateLimit": "Rate limiting", "cors": "CORS", "csrf": "CSRF", "headerModifiers": "Header modifiers",
	"requestMirror": "Request mirroring", "autoHostRewrite": "Auto host rewrite", "buffer": "Buffering",
	"httpUpgrade": "HTTP upgrades", "timeouts": "Timeouts", "retry": "Retries", "internalRedirect": "Internal redirects",
	"rbac": "RBAC", "jwtAuth": "JWT auth", "urlRewrite": "URL rewrite", "compression": "Compression",
	"basicAuth": "Basic auth", "apiKeyAuth": "API key auth", "oauth2": "OAuth2", "tracing": "Tracing",
	"faultInjection": "Fault injection", "acl": "ACL", "statPrefix": "Stat prefix",
	// ListenerPolicy and HTTPListenerPolicy
	"default": "Listener defaults", "perPort": "Per-port listener settings", "accessLog": "Access log",
	"upgradeConfig": "Upgrades", "acceptHttp10": "Accept HTTP/1.0", "defaultHostForHttp10": "Default host for HTTP/1.0",
	"xffNumTrustedHops": "Trusted XFF hops", "serverHeaderTransformation": "Server header",
	"streamIdleTimeout": "Stream idle timeout", "idleTimeout": "Idle timeout",
	// BackendConfigPolicy
	"connectTimeout": "Connect timeout", "dns": "DNS", "perConnectionBufferLimitBytes": "Connection buffer limit",
	"tcpKeepalive": "TCP keepalive", "commonHttpProtocolOptions": "HTTP protocol options",
	"http1ProtocolOptions": "HTTP/1 options", "http2ProtocolOptions": "HTTP/2 options", "tls": "TLS",
	"loadBalancer": "Load balancing", "healthCheck": "Health checks", "outlierDetection": "Outlier detection",
	"circuitBreakers": "Circuit breakers", "upstreamProxyProtocol": "Upstream PROXY protocol", "tunnel": "Tunnel",
	"sessionPersistence": "Session persistence",
}

// directResponseLabels name DirectResponse's spec fields, whose keys other kinds use differently.
var directResponseLabels = map[string]string{"status": "Status code", "body": "Body", "bodyFormat": "Body format"}

func (i interpreter) FeatureLabel(gk objects.GroupKind, key string) string {
	if !i.Handles(gk) {
		return ""
	}
	if gk.Kind == "DirectResponse" {
		return directResponseLabels[key]
	}
	return featureLabels[key]
}

func (i interpreter) PrecedenceAnnotations(gk objects.GroupKind) []string {
	if !i.Handles(gk) {
		return nil
	}
	return []string{PolicyWeightAnnotation, InheritedPolicyPriorityAnnotation}
}

// Summary matches reporter.IsPolicyStatusSummaryAncestor. The namespace is not compared:
// kgateway writes none and the decoded ref carries the policy's by default.
func (i interpreter) Summary(gk objects.GroupKind, a objects.ParentRef) bool {
	return i.Handles(gk) && a.Group == kinds.KgatewayGroup && a.Kind == summaryName && a.Name == summaryName
}

// Condition reads and words PartiallyValid as a warning and Overridden as info (Phase 8 design §5.2), and
// notes Merged and Overridden by the ancestor's granularity (design §6.3): a whole Gateway or ListenerSet, or one backend port.
func (i interpreter) Condition(gk objects.GroupKind, a objects.ParentRef, c metav1.Condition) policy.Reading {
	if !i.Handles(gk) {
		return policy.Reading{}
	}
	switch c.Reason {
	case "PartiallyValid":
		return policy.Reading{Warning: true, Message: "Partially valid; some settings were ignored"}
	case "Merged", "Overridden":
		r := policy.Reading{Info: c.Reason == "Overridden"}
		if r.Info {
			r.Message = "Overridden by another policy"
		}
		switch {
		case gatewayLike(a):
			r.Note = policy.NoteGatewayWide
		case backendLike(a):
			r.Note = policy.NoteBackendPort
		}
		return r
	}
	return policy.Reading{}
}

// gatewayLike reports whether a is a Gateway or a ListenerSet.
func gatewayLike(a objects.ParentRef) bool {
	switch {
	case a.Group == objects.GatewayAPIGroup && (a.Kind == "Gateway" || a.Kind == "ListenerSet"):
		return true
	case a.Group == "gateway.networking.x-k8s.io" && a.Kind == "XListenerSet":
		return true
	}
	return false
}

// backendLike reports whether a is a Service or a kgateway Backend, the ancestors BackendConfigPolicy reports on.
func backendLike(a objects.ParentRef) bool {
	return a.Group == "" && a.Kind == "Service" || a.Group == kinds.KgatewayGroup && a.Kind == "Backend"
}
