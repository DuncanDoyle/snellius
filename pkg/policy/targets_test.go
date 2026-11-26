package policy_test

import (
	"fmt"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/snellius/pkg/computed"
	"github.com/DuncanDoyle/snellius/pkg/computed/kgw23"
	"github.com/DuncanDoyle/snellius/pkg/computed/kgw24"
	"github.com/DuncanDoyle/snellius/pkg/computed/kgw25"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/policy"
)

// alwaysFound is a downstream targeter that ignores watch state; the gate must still apply.
type alwaysFound struct{}

func (alwaysFound) PolicyTargets(in computed.TargetInput) []computed.TargetResult {
	var out []computed.TargetResult
	for i, r := range in.TargetRefs {
		out = append(out, computed.TargetResult{Index: i, Ref: r, Outcome: computed.TargetFound})
	}
	return out
}

// Target checks go through the rule set's PolicyTargeter and are unknown for every
// case of design §5.2 and §7 item 4; selectors get none.
func TestCheckTargets(t *testing.T) {
	const ns = "pt"
	gk := func(kind string) objects.GroupKind { return objects.GroupKind{Group: gw, Kind: kind} }
	u := trafficPolicy(ns, "p", []map[string]any{
		targetRef(gw, "Gateway", "gw", ""),            // 0 found
		targetRef(gw, "Gateway", "gw", "http"),        // 1 found
		targetRef(gw, "Gateway", "gw", "nope"),        // 2 section not found
		targetRef(gw, "HTTPRoute", "missing", ""),     // 3 not found
		targetRef("", "Service", "svc", "http"),       // 4 a section form the targeter does not decode
		targetRef(gw, "HTTPRoute", "r1", "rule-a"),    // 5 found
		targetRef(gw, "HTTPRoute", "r1", "rule-zzz")}) // 6 section not found
	u.Object["spec"].(map[string]any)["targetSelectors"] = []any{map[string]any{"group": gw, "kind": "HTTPRoute", "matchLabels": map[string]any{"a": "b"}}}
	p := policy.Read(u, nil, policy.Guard{})
	gwRef := objects.Ref{Group: gw, Kind: "Gateway", Namespace: ns, Name: "gw"}

	const (
		F  = computed.TargetFound
		NF = computed.TargetNotFound
		SN = computed.TargetSectionNotFound
		U  = computed.TargetUnknown
	)
	full := []string{F, F, SN, NF, U, F, SN}
	allUnknown := []string{U, U, U, U, U, U, U}
	cases := []struct {
		name     string
		targeter computed.PolicyTargeter
		scope    bool
		edit     func(w *world)
		noLookup bool
		want     []string
	}{
		{name: "full reader, 2.4 rules", targeter: rules(kgw24.New()), scope: true, want: full},
		{name: "full reader, 2.3 rules", targeter: rules(kgw23.New()), scope: true, want: full},
		{name: "2.5 explicit refs are unknown", targeter: rules(kgw25.New()), scope: true, want: allUnknown},
		{name: "no targeter", scope: true, want: allUnknown},
		{name: "install scope unknown", targeter: rules(kgw24.New()), want: allUnknown},
		{name: "no lookup", targeter: rules(kgw24.New()), scope: true, noLookup: true, want: allUnknown},
		{name: "HTTPRoute watch denied, pending or scoped away", targeter: rules(kgw24.New()), scope: true,
			edit: func(w *world) { w.unwatched[kindKey(gk("HTTPRoute"), ns)] = true }, want: []string{F, F, SN, U, U, U, U}},
		{name: "Gateway kind not served", targeter: rules(kgw24.New()), scope: true,
			edit: func(w *world) { w.unwatched[kindKey(gk("Gateway"), ns)] = true }, want: []string{U, U, U, NF, U, F, SN}},
		{name: "list allowed, named target denied (D-119)", targeter: rules(kgw24.New()), scope: true,
			edit: func(w *world) { w.hidden[gwRef] = true }, want: []string{U, U, U, NF, U, F, SN}},
		{name: "no list on HTTPRoutes in the namespace: every route verdict unknown", targeter: rules(kgw24.New()), scope: true,
			edit: func(w *world) { w.unlisted[kindKey(gk("HTTPRoute"), ns)] = true }, want: []string{F, F, SN, U, U, U, U}},
		{name: "a targeter that ignores watch state is still gated", targeter: alwaysFound{}, scope: true,
			edit: func(w *world) { w.unwatched[kindKey(gk("Gateway"), ns)] = true }, want: []string{U, U, U, F, F, F, F}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(gateway(ns, "gw", "http"), route(ns, "r1", map[string]any{"name": "gw"}))
			if c.edit != nil {
				c.edit(w)
			}
			in := policy.Inputs{Lookup: w, Targeter: c.targeter, ScopeKnown: c.scope}
			if c.noLookup {
				in.Lookup = nil
			}
			checks := policy.CheckTargets(p, in)
			var got []string
			for i, ch := range checks {
				if ch.Index != i {
					t.Errorf("check %d has index %d", i, ch.Index)
				}
				wantSource := policy.SourceInferred
				if ch.Outcome == U {
					wantSource = policy.SourceUnknown
				}
				if ch.Source != wantSource {
					t.Errorf("check %d: outcome %s source %s", i, ch.Outcome, ch.Source)
				}
				got = append(got, ch.Outcome)
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("outcomes %v, want %v", got, c.want)
			}
		})
	}
}

