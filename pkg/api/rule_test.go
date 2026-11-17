package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/internal/testutil/scale"
	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/app"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/auth/sar"
	"github.com/DuncanDoyle/snellius/pkg/cache"
	"github.com/DuncanDoyle/snellius/pkg/collect"
	"github.com/DuncanDoyle/snellius/pkg/computed"
	"github.com/DuncanDoyle/snellius/pkg/ctrllogs"
	"github.com/DuncanDoyle/snellius/pkg/ctrlmetrics"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/platform"
	"github.com/DuncanDoyle/snellius/pkg/scopeverify"
)

// The D-085 rule harness (design §13): a change to objects a restricted viewer
// may not read never changes what that viewer is served, apart from the install
// context changes TestRuleInstallContextMayChange names.

// ruleTier has twelve namespaces with a Gateway and a Backend each.
var ruleTier = func() scale.Tier {
	t := scale.Typical
	t.Name, t.Gateways, t.HTTPRoutes, t.ListenerSets, t.Namespaces, t.Backends = "rule", 12, 72, 2, 12, 12
	return t
}()

const (
	ruleInstall = "kgw.kgateway"
	ruleBase    = "/api/v1/installs/" + ruleInstall
	// The two viewers: full may list everything, restricted the first ten scale namespaces.
	fullUser       = "full"
	restrictedUser = "restricted"
	routeReader    = "route-reader"
	// nsReader is restricted plus the Namespaces, so a root in ns-000 is readable.
	nsReader = "ns-reader"
	// nobodyUser may read nothing; metaUser+"dcn" reads the controller Deployment, GatewayClasses and Namespaces.
	nobodyUser = "nobody"
	metaUser   = "meta-"
)

var (
	// hiddenNS is where every rule change happens; the controller's kgw is hidden too.
	hiddenNS = testutil.ScaleNamespace(11)
	// newNS holds nothing until the newNamespace change; the restricted viewer cannot read it.
	newNS = "ns-new"
	// ns0 is a namespace the restricted viewer reads.
	ns0 = testutil.ScaleNamespace(0)
	// ruleCommit is the commit clock.
	ruleCommit = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	// ruleSteps are the publication times: a world is computed at each, so
	// objects present from the first are past grace and a stuck count matures.
	ruleSteps = []time.Duration{-10 * time.Minute, -6 * time.Minute, 0}
)

// ruleSnap is a snapshot a change edits.
type ruleSnap struct {
	t *testing.T
	*cache.Snapshot
}

// add adds u under its served kind.
func (s ruleSnap) add(us ...*unstructured.Unstructured) {
	s.t.Helper()
	for _, u := range us {
		gk := u.GroupVersionKind().GroupKind()
		i := slices.IndexFunc(testutil.ServedUpstream(), func(k kinds.Served) bool { return k.Group == gk.Group && k.Kind == gk.Kind })
		if i < 0 {
			s.t.Fatalf("unserved kind %s", gk)
		}
		gvr := testutil.ServedUpstream()[i].GVR()
		s.Objects[gvr] = append(s.Objects[gvr], u)
	}
}

// get returns the stored kind ns/name; the test fails when it is absent.
func (s ruleSnap) get(kind, ns, name string) *unstructured.Unstructured {
	s.t.Helper()
	for _, objs := range s.Objects {
		for _, u := range objs {
			if u.GetKind() == kind && u.GetNamespace() == ns && u.GetName() == name {
				return u
			}
		}
	}
	s.t.Fatalf("no %s %s/%s", kind, ns, name)
	return nil
}

// remove drops kind ns/name.
func (s ruleSnap) remove(kind, ns, name string) {
	s.t.Helper()
	u := s.get(kind, ns, name)
	for gvr, objs := range s.Objects {
		if i := slices.Index(objs, u); i >= 0 {
			s.Objects[gvr] = slices.Delete(objs, i, i+1)
		}
	}
}

// removeDeployment drops the Deployment ns/name.
func (s ruleSnap) removeDeployment(ns, name string) {
	s.Deployments = slices.DeleteFunc(s.Deployments, func(d *appsv1.Deployment) bool { return d.Namespace == ns && d.Name == name })
}

// deployment returns the Deployment ns/name.
func (s ruleSnap) deployment(ns, name string) *appsv1.Deployment {
	s.t.Helper()
	for _, d := range s.Deployments {
		if d.Namespace == ns && d.Name == name {
			return d
		}
	}
	s.t.Fatalf("no Deployment %s/%s", ns, name)
	return nil
}

// routeTo is an HTTPRoute ns/name on parents, accepted by each, sending to the
// Backend beNS/be ("" sends nowhere). status false leaves it without status.
func routeTo(ns, name string, parents []objects.ParentRef, beNS, be string, status bool) *unstructured.Unstructured {
	var st []testutil.RouteParent
	if status {
		for _, p := range parents {
			st = append(st, testutil.AcceptedBy(p, testutil.Controller))
		}
	}
	u := testutil.HTTPRoute(ns, name, parents, st...)
	if be != "" {
		ref := map[string]any{"group": kinds.KgatewayGroup, "kind": "Backend", "name": be}
		if beNS != ns {
			ref["namespace"] = beNS
		}
		u.Object["spec"].(map[string]any)["rules"] = []any{map[string]any{"backendRefs": []any{ref}}}
	}
	if !status {
		delete(u.Object, "status")
	}
	return u
}

// hiddenGW is the parentRef to a Gateway of the hidden namespace.
func hiddenGW(name string) []objects.ParentRef {
	return []objects.ParentRef{testutil.GwRef(hiddenNS, name)}
}

// ruleSnapshot is the base world: ruleTier, plus a hidden Gateway with its
// proxy, a hidden Backend and a hidden route between them, all healthy; a
// hidden route to a hidden Service; a visible route to a visible Service; a
// hidden parent route delegating to a visible child; a visible TrafficPolicy
// selecting routes by label; a visible Backend that is not accepted; visible
// routes naming hidden-gw, an absent hidden Gateway and a hidden child; and
// the Service, EndpointSlice, Event and CRD watches on.
func ruleSnapshot(t *testing.T) ruleSnap {
	s := ruleSnap{t, scale.Snapshot(ruleTier, 1)}
	s.add(testutil.Gateway(hiddenNS, "hidden-gw", scale.Class, nil), testutil.Backend(hiddenNS, "hidden-be", nil),
		routeTo(hiddenNS, "hidden-route", hiddenGW("hidden-gw"), hiddenNS, "hidden-be", true),
		serviceRoute(hiddenNS, "hidden-svc-route", hiddenGW("hidden-gw"), hiddenNS, "hidden-svc"),
		serviceRoute(ns0, "svc-consumer", gw0(), ns0, "shared"),
		failRefs(serviceRoute(ns0, "absent-consumer", gw0(), ns0, "absent-svc")),
		delegating(hiddenNS, "hidden-parent", hiddenGW("hidden-gw"), ns0, "child"),
		child(ns0, "child", objects.ParentRef{Group: objects.GatewayAPIGroup, Kind: "HTTPRoute", Namespace: hiddenNS, Name: "hidden-parent"}),
		selectorPolicy(ns0, "selector-policy"),
		testutil.Backend(ns0, "rejected-be", []metav1.Condition{testutil.Cond("Accepted", metav1.ConditionFalse, "Invalid")}),
		routeTo(ns0, namesHidden, append(gw0(), hiddenGW("hidden-gw")...), ns0, "be-0000", true),
		routeTo(ns0, namesAbsent, hiddenGW("absent-gw"), ns0, "be-0000", false),
		delegating(ns0, delegToHidden, gw0(), hiddenNS, "hidden-child"))
	// hidden-gw's proxy is not ready, so its 006 can carry the controller's deployer errors.
	s.Deployments = append(s.Deployments, testutil.ProxyDeployment(hiddenNS, "hidden-gw", 1, 0))
	// ls-all is a visible ListenerSet on gw0 whose listener admits every namespace.
	lsAll := testutil.ListenerSet(ns0, lsAllName, gw0()[0], nil)
	lsAll.Object["spec"].(map[string]any)["listeners"].([]any)[0].(map[string]any)["allowedRoutes"] = map[string]any{"namespaces": map[string]any{"from": "All"}}
	s.add(lsAll)
	// hidden-spare is a hidden Gateway no readable object depends on; its proxy is not ready either.
	s.add(testutil.Gateway(hiddenNS, "hidden-spare", scale.Class, nil))
	s.Deployments = append(s.Deployments, testutil.ProxyDeployment(hiddenNS, "hidden-spare", 1, 0))
	s.Services = append(s.Services, service(hiddenNS, "hidden-svc"), service(ns0, "shared"), service(kindLimitedNS, limitedSvc))
	s.EndpointSlices = append(s.EndpointSlices, slice(hiddenNS, "hidden-svc", 2), slice(ns0, "shared", 2), slice(kindLimitedNS, limitedSvc, 2))
	// limited-svc is a visible Service whose EndpointSlices the restricted viewer may not list.
	ok := []metav1.Condition{testutil.Cond("Accepted", metav1.ConditionTrue, "Accepted"), testutil.Cond("Programmed", metav1.ConditionTrue, "Programmed")}
	s.add(testutil.Gateway(kindLimitedNS, limitedSvc+"-gw", scale.Class, nil, testutil.Listener{Name: "http", Attached: 1, Conds: ok}),
		serviceRoute(kindLimitedNS, limitedSvc+"-route", []objects.ParentRef{testutil.GwRef(kindLimitedNS, limitedSvc+"-gw")}, kindLimitedNS, limitedSvc))
	s.Deployments = append(s.Deployments, testutil.ProxyDeployment(kindLimitedNS, limitedSvc+"-gw", 1, 1))
	for _, g := range []schema.GroupVersionResource{cache.ServicesGVR, cache.EndpointSlicesGVR, cache.EventsGVR, cache.CRDsGVR} {
		s.Access[g] = cache.Access{OK: true}
	}
	for _, n := range []string{"gateways", "gatewayclasses", "httproutes"} {
		s.CRDs = append(s.CRDs, cache.CRDMeta{Name: n + "." + objects.GatewayAPIGroup, Group: objects.GatewayAPIGroup, BundleVersion: "v1.4.0", Channel: "standard"})
	}
	return s
}

