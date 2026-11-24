package policy_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/policy"
)

// relationWorld is a Gateway infra/gw with listeners http and https, a ListenerSet
// infra/ls on it, routes in shop with each parentRef form, and policies on each.
func relationWorld() (*world, []*unstructured.Unstructured) {
	gwRef := map[string]any{"group": gw, "kind": "Gateway", "name": "gw", "namespace": "infra"}
	ls := obj(gw, "v1", "ListenerSet", "infra", "ls", map[string]any{"parentRef": map[string]any{"name": "gw"},
		"listeners": []any{map[string]any{"name": "extra", "protocol": "HTTP", "port": int64(8082)}}})
	objs := []*unstructured.Unstructured{
		gateway("infra", "gw", "http", "https"), ls,
		route("shop", "r-sectioned", map[string]any{"namespace": "infra", "name": "gw", "sectionName": "http"}),
		route("shop", "r-unsectioned", map[string]any{"namespace": "infra", "name": "gw"}),
		route("shop", "r-port", map[string]any{"namespace": "infra", "name": "gw", "port": int64(8080)}),
		route("shop", "r-ls", map[string]any{"group": gw, "kind": "ListenerSet", "namespace": "infra", "name": "ls"}),
		route("shop", "r-two", map[string]any{"namespace": "infra", "name": "gw", "sectionName": "http"},
			map[string]any{"namespace": "infra", "name": "gw", "sectionName": "https"}),
		obj("", "v1", "Service", "shop", "backend", map[string]any{}),
	}
	policies := []*unstructured.Unstructured{
		trafficPolicy("infra", "p-gw", []map[string]any{targetRef(gw, "Gateway", "gw", "")}),
		trafficPolicy("infra", "p-gw-http", []map[string]any{targetRef(gw, "Gateway", "gw", "http")}),
		trafficPolicy("infra", "p-gw-https", []map[string]any{targetRef(gw, "Gateway", "gw", "https")}),
		trafficPolicy("infra", "p-ls", []map[string]any{targetRef(gw, "ListenerSet", "ls", "")}),
		trafficPolicy("shop", "p-route", []map[string]any{targetRef(gw, "HTTPRoute", "r-sectioned", "")}),
		// Names shop/gw, which does not exist: a targetRef is in the policy's namespace.
		trafficPolicy("shop", "p-gw-shop", []map[string]any{targetRef(gw, "Gateway", "gw", "")}),
		// Reported on gw only: current, stale and another controller's.
		trafficPolicy("infra", "p-att", []map[string]any{targetRef(gw, "HTTPRoute", "elsewhere", "")},
			ancestor(gwRef, kgwController, "True", 1)),
		trafficPolicy("infra", "p-att-false", nil, ancestor(gwRef, kgwController, "False", 1)),
		trafficPolicy("infra", "p-stale", nil, ancestor(gwRef, kgwController, "True", 0)),
		trafficPolicy("infra", "p-other", nil, ancestor(gwRef, "example.com/other", "True", 1)),
		// A backend port ancestor, as BackendConfigPolicy reports it.
		trafficPolicy("shop", "p-backend", []map[string]any{targetRef("", "Service", "backend", "http")},
			ancestor(map[string]any{"group": "", "kind": "Service", "name": "backend", "namespace": "shop", "sectionName": "http"}, kgwController, "True", 1)),
	}
	return newWorld(objs...), policies
}

// rel renders a relation as "kind ns/name -> subject [via]".
func rel(r policy.Relation) string {
	s := fmt.Sprintf("%s %s/%s %s -> %s", r.Kind, r.Policy.Namespace, r.Policy.Name, r.Source, subj(r.Subject))
	if len(r.Via) > 0 {
		var via []string
		for _, v := range r.Via {
			via = append(via, subj(v))
		}
		s += " via " + strings.Join(via, ", ")
	}
	return s
}

func subj(s policy.Subject) string {
	out := s.Ref.Kind + " " + s.Ref.Namespace + "/" + s.Ref.Name
	if s.SectionName != "" {
		out += "#" + s.SectionName
	}
	if s.Port != 0 {
		out += fmt.Sprintf(":%d", s.Port)
	}
	return out
}

