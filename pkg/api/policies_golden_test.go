package api

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/DuncanDoyle/snellius/pkg/kgateway"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/policy"
)

// TestGoldenPolicies pins the Phase 8 policies list and detail bodies.
func TestGoldenPolicies(t *testing.T) { runGolden(t, policiesCases(t)) }

// policiesCases are a TrafficPolicy on shop/cart reported on infra/edge, with
// another controller's stale ancestor and a missing target, and a list-only
// DirectResponse; and the subject relations of edge and cart.
func policiesCases(t *testing.T) []goldenCase {
	cond := testutil.Cond
	stale := cond("Accepted", metav1.ConditionTrue, "Accepted")
	stale.ObservedGeneration = 0
	p := testutil.TrafficPolicy("shop", "tp",
		testutil.PolicyAncestor{Ref: edgeRef, Controller: testutil.Controller, Conds: []metav1.Condition{cond("Accepted", metav1.ConditionTrue, "Accepted"),
			cond("Attached", metav1.ConditionTrue, "Merged")}},
		testutil.PolicyAncestor{Ref: testutil.GwRef("infra", "foreign"), Controller: "example.com/other", Conds: []metav1.Condition{stale}})
	p.Object["spec"] = map[string]any{"targetRefs": []any{
		map[string]any{"group": objects.GatewayAPIGroup, "kind": "HTTPRoute", "name": "cart"},
		map[string]any{"group": objects.GatewayAPIGroup, "kind": "HTTPRoute", "name": "gone"}},
		"timeouts": map[string]any{"request": "5s"}}
	p.SetAnnotations(map[string]string{kgateway.InheritedPolicyPriorityAnnotation: "ShallowMergePreferParent"})
	interp := WithPolicyInterpreters(func(install.Install) policy.Interpreter { return kgateway.PolicyInterpreter() })
	_, srv, _ := startWith(t, setup{opts: []HandlerOption{interp}}, explorerWorld(p, testutil.DirectResponse("shop", "dr"))...)
	// rel adds a policy on the whole of infra/edge, so cart's detail has a parent relation.
	edge := testutil.TrafficPolicy("infra", "tp-edge")
	edge.Object["spec"] = map[string]any{"targetRefs": []any{map[string]any{"group": objects.GatewayAPIGroup, "kind": "Gateway", "name": "edge"}}}
	_, rel, _ := startWith(t, setup{opts: []HandlerOption{interp}}, explorerWorld(p.DeepCopy(), edge)...)
	const base = "/api/v1/installs/kgw.kgateway"
	return []goldenCase{
		{"policies", srv, base + "/policies"},
		{"policies-filtered", srv, base + "/policies?hasProblems=true&kind=gateway.kgateway.dev/TrafficPolicy&limit=1"},
		{"policy-detail", srv, base + "/policies/gateway.kgateway.dev/TrafficPolicy/shop/tp"},
		{"policy-detail-absent", srv, base + "/policies/gateway.kgateway.dev/TrafficPolicy/shop/none"},
		{"gateway-detail-policies", rel, gwDetail},
		{"route-detail-policies", rel, routeDetail + "shop/cart"},
	}
}