// Visible routes of the base world whose neighbours are hidden: one on gw0
// and hidden-gw, one naming an absent hidden Gateway (admitted only by its
// finding), and one delegating to a hidden child.
const (
	lsAllName     = "ls-all"
	namesHidden   = "names-hidden"
	namesAbsent   = "names-absent"
	delegToHidden = "deleg-to-hidden"
)

// lsAllRef is the parentRef to ls-all.
var lsAllRef = objects.ParentRef{Group: objects.GatewayAPIGroup, Kind: "ListenerSet", Namespace: ns0, Name: lsAllName}

// gw0 is the parentRef to the Gateway of ns-000.
func gw0() []objects.ParentRef {
	return []objects.ParentRef{testutil.GwRef(ns0, testutil.ScaleGateway(0))}
}

// service is a ClusterIP Service ns/name with port 80.
func service(ns, name string) cache.ServiceFact {
	return cache.ServiceFact{Namespace: ns, Name: name, Type: "ClusterIP", Ports: []cache.ServicePort{{Port: 80, Target: 8080, Protocol: "TCP"}}}
}

// slice is an EndpointSlice of Service ns/svc with ready of two endpoints ready.
func slice(ns, svc string, ready int) cache.SliceFact {
	return cache.SliceFact{Namespace: ns, Service: svc, Ports: []cache.SlicePort{{Port: 8080}}, Ready: []int{ready}, Total: 2, AddressType: "IPv4"}
}

// serviceRoute is an accepted HTTPRoute ns/name on parents sending to Service svcNS/svc port 80.
func serviceRoute(ns, name string, parents []objects.ParentRef, svcNS, svc string) *unstructured.Unstructured {
	u := routeTo(ns, name, parents, "", "", true)
	ref := map[string]any{"name": svc, "port": int64(80)}
	if svcNS != ns {
		ref["namespace"] = svcNS
	}
	u.Object["spec"].(map[string]any)["rules"] = []any{map[string]any{"backendRefs": []any{ref}}}
	return u
}

// delegating is an accepted HTTPRoute ns/name on parents that delegates /name to the HTTPRoute childNS/childName.
func delegating(ns, name string, parents []objects.ParentRef, childNS, childName string) *unstructured.Unstructured {
	u := routeTo(ns, name, parents, "", "", true)
	u.Object["spec"].(map[string]any)["rules"] = []any{map[string]any{
		"matches":     []any{map[string]any{"path": map[string]any{"type": "PathPrefix", "value": "/" + name}}},
		"backendRefs": []any{map[string]any{"group": objects.GatewayAPIGroup, "kind": "HTTPRoute", "name": childName, "namespace": childNS}},
	}}
	return u
}

// child is a delegated HTTPRoute ns/name serving /<parent>/name, accepted under each of parents.
func child(ns, name string, parents ...objects.ParentRef) *unstructured.Unstructured {
	var st []testutil.RouteParent
	var matches []any
	for _, p := range parents {
		st = append(st, testutil.AcceptedBy(p, testutil.Controller))
		matches = append(matches, map[string]any{"path": map[string]any{"type": "PathPrefix", "value": "/" + p.Name + "/" + name}})
	}
	u := testutil.HTTPRoute(ns, name, nil, st...)
	u.Object["spec"].(map[string]any)["rules"] = []any{map[string]any{"matches": matches,
		"backendRefs": []any{map[string]any{"group": kinds.KgatewayGroup, "kind": "Backend", "name": "be-0000"}}}}
	return u
}

// hop is an HTTPRoute ns/name delegated from parent under match, delegating
// match to the HTTPRoute childNS/childName.
func hop(ns, name string, parent objects.ParentRef, match, childNS, childName string) *unstructured.Unstructured {
	u := testutil.HTTPRoute(ns, name, nil, testutil.AcceptedBy(parent, testutil.Controller))
	u.Object["spec"].(map[string]any)["rules"] = []any{map[string]any{
		"matches":     []any{map[string]any{"path": map[string]any{"type": "PathPrefix", "value": match}}},
		"backendRefs": []any{map[string]any{"group": objects.GatewayAPIGroup, "kind": "HTTPRoute", "name": childName, "namespace": childNS}},
	}}
	return u
}

// The readable chain through a hidden route, which hopPairs adds: chainRoot
// on gw0 delegates to hidden-hop, which delegates to chainMid, then chainLeaf.
const (
	chainRoot = "chain-root"
	chainMid  = "chain-mid"
	chainLeaf = "chain-leaf"
	hiddenHop = "hidden-hop"
)

// chainMatch is the path prefix of the chain route named by steps.
func chainMatch(steps ...string) string { return "/" + strings.Join(steps, "/") }

// chainRoutes are the routes of the chain through hidden-hop.
func chainRoutes() []*unstructured.Unstructured {
	route := func(ns, name string) objects.ParentRef { return testutil.HTTPRouteRef(ns, name) }
	// Listener l0 admits every namespace, so no Namespace object decides the root.
	l0 := gw0()[0]
	l0.SectionName = "l0"
	root := delegating(ns0, chainRoot, []objects.ParentRef{l0}, hiddenNS, hiddenHop)
	leaf := child(ns0, chainLeaf, route(ns0, chainMid))
	// child matches /<parent>/<name>; the leaf's match spells the whole chain.
	leaf.Object["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["matches"] = []any{
		map[string]any{"path": map[string]any{"type": "PathPrefix", "value": chainMatch(chainRoot, hiddenHop, chainMid, chainLeaf)}}}
	return []*unstructured.Unstructured{root,
		hop(hiddenNS, hiddenHop, route(ns0, chainRoot), chainMatch(chainRoot, hiddenHop), ns0, chainMid),
		hop(ns0, chainMid, route(hiddenNS, hiddenHop), chainMatch(chainRoot, hiddenHop, chainMid), ns0, chainLeaf), leaf}
}

