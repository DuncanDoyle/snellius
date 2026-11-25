package api

import (
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// linkedRefs are the objects in the JSON body b that carry a link, with that link.
func linkedRefs(t *testing.T, b []byte) []LinkedRef {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	var out []LinkedRef
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if l, ok := x["link"].(string); ok {
				s := func(k string) string { v, _ := x[k].(string); return v }
				out = append(out, LinkedRef{Ref: objects.Ref{Group: s("group"), Kind: s("kind"), Namespace: s("namespace"), Name: s("name")}, Link: Link(l)})
			}
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return out
}

// linkPath is the endpoint path l opens below installBase; "" for none.
func linkPath(installBase string, l LinkedRef) string {
	switch l.Link {
	case LinkNone:
		return ""
	case LinkYAML:
		return strings.Replace(resourcePath(l.Ref), base, installBase, 1)
	case LinkBackend:
		return installBase + backendDetailPath(l.Ref)
	case LinkDetail:
		p, _ := detailPath(l.Ref)
		return installBase + p
	}
	return string(l.Link)
}

// A route in install A that names a policy only install B owns links it to A's YAML,
// never to A's policy detail, which serves members only (Q8.4-2). B links its detail.
func TestReferencedPolicyOfAnotherInstallLinksYAML(t *testing.T) {
	d2 := testutil.ControllerDeployment("kgw2", "kgateway2", img, 1, 1, testutil.Env(install.DiscoveryNamespaceSelectorsEnv, `[{"matchLabels":{"team":"b"}}]`))
	edgeB := testutil.GwRef("infra-b", "edge")
	tpB := testutil.TrafficPolicy("infra-b", "tp-b", testutil.PolicyAncestor{Ref: edgeB, Controller: testutil.Controller,
		Conds: []metav1.Condition{testutil.Cond("Accepted", metav1.ConditionTrue, "Accepted")}})
	tpB.Object["spec"] = map[string]any{"targetRefs": []any{map[string]any{"group": objects.GatewayAPIGroup, "kind": "Gateway", "name": "edge"}}}
	uses := testutil.HTTPRoute("shop", "uses-tp", []objects.ParentRef{edgeRef}, testutil.AcceptedBy(edgeRef, testutil.Controller))
	uses.Object["spec"].(map[string]any)["rules"] = []any{map[string]any{"backendRefs": []any{
		map[string]any{"group": "gateway.kgateway.dev", "kind": "TrafficPolicy", "namespace": "infra-b", "name": "tp-b"}}}}
	objs := isolated(testutil.Namespace("kgw2", nil), testutil.Namespace("infra-b", map[string]string{"team": "b"}), d2,
		testutil.ControllerPod(d2, "kgateway2-0", true), edgeIn("infra-b", 0), testutil.ProxyDeployment("infra-b", "edge", 1, 1), tpB, uses)
	h := newHarness(t, setup{metrics: okMetrics("kgw.kgateway", "kgw2.kgateway2")}, objs...)

	const tpRef = "gateway.kgateway.dev/TrafficPolicy/infra-b/tp-b"
	ref := objects.Ref{Group: "gateway.kgateway.dev", Kind: "TrafficPolicy", Namespace: "infra-b", Name: "tp-b"}
	baseB := "/api/v1/installs/kgw2.kgateway2"
	routeA := base + "/routes/gateway.networking.k8s.io/HTTPRoute/shop/uses-tp"
	if r := get[TrailRefs](t, h.srv, refsQuery(tpRef), 200); len(r.Refs) != 1 || r.Refs[0].Link != LinkYAML {
		t.Errorf("install A refs: %+v, want a YAML link", r.Refs)
	}
	if r := get[TrailRefs](t, h.srv, baseB+"/refs?ref="+tpRef, 200); len(r.Refs) != 1 || r.Refs[0].Link != policyLink("kgw2.kgateway2", ref) {
		t.Errorf("install B refs: %+v, want its policy link", r.Refs)
	}
	if status, _ := raw(t, h.srv, resourcePath(ref)); status != 200 {
		t.Errorf("install A YAML of B's policy: %d, want 200", status)
	}
	checked, inRoute := 0, false
	for path, installBase := range map[string]string{refsQuery(tpRef): base, routeA: base, baseB + "/refs?ref=" + tpRef: baseB, baseB + "/policies?limit=200": baseB} {
		status, b := raw(t, h.srv, path)
		if status != 200 {
			t.Fatalf("%s: %d", path, status)
		}
		for _, l := range linkedRefs(t, b) {
			if path == routeA && l.Ref == ref {
				inRoute = inRoute || l.Link == LinkYAML
				if l.Link != LinkYAML {
					t.Errorf("A's route detail links B's policy %q, want yaml", l.Link)
				}
			}
			p := linkPath(installBase, l)
			if p == "" {
				continue
			}
			if strings.HasPrefix(p, base+"/policies/") {
				t.Errorf("%s links A's policy detail %s", path, p)
			}
			if status, _ := raw(t, h.srv, p); status != 200 {
				t.Errorf("%s links %s (%s), which answers %d", path, p, l.Link, status)
			}
			checked++
		}
	}
	if !inRoute {
		t.Error("A's route detail does not carry B's policy: the test is vacuous")
	}
	if checked == 0 {
		t.Fatal("no link was checked: the test is vacuous")
	}
}
