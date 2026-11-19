package explore

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// perms is a fake Permissions: everything is allowed except the denied gets
// and lists. It counts asks so a test can see what was consulted.
type perms struct {
	noGet  map[objects.Ref]bool
	noList map[objects.GroupKind]map[string]bool
}

func (p perms) Get(ref objects.Ref) bool { return !p.noGet[ref] }

func (p perms) List(gk objects.GroupKind, ns string) bool { return !p.noList[gk][ns] }

var (
	incGW   = objects.Ref{Group: objects.GatewayAPIGroup, Kind: "Gateway", Namespace: "infra", Name: "edge"}
	incSvc  = objects.Ref{Kind: "Service", Namespace: "shop", Name: "web"}
	gwKind  = objects.GroupKind{Group: objects.GatewayAPIGroup, Kind: "Gateway"}
	sliceGK = objects.GroupKind{Group: "discovery.k8s.io", Kind: "EndpointSlice"}
	routeGK = objects.GroupKind{Group: objects.GatewayAPIGroup, Kind: "HTTPRoute"}
)

// incRoute is an HTTPRoute row that writes a parentRef to gw and a backendRef to svc.
func incRoute(name string, gw, svc objects.Ref) RouteRow {
	r := hr(name, "")
	r.View = &objects.HTTPRouteView{
		ParentRefs: []objects.ParentRef{{Group: gw.Group, Kind: gw.Kind, Namespace: gw.Namespace, Name: gw.Name}},
		Rules:      []objects.Rule{{BackendRefs: []objects.BackendRef{{Ref: svc}}}},
	}
	return r
}

// withParentRoute adds a spec parentRef from r to the HTTPRoute shop/parent.
func withParentRoute(r *RouteRow, parent string) {
	r.View.ParentRefs = append(r.View.ParentRefs, objects.ParentRef{Group: objects.GatewayAPIGroup, Kind: "HTTPRoute", Namespace: "shop", Name: parent})
}

func incIx(rows ...RouteRow) *Index {
	ix := hand(rows...)
	ix.ParentRoutes = map[objects.Ref][]objects.Ref{}
	return ix
}

func incomplete(t *testing.T, ix *Index, name string, p Permissions) bool {
	t.Helper()
	id, _ := row(t, ix, "shop", name)
	return ix.PathIncomplete(id, p)
}

// A readable ref to a missing object, with get allowed, is complete.
func TestIncompleteAbsentIsComplete(t *testing.T) {
	ix := incIx(incRoute("r", incGW, incSvc))
	if incomplete(t, ix, "r", perms{}) {
		t.Error("readable refs to absent objects are incomplete")
	}
}

// A denied get on a written ref is incomplete; TestIncompleteUnreadableExistence
// covers the target present and absent.
func TestIncompleteUnreadable(t *testing.T) {
	for _, ref := range []objects.Ref{incGW, incSvc} {
		ix := incIx(incRoute("r", incGW, incSvc))
		if !incomplete(t, ix, "r", perms{noGet: map[objects.Ref]bool{ref: true}}) {
			t.Errorf("denied get on %v is complete", ref)
		}
	}
}

// A route no parent accepted, with readable refs, is complete.
func TestIncompleteUnattached(t *testing.T) {
	r := incRoute("r", incGW, incSvc)
	r.Parents = []ParentLink{{Target: incGW, Attachment: AttachmentNotAccepted}}
	r.Flags = FlagNoParent
	if incomplete(t, incIx(r), "r", perms{}) {
		t.Error("an unattached route with readable refs is incomplete")
	}
}

// A route with no path rows and no refs is complete.
func TestIncompleteZeroRow(t *testing.T) {
	if incomplete(t, incIx(hr("r", "")), "r", perms{}) {
		t.Error("a route with no refs is incomplete")
	}
}

func TestIncompleteFromListPermission(t *testing.T) {
	ix := incIx(incRoute("r", incGW, incSvc))
	cases := map[string]perms{
		"endpoint slices": {noList: map[objects.GroupKind]map[string]bool{sliceGK: {"shop": true}}},
		"gateways":        {noList: map[objects.GroupKind]map[string]bool{gwKind: {"infra": true}}},
	}
	for name, p := range cases {
		if !incomplete(t, ix, "r", p) {
			t.Errorf("%s not listable but the path is complete", name)
		}
	}
	other := perms{noList: map[objects.GroupKind]map[string]bool{sliceGK: {"elsewhere": true}}}
	if incomplete(t, ix, "r", other) {
		t.Error("a list denial in another namespace made the path incomplete")
	}
}