// setHopMatch sets hidden-hop's one match to the path prefix p.
func setHopMatch(s ruleSnap, p string) {
	rule := s.get("HTTPRoute", hiddenNS, hiddenHop).Object["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)
	rule["matches"] = []any{map[string]any{"path": map[string]any{"type": "PathPrefix", "value": p}}}
}

// selectorPolicy is a TrafficPolicy ns/name selecting the HTTPRoutes labelled app=selected.
func selectorPolicy(ns, name string) *unstructured.Unstructured {
	u := testutil.TrafficPolicy(ns, name)
	u.Object["spec"] = map[string]any{"targetSelectors": []any{map[string]any{"group": objects.GatewayAPIGroup, "kind": "HTTPRoute",
		"matchLabels": map[string]any{"app": "selected"}}}}
	return u
}

// listenerPolicy is a ListenerPolicy ns/name targeting Gateway ns/gw, Accepted as accepted says on it.
func listenerPolicy(ns, name, gw string, accepted metav1.ConditionStatus) *unstructured.Unstructured {
	u := testutil.Policy(kinds.KgatewayGroup+"/v1alpha1", "ListenerPolicy", ns, name, testutil.PolicyAncestor{Ref: testutil.GwRef(ns, gw), Controller: testutil.Controller,
		Conds: []metav1.Condition{testutil.Cond("Accepted", accepted, "Invalid")}})
	u.Object["spec"] = map[string]any{"targetRefs": []any{map[string]any{"group": objects.GatewayAPIGroup, "kind": "Gateway", "name": gw}}}
	return u
}

// ruleChange is one change to the world. apply runs before the first
// publication, late before the last; rejects are the Gateways the controller
// metrics report rejecting.
type ruleChange struct {
	name    string
	apply   func(s ruleSnap)
	late    func(s ruleSnap)
	rejects []ctrlmetrics.GatewayKey
	// logs, when set, turns the controller log reading on with these events.
	logs []ctrllogs.Event
}

// stuck adds n hidden routes on hidden-gw whose status is for an older generation.
func stuck(s ruleSnap, n int) {
	for i := range n {
		u := routeTo(hiddenNS, fmt.Sprintf("hidden-stuck-%02d", i), hiddenGW("hidden-gw"), hiddenNS, "hidden-be", true)
		u.SetGeneration(2)
		s.add(u)
	}
}

// failRefs makes every status.parents entry of route u report ResolvedRefs=False, and returns u.
func failRefs(u *unstructured.Unstructured) *unstructured.Unstructured {
	setParents(u, "ResolvedRefs", "BackendNotFound")
	return u
}

// setParents sets condition typ of every status.parents entry of route u to False with reason.
func setParents(u *unstructured.Unstructured, typ, reason string) {
	ps, _, _ := unstructured.NestedSlice(u.Object, "status", "parents")
	for _, p := range ps {
		for _, c := range p.(map[string]any)["conditions"].([]any) {
			if c := c.(map[string]any); c["type"] == typ {
				c["status"], c["reason"] = "False", reason
			}
		}
	}
	_ = unstructured.SetNestedSlice(u.Object, ps, "status", "parents")
}

// failFirstParent makes the first status.parents entry of route u not Accepted.
func failFirstParent(u *unstructured.Unstructured) {
	ps, _, _ := unstructured.NestedSlice(u.Object, "status", "parents")
	c := ps[0].(map[string]any)["conditions"].([]any)[0].(map[string]any)
	c["status"], c["reason"] = "False", "NotAllowedByListeners"
	_ = unstructured.SetNestedSlice(u.Object, ps, "status", "parents")
}

// setCondition sets u's top-level condition typ to status.
func setCondition(u *unstructured.Unstructured, typ string, status metav1.ConditionStatus) {
	cs, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for i, c := range cs {
		if m := c.(map[string]any); m["type"] == typ {
			m["status"], m["reason"] = string(status), "Invalid"
			cs[i] = m
		}
	}
	_ = unstructured.SetNestedSlice(u.Object, cs, "status", "conditions")
}

// ruleChanges are the hidden changes no restricted response may show (design §13).
var ruleChanges = []ruleChange{
	// A faulty hidden route in a namespace that held nothing before (Task 37: /namespaces).
	{name: "newNamespace", apply: func(s ruleSnap) {
		s.Namespaces = append(s.Namespaces, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: newNS}})
		s.add(failRefs(routeTo(newNS, "new-ns-route", hiddenGW("hidden-gw"), newNS, "new-ns-missing", true)))
	}},
	{name: "created", apply: func(s ruleSnap) {
		s.add(testutil.Gateway(hiddenNS, "hidden-new-gw", scale.Class, nil), testutil.Backend(hiddenNS, "hidden-new-be", nil),
			routeTo(hiddenNS, "hidden-new-route", hiddenGW("hidden-new-gw"), hiddenNS, "hidden-new-be", true),
			// A hidden route naming a visible Backend.
			routeTo(hiddenNS, "hidden-consumer", hiddenGW("hidden-gw"), testutil.ScaleNamespace(0), "be-0000", true))
		s.Deployments = append(s.Deployments, testutil.ProxyDeployment(hiddenNS, "hidden-new-gw", 1, 1))
		s.Services = append(s.Services, service(hiddenNS, "hidden-new-svc"))
		s.EndpointSlices = append(s.EndpointSlices, slice(hiddenNS, "hidden-new-svc", 2))
		s.add(serviceRoute(hiddenNS, "hidden-new-svc-route", hiddenGW("hidden-new-gw"), hiddenNS, "hidden-new-svc"))
	}},
	// hidden-gw goes too: names-hidden keeps its row through gw0, while child, under hidden-parent, leaves the install (ruleMasks).
	{name: "deleted", apply: func(s ruleSnap) {
		s.remove("HTTPRoute", hiddenNS, "hidden-route")
		s.remove("Backend", hiddenNS, "hidden-be")
		s.remove("Gateway", hiddenNS, "hidden-gw")
		s.removeDeployment(hiddenNS, "hidden-gw")
		s.remove("Gateway", hiddenNS, "hidden-spare")
		s.removeDeployment(hiddenNS, "hidden-spare")
	}},
	{name: "faulty", apply: func(s ruleSnap) {
		failFirstParent(s.get("HTTPRoute", hiddenNS, "hidden-route"))
		setCondition(s.get("Backend", hiddenNS, "hidden-be"), "Accepted", metav1.ConditionFalse)
		setCondition(s.get("Gateway", hiddenNS, "hidden-gw"), "Programmed", metav1.ConditionFalse)
		// A hidden Service, and a visible one whose slices the viewer may not list, with no ready endpoints.
		for i := range s.EndpointSlices {
			if es := &s.EndpointSlices[i]; es.Namespace == hiddenNS && es.Service == "hidden-svc" || es.Namespace == kindLimitedNS && es.Service == limitedSvc {
				es.Ready = []int{0}
			}
		}
	}},
	{name: "pending", late: func(s ruleSnap) {
		s.add(routeTo(hiddenNS, "hidden-pending", hiddenGW("hidden-gw"), hiddenNS, "hidden-be", false))
	}},
	{name: "stuckThreshold", apply: func(s ruleSnap) { stuck(s, 12) }},
	{name: "threshold", apply: func(s ruleSnap) {
		stuck(s, 12)
		for i := range 3 {
			name := fmt.Sprintf("hidden-down-%d", i)
			s.add(testutil.Gateway(hiddenNS, name, scale.Class, nil))
			s.Deployments = append(s.Deployments, testutil.ProxyDeployment(hiddenNS, name, 2, 0))
		}
		s.deployment(hiddenNS, "hidden-gw").Status.ReadyReplicas = 0
	}},
	{name: "rejecting", rejects: []ctrlmetrics.GatewayKey{{Namespace: hiddenNS, Name: "hidden-gw"}}},
	{name: "gatewayAdded", apply: func(s ruleSnap) {
		s.add(testutil.Gateway(hiddenNS, "hidden-added", scale.Class, nil))
	}},
	{name: "controllerRestart", apply: func(s ruleSnap) {
		restart := metav1.NewTime(testutil.Now.Add(-time.Minute))
		p := s.Pods[0]
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: p.Spec.Containers[0].Name, Ready: true, RestartCount: 1,
			State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: restart}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 2, Reason: "Error", FinishedAt: restart}}}}
	}},
	// The proxy Deployment of a visible Gateway in kindLimitedNS, which the viewer may not read, loses its ready replicas.
	{name: "proxyReadyDeployment", apply: func(s ruleSnap) {
		s.deployment(kindLimitedNS, testutil.ScaleGateway(9)).Status.ReadyReplicas = 0
	}},
	// Own-object probes: the inputs of visible own-object findings.
	{name: "consumerFirst", apply: func(s ruleSnap) {
		// A hidden route names the absent Service a visible route's 013 is about.
		s.add(failRefs(serviceRoute(hiddenNS, "hidden-absent-consumer", hiddenGW("hidden-gw"), ns0, "absent-svc")))
	}},
	{name: "parentGeneration", apply: func(s ruleSnap) { s.get("HTTPRoute", hiddenNS, "hidden-parent").SetGeneration(2) }},
	// A ListenerPolicy the viewer may not read, not accepted on a visible Gateway.
	{name: "listenerPolicy", apply: func(s ruleSnap) {
		s.add(listenerPolicy(kindLimitedNS, "limited-listeners", testutil.ScaleGateway(9), metav1.ConditionFalse))
	}},
	{name: "selectorTarget", apply: func(s ruleSnap) {
		u := routeTo(hiddenNS, "hidden-selected", hiddenGW("hidden-gw"), hiddenNS, "hidden-be", true)
		u.SetLabels(map[string]string{"app": "selected"})
		s.add(u)
	}},
	// Derived inputs.
	// The controller log reading turns on and reports a deployer error on the hidden Gateway.
	{name: "controller", logs: []ctrllogs.Event{{Time: testutil.Now.Add(-time.Minute), Gateway: objects.Ref{Group: objects.GatewayAPIGroup, Kind: "Gateway",
		Namespace: hiddenNS, Name: "hidden-gw"}, Class: computed.LogClassOther, Object: &objects.Ref{Group: objects.GatewayAPIGroup, Kind: "Gateway", Namespace: hiddenNS, Name: "hidden-gw"}}}},
	{name: "eventsCapped", apply: func(s ruleSnap) { s.EventsCapped = true }},
	// A hidden route attaches to a visible Gateway's listener.
	{name: "listenerCount", apply: func(s ruleSnap) {
		s.add(routeTo(hiddenNS, "hidden-attached", gw0(), hiddenNS, "hidden-be", true))
	}},
	// A second hidden parent appears late, so the visible child's computed window has just started.
	{name: "ancestorWindow", late: func(s ruleSnap) {
		s.add(delegating(hiddenNS, "hidden-parent-2", hiddenGW("hidden-gw"), ns0, "child"))
	}},
	// A second hidden parent delegates to the visible child, which has no status for it.
	{name: "computedTrigger", apply: func(s ruleSnap) {
		s.add(delegating(hiddenNS, "hidden-parent-2", hiddenGW("hidden-gw"), ns0, "child"))
	}},
	{name: "crdBundle", apply: func(s ruleSnap) {
		for i := range s.CRDs[:2] {
			s.CRDs[i].BundleVersion = "v1.3.0"
		}
	}},
	// The only consumer of a readable rejected Backend is hidden.
	{name: "backendConsumer", apply: func(s ruleSnap) {
		s.add(routeTo(hiddenNS, "hidden-rejected-consumer", hiddenGW("hidden-gw"), ns0, "rejected-be", true))
	}},
	// A hidden child of a visible delegating parent appears.
	{name: "hiddenChild", apply: func(s ruleSnap) {
		s.add(child(hiddenNS, "hidden-child", objects.ParentRef{Group: objects.GatewayAPIGroup, Kind: "HTTPRoute", Namespace: ns0, Name: delegToHidden}))
	}},
	// A hidden route attaches to a visible ListenerSet whose listener admits every namespace.
	{name: "listenerSetRoute", apply: func(s ruleSnap) {
		s.add(routeTo(hiddenNS, "hidden-ls-route", []objects.ParentRef{lsAllRef}, hiddenNS, "hidden-be", true))
	}},
	// The extension check fails on a hidden Backend once it is marked.
	{name: "extension", apply: func(s ruleSnap) {
		s.get("Backend", hiddenNS, "hidden-be").SetLabels(map[string]string{extLabel: "fail"})
	}},
}

