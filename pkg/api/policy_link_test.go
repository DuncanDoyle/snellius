package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

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

const (
	tpBRef = "gateway.kgateway.dev/TrafficPolicy/infra-b/tp-b"
	routeA = base + "/routes/gateway.networking.k8s.io/HTTPRoute/shop/uses-tp"
)

var tpB = objects.Ref{Group: "gateway.kgateway.dev", Kind: "TrafficPolicy", Namespace: "infra-b", Name: "tp-b"}

// twoInstalls serves install B, which owns tp-b when withPolicy, and a route in
// install A that names tp-b, to callers authorized by authz.
func twoInstalls(t *testing.T, withPolicy bool, authz Authorizer) *harness {
	t.Helper()
	d2 := testutil.ControllerDeployment("kgw2", "kgateway2", img, 1, 1, testutil.Env(install.DiscoveryNamespaceSelectorsEnv, `[{"matchLabels":{"team":"b"}}]`))
	uses := testutil.HTTPRoute("shop", "uses-tp", []objects.ParentRef{edgeRef}, testutil.AcceptedBy(edgeRef, testutil.Controller))
	uses.Object["spec"].(map[string]any)["rules"] = []any{map[string]any{"backendRefs": []any{
		map[string]any{"group": tpB.Group, "kind": tpB.Kind, "namespace": tpB.Namespace, "name": tpB.Name}}}}
	extra := []runtime.Object{testutil.Namespace("kgw2", nil), testutil.Namespace("infra-b", map[string]string{"team": "b"}), d2,
		testutil.ControllerPod(d2, "kgateway2-0", true), edgeIn("infra-b", 0), testutil.ProxyDeployment("infra-b", "edge", 1, 1), uses}
	if withPolicy {
		tp := testutil.TrafficPolicy("infra-b", "tp-b", testutil.PolicyAncestor{Ref: testutil.GwRef("infra-b", "edge"), Controller: testutil.Controller,
			Conds: []metav1.Condition{testutil.Cond("Accepted", metav1.ConditionTrue, "Accepted")}})
		tp.Object["spec"] = map[string]any{"targetRefs": []any{map[string]any{"group": objects.GatewayAPIGroup, "kind": "Gateway", "name": "edge"}}}
		extra = append(extra, tp)
	}
	return newHarness(t, setup{metrics: okMetrics("kgw.kgateway", "kgw2.kgateway2"), authz: authz}, isolated(extra...)...)
}

// denyNamedRef allows every member and every list but refuses one name by name,
// as a downstream authorizer narrowing SAR can (D-119).
type denyNamedRef struct{ name string }

func (denyNamedRef) Authorize(*http.Request, objects.Ref) bool { return true }

func (d denyNamedRef) AuthorizeNamedErr(_ *http.Request, ref objects.Ref) (bool, error) {
	return ref.Name != d.name, nil
}

func (denyNamedRef) CanList(*http.Request, objects.GroupKind, string) (bool, error) { return true, nil }

// Another install's policy that the caller may not get by name links nowhere, as an
// absent one does: its YAML needs named get like every YAML link (8.4 review, new 1).
func TestReferencedPolicyOfAnotherInstallDeniedByName(t *testing.T) {
	deny := denyNamedRef{name: tpB.Name}
	denied, absent := twoInstalls(t, true, deny), twoInstalls(t, false, deny)
	_, db := raw(t, denied.srv, refsQuery(tpBRef))
	_, ab := raw(t, absent.srv, refsQuery(tpBRef))
	if string(db) != string(ab) {
		t.Errorf("refs: named-denied %s, absent %s", db, ab)
	}
	for name, h := range map[string]*harness{"named-denied": denied, "absent": absent} {
		if r := get[TrailRefs](t, h.srv, refsQuery(tpBRef), 200); len(r.Refs) != 1 || r.Refs[0].Link != LinkNone {
			t.Errorf("%s refs: %+v, want none", name, r.Refs)
		}
		status, b := raw(t, h.srv, routeA)
		if status != 200 {
			t.Fatalf("%s route: %d", name, status)
		}
		for _, l := range linkedRefs(t, b) {
			if l.Ref == tpB && l.Link != LinkNone {
				t.Errorf("%s route links tp-b %q, want none", name, l.Link)
			}
		}
	}
	if status, _ := raw(t, denied.srv, resourcePath(tpB)); status != 404 {
		t.Errorf("named-denied YAML: %d, want 404", status)
	}
}

// A route in install A that names a policy only install B owns links it to A's YAML,
// never to A's policy detail, which serves members only (Q8.4-2). B links its detail.
func TestReferencedPolicyOfAnotherInstallLinksYAML(t *testing.T) {
	h := twoInstalls(t, true, nil)
	const tpRef = tpBRef
	ref := tpB
	baseB := "/api/v1/installs/kgw2.kgateway2"
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