func TestIncompleteDelegationList(t *testing.T) {
	r := incRoute("r", incGW, incSvc)
	r.View.Rules[0].BackendRefs = []objects.BackendRef{{Delegation: &objects.DelegationRef{Mode: objects.DelegationWildcard, Namespace: "team"}}}
	p := perms{noList: map[objects.GroupKind]map[string]bool{routeGK: {"team": true}}}
	if !incomplete(t, incIx(r), "r", p) {
		t.Error("HTTPRoutes not listable in the delegation namespace but the path is complete")
	}
}

// A readable parent route's unreadable parentRef makes the child incomplete.
func TestIncompleteThroughReadableChain(t *testing.T) {
	hiddenGW := objects.Ref{Group: objects.GatewayAPIGroup, Kind: "Gateway", Namespace: "other", Name: "x"}
	ix := incIx(incRoute("child", incGW, incSvc), incRoute("parent", hiddenGW, incSvc))
	withParentRoute(&ix.Routes[0], "parent")
	if !incomplete(t, ix, "child", perms{noGet: map[objects.Ref]bool{hiddenGW: true}}) {
		t.Error("an unreadable ref in a readable parent route is complete")
	}
	if incomplete(t, ix, "child", perms{}) {
		t.Error("a readable chain is incomplete")
	}
}

// An unreadable parent route contributes its own get failure only.
func TestHiddenChainStops(t *testing.T) {
	hiddenGW := objects.Ref{Group: objects.GatewayAPIGroup, Kind: "Gateway", Namespace: "other", Name: "x"}
	ix := incIx(incRoute("child", incGW, incSvc), incRoute("parent", hiddenGW, incSvc))
	withParentRoute(&ix.Routes[0], "parent")
	asked := map[objects.Ref]bool{}
	p := recorder{perms{noGet: map[objects.Ref]bool{routeRef("shop", "parent"): true}}, asked}
	if !incomplete(t, ix, "child", p) {
		t.Error("an unreadable parent route is complete")
	}
	if asked[hiddenGW] {
		t.Error("the walk read refs of an unreadable parent route")
	}
}

type recorder struct {
	perms
	asked map[objects.Ref]bool
}

func (r recorder) Get(ref objects.Ref) bool {
	r.asked[ref] = true
	return r.perms.Get(ref)
}

// incRule is an HTTPRoute rule with the given backendRefs.
func incRule(refs ...map[string]any) map[string]any {
	bs := make([]any, 0, len(refs))
	for _, r := range refs {
		bs = append(bs, r)
	}
	return map[string]any{"backendRefs": bs}
}

func svcRef(name string) map[string]any { return map[string]any{"name": name, "port": int64(80)} }

// beRef is a backendRef to the kgateway Backend of that name.
func beRef(name string) map[string]any {
	return map[string]any{"group": "gateway.kgateway.dev", "kind": "Backend", "name": name}
}

// withNS sets the namespace of a backendRef.
func withNS(ref map[string]any, ns string) map[string]any {
	ref["namespace"] = ns
	return ref
}

func backendObj(name string) objects.Ref {
	return objects.Ref{Group: "gateway.kgateway.dev", Kind: "Backend", Namespace: "shop", Name: name}
}

// delegatingParent is an HTTPRoute on the edge Gateway that delegates to shop/c.
func delegatingParent(name string) *unstructured.Unstructured {
	return testutil.WithRules(testutil.HTTPRoute("shop", name, []objects.ParentRef{gw}, accepted(gw)), incRule(testutil.DelegateTo("shop", "c")))
}