// hopChanges change only hidden-hop's matcher. The namespace reader is
// the viewer they apply to: every root's view A reads its Namespace.
var hopChanges = []ruleChange{
	// hidden-hop narrows the match it delegates under; chain-mid still fits.
	{name: "hopMatchNarrows", apply: func(s ruleSnap) { setHopMatch(s, chainMatch(chainRoot, hiddenHop, chainMid)) }},
	// hidden-hop moves its match elsewhere, so it drops chain-mid.
	{name: "hopMatchDrops", apply: func(s ruleSnap) { setHopMatch(s, chainMatch(chainRoot, "elsewhere")) }},
}

// extLabel marks the objects extCheck reports.
const extLabel = "example.com/ext"

// extCheck is an extension check: an error finding on each Backend marked
// with extLabel, and a failed result naming it. Every world runs it.
type extCheck struct{}

func (extCheck) Name() string { return "extension check" }

func (extCheck) Inputs() health.InputSet { return health.InputSet{Clock: true} }

func (c extCheck) Run(ctx health.RunContext) ([]health.Finding, health.CheckResult) {
	res := health.CheckResult{Name: c.Name(), OK: true, Required: true, CollectedAt: ctx.Input.Now}
	var out []health.Finding
	for _, objs := range ctx.Input.Snapshot.Objects {
		for _, u := range objs {
			if u.GetLabels()[extLabel] != "" {
				r := objects.RefOf(u)
				out = append(out, health.Finding{Code: "EXT-001", Slug: "ext", Severity: health.SeverityError, Install: ctx.Input.Install.ID(), Resource: &r,
					Message: "extension says " + r.Name})
				res.OK, res.Error = false, "extension failed on "+r.Namespace+"/"+r.Name
			}
		}
	}
	return out, res
}

// ruleInstallOf is the rule install of d's current publication.
func ruleInstallOf(d extension.Deps) *engine.InstallModel {
	m := d.Current().Model
	for k := range m.Installs {
		if m.Installs[k].Resolved.ID() == ruleInstall {
			return &m.Installs[k]
		}
	}
	return nil
}

// extBackend is a row of the rule extension: a Backend and the codes of the findings on it the caller is shown.
type extBackend struct {
	Ref   objects.Ref `json:"ref"`
	Codes []string    `json:"codes"`
}

// viewerAnswers are ViewerOf's answers about the rule install, for TestViewerOfMatchesView.
type viewerAnswers struct {
	Secure, ReadsAll bool
	// Gateways are the Gateways Can admits; Shown the IDs of the findings Shown admits.
	Gateways, Shown []string
	// Named is CanNamed of the namedOnly route; Lists are CanList answers by "kind/namespace".
	Named bool
	Lists map[string]bool
}

// ruleExt serves the install's Backends through Deps.View, as an extension
// serving cluster objects must, and the Viewer's raw answers. /owned-backends
// is the unsafe form, VisibleTo over Install.Owned.Backends, kept as a negative.
func ruleExt(r extension.Router, d extension.Deps) {
	serve := func(backends func(api.Viewer, *engine.InstallModel) []*unstructured.Unstructured) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			v, i := d.View(req), ruleInstallOf(d)
			out := []extBackend{}
			for _, u := range backends(v, i) {
				row := extBackend{Ref: objects.RefOf(u), Codes: []string{}}
				for k := range i.Health.Findings {
					if f := &i.Health.Findings[k]; f.Resource != nil && *f.Resource == row.Ref && v.Shown(f) {
						row.Codes = append(row.Codes, f.Code)
					}
				}
				out = append(out, row)
			}
			api.WriteJSON(w, out)
		})
	}
	r.Handle("GET /backends", serve(func(v api.Viewer, i *engine.InstallModel) []*unstructured.Unstructured { return v.Backends(i) }))
	r.Handle("GET /owned-backends", serve(func(v api.Viewer, i *engine.InstallModel) []*unstructured.Unstructured {
		return api.VisibleTo(v, i.Owned.Backends, objects.RefOf)
	}))
	r.Handle("GET /viewer", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		v, i := d.View(req), ruleInstallOf(d)
		out := viewerAnswers{Secure: v.Secure(), ReadsAll: v.ReadsAll(), Gateways: []string{}, Shown: []string{}, Lists: map[string]bool{}}
		for _, u := range i.Owned.Gateways {
			if ref := objects.RefOf(u); v.Can(ref) {
				out.Gateways = append(out.Gateways, ref.Namespace+"/"+ref.Name)
			}
		}
		for k := range i.Health.Findings {
			if f := &i.Health.Findings[k]; v.Shown(f) {
				out.Shown = append(out.Shown, f.ID)
			}
		}
		out.Named = v.CanNamed(objects.Ref{Group: objects.GatewayAPIGroup, Kind: "HTTPRoute", Namespace: hiddenNS, Name: namedOnly})
		route := objects.GroupKind{Group: objects.GatewayAPIGroup, Kind: "HTTPRoute"}
		for _, ns := range []string{ns0, hiddenNS, ""} {
			out.Lists["HTTPRoute/"+ns] = v.CanList(route, ns)
		}
		api.WriteJSON(w, out)
	}))
}

// holder serves the snapshot a world's current publication reads.
type holder struct {
	mu sync.Mutex
	s  *cache.Snapshot
}

func (h *holder) Snapshot() *cache.Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.s
}

// sortObjects orders each kind and the Services by "namespace/name", as the cache does.
func sortObjects(s *cache.Snapshot) {
	for _, objs := range s.Objects {
		slices.SortFunc(objs, func(x, y *unstructured.Unstructured) int {
			return strings.Compare(x.GetNamespace()+"/"+x.GetName(), y.GetNamespace()+"/"+y.GetName())
		})
	}
	slices.SortFunc(s.Services, func(x, y cache.ServiceFact) int {
		return strings.Compare(x.Namespace+"/"+x.Name, y.Namespace+"/"+y.Name)
	})
}

// ruleWorld computes the base world with c applied, through a fresh engine
// publishing at each of ruleSteps, and returns the engine.
func ruleWorld(t *testing.T, c ruleChange) *engine.Engine {
	t.Helper()
	return worldWith(t, ruleSnapshot(t), c)
}

// worldWith computes s with c applied, as ruleWorld does.
func worldWith(t *testing.T, s ruleSnap, c ruleChange) *engine.Engine {
	t.Helper()
	if c.apply != nil {
		c.apply(s)
	}
	r := newRuleEngine(t, s, c)
	for k, at := range ruleSteps {
		if k == len(ruleSteps)-1 && c.late != nil {
			c.late(s)
		}
		r.compute(at)
	}
	return r.e
}

// ruleEngine is one engine over a snapshot that a test edits between publications.
type ruleEngine struct {
	t   *testing.T
	e   *engine.Engine
	h   *holder
	s   ruleSnap
	now time.Time
}

// newRuleEngine is an engine over s with c's controller signals, publishing nothing yet.
func newRuleEngine(t *testing.T, s ruleSnap, c ruleChange) *ruleEngine {
	r := &ruleEngine{t: t, h: &holder{}, s: s}
	clock := func() time.Time { return r.now }
	r.e = engine.New(engine.Config{Snapshots: r.h, Served: testutil.ServedUpstream(), Detectors: []install.Detector{install.UpstreamDetector{}},
		Scope: install.NamespaceSelectorScope{}, Verifier: scopeverify.NewManagedCountVerifier(time.Hour), Checks: append(health.Upstream(), extCheck{}),
		Now: clock, Collected: api.RuleCollected(ruleInstall, clock, logsOf(c, clock), c.rejects...)})
	return r
}

// compute publishes the snapshot as taken at testutil.Now+at.
func (r *ruleEngine) compute(at time.Duration) {
	r.t.Helper()
	r.now = testutil.Now.Add(at)
	sortObjects(r.s.Snapshot)
	r.s.TakenAt = r.now
	r.h.mu.Lock()
	r.h.s = r.s.Snapshot
	r.h.mu.Unlock()
	if m := r.e.Compute(); len(m.Installs) != 1 {
		r.t.Fatalf("%d installs, want 1", len(m.Installs))
	}
}

// logsOf is the controller log reading c sets: on, with c.logs' events, or none.
func logsOf(c ruleChange, now func() time.Time) func() collect.Snapshot {
	if c.logs == nil {
		return nil
	}
	store := collect.New(nil, now)
	logs := ctrllogs.Logs{ruleInstall: {State: ctrllogs.StateOn, Events: c.logs}}
	store.Publish(ctrllogs.Key, logs, 1, 0)
	return store.Snapshot
}

// ruleKinds maps every upstream kind and the core kinds findings name.
type ruleKinds struct{}

func (ruleKinds) Lookup(group, kind string) (sar.Kind, bool) {
	switch {
	case group == "apps" && kind == "Deployment":
		return sar.Kind{Group: "apps", Resource: "deployments", Namespaced: true}, true
	case group == "" && kind == "Pod":
		return sar.Kind{Resource: "pods", Namespaced: true}, true
	case group == "" && kind == "Service":
		return sar.Kind{Resource: "services", Namespaced: true}, true
	case group == "" && kind == "Namespace":
		return sar.Kind{Resource: "namespaces"}, true
	case group == "discovery.k8s.io" && kind == "EndpointSlice":
		return sar.Kind{Group: "discovery.k8s.io", Resource: "endpointslices", Namespaced: true}, true
	}
	for _, s := range testutil.ServedUpstream() {
		if s.Group == group && s.Kind == kind {
			return sar.Kind{Group: s.Group, Resource: s.Resource, Namespaced: s.Namespaced}, true
		}
	}
	return sar.Kind{}, false
}

func (ruleKinds) All() ([]sar.Kind, string) {
	var out []sar.Kind
	for _, s := range testutil.ServedUpstream() {
		out = append(out, sar.Kind{Group: s.Group, Resource: s.Resource, Namespaced: s.Namespaced})
	}
	return out, "rule"
}

