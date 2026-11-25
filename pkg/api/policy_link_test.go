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

// installPaths are the install-scoped paths among the string values of the JSON body b.
func installPaths(t *testing.T, b []byte) []string {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		case string:
			if strings.HasPrefix(x, "/api/v1/installs/") {
				out = append(out, x)
			}
		}
	}
	walk(v)
	return out
}

// A route in install A that names a policy only install B owns gets no link to it:
// A's policy detail would not find it, nor would A's YAML path (Q8.4-2). B links it.
func TestReferencedPolicyOfAnotherInstallHasNoLink(t *testing.T) {
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
	if r := get[TrailRefs](t, h.srv, refsQuery(tpRef), 200); len(r.Refs) != 1 || r.Refs[0].Link != LinkNone {
		t.Errorf("install A refs: %+v, want link none", r.Refs)
	}
	if r := get[TrailRefs](t, h.srv, baseB+"/refs?ref="+tpRef, 200); len(r.Refs) != 1 || r.Refs[0].Link != policyLink("kgw2.kgateway2", ref) {
		t.Errorf("install B refs: %+v, want its policy link", r.Refs)
	}
	if status, _ := raw(t, h.srv, resourcePath(ref)); status != 404 {
		t.Errorf("install A YAML of B's policy: %d, want 404", status)
	}
	checked := 0
	for _, path := range []string{refsQuery(tpRef), base + "/routes/gateway.networking.k8s.io/HTTPRoute/shop/uses-tp",
		baseB + "/refs?ref=" + tpRef, baseB + "/policies?limit=200"} {
		status, b := raw(t, h.srv, path)
		if status != 200 {
			t.Fatalf("%s: %d", path, status)
		}
		for _, p := range installPaths(t, b) {
			if strings.Contains(p, "/policies/") && strings.HasPrefix(p, base+"/") {
				t.Errorf("%s links A's policy detail %s", path, p)
			}
			if status, _ := raw(t, h.srv, p); status != 200 {
				t.Errorf("%s links %s, which answers %d", path, p, status)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no link was checked: the test is vacuous")
	}
}