// Parent joins come from the subject's own spec.parentRefs, section-exact, with
// one extra hop through a ListenerSet; every hop must be readable (design §5.3, D-129).
func TestRelations(t *testing.T) {
	r := func(kind, name string) objects.Ref {
		switch kind {
		case "HTTPRoute", "Service":
			g := gw
			if kind == "Service" {
				g = ""
			}
			return objects.Ref{Group: g, Kind: kind, Namespace: "shop", Name: name}
		}
		return objects.Ref{Group: gw, Kind: kind, Namespace: "infra", Name: name}
	}
	gwRef, lsRef := r("Gateway", "gw"), r("ListenerSet", "ls")
	const (
		tp  = "targets-parent "
		dcl = " declared -> "
	)
	cases := []struct {
		name    string
		subject policy.Subject
		edit    func(w *world)
		want    []string
	}{
		{name: "sectioned parentRef", subject: policy.Subject{Ref: r("HTTPRoute", "r-sectioned")}, want: []string{
			tp + "infra/p-gw" + dcl + "HTTPRoute shop/r-sectioned via Gateway infra/gw#http",
			tp + "infra/p-gw-http" + dcl + "HTTPRoute shop/r-sectioned via Gateway infra/gw#http",
			"targets shop/p-route" + dcl + "HTTPRoute shop/r-sectioned"}},
		{name: "unsectioned parentRef", subject: policy.Subject{Ref: r("HTTPRoute", "r-unsectioned")}, want: []string{
			tp + "infra/p-gw" + dcl + "HTTPRoute shop/r-unsectioned via Gateway infra/gw"}},
		{name: "port-only parentRef is unsectioned", subject: policy.Subject{Ref: r("HTTPRoute", "r-port")}, want: []string{
			tp + "infra/p-gw" + dcl + "HTTPRoute shop/r-port via Gateway infra/gw:8080"}},
		{name: "two sections of one Gateway", subject: policy.Subject{Ref: r("HTTPRoute", "r-two")}, want: []string{
			tp + "infra/p-gw" + dcl + "HTTPRoute shop/r-two via Gateway infra/gw#http",
			tp + "infra/p-gw" + dcl + "HTTPRoute shop/r-two via Gateway infra/gw#https",
			tp + "infra/p-gw-http" + dcl + "HTTPRoute shop/r-two via Gateway infra/gw#http",
			tp + "infra/p-gw-https" + dcl + "HTTPRoute shop/r-two via Gateway infra/gw#https"}},
		{name: "ListenerSet two-hop", subject: policy.Subject{Ref: r("HTTPRoute", "r-ls")}, want: []string{
			tp + "infra/p-gw" + dcl + "HTTPRoute shop/r-ls via ListenerSet infra/ls, Gateway infra/gw",
			tp + "infra/p-ls" + dcl + "HTTPRoute shop/r-ls via ListenerSet infra/ls"}},
		{name: "hidden Gateway hop drops its relations", subject: policy.Subject{Ref: r("HTTPRoute", "r-sectioned")},
			edit: func(w *world) { w.hidden[gwRef] = true }, want: []string{"targets shop/p-route" + dcl + "HTTPRoute shop/r-sectioned"}},
		{name: "hidden second hop keeps the first", subject: policy.Subject{Ref: r("HTTPRoute", "r-ls")},
			edit: func(w *world) { w.hidden[gwRef] = true }, want: []string{tp + "infra/p-ls" + dcl + "HTTPRoute shop/r-ls via ListenerSet infra/ls"}},
		{name: "hidden ListenerSet drops both hops", subject: policy.Subject{Ref: r("HTTPRoute", "r-ls")},
			edit: func(w *world) { w.hidden[lsRef] = true }},
		// A hop missing from the inputs drops its relations, as a hidden one does (design §7 item 5).
		{name: "absent ListenerSet drops both hops", subject: policy.Subject{Ref: r("HTTPRoute", "r-ls")},
			edit: func(w *world) { delete(w.objs, lsRef) }},
		{name: "absent Gateway hop drops its relations", subject: policy.Subject{Ref: r("HTTPRoute", "r-sectioned")},
			edit: func(w *world) { delete(w.objs, gwRef) }, want: []string{"targets shop/p-route" + dcl + "HTTPRoute shop/r-sectioned"}},
		{name: "absent second hop keeps the first", subject: policy.Subject{Ref: r("HTTPRoute", "r-ls")},
			edit: func(w *world) { delete(w.objs, gwRef) }, want: []string{tp + "infra/p-ls" + dcl + "HTTPRoute shop/r-ls via ListenerSet infra/ls"}},
		{name: "ListenerSet whose Gateway is absent", subject: policy.Subject{Ref: lsRef},
			edit: func(w *world) { delete(w.objs, gwRef) }, want: []string{"targets infra/p-ls" + dcl + "ListenerSet infra/ls"}},
		{name: "hidden subject", subject: policy.Subject{Ref: r("HTTPRoute", "r-sectioned")},
			edit: func(w *world) { w.hidden[r("HTTPRoute", "r-sectioned")] = true }},
		{name: "Gateway: targets and current Attached=True ancestors", subject: policy.Subject{Ref: gwRef}, want: []string{
			"attached-on-ancestor infra/p-att controller-reported -> Gateway infra/gw",
			"targets infra/p-gw" + dcl + "Gateway infra/gw",
			"targets infra/p-gw-http" + dcl + "Gateway infra/gw#http",
			"targets infra/p-gw-https" + dcl + "Gateway infra/gw#https"}},
		{name: "one listener", subject: policy.Subject{Ref: gwRef, SectionName: "http"}, want: []string{
			"targets infra/p-gw-http" + dcl + "Gateway infra/gw#http"}},
		{name: "ListenerSet: its own and its Gateway's unsectioned", subject: policy.Subject{Ref: lsRef}, want: []string{
			tp + "infra/p-gw" + dcl + "ListenerSet infra/ls via Gateway infra/gw",
			"targets infra/p-ls" + dcl + "ListenerSet infra/ls"}},
		{name: "backend port", subject: policy.Subject{Ref: r("Service", "backend")}, want: []string{
			"attached-on-ancestor shop/p-backend controller-reported -> Service shop/backend#http",
			"targets shop/p-backend" + dcl + "Service shop/backend#http"}},
		{name: "backend, other port", subject: policy.Subject{Ref: r("Service", "backend"), SectionName: "grpc"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, us := relationWorld()
			if c.edit != nil {
				c.edit(w)
			}
			var ps []policy.Policy
			for _, u := range us {
				ps = append(ps, policy.Read(u, nil, guard()))
			}
			var got []string
			for _, x := range policy.Relations(c.subject, policy.Inputs{Policies: ps, Lookup: w}) {
				got = append(got, rel(x))
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("relations:\n got %q\nwant %q", got, c.want)
			}
		})
	}
}