type ruleUserKey struct{}

func ruleIdentity(ctx context.Context) (oidc.Identity, bool) {
	u, ok := ctx.Value(ruleUserKey{}).(string)
	return oidc.Identity{Username: u}, ok && u != ""
}

// ruleChecker allows the full viewer everything and the restricted one every
// list and get in the first ten scale namespaces, apart from kindLimited in
// kindLimitedNS, plus a get of namedOnly. routeReader is restricted plus every
// route kind in every namespace and cluster-wide, nsReader restricted plus
// the Namespaces. nobody may read nothing;
// a metaUser reads only the install metadata sources its name lists.
type ruleChecker struct{}

func (ruleChecker) Check(_ context.Context, id oidc.Identity, a sar.Attributes) (bool, error) {
	if id.Username == nobodyUser {
		return false, nil
	}
	if src, ok := strings.CutPrefix(id.Username, metaUser); ok {
		return a.Verb == "list" && (strings.Contains(src, "d") && a.Resource == "deployments" && a.Namespace == "kgw" ||
			strings.Contains(src, "c") && a.Resource == "gatewayclasses" || strings.Contains(src, "n") && a.Resource == "namespaces"), nil
	}
	if id.Username == fullUser || id.Username == routeReader && strings.HasSuffix(a.Resource, "routes") {
		return true, nil
	}
	if id.Username == nsReader && a.Resource == "namespaces" {
		return true, nil
	}
	if a.Verb == "get" && a.Namespace == hiddenNS && a.Name == namedOnly {
		return true, nil
	}
	if a.Namespace == kindLimitedNS && slices.Contains(kindLimited, a.Resource) {
		return false, nil
	}
	for i := range 10 {
		if a.Namespace == testutil.ScaleNamespace(i) {
			return true, nil
		}
	}
	return false, nil
}

// In kindLimitedNS the restricted viewer reads routes, Gateways and Services
// but not the kindLimited resources; namedOnly, in hiddenNS, it may get by name only.
var (
	kindLimitedNS = testutil.ScaleNamespace(9)
	kindLimited   = []string{"deployments", "listenerpolicies", "endpointslices"}
)

const (
	namedOnly  = "named-only"
	limitedSvc = "limited-svc"
)

// ruleServe serves src in complete secure mode through a real sar.Authorizer,
// the user taken from X-User, with the core capabilities and the rule extension.
func ruleServe(t *testing.T, src api.ModelSource, opts ...api.HandlerOption) *httptest.Server {
	t.Helper()
	return ruleServeWith(t, src, ruleChecker{}, opts...)
}

// ruleServeWith is ruleServe answering SARs through c.
func ruleServeWith(t *testing.T, src api.ModelSource, c sar.Checker, opts ...api.HandlerOption) *httptest.Server {
	t.Helper()
	const unlimited = 1 << 30
	az := sar.New(sar.Options{Checker: c, Kinds: ruleKinds{}, Clock: clocktesting.NewFakeClock(ruleCommit), IdentityFrom: ruleIdentity,
		MaxInFlight: unlimited, Rate: unlimited, Burst: unlimited})
	mounts, err := extension.NewCore(extension.WithHandlers("rule", ruleExt)).Mounts(extension.Deps{Current: src.Current, Authorizer: az})
	if err != nil {
		t.Fatal(err)
	}
	core := func() []api.Capability {
		m := src.Current().Model
		return app.CoreCapabilities(m.Snapshot, app.Diagnostics{ControllerLogs: true}, m.Collected)
	}
	opts = append([]api.HandlerOption{api.WithSecureMode(true), api.WithCommitClock(func() time.Time { return ruleCommit }),
		api.WithExtMounts(mounts...), api.WithCoreCapabilities(core)}, opts...)
	h := api.NewHandler(src, fstest.MapFS{}, az, opts...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ruleUserKey{}, r.Header.Get("X-User"))))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fetch returns the status and body of GET path as user.
