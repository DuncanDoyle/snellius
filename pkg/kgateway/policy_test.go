package kgateway

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/policy"
)

// policyWith is a policy of apiVersion/kind on Gateway infra/gw whose ancestor
// conditions carry reasons PartiallyValid and Merged.
func policyWith(apiVersion, kind string) *unstructured.Unstructured {
	cond := func(typ, reason string) any {
		return map[string]any{"type": typ, "status": "True", "reason": reason, "message": "m", "observedGeneration": int64(1),
			"lastTransitionTime": "2026-11-01T10:00:00Z"}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]any{"name": "p", "namespace": "infra", "generation": int64(1),
			"annotations": map[string]any{PolicyWeightAnnotation: "5"}},
		"spec": map[string]any{"targetRefs": []any{map[string]any{"group": objects.GatewayAPIGroup, "kind": "Gateway", "name": "gw"}},
			"cors": map[string]any{}},
		"status": map[string]any{"ancestors": []any{
			map[string]any{"ancestorRef": map[string]any{"group": objects.GatewayAPIGroup, "kind": "Gateway", "name": "gw", "namespace": "infra"},
				"controllerName": "kgateway.dev/kgateway", "conditions": []any{cond("Accepted", "PartiallyValid"), cond("Attached", "Merged")}},
			map[string]any{"ancestorRef": map[string]any{"group": "gateway.kgateway.dev", "kind": "StatusSummary", "name": "StatusSummary"},
				"controllerName": "kgateway.dev/kgateway", "conditions": []any{cond("Accepted", "TargetNotFound")}},
		}},
	}}
}

// The same reason strings on a core policy and on a downstream policy get no
// kgateway interpretation; on a kgateway policy they do (Phase 8 design §8).
func TestPolicyInterpreterReadsOnlyKgatewayKinds(t *testing.T) {
	interp := PolicyInterpreter()
	for _, c := range []struct {
		apiVersion, kind string
		kgateway         bool
	}{
		{"gateway.kgateway.dev/v1alpha1", "TrafficPolicy", true},
		{"gateway.networking.k8s.io/v1", "BackendTLSPolicy", false},
		{"policy.acme.example/v1", "TrafficPolicy", false},
	} {
		p := policy.Read(policyWith(c.apiVersion, c.kind), interp, policy.Guard{})
		interpreted := p.Interpreted || len(p.Annotations) > 0 || p.Features[0].Label != "" || p.Ancestors[1].Summary ||
			len(p.Ancestors[0].Readings) > 0 || len(p.Ancestors[1].Readings) > 0
		if interpreted != c.kgateway {
			t.Errorf("%s %s: interpreted %v, want %v: %+v", c.apiVersion, c.kind, interpreted, c.kgateway, p)
		}
		if !c.kgateway {
			continue
		}
		rs := p.Ancestors[0].Readings
		want := []policy.Reading{{Type: "Accepted", Warning: true, Message: "Partially valid; some settings were ignored", Source: policy.SourceInferred},
			{Type: "Attached", Note: policy.NoteGatewayWide, Source: policy.SourceInferred}}
		if len(rs) != 2 || rs[0] != want[0] || rs[1] != want[1] {
			t.Errorf("readings %+v, want %+v", rs, want)
		}
		if !p.Ancestors[1].Summary || p.Features[0].Label != "CORS" || p.Annotations[0].Key != PolicyWeightAnnotation {
			t.Errorf("summary, label or annotation missing: %+v", p)
		}
	}
}

// The summary marker matches kgateway's synthetic ancestor only, and the notes follow the ancestor's granularity.
func TestPolicyInterpreterAncestors(t *testing.T) {
	tp := objects.GroupKind{Group: "gateway.kgateway.dev", Kind: "TrafficPolicy"}
	i := PolicyInterpreter()
	for _, a := range []objects.ParentRef{
		{Group: "gateway.networking.k8s.io", Kind: "StatusSummary", Name: "StatusSummary"},
		{Group: "gateway.kgateway.dev", Kind: "StatusSummary", Name: "other"},
		{Group: "gateway.networking.k8s.io", Kind: "Gateway", Name: "StatusSummary"},
	} {
		if i.Summary(tp, a) {
			t.Errorf("%+v marked as the summary ancestor", a)
		}
	}
	merged := func(a objects.ParentRef) string {
		return i.Condition(tp, a, attachedWith("Merged")).Note
	}
	for a, want := range map[objects.ParentRef]string{
		{Group: "gateway.networking.k8s.io", Kind: "Gateway", Name: "g"}:              policy.NoteGatewayWide,
		{Group: "gateway.networking.k8s.io", Kind: "ListenerSet", Name: "l"}:          policy.NoteGatewayWide,
		{Group: "gateway.networking.x-k8s.io", Kind: "XListenerSet", Name: "l"}:       policy.NoteGatewayWide,
		{Group: "", Kind: "Service", Name: "s", SectionName: "http"}:                  policy.NoteBackendPort,
		{Group: "gateway.kgateway.dev", Kind: "Backend", Name: "b"}:                   policy.NoteBackendPort,
		{Group: "example.com", Kind: "Mesh", Name: "m"}:                               "",
		{Group: "gateway.kgateway.dev", Kind: "StatusSummary", Name: "StatusSummary"}: "",
	} {
		if got := merged(a); got != want {
			t.Errorf("%+v: note %q, want %q", a, got, want)
		}
	}
}

// attachedWith is an Attached=True condition with reason.
func attachedWith(reason string) metav1.Condition {
	return metav1.Condition{Type: "Attached", Status: metav1.ConditionTrue, Reason: reason}
}