// A policy the caller leaves out of Inputs is never related, and a nil Lookup relates nothing.
func TestRelationsUseOnlyInputs(t *testing.T) {
	w, us := relationWorld()
	var ps []policy.Policy
	for _, u := range us {
		if u.GetName() != "p-gw" {
			ps = append(ps, policy.Read(u, nil, guard()))
		}
	}
	s := policy.Subject{Ref: objects.Ref{Group: gw, Kind: "HTTPRoute", Namespace: "shop", Name: "r-unsectioned"}}
	if got := policy.Relations(s, policy.Inputs{Policies: ps, Lookup: w}); len(got) != 0 {
		t.Errorf("relations %+v, want none without p-gw", got)
	}
	if got := policy.Relations(s, policy.Inputs{Policies: ps}); len(got) != 0 {
		t.Errorf("relations %+v, want none without a lookup", got)
	}
}

// Two policy kinds with the same namespace and name in different groups stay distinct.
func TestRelationsGroupCollision(t *testing.T) {
	w, _ := relationWorld()
	a := trafficPolicy("infra", "same", []map[string]any{targetRef(gw, "Gateway", "gw", "")})
	b := trafficPolicy("infra", "same", []map[string]any{targetRef(gw, "Gateway", "gw", "")})
	b.SetAPIVersion("policy.acme.example/v1")
	ps := []policy.Policy{policy.Read(b, nil, guard()), policy.Read(a, nil, guard())}
	got := policy.Relations(policy.Subject{Ref: objects.Ref{Group: gw, Kind: "Gateway", Namespace: "infra", Name: "gw"}}, policy.Inputs{Policies: ps, Lookup: w})
	if len(got) != 2 || got[0].Policy.Group != kgw || got[1].Policy.Group != "policy.acme.example" {
		t.Errorf("relations %+v, want both groups, ordered by group after namespace and name", got)
	}
}

// The caller names the ListenerSet kinds (E7): a downstream ListenerSet kind gets its own
// parent relation and is a route's second hop, as an upstream one is (8.2 review #7).
func TestRelationsCallerListenerSetKinds(t *testing.T) {
	const acme = "listeners.acme.example"
	w, us := relationWorld()
	als := obj(acme, "v1", "AcmeListenerSet", "infra", "als", map[string]any{"parentRef": map[string]any{"group": gw, "kind": "Gateway", "name": "gw"}})
	r := route("shop", "r-als", map[string]any{"group": acme, "kind": "AcmeListenerSet", "namespace": "infra", "name": "als"})
	w.objs[objects.RefOf(als)], w.objs[objects.RefOf(r)] = als, r
	us = append(us, trafficPolicy("infra", "p-als", []map[string]any{targetRef(acme, "AcmeListenerSet", "als", "")}))
	var ps []policy.Policy
	for _, u := range us {
		ps = append(ps, policy.Read(u, nil, guard()))
	}
	in := policy.Inputs{Policies: ps, Lookup: w, ListenerSetKinds: []objects.GroupKind{{Group: gw, Kind: "ListenerSet"}, {Group: acme, Kind: "AcmeListenerSet"}}}
	for _, c := range []struct {
		subject objects.Ref
		want    []string
	}{
		{objects.RefOf(r), []string{
			"targets-parent infra/p-als declared -> HTTPRoute shop/r-als via AcmeListenerSet infra/als",
			"targets-parent infra/p-gw declared -> HTTPRoute shop/r-als via AcmeListenerSet infra/als, Gateway infra/gw"}},
		{objects.RefOf(als), []string{
			"targets infra/p-als declared -> AcmeListenerSet infra/als",
			"targets-parent infra/p-gw declared -> AcmeListenerSet infra/als via Gateway infra/gw"}},
	} {
		var got []string
		for _, x := range policy.Relations(policy.Subject{Ref: c.subject}, in) {
			got = append(got, rel(x))
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.subject, got, c.want)
		}
	}
}
