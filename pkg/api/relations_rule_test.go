package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/auth/sar"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/health"
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
				if r.Policy.Link != api.Link(policyPath(r.Policy.Group, r.Policy.Kind, r.Policy.Namespace, r.Policy.Name)) || r.Policy.Group == "" {
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

// A policy link is given only to a viewer who may get the policy by name, as its detail
// asks; the row and the relation stay for a member the authoriser denies by name (8.2 review #3).
func TestPolicyLinkOnlyByName(t *testing.T) {
	srv := policyServeWith(t, relEngine(t, ruleChange{name: "base"}), relChecker{}, func(a api.Authorizer) api.Authorizer {
		return api.Narrowed{Base: a, Policy: denyNamed{name: "tp-gw"}}
	})
	rel := decode[relBody](t, srv, fullUser, gwPath(ns0, relGW.Name)).PolicyRelations
	if rel == nil || rel.AttachedHere == nil {
		t.Fatal("no relations")
	}
	links := map[string]api.Link{}
	for _, r := range slices.Concat(rel.Targeting, *rel.AttachedHere) {
		links[r.Policy.Name] = r.Policy.Link
	}
	if l, ok := links["tp-gw"]; !ok || l != "" {
		t.Errorf("tp-gw relation present %v with link %q, want present without a link", ok, l)
	}
	if l := links["tp-listener"]; l != api.Link(tpPath(ns0, "tp-listener")) {
		t.Errorf("tp-listener link %q", l)
	}
	code, body := fetch(t, srv, fullUser, policiesPath+"?limit=200")
	if code != http.StatusOK || !strings.Contains(body, `"name":"tp-gw"}`) {
		t.Errorf("tp-gw row is missing or linked: %d %.300s", code, body)
	}
	if row := policyRowOf(t, decode[api.PoliciesResponse](t, srv, fullUser, policiesPath+"?limit=200"), kgwGroup, ns0, "tp-listener"); row.Ref.Link != api.Link(tpPath(ns0, "tp-listener")) {
		t.Errorf("tp-listener row link %q", row.Ref.Link)
	}
	if code, _ := fetch(t, srv, fullUser, tpPath(ns0, "tp-gw")); code != http.StatusNotFound {
		t.Errorf("tp-gw detail %d, want 404: the test does not deny the name", code)
	}
}

// fixtureDirectResponse is kgateway 2.3's DirectResponse fixture with a live-like status:
// in ns0, reported Accepted and Attached on gw-000 by the install's controller.
func fixtureDirectResponse(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "policy", "testdata", "kgateway-2.3.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for doc := range strings.SplitSeq(string(b), "\n---\n") {
		j, err := sigsyaml.YAMLToJSON([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		u := &unstructured.Unstructured{}
		if err := u.UnmarshalJSON(j); err != nil {
			t.Fatal(err)
		}
		if u.GetKind() != "DirectResponse" {
			continue
		}
		u.SetNamespace(ns0)
		as, _, _ := unstructured.NestedSlice(u.Object, "status", "ancestors")
		ref := as[0].(map[string]any)["ancestorRef"].(map[string]any)
		ref["namespace"], ref["name"] = ns0, relGW.Name
		_ = unstructured.SetNestedSlice(u.Object, as, "status", "ancestors")
		return u
	}
	t.Fatal("no DirectResponse in the 2.3 fixture")
	return nil
}

// A list-only policy's ancestors feed only its findings (D-129, design §6.6): a
// DirectResponse reported Attached on gw-000 is not "reported attached here" (8.2 review #2).
func TestListOnlyPolicyMakesNoAttachedRelation(t *testing.T) {
	dr := fixtureDirectResponse(t)
	srv := policyServeWith(t, relEngine(t, ruleChange{name: "dr", apply: func(s ruleSnap) { s.add(dr) }}), relChecker{}, nil)
	row := policyRowOf(t, decode[api.PoliciesResponse](t, srv, fullUser, policiesPath+"?limit=200"), kgwGroup, ns0, dr.GetName())
	if len(row.AttachedOn) != 1 {
		t.Fatalf("DirectResponse attached on %v: the ancestor is not current, so the test is vacuous", row.AttachedOn)
	}
	for _, user := range []string{fullUser, restrictedUser} {
		rel := decode[relBody](t, srv, user, gwPath(ns0, relGW.Name)).PolicyRelations
		for _, r := range slices.Concat(rel.Targeting, deref(rel.AttachedHere)) {
			if r.Policy.Kind == "DirectResponse" {
				t.Errorf("%s: DirectResponse relation %+v on gw-000", user, r)
			}
		}
	}
}

// failingTP is relChecker, except that a list of TrafficPolicies in the hidden namespace fails.
type failingTP struct{}

func (failingTP) Check(ctx context.Context, id oidc.Identity, a sar.Attributes) (bool, error) {
	if a.Resource == "trafficpolicies" && a.Namespace == hiddenNS {
		return false, errors.New("SAR unavailable")
	}
	return relChecker{}.Check(ctx, id, a)
}

// denyHiddenTP is a downstream authoriser that refuses TrafficPolicies in the hidden namespace.
type denyHiddenTP struct{}

func (denyHiddenTP) Authorize(_ *http.Request, ref objects.Ref) bool {
	return ref.Kind != "TrafficPolicy" || ref.Namespace != hiddenNS
}

func (d denyHiddenTP) AuthorizeNamedErr(r *http.Request, ref objects.Ref) (bool, error) {
	return d.Authorize(r, ref), nil
}

func (denyHiddenTP) CanList(_ *http.Request, gk objects.GroupKind, ns string) (bool, error) {
	return gk.Kind != "TrafficPolicy" || ns != hiddenNS, nil
}

// A policy the downstream authoriser denies asks no access check, so a failing one in its
// otherwise unqueried bucket cannot turn a readable subject detail into a 503 (8.2 review #1).
func TestDeniedPolicyAsksNoCheck(t *testing.T) {
	denied := tp(hiddenNS, "tp-denied-anc", []any{targetRef(gatewaysGroup, "HTTPRoute", "hidden-route", "")}, attachedAncestor(gw0()[0], "Merged"))
	narrow := func(a api.Authorizer) api.Authorizer { return api.Narrowed{Base: a, Policy: denyHiddenTP{}} }
	absent := policyServeWith(t, relEngine(t, ruleChange{name: "base"}), failingTP{}, narrow)
	present := policyServeWith(t, relEngine(t, ruleChange{name: "denied", apply: func(s ruleSnap) { s.add(denied) }}), failingTP{}, narrow)
	for _, path := range []string{gwPath(ns0, relGW.Name), svcPath(ns0, "shared"), lsPath(ns0, lsAllName)} {
		ac, ab := fetch(t, absent, restrictedUser, path)
		pc, pb := fetch(t, present, restrictedUser, path)
		if ac != http.StatusOK || pc != ac || pb != ab {
			t.Errorf("%s: %d without the denied policy, %d with it:\n%.300s", path, ac, pc, pb)
		}
	}
}

// genPolicy is a TrafficPolicy ns0/name at generation 2 on gw-000, reported with
// Accepted (reason) at generation 2 and Attached (status) at generation 1.
func genPolicy(name string, accepted metav1.ConditionStatus, reason string, attached metav1.ConditionStatus) *unstructured.Unstructured {
	acc, att := testutil.Cond("Accepted", accepted, reason), testutil.Cond("Attached", attached, "Pending")
	acc.ObservedGeneration = 2
	if attached == metav1.ConditionTrue {
		att.Reason = "Attached"
	}
	u := tp(ns0, name, []any{targetGW("")}, testutil.PolicyAncestor{Ref: gw0()[0], Controller: testutil.Controller, Conds: []metav1.Condition{acc, att}})
	u.SetGeneration(2)
	return u
}

// A policy ancestor's freshness follows Accepted's generation alone (D-135): a current
// rejection beside an older Attached raises KGWUI-002, not "not yet processed", and counts
// in the summary; an older Attached=True is no attachment evidence (8.2 review #5).
func TestPolicyFreshnessOnAcceptedAlone(t *testing.T) {
	rejected, olderAttached := genPolicy("tp-rejected", metav1.ConditionFalse, "Invalid", metav1.ConditionFalse),
		genPolicy("tp-older-attached", metav1.ConditionTrue, "Accepted", metav1.ConditionTrue)
	srv := policyServeWith(t, relEngine(t, ruleChange{name: "gen", apply: func(s ruleSnap) { s.add(rejected, olderAttached) }}), relChecker{}, nil)
	codes := map[string]string{}
	for _, f := range decode[api.Page[api.FindingRow]](t, srv, fullUser, ruleBase+"/findings?limit=200").Items {
		if f.Resource != nil && f.Resource.Kind == "TrafficPolicy" && f.Resource.Namespace == ns0 {
			codes[f.Resource.Name] += f.Code + " "
		}
	}
	if codes["tp-rejected"] != health.CodeConditionFalse+" " || codes["tp-older-attached"] != "" {
		t.Errorf("findings %v, want KGWUI-002 on tp-rejected only", codes)
	}
	rows := decode[api.PoliciesResponse](t, srv, fullUser, policiesPath+"?limit=200")
	if r := policyRowOf(t, rows, kgwGroup, ns0, "tp-rejected"); r.Accepted == nil || r.Accepted.Status != metav1.ConditionFalse || !r.Problems.Has {
		t.Errorf("tp-rejected row %+v: want Accepted False with problems", r)
	}
	if r := policyRowOf(t, rows, kgwGroup, ns0, "tp-older-attached"); r.Accepted == nil || r.Accepted.Status != metav1.ConditionTrue || len(r.AttachedOn) != 0 {
		t.Errorf("tp-older-attached row %+v: want current Accepted True and no attachedOn", r)
	}
	rel := decode[relBody](t, srv, fullUser, gwPath(ns0, relGW.Name)).PolicyRelations
	for _, r := range deref(rel.AttachedHere) {
		if r.Policy.Name == "tp-older-attached" || r.Policy.Name == "tp-rejected" {
			t.Errorf("%s reported attached on gw-000", r.Policy.Name)
		}
	}
}

// A KGWUI-002 about a ListenerSet ancestor rests on its parent Gateway too, as the policy
// page's currentness does (D-134): a viewer who may not read that Gateway is not shown it (8.2 review #6).
func TestListenerSetAncestorFindingNeedsItsGateway(t *testing.T) {
	hop := objects.ParentRef{Group: objects.GatewayAPIGroup, Kind: "ListenerSet", Namespace: ns0, Name: lsHop}
	rejected := tp(ns0, "tp-ls-hop-rejected", []any{targetRef(gatewaysGroup, "ListenerSet", lsHop, "")},
		ancestor(hop, testutil.Controller, metav1.ConditionFalse, metav1.ConditionFalse))
	srv := policyServeWith(t, relEngine(t, ruleChange{name: "rejected", apply: func(s ruleSnap) { s.add(rejected) }}), relChecker{}, nil)
	shown := func(user string) bool {
		for _, f := range decode[api.Page[api.FindingRow]](t, srv, user, ruleBase+"/findings?limit=200").Items {
			if f.Code == health.CodeConditionFalse && f.Resource != nil && f.Resource.Name == rejected.GetName() {
				return true
			}
		}
		return false
	}
	if !shown(fullUser) {
		t.Fatal("the full reader sees no KGWUI-002 on tp-ls-hop-rejected: the test is vacuous")
	}
	if shown(restrictedUser) {
		t.Error("the restricted viewer, who may not read ls-hop's Gateway, is shown the KGWUI-002 about ls-hop")
	}
	d := decode[api.PolicyDetail](t, srv, restrictedUser, tpPath(ns0, rejected.GetName()))
	if len(d.Ancestors) != 1 || d.Ancestors[0].NotCurrent != policy.NotCurrentUnreadable {
		t.Errorf("restricted ancestor %+v, want unreadable", d.Ancestors)
	}
}

// failingHLP is relChecker, except that a check on HTTPListenerPolicies in the hidden namespace fails.
type failingHLP struct{}

func (failingHLP) Check(ctx context.Context, id oidc.Identity, a sar.Attributes) (bool, error) {
	if a.Resource == "httplistenerpolicies" && a.Namespace == hiddenNS {
		return false, errors.New("SAR unavailable")
	}
	return relChecker{}.Check(ctx, id, a)
}

// denyHiddenHLP is a downstream authoriser that refuses HTTPListenerPolicies in the hidden namespace.
type denyHiddenHLP struct{}

func (denyHiddenHLP) Authorize(_ *http.Request, ref objects.Ref) bool {
	return ref.Kind != "HTTPListenerPolicy" || ref.Namespace != hiddenNS
}

func (d denyHiddenHLP) AuthorizeNamedErr(r *http.Request, ref objects.Ref) (bool, error) {
	return d.Authorize(r, ref), nil
}

func (denyHiddenHLP) CanList(_ *http.Request, gk objects.GroupKind, ns string) (bool, error) {
	return gk.Kind != "HTTPListenerPolicy" || ns != hiddenNS, nil
}

// The policies list, its facets and totals, and the policy detail ask no check for a policy the
// downstream authoriser denies: a failing one in an otherwise unqueried bucket answers as if the
// policy were absent, never 503 (8.2 review #1, list).
func TestDeniedPolicyAsksNoCheckInPolicies(t *testing.T) {
	denied := testutil.Policy(kinds.KgatewayGroup+"/v1alpha1", "HTTPListenerPolicy", hiddenNS, "hlp-denied", attachedAncestor(hiddenGwRef("hidden-gw"), "Attached"))
	denied.Object["spec"] = map[string]any{"targetRefs": []any{targetRef(gatewaysGroup, "Gateway", "hidden-gw", "")}}
	narrow := func(a api.Authorizer) api.Authorizer { return api.Narrowed{Base: a, Policy: denyHiddenHLP{}} }
	absent := policyServeWith(t, relEngine(t, ruleChange{name: "base"}), failingHLP{}, narrow)
	present := policyServeWith(t, relEngine(t, ruleChange{name: "denied", apply: func(s ruleSnap) { s.add(denied) }}), failingHLP{}, narrow)
	paths := []string{policiesPath + "?limit=200", policiesPath + "?limit=200&hasProblems=true", policiesPath + "?limit=1",
		policiesPath + "?limit=200&kind=" + kgwGroup + "/HTTPListenerPolicy", policyPath(kgwGroup, "HTTPListenerPolicy", hiddenNS, "hlp-denied")}
	for k, path := range paths {
		ac, ab := fetch(t, absent, restrictedUser, path)
		pc, pb := fetch(t, present, restrictedUser, path)
		want := http.StatusOK
		if k == len(paths)-1 {
			want = http.StatusNotFound
		}
		if ac != want || pc != ac || pb != ab {
			t.Errorf("%s: %d without the denied policy, %d with it:\n%.300s", path, ac, pc, pb)
		}
	}
	// Without the downstream authoriser, the full reader lists it: the policy is in the install.
	plain := policyServeWith(t, relEngine(t, ruleChange{name: "denied", apply: func(s ruleSnap) { s.add(denied) }}), failingHLP{}, nil)
	if code, body := fetch(t, plain, fullUser, paths[0]); code != http.StatusOK || !strings.Contains(body, "hlp-denied") {
		t.Errorf("the full reader does not list hlp-denied (%d): the test is vacuous", code)
	}
}

// Each summary names its source, also when it is empty: Accepted is inferred, and
// attachedOn is controller-reported when it lists ancestors, inferred when Snellius
// found none to list (8.2 re-review: summary sources).
func TestPolicySummarySources(t *testing.T) {
	srv := policyServeWith(t, relEngine(t, ruleChange{name: "base"}), relChecker{}, nil)
	type sources struct {
		Name             string `json:"name"`
		AcceptedSource   string `json:"acceptedSource"`
		AttachedOnSource string `json:"attachedOnSource"`
	}
	var list struct {
		Items []struct {
			Ref struct {
				Name string `json:"name"`
			} `json:"ref"`
			AcceptedSource   string `json:"acceptedSource"`
			AttachedOnSource string `json:"attachedOnSource"`
		} `json:"items"`
	}
	_, body := fetch(t, srv, restrictedUser, policiesPath+"?limit=200&namespace="+ns0)
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	got := map[string]sources{}
	for _, r := range list.Items {
		got[r.Ref.Name] = sources{r.Ref.Name, r.AcceptedSource, r.AttachedOnSource}
	}
	for name, want := range map[string]string{"tp-gw": "controller-reported", "tp-listener": "inferred"} {
		if s := got[name]; s.AcceptedSource != "inferred" || s.AttachedOnSource != want {
			t.Errorf("row %s: %+v, want acceptedSource inferred and attachedOnSource %s", name, s, want)
		}
		var d sources
		_, body := fetch(t, srv, restrictedUser, tpPath(ns0, name))
		if err := json.Unmarshal([]byte(body), &d); err != nil || d.AcceptedSource != "inferred" || d.AttachedOnSource != want {
			t.Errorf("detail %s: %+v, want acceptedSource inferred and attachedOnSource %s", name, d, want)
		}
	}
}

// An Attached condition older than the policy's generation is ignored beside a current
// Accepted=True: a negative one is no problem and raises no KGWUI-002 (8.2 re-review: old Attached).
func TestOlderNegativeAttachedIgnored(t *testing.T) {
	acc, att := testutil.Cond("Accepted", metav1.ConditionTrue, "Accepted"), testutil.Cond("Attached", metav1.ConditionFalse, "Invalid")
	acc.ObservedGeneration = 2
	u := tp(ns0, "tp-old-negative", []any{targetGW("")}, testutil.PolicyAncestor{Ref: gw0()[0], Controller: testutil.Controller, Conds: []metav1.Condition{acc, att}})
	u.SetGeneration(2)
	srv := policyServeWith(t, relEngine(t, ruleChange{name: "old", apply: func(s ruleSnap) { s.add(u) }}), relChecker{}, nil)
	for _, f := range decode[api.Page[api.FindingRow]](t, srv, fullUser, ruleBase+"/findings?limit=200").Items {
		if f.Resource != nil && f.Resource.Name == u.GetName() {
			t.Errorf("finding %s on tp-old-negative", f.Code)
		}
	}
	row := policyRowOf(t, decode[api.PoliciesResponse](t, srv, fullUser, policiesPath+"?limit=200"), kgwGroup, ns0, u.GetName())
	if row.Problems.Has || row.Accepted == nil || row.Accepted.Status != metav1.ConditionTrue {
		t.Errorf("row %+v: want current Accepted True and no problem", row)
	}
	if d := decode[api.PolicyDetail](t, srv, fullUser, tpPath(ns0, u.GetName())); d.Problems.Has {
		t.Error("detail has problems")
	}
}

// failingFullRead is relChecker, except that every cluster-wide list fails, so a full-read probe fails.
type failingFullRead struct{}

func (failingFullRead) Check(ctx context.Context, id oidc.Identity, a sar.Attributes) (bool, error) {
	if a.Verb == "list" && a.Namespace == "" {
		return false, errors.New("SAR unavailable")
	}
	return relChecker{}.Check(ctx, id, a)
}

// The policy detail asks about the name before any full-read probe: a policy the downstream
// authoriser denies, present or absent, answers 404 without it (8.2 re-review: named first, D-137).
func TestPolicyDetailNamedBeforeFullRead(t *testing.T) {
	denied := testutil.Policy(kinds.KgatewayGroup+"/v1alpha1", "HTTPListenerPolicy", hiddenNS, "hlp-denied", attachedAncestor(hiddenGwRef("hidden-gw"), "Attached"))
	narrow := func(a api.Authorizer) api.Authorizer { return api.Narrowed{Base: a, Policy: denyHiddenHLP{}} }
	srv := policyServeWith(t, relEngine(t, ruleChange{name: "denied", apply: func(s ruleSnap) { s.add(denied) }}), failingFullRead{}, narrow)
	for _, name := range []string{"hlp-denied", "hlp-absent"} {
		if code, body := fetch(t, srv, restrictedUser, policyPath(kgwGroup, "HTTPListenerPolicy", hiddenNS, name)); code != http.StatusNotFound {
			t.Errorf("%s: %d %.200s, want 404", name, code, body)
		}
	}
}

// A policy-ancestor KGWUI-002 needs list and named access to each object it rests on, as
// the policy page's currentness does: a viewer who may list gw-000 but not get it by name sees
// the ancestor unreadable and is not shown the finding (8.2 re-review: 002 basis).
func TestPolicyAncestorFindingNeedsListAndName(t *testing.T) {
	rejected := tp(ns0, "tp-gw-rejected", []any{targetGW("")}, ancestor(gw0()[0], testutil.Controller, metav1.ConditionFalse, metav1.ConditionFalse))
	e := relEngine(t, ruleChange{name: "rejected", apply: func(s ruleSnap) { s.add(rejected) }})
	plain := policyServeWith(t, e, relChecker{}, nil)
	narrowed := policyServeWith(t, e, relChecker{}, func(a api.Authorizer) api.Authorizer {
		return api.Narrowed{Base: a, Policy: denyNamed{name: relGW.Name}}
	})
	shown := func(srv *httptest.Server) bool {
		for _, f := range decode[api.Page[api.FindingRow]](t, srv, restrictedUser, ruleBase+"/findings?limit=200").Items {
			if f.Code == health.CodeConditionFalse && f.Resource != nil && f.Resource.Name == rejected.GetName() {
				return true
			}
		}
		return false
	}
	if !shown(plain) {
		t.Fatal("the restricted viewer sees no KGWUI-002 on tp-gw-rejected without the narrowing: the test is vacuous")
	}
	if shown(narrowed) {
		t.Error("a viewer denied gw-000 by name is shown the KGWUI-002 about it")
	}
	if d := decode[api.PolicyDetail](t, narrowed, restrictedUser, tpPath(ns0, rejected.GetName())); len(d.Ancestors) != 1 || d.Ancestors[0].NotCurrent != policy.NotCurrentUnreadable {
		t.Errorf("ancestor %+v, want unreadable", d.Ancestors)
	}
}

// A policy link anywhere needs named get on the policy, as its detail does: a finding on a
// policy the viewer may not get by name keeps its resource but links nowhere (8.2 re-review: links).
func TestPolicyFindingLinkNeedsName(t *testing.T) {
	rejected := tp(ns0, "tp-rejected-link", []any{targetGW("")}, ancestor(gw0()[0], testutil.Controller, metav1.ConditionFalse, metav1.ConditionFalse))
	e := relEngine(t, ruleChange{name: "rejected", apply: func(s ruleSnap) { s.add(rejected) }})
	for _, c := range []struct {
		narrow func(api.Authorizer) api.Authorizer
		want   api.Link
	}{
		{nil, api.Link(tpPath(ns0, rejected.GetName()))},
		{func(a api.Authorizer) api.Authorizer {
			return api.Narrowed{Base: a, Policy: denyNamed{name: rejected.GetName()}}
		}, api.LinkNone},
	} {
		srv := policyServeWith(t, e, relChecker{}, c.narrow)
		var id string
		for _, f := range decode[api.Page[api.FindingRow]](t, srv, restrictedUser, ruleBase+"/findings?limit=200").Items {
			if f.Resource != nil && f.Resource.Name == rejected.GetName() {
				id = f.ID
				if f.Resource.Link != c.want {
					t.Errorf("findings list: link %q, want %q", f.Resource.Link, c.want)
				}
			}
		}
		if id == "" {
			t.Fatal("no finding on tp-rejected-link")
		}
		// The finding detail reads its resource by name, so a policy denied by name has none.
		code, body := fetch(t, srv, restrictedUser, ruleBase+"/findings/"+id)
		switch {
		case c.want == api.LinkNone && code != http.StatusNotFound:
			t.Errorf("finding detail %d for a policy denied by name", code)
		case c.want != api.LinkNone && (code != http.StatusOK || !strings.Contains(body, `"link":"`+string(c.want)+`"`)):
			t.Errorf("finding detail %d does not link %q: %.300s", code, c.want, body)
		}
	}
}

// denyPolicy is a downstream authoriser that refuses one policy, as a member and by name.
type denyPolicy struct{ name string }

func (d denyPolicy) Authorize(_ *http.Request, ref objects.Ref) bool {
	return ref.Kind != "TrafficPolicy" || ref.Name != d.name
}

func (d denyPolicy) AuthorizeNamedErr(r *http.Request, ref objects.Ref) (bool, error) {
	return d.Authorize(r, ref), nil
}

func (denyPolicy) CanList(*http.Request, objects.GroupKind, string) (bool, error) { return true, nil }

// With every check answering, a policy the downstream authoriser denies changes no byte or
// status of the policy list, the policy detail or a subject detail (D-085, D-137).
func TestDownstreamDeniedPolicyChangesNothing(t *testing.T) {
	down := tp(ns0, "tp-down", []any{targetGW("http"), targetRef(gatewaysGroup, "ListenerSet", lsAllName, ""), targetRef(gatewaysGroup, "HTTPRoute", relSect, "")},
		attachedAncestor(gw0()[0], "Merged"), attachedAncestor(lsAllRef, "Attached"))
	down.Object["spec"].(map[string]any)["targetSelectors"] = []any{map[string]any{"group": gatewaysGroup, "kind": "HTTPRoute", "matchLabels": map[string]any{"app": "x"}}}
	add := ruleChange{name: "down", apply: func(s ruleSnap) { s.add(down) }}
	narrow := func(a api.Authorizer) api.Authorizer {
		return api.Narrowed{Base: a, Policy: denyPolicy{name: "tp-down"}}
	}
	absent := policyServeWith(t, relEngine(t, ruleChange{name: "base"}), relChecker{}, narrow)
	present := policyServeWith(t, relEngine(t, add), relChecker{}, narrow)
	surfaces := append(slices.Clone(relSubjects), slices.Concat(policySurfaces[:6], []string{tpPath(ns0, "tp-down")})...)
	for _, path := range surfaces {
		ac, ab := fetch(t, absent, restrictedUser, path)
		pc, pb := fetch(t, present, restrictedUser, path)
		if ac != pc || ab != pb {
			t.Errorf("%s changed for the restricted viewer:\n  %d %.400s\n  vs %d %.400s", path, ac, ab, pc, pb)
		}
	}
	// Without the downstream authoriser the policy reaches the restricted viewer.
	plainAbsent := policyServeWith(t, relEngine(t, ruleChange{name: "base"}), relChecker{}, nil)
	plainPresent := policyServeWith(t, relEngine(t, add), relChecker{}, nil)
	moved := 0
	for _, path := range surfaces {
		_, a := fetch(t, plainAbsent, restrictedUser, path)
		if _, p := fetch(t, plainPresent, restrictedUser, path); a != p {
			moved++
		}
	}
	if moved < 5 {
		t.Errorf("tp-down moves only %d surfaces without the downstream authoriser: the test is weak", moved)
	}
}

// A policy ref never falls back to a backend link: a readable route that writes the policy
// as a backendRef does not make a policy denied by name link anywhere (8.2 re-review 2: backend link).
func TestPolicyNeverBackendLink(t *testing.T) {
	rejected := tp(ns0, "tp-as-backend", []any{targetGW("")}, ancestor(gw0()[0], testutil.Controller, metav1.ConditionFalse, metav1.ConditionFalse))
	route := accepted(ns0, "rel-to-policy", gw0()...)
	route.Object["spec"].(map[string]any)["rules"] = []any{map[string]any{"backendRefs": []any{
		map[string]any{"group": kgwGroup, "kind": "TrafficPolicy", "name": rejected.GetName()}}}}
	e := relEngine(t, ruleChange{name: "as-backend", apply: func(s ruleSnap) { s.add(rejected, route) }})
	srv := policyServeWith(t, e, relChecker{}, func(a api.Authorizer) api.Authorizer {
		return api.Narrowed{Base: a, Policy: denyNamed{name: rejected.GetName()}}
	})
	found := false
	for _, f := range decode[api.Page[api.FindingRow]](t, srv, restrictedUser, ruleBase+"/findings?limit=200").Items {
		if f.Resource != nil && f.Resource.Name == rejected.GetName() {
			found = true
			if f.Resource.Link != api.LinkNone {
				t.Errorf("finding %s links %q, want none", f.Code, f.Resource.Link)
			}
		}
	}
	if !found {
		t.Fatal("no finding on tp-as-backend: the test is vacuous")
	}
}

// The strict basis asks what the policy page asks: list on the kind in the namespace and get
// by name. A viewer whose authoriser refuses the Gateway list in ns0, while it allows the member
// and the name, sees gw-000 unreadable and no KGWUI-002 about it (8.2 re-review 2: strict basis).
func TestPolicyAncestorFindingNeedsList(t *testing.T) {
	rejected := tp(ns0, "tp-gw-unlisted", []any{targetGW("")}, ancestor(gw0()[0], testutil.Controller, metav1.ConditionFalse, metav1.ConditionFalse))
	srv := policyServeWith(t, relEngine(t, ruleChange{name: "rejected", apply: func(s ruleSnap) { s.add(rejected) }}), relChecker{}, func(a api.Authorizer) api.Authorizer {
		return api.Narrowed{Base: a, Policy: denyList{ns: ns0, kinds: []string{"Gateway"}}}
	})
	if d := decode[api.PolicyDetail](t, srv, restrictedUser, tpPath(ns0, rejected.GetName())); len(d.Ancestors) != 1 || d.Ancestors[0].NotCurrent != policy.NotCurrentUnreadable {
		t.Fatalf("ancestor %+v, want unreadable", d.Ancestors)
	}
	for _, f := range decode[api.Page[api.FindingRow]](t, srv, restrictedUser, ruleBase+"/findings?limit=200").Items {
		if f.Code == health.CodeConditionFalse && f.Resource != nil && f.Resource.Name == rejected.GetName() {
			t.Error("a KGWUI-002 about gw-000 is shown to a viewer who may not list Gateways in ns-000")
		}
	}
}
