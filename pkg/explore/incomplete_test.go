package explore

import (
	"testing"

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

// A denied get is incomplete, with the object present or absent alike.
func TestIncompleteUnreadable(t *testing.T) {
	for _, ref := range []objects.Ref{incGW, incSvc} {
		for _, exists := range []bool{true, false} {
			ix := incIx(incRoute("r", incGW, incSvc))
			ix.Findings = map[objects.Ref]Worst{}
			if exists {
				ix.Findings[ref] = Worst{}
			}
			if !incomplete(t, ix, "r", perms{noGet: map[objects.Ref]bool{ref: true}}) {
				t.Errorf("denied get on %v (exists=%v) is complete", ref, exists)
			}
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
	ix.ParentRoutes[routeRef("shop", "child")] = []objects.Ref{routeRef("shop", "parent")}
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
	ix.ParentRoutes[routeRef("shop", "child")] = []objects.Ref{routeRef("shop", "parent")}
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

// Creating, deleting and faulting hidden targets never changes the answer.
func TestIncompleteIgnoresHiddenObjects(t *testing.T) {
	p := perms{noGet: map[objects.Ref]bool{incSvc: true}}
	base := incIx(incRoute("r", incGW, incSvc))
	want := incomplete(t, base, "r", p)
	faulted := incIx(incRoute("r", incGW, incSvc))
	faulted.Findings = map[objects.Ref]Worst{incSvc: {}, incGW: {}}
	r := &faulted.Routes[0]
	r.Parents = []ParentLink{{Target: incGW, Attachment: AttachmentAccepted}}
	r.Flags = FlagRefsUnresolved | FlagNotAccepted
	if got := incomplete(t, faulted, "r", p); got != want {
		t.Errorf("hidden objects changed the answer: %v, want %v", got, want)
	}
	clean := perms{}
	if incomplete(t, base, "r", clean) != incomplete(t, faulted, "r", clean) {
		t.Error("hidden objects changed a complete answer")
	}
}