// hiddenWorld builds shop/c with reported parents p1 (readable) and p2 (hidden)
// in its own status; p2 and the hidden Gateway and Service vary per variant.
func hiddenWorld(variant string) *world {
	p1, p2 := testutil.HTTPRouteRef("shop", "p1"), testutil.HTTPRouteRef("shop", "p2")
	c := testutil.WithRules(testutil.HTTPRoute("shop", "c", []objects.ParentRef{gw, testutil.GwRef("infra", "hidden")}, accepted(gw), accepted(p1), accepted(p2)), incRule(beRef("secret")))
	objs := []*unstructured.Unstructured{c, delegatingParent("p1")}
	switch variant {
	case "p2 absent":
	case "p2 present":
		objs = append(objs, delegatingParent("p2"))
	case "p2 faulted":
		p := delegatingParent("p2")
		p.Object["status"] = map[string]any{"parents": []any{}}
		objs = append(objs, p)
	case "p2 detached":
		objs = append(objs, testutil.WithRules(testutil.HTTPRoute("shop", "p2", nil), incRule(testutil.DelegateTo("shop", "c"))))
	case "hidden gateway and backend":
		objs = append(objs, delegatingParent("p2"), testutil.Gateway("infra", "hidden", "kgateway", nil), testutil.Backend("shop", "secret", nil))
	case "hidden gateway in another class, backend faulted":
		objs = append(objs, testutil.GatewayClass("other", "example.com/other", true), testutil.Gateway("infra", "hidden", "other", nil),
			testutil.Backend("shop", "secret", []metav1.Condition{testutil.Cond("Accepted", metav1.ConditionFalse, "Invalid")}))
	}
	return newWorld(objs...)
}

var hiddenVariants = []string{"p2 absent", "p2 present", "p2 faulted", "p2 detached", "hidden gateway and backend", "hidden gateway in another class, backend faulted"}

// The answer for a readable route never depends on hidden objects: it comes
// from the route and the viewer's permissions, on a production-built index.
func TestIncompleteIgnoresHiddenObjects(t *testing.T) {
	deny := perms{noGet: map[objects.Ref]bool{routeRef("shop", "p2"): true, backendObj("secret"): true, {Group: objects.GatewayAPIGroup, Kind: "Gateway", Namespace: "infra", Name: "hidden"}: true}}
	for _, p := range []Permissions{deny, perms{}} {
		var want bool
		for i, v := range hiddenVariants {
			ix := hiddenWorld(v).build(t)
			got := incomplete(t, ix, "c", p)
			if i == 0 {
				want = got
			}
			if got != want {
				t.Errorf("%s: incomplete %v, but %v with p2 absent", v, got, want)
			}
		}
	}
	// The unreadable parent named in the readable route's status is incomplete.
	if !incomplete(t, hiddenWorld("p2 absent").build(t), "c", deny) {
		t.Error("an unreadable parent route in the route's status is complete")
	}
}

// Whether a readable ref's target exists never changes the answer.
func TestIncompleteUnreadableExistence(t *testing.T) {
	svc := backendObj("web")
	for _, denied := range []bool{true, false} {
		for _, exists := range []bool{true, false} {
			objs := []*unstructured.Unstructured{testutil.WithRules(testutil.HTTPRoute("shop", "r", []objects.ParentRef{gw}, accepted(gw)), incRule(beRef("web")))}
			if exists {
				objs = append(objs, testutil.Backend("shop", "web", nil))
			}
			ix := newWorld(objs...).build(t)
			p := perms{noGet: map[objects.Ref]bool{svc: denied}}
			if got := incomplete(t, ix, "r", p); got != denied {
				t.Errorf("denied=%v exists=%v: incomplete %v", denied, exists, got)
			}
		}
	}
}

// A GRPCRoute is decided from its own object like an HTTPRoute.
func TestIncompleteNonHTTPRoute(t *testing.T) {
	g := testutil.WithRules(testutil.HTTPRoute("shop", "g", []objects.ParentRef{gw}, accepted(gw)), incRule(svcRef("web")))
	g.SetKind("GRPCRoute")
	ix := newWorld(g).build(t)
	if !incomplete(t, ix, "g", perms{noGet: map[objects.Ref]bool{incSvc: true}}) {
		t.Error("a GRPCRoute with an unreadable backend is complete")
	}
	if !incomplete(t, ix, "g", perms{noGet: map[objects.Ref]bool{gwObj: true}}) {
		t.Error("a GRPCRoute with an unreadable Gateway is complete")
	}
	if incomplete(t, ix, "g", perms{}) {
		t.Error("a readable GRPCRoute is incomplete")
	}
}