func fetch(t *testing.T, srv *httptest.Server, user, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

// decode is GET path as user decoded into T; it must answer 200.
func decode[T any](t *testing.T, srv *httptest.Server, user, path string) T {
	t.Helper()
	code, b := fetch(t, srv, user, path)
	if code != http.StatusOK {
		t.Fatalf("GET %s as %s: %d %s", path, user, code, b)
	}
	var v T
	if err := json.Unmarshal([]byte(b), &v); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return v
}

// ruleSurface is one response the rule covers; its path may depend on the
// worlds compared. Every change must leave it unchanged for the restricted
// viewer; when changes is set, the reach and bypass tests ask only those.
type ruleSurface struct {
	name    string
	path    func(t *testing.T, before, after *httptest.Server) string
	changes []string
}

// asks reports whether the reach and bypass tests ask surface s about change c.
func (s ruleSurface) asks(c string) bool { return s.changes == nil || slices.Contains(s.changes, c) }

// routePath is the detail path of the HTTPRoute ns/name, plus suffix.
func routePath(ns, name, suffix string) func(*testing.T, *httptest.Server, *httptest.Server) string {
	return fixed("/routes/" + objects.GatewayAPIGroup + "/HTTPRoute/" + ns + "/" + name + suffix)
}

func fixed(p string) func(*testing.T, *httptest.Server, *httptest.Server) string {
	return func(*testing.T, *httptest.Server, *httptest.Server) string { return ruleBase + p }
}

// changedFinding is a finding row the full viewer sees in one world only, or
// with other content, so the change decides it; false when there is none.
func changedFinding(t *testing.T, before, after *httptest.Server) (api.FindingRow, bool) {
	t.Helper()
	b, a := decode[api.Page[api.FindingRow]](t, before, fullUser, ruleBase+"/findings?limit=200").Items, decode[api.Page[api.FindingRow]](t, after, fullUser, ruleBase+"/findings?limit=200").Items
	// has reports whether rows hold f's ID, and with whole also its content.
	has := func(rows []api.FindingRow, f api.FindingRow, whole bool) bool {
		want, _ := json.Marshal(f)
		return slices.ContainsFunc(rows, func(r api.FindingRow) bool {
			got, _ := json.Marshal(r)
			return r.ID == f.ID && (!whole || string(got) == string(want))
		})
	}
	// A finding that appears or goes first, then one whose content changes.
	for _, whole := range []bool{false, true} {
		for _, f := range append(slices.Clone(a), b...) {
			if !has(a, f, whole) || !has(b, f, whole) {
				return f, true
			}
		}
	}
	return api.FindingRow{}, false
}

// hiddenFindingDetail is the detail path of changedFinding, else of none.
func hiddenFindingDetail(t *testing.T, before, after *httptest.Server) string {
	t.Helper()
	if f, ok := changedFinding(t, before, after); ok {
		return ruleBase + "/findings/" + url.PathEscape(f.ID)
	}
	return ruleBase + "/findings/f1-none"
}

// hiddenSearch is the findings list searched for changedFinding's resource
// name, else its code, else "hidden".
func hiddenSearch(t *testing.T, before, after *httptest.Server) string {
	t.Helper()
	q := "hidden"
	if f, ok := changedFinding(t, before, after); ok {
		q = f.Code
		if f.Resource != nil {
			q = f.Resource.Name
		}
	}
	return ruleBase + "/findings?limit=200&q=" + url.QueryEscape(q)
}

// findingIDs is the ID of every finding row user sees.
func findingIDs(t *testing.T, srv *httptest.Server, user string) []string {
	t.Helper()
	var out []string
	for _, f := range decode[api.Page[api.FindingRow]](t, srv, user, ruleBase+"/findings?limit=200").Items {
		out = append(out, f.ID)
	}
	return out
}

// readable reports whether the restricted viewer may read ref as a population member.
func readable(ref objects.Ref) bool {
	if ref.Namespace == kindLimitedNS && (ref.Kind == "Deployment" || ref.Kind == "ListenerPolicy") {
		return false
	}
	for i := range 10 {
		if ref.Namespace == testutil.ScaleNamespace(i) {
			return true
		}
	}
	return false
}

var ruleSurfaces = []ruleSurface{
	{name: "/health", path: fixed("/health")},
	{name: "/findings", path: fixed("/findings?limit=200")},
	{name: "/findings?q", path: hiddenSearch},
	{name: "/findings?view=groups", path: fixed("/findings?view=groups&limit=200")},
	{name: "/diagnostics", path: fixed("/diagnostics")},
	{name: "finding detail", path: hiddenFindingDetail},
	// Lists and details whose neighbours the changes reach (Task 33).
	{name: "/routes", path: fixed("/routes?limit=200"), changes: []string{"listenerCount", "listenerSetRoute", "rejecting", "hiddenChild"}},
	{name: "/gateways", path: fixed("/gateways?limit=200"), changes: []string{"listenerCount", "created"}},
	{name: "/listenersets", path: fixed("/listenersets?limit=200"), changes: []string{"listenerSetRoute"}},
	{name: "/processes", path: fixed("/processes?limit=200"), changes: []string{"proxyReadyDeployment", "threshold"}},
	{name: "route detail (hidden parent)", path: routePath(ns0, "child", ""), changes: []string{"computedTrigger"}},
	{name: "route detail (hidden Gateway)", path: routePath(ns0, namesHidden, ""), changes: []string{"rejecting"}},
	{name: "delegation", path: routePath(ns0, delegToHidden, "/delegation"), changes: []string{"hiddenChild"}},
	{name: "route detail (hidden child)", path: routePath(ns0, delegToHidden, ""), changes: []string{"hiddenChild"}},
	{name: "Gateway detail", path: fixed("/gateways/" + ns0 + "/" + testutil.ScaleGateway(0)), changes: []string{"listenerCount"}},
	{name: "ListenerSet detail", path: fixed("/listenersets/" + objects.GatewayAPIGroup + "/ListenerSet/" + ns0 + "/" + lsAllName), changes: []string{"listenerSetRoute"}},
	// Only the route reader's worlds have it; elsewhere it answers 404 alike.
	{name: "Gateway detail (selector)", path: fixed("/gateways/" + ns0 + "/" + readerSelectorGW), changes: []string{}},
	{name: "YAML (finding only)", path: fixed("/resources/" + objects.GatewayAPIGroup + "/HTTPRoute/" + ns0 + "/" + namesAbsent), changes: []string{}},
	{name: "YAML (hidden)", path: fixed("/resources/" + objects.GatewayAPIGroup + "/HTTPRoute/" + hiddenNS + "/hidden-route"), changes: []string{"faulty"}},
	// Installs, the overview, meta and an extension mount (Task 34).
	{name: "/installs", path: abs("/api/v1/installs"), changes: []string{"faulty", "threshold", "proxyReadyDeployment"}},
	{name: "/overview", path: fixed("/overview"), changes: []string{"proxyReadyDeployment", "backendConsumer"}},
	{name: "/meta", path: abs("/api/v1/meta"), changes: []string{"controller"}},
	{name: "extension", path: abs("/api/ext/rule/backends"), changes: []string{"backendConsumer"}},
	// Paths and backends (Task 35).
	{name: "/paths", path: fixed("/paths?limit=200"), changes: []string{"faulty", "proxyReadyDeployment"}},
	{name: "/backends", path: fixed("/backends?limit=200"), changes: []string{"faulty", "proxyReadyDeployment"}},
	{name: "backend detail (slices not listable)", path: fixed("/backends/-/Service/" + kindLimitedNS + "/" + limitedSvc), changes: []string{"faulty"}},
	// The namespace scope and its namespace list (Task 37).
	{name: "/routes?namespaces=visible", path: fixed("/routes?limit=200&namespaces=" + ns0), changes: []string{"rejecting", "hiddenChild"}},
	// An empty page before and after for the restricted viewer; without the projection the D-013 count moves.
	{name: "/routes?namespaces=hidden", path: fixed("/routes?limit=200&namespaces=" + hiddenNS), changes: []string{"created", "deleted"}},
	{name: "/namespaces", path: fixed("/namespaces"), changes: []string{"newNamespace"}},
	// A readable chain through hidden-hop: delegation, effective matches and the dropped flag.
	{name: "delegation (hidden hop)", path: routePath(ns0, chainMid, "/delegation"), changes: []string{"hopMatchNarrows", "hopMatchDrops"}},
	{name: "route detail (hidden hop)", path: routePath(ns0, chainLeaf, ""), changes: []string{"hopMatchDrops"}},
	{name: "/routes?state=dropped", path: fixed("/routes?limit=200&state=dropped"), changes: []string{"hopMatchDrops"}},
}

// abs is the fixed path p outside the install's base.
func abs(p string) func(*testing.T, *httptest.Server, *httptest.Server) string {
	return func(*testing.T, *httptest.Server, *httptest.Server) string { return p }
}

// ruleMasks are, per surface and change, the JSON paths (dot-separated) a
// change may alter, each with its reason. "@name" drops the array elements
// whose ref is named name, or with a path after it only that field of them;
// "" masks the whole response, status included.
var ruleMasks = map[[2]string][]string{
	// Deleting hidden-gw takes hidden-parent's child out of the install (D-107); nothing else moves.
	{"/routes", "deleted"}:                      {"items.@child", "total"},
	{"/routes?namespaces=visible", "deleted"}:   {"items.@child", "total"},
	{"route detail (hidden parent)", "deleted"}: {""},
	// The same move takes child out of the overview's HTTPRoute count and be-0000's routes.
	{"/overview", "deleted"}: {"counts"},
	{"/backends", "deleted"}: {"rows.@be-0000.routes"},
}

// ruleNotApplicable are the pairs a change cannot reach for the full viewer either.
var ruleNotApplicable = map[[2]string]string{
	{"/findings", "controller"}:                 ctrlLogsOnly,
	{"/findings?q", "controller"}:               ctrlLogsOnly,
	{"/findings?view=groups", "controller"}:     ctrlLogsOnly,
	{"/diagnostics", "controller"}:              ctrlLogsOnly,
	{"finding detail", "controller"}:            ctrlLogsOnly,
	{"/findings", "eventsCapped"}:               eventsCapOnly,
	{"/findings?q", "eventsCapped"}:             eventsCapOnly,
	{"/findings?view=groups", "eventsCapped"}:   eventsCapOnly,
	{"finding detail", "eventsCapped"}:          eventsCapOnly,
	{"/findings?view=groups", "listenerCount"}:  detailOnly,
	{"/diagnostics", "listenerCount"}:           detailOnly,
	{"/findings?view=groups", "selectorTarget"}: detailOnly,
	{"/diagnostics", "selectorTarget"}:          detailOnly,
}

// Why a change cannot reach a surface for the full viewer either.
const (
	ctrlLogsOnly  = "the deployer error lands only in the hidden Gateway's 006 evidence, which only /health serialises"
	eventsCapOnly = "the Events cap changes only the probe failures check's detail; no finding"
	detailOnly    = "only a 012's detail changes; group rows and the diagnostics show its message and counts"
)

// ruleHeldBefore are the pairs the 7.3 code already holds without the projection.
var ruleHeldBefore = map[[2]string]string{
	{"/health", "controller"}:            rowDropsDetail,
	{"/health", "selectorTarget"}:        rowDropsDetail,
	{"/findings", "selectorTarget"}:      rowDropsDetail,
	{"/findings?q", "selectorTarget"}:    rowDropsDetail,
	{"finding detail", "selectorTarget"}: rowDropsDetail,
	{"YAML (hidden)", "faulty"}:          "a hidden object's YAML answers 404 since 7.3",
	{"delegation", "hiddenChild"}:        "rows of a hidden child are left out since 7.3, and the name ref's row stands for it either way",
	{"/namespaces", "newNamespace"}:      "list permission decides the namespaces, as it decides list rows since 7.3; the projection adds nothing",
}

// rowDropsDetail: the change alters only a hidden finding's detail or
// evidence, which D-013's restricted row already empties.
const rowDropsDetail = "only a hidden finding's detail or evidence changes, which a D-013 restricted row empties"

// mask is body without the fields paths name; a body that is not JSON is unchanged.
func mask(t *testing.T, body string, paths []string) string {
	t.Helper()
	if len(paths) == 0 {
		return body
	}
	if slices.Contains(paths, "") {
		return ""
	}
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return body
	}
	for _, p := range paths {
		drop(v, strings.Split(p, "."))
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// drop deletes the field path names in v; "*" walks every array element.
func drop(v any, path []string) {
	switch x := v.(type) {
	case map[string]any:
		if len(path) == 1 {
			delete(x, path[0])
			return
		}
		if arr, ok := x[path[0]].([]any); ok && strings.HasPrefix(path[1], "@") {
			named := func(e any) bool {
				m, _ := e.(map[string]any)
				ref, _ := m["ref"].(map[string]any)
				return ref != nil && "@"+fmt.Sprint(ref["name"]) == path[1]
			}
			if len(path) == 2 {
				x[path[0]] = slices.DeleteFunc(arr, named)
				return
			}
			for _, e := range arr {
				if named(e) {
					drop(e, path[2:])
				}
			}
			return
		}
		drop(x[path[0]], path[1:])
	case []any:
		if path[0] == "*" {
			for _, e := range x {
				drop(e, path[1:])
			}
		}
	}
}

// ruleResponse is one surface's status and body.
type ruleResponse struct {
	code int
	body string
}

// rulePair is a change's before and after servers.
type rulePair struct {
	change        ruleChange
	before, after *httptest.Server
}

// rulePairs serves the base world and each change, with opts.
func rulePairs(t *testing.T, changes []ruleChange, opts ...api.HandlerOption) []rulePair {
	t.Helper()
	before := ruleServe(t, ruleWorld(t, ruleChange{name: "base"}), opts...)
	var out []rulePair
	for _, c := range changes {
		out = append(out, rulePair{change: c, before: before, after: ruleServe(t, ruleWorld(t, c), opts...)})
	}
	return out
}

// differences returns, for user, the surfaces whose masked response differs
// between each pair's worlds, keyed by surface and change.
func differences(t *testing.T, pairs []rulePair, user string, masks map[[2]string][]string) map[[2]string]string {
	t.Helper()
	out := map[[2]string]string{}
	for _, p := range pairs {
		for _, s := range ruleSurfaces {
			key := [2]string{s.name, p.change.name}
			path := s.path(t, p.before, p.after)
			var rs [2]ruleResponse
			for k, srv := range []*httptest.Server{p.before, p.after} {
				code, body := fetch(t, srv, user, path)
				if slices.Contains(masks[key], "") {
					code = 0
				}
				rs[k] = ruleResponse{code, mask(t, body, masks[key])}
			}
			if rs[0] != rs[1] {
				out[key] = fmt.Sprintf("%s: %d %.300s\n  vs %d %.300s", path, rs[0].code, rs[0].body, rs[1].code, rs[1].body)
			}
		}
	}
	return out
}

// TestRuleUnchangedForRestricted: every hidden change leaves every surface
// byte-equal for the restricted viewer (D-085), and every change hidden from
// the route reader leaves its surfaces byte-equal too.
func TestRuleUnchangedForRestricted(t *testing.T) {
	for key, diff := range differences(t, rulePairs(t, ruleChanges), restrictedUser, ruleMasks) {
		t.Errorf("%s changed for the restricted viewer under %s:\n%s", key[0], key[1], diff)
	}
	for key, diff := range differences(t, readerPairs(t), routeReader, ruleMasks) {
		t.Errorf("%s changed for the route reader under %s:\n%s", key[0], key[1], diff)
	}
}

// readerSelectorGW is a visible Gateway whose listener selects namespaces labelled sel=on.
const readerSelectorGW = "reader-sel-gw"

// readerWorld adds readerSelectorGW and a route in hiddenNS on it: the route
// reader may read the route, not the Namespace labels that make it a member.
func readerWorld(s ruleSnap) {
	g := testutil.Gateway(ns0, readerSelectorGW, scale.Class, nil)
	g.Object["spec"].(map[string]any)["listeners"].([]any)[0].(map[string]any)["allowedRoutes"] = map[string]any{
		"namespaces": map[string]any{"from": "Selector", "selector": map[string]any{"matchLabels": map[string]any{"sel": "on"}}}}
	s.add(g, routeTo(hiddenNS, "hidden-on-reader-sel", []objects.ParentRef{testutil.GwRef(ns0, readerSelectorGW)}, hiddenNS, "hidden-be", true))
}

// readerChanges are the changes to objects the route reader may not read:
// ruleChanges that touch no route, and hiddenNS's Namespace labels.
var readerChanges = []string{"gatewayAdded", "extension", "proxyReadyDeployment", "controllerRestart", "crdBundle", "listenerPolicy", "eventsCapped", "rejecting"}

// readerPairs serves readerWorld and each of readerChanges, plus the label change.
func readerPairs(t *testing.T) []rulePair {
	t.Helper()
	world := func(c ruleChange) *httptest.Server {
		s := ruleSnapshot(t)
		readerWorld(s)
		return ruleServe(t, worldWith(t, s, c))
	}
	before := world(ruleChange{name: "base"})
	labels := ruleChange{name: "selectorLabels", apply: func(s ruleSnap) {
		for _, n := range s.Namespaces {
			if n.Name == hiddenNS {
				n.Labels = map[string]string{"sel": "on"}
			}
		}
	}}
	out := []rulePair{{change: labels, before: before, after: world(labels)}}
	for _, name := range readerChanges {
		c := ruleChanges[slices.IndexFunc(ruleChanges, func(c ruleChange) bool { return c.name == name })]
		out = append(out, rulePair{change: c, before: before, after: world(c)})
	}
	return out
}

// TestReaderSelectorLabelsReachFullReader: the label change reaches the full
// viewer's Gateway detail, so the route reader's case is not vacuous.
func TestReaderSelectorLabelsReachFullReader(t *testing.T) {
	p := readerPairs(t)[0]
	path := ruleBase + "/gateways/" + ns0 + "/" + readerSelectorGW
	if _, b := fetch(t, p.before, fullUser, path); b == func() string { _, a := fetch(t, p.after, fullUser, path); return a }() {
		t.Fatal("the label change does not reach the full viewer")
	}
}

// TestRuleChangesReachFullReader: each change reaches each surface for the full
// viewer, so the rule tests are not vacuous; ruleNotApplicable lists the exceptions.
func TestRuleChangesReachFullReader(t *testing.T) {
	diffs := differences(t, rulePairs(t, ruleChanges), fullUser, nil)
	for _, c := range ruleChanges {
		for _, s := range ruleSurfaces {
			if !s.asks(c.name) {
				continue
			}
			key := [2]string{s.name, c.name}
			_, differs := diffs[key]
			reason, listed := ruleNotApplicable[key]
			switch {
			case !differs && !listed:
				t.Errorf("%s does not reach %s for the full viewer; list it in ruleNotApplicable with a reason", c.name, s.name)
			case differs && listed:
				t.Errorf("%s reaches %s for the full viewer, but ruleNotApplicable says %q", c.name, s.name, reason)
			}
		}
	}
}

// TestRuleBypassBreaksInvariant: without the projection, every applicable pair
// changes for the restricted viewer, so a regression in any step fails the rule.
func TestRuleBypassBreaksInvariant(t *testing.T) {
	diffs := differences(t, rulePairs(t, ruleChanges, api.WithProjectionBypass()), restrictedUser, ruleMasks)
	for _, c := range ruleChanges {
		for _, s := range ruleSurfaces {
			key := [2]string{s.name, c.name}
			if _, na := ruleNotApplicable[key]; na || !s.asks(c.name) {
				continue
			}
			_, differs := diffs[key]
			reason, held := ruleHeldBefore[key]
			switch {
			case !differs && !held:
				t.Errorf("%s under %s holds without the projection; list it in ruleHeldBefore with a reason", s.name, c.name)
			case differs && held:
				t.Errorf("%s under %s fails without the projection, but ruleHeldBefore says %q", s.name, c.name, reason)
			}
		}
	}
}

// hopPairs serves the base world with the chain through hidden-hop, and each of hopChanges, with opts.
func hopPairs(t *testing.T, opts ...api.HandlerOption) []rulePair {
	t.Helper()
	world := func(c ruleChange) *httptest.Server {
		s := ruleSnapshot(t)
		s.add(chainRoutes()...)
		return ruleServe(t, worldWith(t, s, c), opts...)
	}
	before := world(ruleChange{name: "base"})
	var out []rulePair
	for _, c := range hopChanges {
		out = append(out, rulePair{change: c, before: before, after: world(c)})
	}
	return out
}

// TestRuleHiddenHopUnchanged: a change to hidden-hop's matcher alone leaves
// every surface byte-equal for the namespace reader, though the chain's
// root, middle and leaf are readable; it reaches the full viewer's chain
// surfaces, and without the projection the namespace reader's too.
func TestRuleHiddenHopUnchanged(t *testing.T) {
	pairs := hopPairs(t)
	for key, diff := range differences(t, pairs, nsReader, ruleMasks) {
		t.Errorf("%s changed for the namespace reader under %s:\n%s", key[0], key[1], diff)
	}
	full, bypass := differences(t, pairs, fullUser, nil), differences(t, hopPairs(t, api.WithProjectionBypass()), nsReader, ruleMasks)
	for _, c := range hopChanges {
		for _, s := range ruleSurfaces {
			if s.changes == nil || !s.asks(c.name) {
				continue
			}
			key := [2]string{s.name, c.name}
			if _, ok := full[key]; !ok {
				t.Errorf("%s does not reach %s for the full viewer", c.name, s.name)
			}
			if _, ok := bypass[key]; !ok {
				t.Errorf("%s under %s holds for the namespace reader without the projection", s.name, c.name)
			}
		}
	}
}

// checksOf is the "What was checked" list of /health and /diagnostics for user.
func checksOf(t *testing.T, srv *httptest.Server, user string) [2][]health.CheckResult {
	t.Helper()
	return [2][]health.CheckResult{
		decode[api.HealthResponse](t, srv, user, ruleBase+"/health").Checks,
		decode[api.Diagnostics](t, srv, user, ruleBase+"/diagnostics").Checks,
	}
}

// collectionChecks are the checks whose collection failures every viewer sees (D-106 item 3).
var collectionChecks = map[string]bool{"cluster access": true, "served kinds": true, "backend endpoints": true}

// TestRuleCheckedListUnchanged: the restricted viewer's "What was checked" is
// the same across every change, and every result but a collection failure is "Not checked".
func TestRuleCheckedListUnchanged(t *testing.T) {
	pairs := rulePairs(t, ruleChanges)
	want := checksOf(t, pairs[0].before, restrictedUser)
	for _, cs := range want {
		for _, c := range cs {
			if collectionChecks[c.Name] {
				continue
			}
			if !c.NotAssessed || !c.OK || c.Error != "" || c.Detail != "" || c.Hold != nil || c.Pending {
				t.Errorf("check %+v: want Not checked", c)
			}
		}
	}
	for _, p := range pairs {
		got := checksOf(t, p.after, restrictedUser)
		for k := range got {
			a, _ := json.Marshal(want[k])
			b, _ := json.Marshal(got[k])
			if string(a) != string(b) {
				t.Errorf("%s: checks changed for the restricted viewer:\n%s\n  vs %s", p.change.name, a, b)
			}
		}
	}
}

// contextChange is a hidden change that may change a restricted viewer's
// output (D-106 item 1, D-107): base readies both worlds, masks name exactly
// the fields of each surface it may change.
type contextChange struct {
	change ruleChange
	base   func(s ruleSnap)
	masks  map[string][]string
}

// withSelector makes the controller watch only namespaces labelled sel.
func withSelector(sel string) func(s ruleSnap) {
	return func(s ruleSnap) {
		d := s.deployment("kgw", "kgateway")
		d.Spec.Template.Spec.Containers[0].Env = append(d.Spec.Template.Spec.Containers[0].Env,
			testutil.Env(install.DiscoveryNamespaceSelectorsEnv, `[{"matchLabels":{"team":"`+sel+`"}}]`))
	}
}

var installContext = []contextChange{
	{
		// The controller's discovery selector narrows the install to labelled namespaces.
		change: ruleChange{name: "controllerSettings", apply: withSelector(testutil.ScaleTeamLabel["team"])},
		masks:  scopeMasks,
	},
	{
		// A hidden GatewayClass starts naming kgateway's controller, so a readable Gateway joins the install.
		change: ruleChange{name: "classController", apply: func(s ruleSnap) {
			s.get("GatewayClass", "", "other").Object["spec"].(map[string]any)["controllerName"] = testutil.Controller
		}},
		// other-gw has no finding; it joins the Gateway counts.
		masks: map[string][]string{"/diagnostics": gatewayCounts},
	},
	{
		// A hidden ListenerSet moves to a Gateway of another controller, taking a readable route out of the install.
		change: ruleChange{name: "parentListenerSet", apply: func(s ruleSnap) {
			s.get("ListenerSet", hiddenNS, "hidden-ls").Object["spec"].(map[string]any)["parentRef"] = map[string]any{"name": "other-gw", "namespace": ns0}
		}},
		masks: map[string][]string{"/routes": listItems},
	},
	{
		// Under a label selector, a readable namespace's Namespace object, hidden as every cluster-scoped object is, gains the label.
		change: ruleChange{name: "namespaceLabels", apply: func(s ruleSnap) {
			for _, n := range s.Namespaces {
				if n.Name == testutil.ScaleNamespace(1) {
					n.Labels = maps.Clone(testutil.ScaleTeamLabel)
				}
			}
		}},
		base:  withSelector(testutil.ScaleTeamLabel["team"]),
		masks: withSearch(scopeMasks),
	},
}

// listItems are a list's rows and total.
var listItems = []string{"items", "total"}

// gatewayCounts are the /diagnostics fields that count the install's Gateways.
var gatewayCounts = []string{"dataPlane.gateways", "processes.proxies.gateways", "processes.proxies.notObserved"}

// scopeMasks are the fields a scope change moves: whole namespaces of rows,
// their errors, groups and Gateways leave or join the install.
var scopeMasks = map[string][]string{
	"/health":               {"counts.error", "findings"},
	"/findings":             {"items", "total"},
	"/findings?view=groups": {"items", "total"},
	"/diagnostics":          append([]string{"groupTotal", "groups", "verdict.counts.error", "verdict.problems"}, gatewayCounts...),
	"/routes":               listItems,
}

// withSearch is m with the search surface masked as the findings list.
func withSearch(m map[string][]string) map[string][]string {
	out := maps.Clone(m)
	out["/findings?q"] = out["/findings"]
	return out
}

// contextSnapshot is the base world with a GatewayClass of another controller,
// a readable Gateway of that class, a hidden ListenerSet on hidden-gw and a
// readable route attached to it, then base applied.
func contextSnapshot(t *testing.T, base func(ruleSnap)) ruleSnap {
	s := ruleSnapshot(t)
	ls := objects.ParentRef{Group: objects.GatewayAPIGroup, Kind: "ListenerSet", Namespace: hiddenNS, Name: "hidden-ls"}
	s.add(testutil.GatewayClass("other", "example.com/other", true), testutil.Gateway(ns0, "other-gw", "other", nil),
		testutil.ListenerSet(hiddenNS, "hidden-ls", testutil.GwRef(hiddenNS, "hidden-gw"), nil),
		routeTo(ns0, "ls-route", []objects.ParentRef{ls}, ns0, "be-0000", true))
	if base != nil {
		base(s)
	}
	return s
}

// stableFindingDetail is the detail path of a finding the restricted viewer
// sees alike in both worlds, so a membership change must leave it alone.
func stableFindingDetail(t *testing.T, before, after *httptest.Server) string {
	t.Helper()
	b := decode[api.Page[api.FindingRow]](t, before, restrictedUser, ruleBase+"/findings?limit=200").Items
	a := decode[api.Page[api.FindingRow]](t, after, restrictedUser, ruleBase+"/findings?limit=200").Items
	for _, f := range b {
		want, _ := json.Marshal(f)
		if slices.ContainsFunc(a, func(r api.FindingRow) bool { got, _ := json.Marshal(r); return string(got) == string(want) }) {
			return ruleBase + "/findings/" + url.PathEscape(f.ID)
		}
	}
	t.Fatal("no finding the restricted viewer sees alike in both worlds")
	return ""
}

// contextSurfaces are the finding surfaces, with the detail of a finding
// membership leaves alone, and the routes list, whose rows membership moves.
func contextSurfaces() []ruleSurface {
	out := slices.Clone(ruleSurfaces[:6])
	out[5] = ruleSurface{name: "finding detail", path: stableFindingDetail}
	return append(out, ruleSurface{name: "/routes", path: fixed("/routes?limit=200")})
}

// TestRuleInstallContextMayChange: an install-context change changes only the
// fields its mask names, each mask is needed, and every other field stays byte-equal.
func TestRuleInstallContextMayChange(t *testing.T) {
	for _, ic := range installContext {
		t.Run(ic.change.name, func(t *testing.T) {
			before := ruleServe(t, worldWith(t, contextSnapshot(t, ic.base), ruleChange{name: "base"}))
			after := ruleServe(t, worldWith(t, contextSnapshot(t, ic.base), ic.change))
			_, fb := fetch(t, before, fullUser, ruleBase+"/findings?limit=200")
			if _, fa := fetch(t, after, fullUser, ruleBase+"/findings?limit=200"); fb == fa {
				t.Fatal("the change does not reach the full viewer's findings")
			}
			for _, s := range contextSurfaces() {
				path := s.path(t, before, after)
				_, b := fetch(t, before, restrictedUser, path)
				_, a := fetch(t, after, restrictedUser, path)
				m := ic.masks[s.name]
				if mask(t, b, m) != mask(t, a, m) {
					t.Errorf("%s changed outside its mask %v; differing fields %v", s.name, m, diffPaths(t, b, a))
				}
				for k := range m {
					rest := slices.Delete(slices.Clone(m), k, k+1)
					if mask(t, b, rest) == mask(t, a, rest) {
						t.Errorf("%s: mask field %s never changes; drop it", s.name, m[k])
					}
				}
			}
		})
	}
}

// diffPaths are the dot-separated JSON paths at which a and b differ, down to
// the first array; "" for bodies that are not JSON objects.
func diffPaths(t *testing.T, a, b string) []string {
	t.Helper()
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return []string{""}
	}
	var out []string
	var walk func(p string, x, y any)
	walk = func(p string, x, y any) {
		mx, okx := x.(map[string]any)
		my, oky := y.(map[string]any)
		if !okx || !oky {
			if fmt.Sprint(x) != fmt.Sprint(y) {
				out = append(out, p)
			}
			return
		}
		keys := map[string]bool{}
		for k := range mx {
			keys[k] = true
		}
		for k := range my {
			keys[k] = true
		}
		for _, k := range slices.Sorted(maps.Keys(keys)) {
			walk(strings.TrimPrefix(p+"."+k, "."), mx[k], my[k])
		}
	}
	walk("", x, y)
	return out
}

// TestRuleCollectionErrorTextHidden: a collection failure reaches a restricted
// viewer with its fixed text, never the API server's, which names a namespace and a ServiceAccount.
func TestRuleCollectionErrorTextHidden(t *testing.T) {
	const raw = `pods is forbidden: User "system:serviceaccount:secret-ops:snellius-reader" cannot list resource "pods" in the namespace "secret-ops"`
	src := ruleWorld(t, ruleChange{name: "denied", apply: func(s ruleSnap) {
		s.Access[cache.PodsGVR] = cache.Access{Err: raw, Denied: true}
	}})
	srv := ruleServe(t, src)
	if _, full := fetch(t, srv, fullUser, ruleBase+"/health"); !strings.Contains(full, "snellius-reader") {
		t.Fatal("the full viewer lacks the API server's text; the fixture does not reach it")
	}
	paths := []string{"/health", "/diagnostics", "/findings?limit=200"}
	for _, f := range findingsOf(t, srv, fullUser) {
		if f.Code == health.CodeSignalUnavailable {
			paths = append(paths, "/findings/"+f.ID)
		}
	}
	if len(paths) == 3 {
		t.Fatal("no KGWUI-008 for the denied watch")
	}
	for _, p := range paths {
		_, body := fetch(t, srv, restrictedUser, ruleBase+p)
		for _, leak := range []string{"secret-ops", "snellius-reader", "serviceaccount"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s: the restricted viewer sees %q:\n%.400s", p, leak, body)
			}
		}
	}
}