// peeker is a downstream targeter that consults Gateway pt/secret, or any other
// target of the policy, before it answers: found when it sees one, else section not found.
type peeker struct{}

func (peeker) PolicyTargets(in computed.TargetInput) []computed.TargetResult {
	_, seen := in.Lookup(objects.GroupKind{Group: gw, Kind: "Gateway"}, "pt", "secret")
	for _, r := range in.TargetRefs[1:] {
		_, ok := in.Lookup(objects.GroupKind{Group: r.Group, Kind: r.Kind}, in.Policy.Namespace, r.Name)
		seen = seen || ok
	}
	out := []computed.TargetResult{{Index: 0, Ref: in.TargetRefs[0], Outcome: computed.TargetSectionNotFound}}
	if seen {
		out[0].Outcome = computed.TargetFound
	}
	return out
}

// A targeter reads only the target it checks: a hidden object it consults, or the
// policy's other target, never changes a readable target's check (8.1 review #4).
func TestCheckTargetsLookupLimitedToTarget(t *testing.T) {
	const ns = "pt"
	u := trafficPolicy(ns, "p", []map[string]any{targetRef(gw, "HTTPRoute", "r1", "rule-a"), targetRef(gw, "Gateway", "other", "")})
	p := policy.Read(u, nil, policy.Guard{})
	secret, other := objects.Ref{Group: gw, Kind: "Gateway", Namespace: ns, Name: "secret"}, objects.Ref{Group: gw, Kind: "Gateway", Namespace: ns, Name: "other"}
	check := func(objs ...*unstructured.Unstructured) string {
		w := newWorld(append(objs, route(ns, "r1", map[string]any{"name": "gw"}))...)
		w.hidden[secret], w.hidden[other] = true, true
		return policy.CheckTargets(p, policy.Inputs{Lookup: w, Targeter: peeker{}, ScopeKnown: true})[0].Outcome
	}
	absent := check()
	for name, present := range map[string]string{"hidden object": check(gateway(ns, "secret")), "other target": check(gateway(ns, "other"))} {
		if present != absent {
			t.Errorf("%s: check %s when present, %s when absent", name, present, absent)
		}
	}
}

// counting is a world that counts the access questions asked of it.
type counting struct {
	*world
	gets, lists int
}

func (c *counting) CanGet(r objects.Ref) bool { c.gets++; return c.world.CanGet(r) }

func (c *counting) CanList(gk objects.GroupKind, ns string) bool {
	c.lists++
	return c.world.CanList(gk, ns)
}

// An outcome the targeter leaves unknown, as 2.5 does for explicit refs, asks
// no access question: no check is spent on a verdict that cannot change (8.1 re-review: budget).
func TestCheckTargetsUnknownAsksNothing(t *testing.T) {
	u := trafficPolicy("pt", "p", []map[string]any{targetRef(gw, "Gateway", "gw", ""), targetRef(gw, "HTTPRoute", "missing", "")})
	c := &counting{world: newWorld(gateway("pt", "gw", "http"))}
	checks := policy.CheckTargets(policy.Read(u, nil, policy.Guard{}), policy.Inputs{Lookup: c, Targeter: rules(kgw25.New()), ScopeKnown: true})
	if len(checks) != 2 || checks[0].Outcome != computed.TargetUnknown || c.gets+c.lists != 0 {
		t.Errorf("checks %+v asked %d gets, %d lists; want unknown and none", checks, c.gets, c.lists)
	}
}

// A policy with no targetRefs, or selectors only, has no checks.
func TestCheckTargetsSelectorsOnly(t *testing.T) {
	u := obj(kgw, "v1alpha1", "TrafficPolicy", "pt", "sel", map[string]any{
		"targetSelectors": []any{map[string]any{"group": gw, "kind": "HTTPRoute", "matchLabels": map[string]any{"a": "b"}}}})
	in := policy.Inputs{Lookup: newWorld(), Targeter: rules(kgw24.New()), ScopeKnown: true}
	if got := policy.CheckTargets(policy.Read(u, nil, policy.Guard{}), in); len(got) != 0 {
		t.Errorf("checks %s, want none", fmt.Sprint(got))
	}
}

// rules is a rule set's PolicyTargeter, as the install's Selection gives it.
func rules(r computed.Rules) computed.PolicyTargeter {
	return computed.Selection{Rules: r}.PolicyTargeter()
}