// A ListenerSet parent needs its Gateway readable and listable too.
func TestIncompleteListenerSetGateway(t *testing.T) {
	ls := objects.ParentRef{Group: objects.GatewayAPIGroup, Kind: "ListenerSet", Namespace: "shop", Name: "ls"}
	ix := newWorld(
		testutil.ListenerSet("shop", "ls", gw, nil),
		testutil.HTTPRoute("shop", "r", []objects.ParentRef{ls}, accepted(ls)),
	).build(t)
	if incomplete(t, ix, "r", perms{}) {
		t.Error("a readable ListenerSet chain is incomplete")
	}
	if !incomplete(t, ix, "r", perms{noGet: map[objects.Ref]bool{gwObj: true}}) {
		t.Error("an unreadable ListenerSet Gateway is complete")
	}
	if !incomplete(t, ix, "r", perms{noList: map[objects.GroupKind]map[string]bool{gwKind: {"infra": true}}}) {
		t.Error("Gateways not listable in the ListenerSet's Gateway namespace but the path is complete")
	}
}

// A readable parent cycle terminates.
func TestIncompleteCycle(t *testing.T) {
	ix := incIx(hr("a", ""), hr("b", ""))
	ix.controllers = []string{testutil.Controller}
	for i, other := range []string{"b", "a"} {
		ix.Routes[i].Obj = testutil.HTTPRoute("shop", ix.Routes[i].Ref.Name, []objects.ParentRef{testutil.HTTPRouteRef("shop", other)})
	}
	if incomplete(t, ix, "a", perms{}) {
		t.Error("a readable cycle is incomplete")
	}
	if !incomplete(t, ix, "a", perms{noGet: map[objects.Ref]bool{routeRef("shop", "b"): true}}) {
		t.Error("an unreadable parent in a cycle is complete")
	}
}

// A named delegation ref needs a get; NoDelegation makes it an ordinary target.
func TestIncompleteDelegationRefs(t *testing.T) {
	r := testutil.WithRules(testutil.HTTPRoute("shop", "r", []objects.ParentRef{gw}, accepted(gw)), incRule(testutil.DelegateTo("team", "kid")))
	w := newWorld(r)
	kid := routeRef("team", "kid")
	if !incomplete(t, w.build(t), "r", perms{noGet: map[objects.Ref]bool{kid: true}}) {
		t.Error("a named delegation ref without get is complete")
	}
	ix := w.build(t)
	ix.noDelegation = true
	if !incomplete(t, ix, "r", perms{noList: map[objects.GroupKind]map[string]bool{sliceGK: {"team": true}}}) {
		t.Error("with NoDelegation an HTTPRoute backendRef needs an EndpointSlice list")
	}
	if incomplete(t, w.build(t), "r", perms{noList: map[objects.GroupKind]map[string]bool{sliceGK: {"team": true}}}) {
		t.Error("a delegation ref needed an EndpointSlice list")
	}
}

// A readable ListenerSet of any served kind outside the install is read from
// its own object: its unreadable Gateway makes the path incomplete whether or
// not that Gateway exists, or which class it is in. The owned case (Gateway
// edge, which the install owns) is covered too.
func TestIncompleteForeignListenerSet(t *testing.T) {
	const xGroup = "gateway.networking.x-k8s.io"
	for _, kind := range []string{"ListenerSet", "XListenerSet"} {
		group := objects.GatewayAPIGroup
		if kind == "XListenerSet" {
			group = xGroup
		}
		ls := objects.ParentRef{Group: group, Kind: kind, Namespace: "shop", Name: "ls"}
		for _, v := range []string{"owned", "absent", "present", "other class"} {
			hid := objects.Ref{Group: objects.GatewayAPIGroup, Kind: "Gateway", Namespace: "infra", Name: "hid"}
			set := testutil.ListenerSet("shop", "ls", testutil.GwRef("infra", "hid"), nil)
			if v == "owned" {
				hid.Name = "edge"
				set = testutil.ListenerSet("shop", "ls", gw, nil)
			}
			if kind == "XListenerSet" {
				set.SetAPIVersion(xGroup + "/v1alpha1")
				set.SetKind(kind)
			}
			route := testutil.HTTPRoute("shop", "r", []objects.ParentRef{gw, ls}, accepted(gw))
			if v == "owned" {
				route = testutil.HTTPRoute("shop", "r", []objects.ParentRef{ls}, accepted(ls))
			}
			objs := []*unstructured.Unstructured{set, route}
			switch v {
			case "present":
				objs = append(objs, testutil.Gateway("infra", "hid", "kgateway", nil))
			case "other class":
				objs = append(objs, testutil.GatewayClass("other", "example.com/other", true), testutil.Gateway("infra", "hid", "other", nil))
			}
			ix := newWorld(objs...).build(t)
			if !incomplete(t, ix, "r", perms{noGet: map[objects.Ref]bool{hid: true}}) {
				t.Errorf("%s, Gateway %s: an unreadable ListenerSet Gateway is complete", kind, v)
			}
			if incomplete(t, ix, "r", perms{}) {
				t.Errorf("%s, Gateway %s: a readable path is incomplete", kind, v)
			}
		}
	}
}