// confirmed serves one publication with a confirm tick's ConfirmedAt.
type confirmed struct {
	e *engine.Engine
	p *engine.Published
}

func (c confirmed) Current() *engine.Published { return c.p }
func (c confirmed) Platform() platform.Info    { return c.e.Platform() }

// TestRuleHiddenRecomputeLeavesNoTime: on one engine, a confirm tick at the
// last step ("before") and a recompute there for a hidden change ("after")
// answer the restricted viewer alike: no timestamp shows when the cluster last changed.
func TestRuleHiddenRecomputeLeavesNoTime(t *testing.T) {
	for _, c := range ruleChanges {
		if c.apply == nil || c.late != nil || c.rejects != nil || c.logs != nil {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			r := newRuleEngine(t, ruleSnapshot(t), ruleChange{name: "base"})
			for _, at := range ruleSteps[:len(ruleSteps)-1] {
				r.compute(at)
			}
			cur := r.e.Current()
			before := ruleServe(t, confirmed{e: r.e, p: &engine.Published{Model: cur.Model, Source: cur.Source, ConfirmedAt: testutil.Now}})
			// The recompute reads its own copy of the world, so before keeps the snapshot it was computed from.
			r.s = ruleSnapshot(t)
			c.apply(r.s)
			r.compute(0)
			after := ruleServe(t, r.e)
			for key, diff := range differences(t, []rulePair{{change: c, before: before, after: after}}, restrictedUser, ruleMasks) {
				t.Errorf("%s differs for the restricted viewer after a hidden recompute:\n%s", key[0], diff)
			}
		})
	}
}
