package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/auth/sar"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/policy"
)

// The Phase 8 subject relations (design §5.3, §6.5, §7 item 5) on the Gateway,
// ListenerSet, route and backend details, over the rule world plus policyWorld.

// Relation fixture names: routes and ListenerSets in ns0 and the hidden-Gateway namespace.
const (
	relSect      = "rel-sect"
	relUnsect    = "rel-unsect"
	relPort      = "rel-port"
	relLS        = "rel-ls"
	relGRPC      = "rel-grpc"
	relHop       = "rel-hop"
	relHop2      = "rel-hop2"
	relForeign   = "rel-foreign"
	lsHop        = "ls-hop"
	lsForeignHop = "ls-foreign-hop"
	// The foreign Gateways belong to no install; the changes create them.
	foreignParent = "foreign-parent"
	foreignHop    = "foreign-hop"
	limitedGW     = limitedSvc + "-gw"
	limitedRoute  = limitedSvc + "-route"
	hiddenHLP     = "hlp-hidden"
)

// relGW is gw-000 as a ref.
var relGW = gw0()[0].Ref()

// targetGW and the other target helpers name gw-000, ls-all and ls-hop.
func targetGW(section string) map[string]any {
	return targetRef(gatewaysGroup, "Gateway", relGW.Name, section)
}

// attachedAncestor is an Accepted and Attached=True (reason) ancestor entry on ref by the install's controller.
func attachedAncestor(ref objects.ParentRef, reason string) testutil.PolicyAncestor {
	return testutil.PolicyAncestor{Ref: ref, Controller: testutil.Controller, Conds: []metav1.Condition{
		testutil.Cond("Accepted", metav1.ConditionTrue, "Accepted"), testutil.Cond("Attached", metav1.ConditionTrue, reason)}}
}

// accepted is an HTTPRoute ns/name on parents, accepted by each.
func accepted(ns, name string, parents ...objects.ParentRef) *unstructured.Unstructured {
	return routeTo(ns, name, parents, "", "", true)
}