// parentRowCase is a readable parent route "p" that delegates to shop/kid,
// with or without a row: out of scope it has none.
func parentRowCase(t *testing.T, ns string) *Index {
	t.Helper()
	p := testutil.WithRules(testutil.HTTPRoute(ns, "p", []objects.ParentRef{testutil.GwRef("infra", "hid")}), incRule(testutil.DelegateTo("shop", "kid"), withNS(beRef("be"), "shop")))
	r := testutil.HTTPRoute("shop", "r", []objects.ParentRef{gw, testutil.HTTPRouteRef(ns, "p")}, accepted(gw))
	return newWorld(p, r).build(t)
}

// Membership never flips the answer for the same readable parent: a parent
// with a row and one without agree, on delegation and on ordinary targets.
func TestIncompleteParentRowAgreesWithoutRow(t *testing.T) {
	perm := map[string]perms{
		"all allowed":       {},
		"no HTTPRoute list": {noList: map[objects.GroupKind]map[string]bool{routeGK: {"shop": true}}},
		"no slice list":     {noList: map[objects.GroupKind]map[string]bool{sliceGK: {"shop": true}}},
		"no kid get":        {noGet: map[objects.Ref]bool{routeRef("shop", "kid"): true}},
		"no backend get":    {noGet: map[objects.Ref]bool{backendObj("be"): true}},
	}
	with, without := parentRowCase(t, "shop"), parentRowCase(t, "out")
	if _, ok := with.Row(routeRef("shop", "p")); !ok {
		t.Fatal("the in-scope parent has no row")
	}
	if _, ok := without.Row(routeRef("out", "p")); ok {
		t.Fatal("the out-of-scope parent has a row")
	}
	for name, p := range perm {
		if a, b := incomplete(t, with, "r", p), incomplete(t, without, "r", p); a != b {
			t.Errorf("%s: with a row %v, without %v", name, a, b)
		}
	}
	// The unreadable delegation list is what makes the path incomplete.
	if !incomplete(t, without, "r", perm["no HTTPRoute list"]) {
		t.Error("a row-less parent with an unlistable delegation namespace is complete")
	}
}

// A readable parent route with no row is read from its own object.
func TestIncompleteParentRouteWithoutRow(t *testing.T) {
	hid := objects.Ref{Group: objects.GatewayAPIGroup, Kind: "Gateway", Namespace: "infra", Name: "hid"}
	parent := testutil.HTTPRoute("out", "p", []objects.ParentRef{testutil.GwRef("infra", "hid")})
	ix := newWorld(parent, testutil.HTTPRoute("shop", "r", []objects.ParentRef{gw, testutil.HTTPRouteRef("out", "p")}, accepted(gw))).build(t)
	if _, ok := ix.Row(routeRef("out", "p")); ok {
		t.Fatal("the parent route has a row; the test needs one without")
	}
	if !incomplete(t, ix, "r", perms{noGet: map[objects.Ref]bool{hid: true}}) {
		t.Error("an unreadable ref in a row-less parent route is complete")
	}
	if incomplete(t, ix, "r", perms{}) {
		t.Error("a readable row-less parent route is incomplete")
	}
}