// relWorld adds the relation fixtures to policyWorld: routes with a sectioned, an
// unsectioned, a port-only and a ListenerSet parent, a GRPCRoute, ListenerSets whose
// parent Gateway the restricted viewer may not read or that does not exist yet, and
// policies naming each of them.
func relWorld(s ruleSnap) {
	policyWorld(s)
	gw, ls := gw0()[0], lsAllRef
	sect, port := gw, gw
	sect.SectionName, port.Port = "http", 80
	lsExtra := ls
	lsExtra.SectionName = "extra"
	hop := objects.ParentRef{Group: objects.GatewayAPIGroup, Kind: "ListenerSet", Namespace: ns0, Name: lsHop}
	foreignLS := objects.ParentRef{Group: objects.GatewayAPIGroup, Kind: "ListenerSet", Namespace: ns0, Name: lsForeignHop}
	grpc := accepted(ns0, relGRPC, sect)
	grpc.SetKind("GRPCRoute")
	s.add(accepted(ns0, relSect, sect), accepted(ns0, relUnsect, gw), accepted(ns0, relPort, port), accepted(ns0, relLS, lsExtra), grpc,
		testutil.ListenerSet(ns0, lsHop, testutil.GwRef(kindLimitedNS, limitedGW), nil), accepted(ns0, relHop, gw, hop),
		testutil.ListenerSet(ns0, lsForeignHop, testutil.GwRef(kindLimitedNS, foreignHop), nil), accepted(ns0, relHop2, gw, foreignLS),
		accepted(kindLimitedNS, relForeign, testutil.GwRef(kindLimitedNS, limitedGW), testutil.GwRef(kindLimitedNS, foreignParent)))
	s.add(tp(ns0, "tp-gw", []any{targetGW("")}, attachedAncestor(gw, "Merged")),
		tp(ns0, "tp-listener", []any{targetGW("http")}),
		tp(ns0, "tp-https", []any{targetGW("https")}),
		tp(ns0, "tp-ls", []any{targetRef(gatewaysGroup, "ListenerSet", lsAllName, "")}, attachedAncestor(ls, "Attached")),
		tp(ns0, "tp-ls-extra", []any{targetRef(gatewaysGroup, "ListenerSet", lsAllName, "extra")}),
		tp(ns0, "tp-ls-hop", []any{targetRef(gatewaysGroup, "ListenerSet", lsHop, "")}, attachedAncestor(hop, "Attached")),
		tp(ns0, "tp-route", []any{targetRef(gatewaysGroup, "HTTPRoute", relSect, ""), targetRef(gatewaysGroup, "GRPCRoute", relGRPC, "")}),
		tp(kindLimitedNS, "tp-limited", []any{targetRef(gatewaysGroup, "Gateway", limitedGW, "")}),
		tp(kindLimitedNS, "tp-foreign", []any{targetRef(gatewaysGroup, "Gateway", foreignParent, ""), targetRef(gatewaysGroup, "Gateway", foreignHop, "")}))
	bcp := coreAncestors(testutil.Policy(kinds.KgatewayGroup+"/v1alpha1", "BackendConfigPolicy", ns0, "bcp", attachedAncestor(sharedSvc, "Merged")))
	bcp.Object["spec"] = map[string]any{"targetRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "shared"}}}
	s.add(bcp)
}

// sharedSvc is port 80 of Service ns0/shared as an ancestor.
var sharedSvc = objects.ParentRef{Kind: "Service", Namespace: ns0, Name: "shared", Port: 80}

// coreAncestors writes group "" on u's Service ancestorRefs, which the fixture
// leaves out and Gateway API then defaults to its own group.
func coreAncestors(u *unstructured.Unstructured) *unstructured.Unstructured {
	as, _, _ := unstructured.NestedSlice(u.Object, "status", "ancestors")
	for _, a := range as {
		if ref := a.(map[string]any)["ancestorRef"].(map[string]any); ref["kind"] == "Service" {
			ref["group"] = ""
		}
	}
	_ = unstructured.SetNestedSlice(u.Object, as, "status", "ancestors")
	return u
}

// hiddenListenerPolicy is an HTTPListenerPolicy in ns0, a kind the restricted viewer may
// not read, naming gw-000's http listener and ls-all, selecting HTTPRoutes, attached on gw-000.
func hiddenListenerPolicy() *unstructured.Unstructured {
	u := testutil.Policy(kinds.KgatewayGroup+"/v1alpha1", "HTTPListenerPolicy", ns0, hiddenHLP, attachedAncestor(gw0()[0], "Attached"))
	u.Object["spec"] = map[string]any{"targetRefs": []any{targetGW("http"), targetRef(gatewaysGroup, "ListenerSet", lsAllName, "")},
		"targetSelectors": []any{map[string]any{"group": gatewaysGroup, "kind": "HTTPRoute", "matchLabels": map[string]any{"app": "x"}}}}
	return u
}

// relChanges are the hidden changes no relation may show the restricted viewer: a hidden
// policy, a hidden policy's ancestor, a hidden parent and a hidden hop appearing.
var relChanges = []ruleChange{
	{name: "relPolicyKindHidden", apply: func(s ruleSnap) { s.add(hiddenListenerPolicy()) }},
	{name: "relPolicyAncestorHidden", apply: func(s ruleSnap) {
		s.add(coreAncestors(tp(hiddenNS, "tp-hidden-anc", []any{targetRef(gatewaysGroup, "HTTPRoute", "hidden-route", "")},
			attachedAncestor(gw0()[0], "Merged"), attachedAncestor(lsAllRef, "Merged"), attachedAncestor(sharedSvc, "Merged"))))
	}},
	{name: "relParentCreated", apply: func(s ruleSnap) { s.add(testutil.Gateway(kindLimitedNS, foreignParent, "other-class", nil)) }},
	{name: "relHopCreated", apply: func(s ruleSnap) { s.add(testutil.Gateway(kindLimitedNS, foreignHop, "other-class", nil)) }},
}

// relChecker is policyChecker, except that the restricted viewers may not read HTTPListenerPolicies.
type relChecker struct{}

func (relChecker) Check(ctx context.Context, id oidc.Identity, a sar.Attributes) (bool, error) {
	if id.Username != fullUser && a.Resource == "httplistenerpolicies" {
		return false, nil
	}
	return policyChecker{}.Check(ctx, id, a)
}

// relEngine is the rule world plus relWorld, with c applied.
func relEngine(t *testing.T, c ruleChange) *engine.Engine {
	t.Helper()
	s := ruleSnapshot(t)
	relWorld(s)
	return worldWith(t, s, c)
}

// Subject detail paths.
func gwPath(ns, name string) string { return ruleBase + "/gateways/" + ns + "/" + name }
func lsPath(ns, name string) string {
	return ruleBase + "/listenersets/" + objects.GatewayAPIGroup + "/ListenerSet/" + ns + "/" + name
}
func relRoutePath(kind, ns, name string) string {
	return ruleBase + "/routes/" + objects.GatewayAPIGroup + "/" + kind + "/" + ns + "/" + name
}
func svcPath(ns, name string) string { return ruleBase + "/backends/-/Service/" + ns + "/" + name }

// relSubjects are the subject details the restricted viewer reads; relSurfaces add details
// it may not open (404 either way), where the hidden changes reach the full reader.
var relSubjects = []string{
	gwPath(ns0, relGW.Name), lsPath(ns0, lsAllName), lsPath(ns0, lsHop), lsPath(ns0, lsForeignHop),
	relRoutePath("HTTPRoute", ns0, relSect), relRoutePath("HTTPRoute", ns0, relUnsect), relRoutePath("HTTPRoute", ns0, relPort),
	relRoutePath("HTTPRoute", ns0, relLS), relRoutePath("GRPCRoute", ns0, relGRPC), relRoutePath("HTTPRoute", ns0, relHop),
	relRoutePath("HTTPRoute", ns0, relHop2), relRoutePath("HTTPRoute", kindLimitedNS, relForeign), relRoutePath("HTTPRoute", kindLimitedNS, limitedRoute),
	relRoutePath("HTTPRoute", ns0, namesHidden), svcPath(ns0, "shared"),
}

var relSurfaces = append(slices.Clone(relSubjects), gwPath(hiddenNS, "hidden-gw"), relRoutePath("HTTPRoute", hiddenNS, "hidden-route"),
	gwPath(kindLimitedNS, testutil.ScaleGateway(9)), gwPath(kindLimitedNS, tgtGwNew), gwPath(kindLimitedNS, tgtGwChanged), gwPath(kindLimitedNS, tgtGwDeleted),
	gwPath(hiddenNS, ancGwNew), gwPath(hiddenNS, ancGwChanged), gwPath(hiddenNS, ancGwDeleted))

// relBody is a subject detail's relations field.
type relBody struct {
	PolicyRelations *api.PolicyRelationsDTO `json:"policyRelations"`
}

// relStrings writes each relation as name[@section][:port][ via hop>hop], with its source.
func relStrings(rs []api.PolicyRelationDTO) []string {
	out := []string{}
	at := func(name, section string, port int32) string {
		s := name
		if section != "" {
			s += "@" + section
		}
		if port != 0 {
			s += fmt.Sprintf(":%d", port)
		}
		return s
	}
	for _, r := range rs {
		s := at(r.Policy.Name, r.SectionName, r.Port)
		var hops []string
		for _, h := range r.Via {
			hops = append(hops, at(h.Ref.Name, h.SectionName, h.Port))
		}
		if len(hops) > 0 {
			s += " via " + strings.Join(hops, ">")
		}
		out = append(out, s+" "+string(r.Source))
	}
	return out
}

// relGroup is one group's expected relations per viewer; nil means the group is omitted.
type relGroup struct{ full, restricted []string }

// Each subject detail carries its groups (design §6.5): targeting, targeting a parent through
// spec.parentRefs only (sectioned, unsectioned, port-only, ListenerSet two-hop) and reported
// attached here. A hidden policy, parent, hop or ownership hop makes no relation for the restricted viewer.
func TestPolicyRelationsPerSubject(t *testing.T) {
	srv := policyServeWith(t, relEngine(t, relChanges[0]), relChecker{}, nil)
	const d, c = "declared", "controller-reported"
	none := []string{}
	same := func(rs ...string) relGroup { return relGroup{rs, rs} }
	for _, tc := range []struct {
		path                               string
		targeting, parent, attached        *relGroup
		selectorsFull, selectorsRestricted []string
	}{
		{path: gwPath(ns0, relGW.Name),
			targeting: &relGroup{[]string{hiddenHLP + "@http " + d, "tp-gw " + d, "tp-https@https " + d, "tp-listener@http " + d},
				[]string{"tp-gw " + d, "tp-https@https " + d, "tp-listener@http " + d}},
			attached: &relGroup{[]string{hiddenHLP + " " + c, tpAncestors + " " + c, "tp-gw " + c}, []string{tpAncestors + " " + c, "tp-gw " + c}}},
		{path: lsPath(ns0, lsAllName),
			targeting: &relGroup{[]string{hiddenHLP + " " + d, "tp-ls " + d, "tp-ls-extra@extra " + d}, []string{"tp-ls " + d, "tp-ls-extra@extra " + d}},
			// A ListenerSet's own parentRef is unsectioned: only whole-Gateway policies relate.
			parent:   ptr(same("tp-gw via gw-000 " + d)),
			attached: ptr(same("tp-ls " + c))},
		// The ListenerSet's parent Gateway is hidden: no parent relation, and its ancestor
		// is not current for the restricted viewer, whose ownership hop is unreadable.
		{path: lsPath(ns0, lsHop),
			targeting: ptr(same("tp-ls-hop " + d)),
			parent:    &relGroup{[]string{"tp-limited via " + limitedGW + " " + d}, none},
			attached:  &relGroup{[]string{"tp-ls-hop " + c}, none}},
		{path: relRoutePath("HTTPRoute", ns0, relSect), targeting: ptr(same("tp-route " + d)),
			parent:        &relGroup{[]string{hiddenHLP + " via gw-000@http " + d, "tp-gw via gw-000@http " + d, "tp-listener via gw-000@http " + d}, []string{"tp-gw via gw-000@http " + d, "tp-listener via gw-000@http " + d}},
			selectorsFull: []string{"HTTPListenerPolicy", "TrafficPolicy"}, selectorsRestricted: []string{"TrafficPolicy"}},
		{path: relRoutePath("HTTPRoute", ns0, relUnsect), targeting: ptr(same()), parent: ptr(same("tp-gw via gw-000 " + d)),
			selectorsFull: []string{"HTTPListenerPolicy", "TrafficPolicy"}, selectorsRestricted: []string{"TrafficPolicy"}},
		{path: relRoutePath("HTTPRoute", ns0, relPort), targeting: ptr(same()), parent: ptr(same("tp-gw via gw-000:80 " + d)),
			selectorsFull: []string{"HTTPListenerPolicy", "TrafficPolicy"}, selectorsRestricted: []string{"TrafficPolicy"}},
		// Two hops: the ListenerSet's policies, sectioned or not, and its Gateway's unsectioned ones.
		{path: relRoutePath("HTTPRoute", ns0, relLS), targeting: ptr(same()),
			parent: &relGroup{[]string{hiddenHLP + " via ls-all@extra " + d, "tp-gw via ls-all@extra>gw-000 " + d, "tp-ls via ls-all@extra " + d, "tp-ls-extra via ls-all@extra " + d},
				[]string{"tp-gw via ls-all@extra>gw-000 " + d, "tp-ls via ls-all@extra " + d, "tp-ls-extra via ls-all@extra " + d}},
			selectorsFull: []string{"HTTPListenerPolicy", "TrafficPolicy"}, selectorsRestricted: []string{"TrafficPolicy"}},
		{path: relRoutePath("GRPCRoute", ns0, relGRPC), targeting: ptr(same("tp-route " + d)),
			parent: &relGroup{[]string{hiddenHLP + " via gw-000@http " + d, "tp-gw via gw-000@http " + d, "tp-listener via gw-000@http " + d}, []string{"tp-gw via gw-000@http " + d, "tp-listener via gw-000@http " + d}}},
		// A hidden second hop drops only the two-hop relation.
		{path: relRoutePath("HTTPRoute", ns0, relHop), targeting: ptr(same()),
			parent: &relGroup{[]string{"tp-gw via gw-000 " + d, "tp-ls-hop via ls-hop " + d, "tp-limited via ls-hop>" + limitedGW + " " + d},
				[]string{"tp-gw via gw-000 " + d, "tp-ls-hop via ls-hop " + d}},
			selectorsFull: []string{"HTTPListenerPolicy", "TrafficPolicy"}, selectorsRestricted: []string{"TrafficPolicy"}},
		// A hidden parent makes no relation.
		{path: relRoutePath("HTTPRoute", kindLimitedNS, limitedRoute), targeting: ptr(same()), parent: &relGroup{[]string{"tp-limited via " + limitedGW + " " + d}, none}},
		{path: svcPath(ns0, "shared"), targeting: ptr(same("bcp " + d)), attached: ptr(same("bcp:80 " + c))},
	} {
		for _, user := range []string{fullUser, restrictedUser} {
			full := user == fullUser
			code, body := fetch(t, srv, user, tc.path)
			var b relBody
			if code != 200 || json.Unmarshal([]byte(body), &b) != nil || b.PolicyRelations == nil {
				t.Errorf("%s as %s: %d, no policyRelations: %.300s", tc.path, user, code, body)
				continue
			}
			rel := b.PolicyRelations
			check := func(group string, want *relGroup, got *[]api.PolicyRelationDTO) {
				switch {
				case want == nil && got != nil:
					t.Errorf("%s as %s: %s present: %v", tc.path, user, group, relStrings(*got))
				case want != nil && got == nil:
					t.Errorf("%s as %s: %s omitted", tc.path, user, group)
				case want != nil:
					w := want.restricted
					if full {
						w = want.full
					}
					if g := relStrings(*got); !slices.Equal(g, w) {
						t.Errorf("%s as %s: %s\n got %q\nwant %q", tc.path, user, group, g, w)
					}
				}
			}
			check("targeting", tc.targeting, &rel.Targeting)
			check("targetingParent", tc.parent, rel.TargetingParent)
			check("attachedHere", tc.attached, rel.AttachedHere)
			var kinds []string
			for _, k := range rel.Selectors.Kinds {
				kinds = append(kinds, k.Kind)
			}
			want := tc.selectorsRestricted
			if full {
				want = tc.selectorsFull
			}
			if !slices.Equal(kinds, want) || rel.Selectors.Source != policy.SourceDeclared {
				t.Errorf("%s as %s: selectors %+v, want kinds %v", tc.path, user, rel.Selectors, want)
			}
			for _, r := range slices.Concat(rel.Targeting, deref(rel.TargetingParent), deref(rel.AttachedHere)) {
				if r.Policy.Link != api.LinkDetail || r.Policy.Group == "" {
					t.Errorf("%s as %s: policy %+v does not link its detail with its group", tc.path, user, r.Policy)
				}
			}
		}
	}
}

func ptr[T any](v T) *T { return &v }

func deref(p *[]api.PolicyRelationDTO) []api.PolicyRelationDTO {
	if p == nil {
		return nil
	}
	return *p
}

// An attached-on-ancestor relation carries the ancestor's Attached condition verbatim
// and the pack's granularity note: Gateway-wide on a Gateway, per port on a backend.
func TestPolicyRelationsAttachedCondition(t *testing.T) {
	srv := policyServeWith(t, relEngine(t, ruleChange{name: "base"}), relChecker{}, nil)
	for _, tc := range []struct{ path, policy, note string }{
		{gwPath(ns0, relGW.Name), "tp-gw", "gateway-wide"},
		{svcPath(ns0, "shared"), "bcp", "backend-port"},
	} {
		rel := decode[relBody](t, srv, restrictedUser, tc.path).PolicyRelations
		if rel == nil || rel.AttachedHere == nil {
			t.Fatalf("%s: no attachedHere", tc.path)
		}
		k := slices.IndexFunc(*rel.AttachedHere, func(r api.PolicyRelationDTO) bool { return r.Policy.Name == tc.policy })
		if k < 0 {
			t.Fatalf("%s: %s not attached here", tc.path, tc.policy)
		}
		r := (*rel.AttachedHere)[k]
		if r.Condition == nil || r.Condition.Type != "Attached" || r.Condition.Status != "True" || r.Condition.Reason != "Merged" ||
			r.Note != tc.note || r.NoteSource != policy.SourceInferred || r.Relation != policy.RelationAttachedOnAncestor {
			t.Errorf("%s: %+v (condition %+v), want Attached True Merged with note %s", tc.path, r, r.Condition, tc.note)
		}
	}
}

// relPairs serves the relation base world and each policy and relation change.
func relPairs(t *testing.T) []rulePair {
	t.Helper()
	before := policyServeWith(t, relEngine(t, ruleChange{name: "base"}), relChecker{}, nil)
	var out []rulePair
	for _, c := range slices.Concat(policyChanges, relChanges) {
		out = append(out, rulePair{change: c, before: before, after: policyServeWith(t, relEngine(t, c), relChecker{}, nil)})
	}
	return out
}

// A hidden policy, parent, hop or ancestor moves no byte of a subject detail the restricted
// viewer is served (design §7 item 5); each change reaches the full reader.
func TestPolicyRelationsHiddenChangesUnchanged(t *testing.T) {
	for _, p := range relPairs(t) {
		reached := false
		for _, path := range relSurfaces {
			bc, bb := fetch(t, p.before, restrictedUser, path)
			ac, ab := fetch(t, p.after, restrictedUser, path)
			if bc != ac || bb != ab {
				t.Errorf("%s: %s changed for the restricted viewer:\n  %d %.600s\n  vs %d %.600s", p.change.name, path, bc, bb, ac, ab)
			}
			_, fb := fetch(t, p.before, fullUser, path)
			_, fa := fetch(t, p.after, fullUser, path)
			reached = reached || fb != fa
		}
		if !reached {
			t.Errorf("%s reaches no relation surface for the full reader", p.change.name)
		}
	}
}

// The relation changes reach a relation, not only some other byte, for the full reader.
func TestPolicyRelationsChangesReachRelations(t *testing.T) {
	base := policyServeWith(t, relEngine(t, ruleChange{name: "base"}), relChecker{}, nil)
	for _, c := range relChanges {
		after := policyServeWith(t, relEngine(t, c), relChecker{}, nil)
		moved := false
		for _, path := range relSubjects {
			if code, _ := fetch(t, base, fullUser, path); code != 200 {
				t.Fatalf("%s: full reader gets %d", path, code)
			}
			b, a := decode[relBody](t, base, fullUser, path).PolicyRelations, decode[relBody](t, after, fullUser, path).PolicyRelations
			if b == nil || a == nil {
				t.Fatalf("%s: no policyRelations", path)
			}
			moved = moved || fmt.Sprint(relStrings(b.Targeting), relStrings(deref(b.TargetingParent)), relStrings(deref(b.AttachedHere)), b.Selectors) !=
				fmt.Sprint(relStrings(a.Targeting), relStrings(deref(a.TargetingParent)), relStrings(deref(a.AttachedHere)), a.Selectors)
		}
		if !moved {
			t.Errorf("%s moves no relation for the full reader: the byte test is vacuous for it", c.name)
		}
	}
}

// No label Snellius writes in a subject's relations says "effective", "applied" or "active".
func TestPolicyRelationsLabelsCopy(t *testing.T) {
	srv := policyServeWith(t, relEngine(t, relChanges[0]), relChecker{}, nil)
	ls := []string{string(policy.RelationTargets), string(policy.RelationTargetsParent), string(policy.RelationAttachedOnAncestor)}
	for _, path := range relSubjects {
		var v struct {
			PolicyRelations any `json:"policyRelations"`
		}
		if code, body := fetch(t, srv, fullUser, path); code != 200 || json.Unmarshal([]byte(body), &v) != nil || v.PolicyRelations == nil {
			t.Fatalf("%s: %d, no policyRelations", path, code)
		}
		labels(v.PolicyRelations, &ls)
	}
	for _, l := range ls {
		if forbidden.MatchString(l) {
			t.Errorf("label %q says %q", l, forbidden.FindString(l))
		}
	}
	if len(ls) < 100 {
		t.Fatalf("only %d labels checked", len(ls))
	}
}
