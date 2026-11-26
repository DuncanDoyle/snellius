package policy_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigsyaml "sigs.k8s.io/yaml"

	"github.com/DuncanDoyle/snellius/pkg/health/own"
	"github.com/DuncanDoyle/snellius/pkg/kgateway"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/policy"
)

const kgwController = "kgateway.dev/kgateway"

// load reads the YAML documents of testdata/name.
func load(t *testing.T, name string) []*unstructured.Unstructured {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var out []*unstructured.Unstructured
	for doc := range strings.SplitSeq(string(b), "\n---\n") {
		j, err := sigsyaml.YAMLToJSON([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		// UnmarshalJSON keeps integers as int64, as the API server's objects have them.
		u := &unstructured.Unstructured{}
		if err := u.UnmarshalJSON(j); err != nil {
			t.Fatal(err)
		}
		out = append(out, u)
	}
	return out
}

// guard is the install the fixtures run under: kgateway's controller, owning every
// Gateway but other-gw, with the freshness the policy findings use.
func guard() policy.Guard {
	return policy.Guard{
		ControllerNames: []string{kgwController},
		Owns: func(r objects.Ref) bool {
			return r.Kind != "Gateway" || r.Name != "other-gw"
		},
		Fresh: fresh,
	}
}

// fresh is own.PolicyFreshness, as policyAncestors calls it.
func fresh(u *unstructured.Unstructured, cs []metav1.Condition) (string, bool) {
	why, kind, _ := own.PolicyFreshness(u, cs)
	return why, kind == own.FreshnessStale
}

// summary renders p for comparison: targets, features, annotations and ancestors.
type summary struct {
	Interpreted bool
	Targets     []string
	Features    []string
	Annotations []string
	Ancestors   []string
}

func summarize(p policy.Policy) summary {
	s := summary{Interpreted: p.Interpreted}
	for _, t := range p.Targets {
		if t.Source != policy.SourceDeclared {
			s.Targets = append(s.Targets, "bad source "+string(t.Source))
		}
		if t.Selector {
			s.Targets = append(s.Targets, fmt.Sprintf("%d sel %s/%s %v #%s", t.Index, t.Group, t.Kind, t.MatchLabels, t.SectionName))
			continue
		}
		s.Targets = append(s.Targets, fmt.Sprintf("%d %s/%s %s #%s", t.Index, t.Group, t.Kind, t.Name, t.SectionName))
	}
	for _, f := range p.Features {
		s.Features = append(s.Features, f.Key+"="+f.Label+" "+string(f.Source))
	}
	for _, a := range p.Annotations {
		s.Annotations = append(s.Annotations, a.Key+"="+a.Value+" "+string(a.Source))
	}
	for _, a := range p.Ancestors {
		state := "current"
		if !a.Current {
			state = a.NotCurrent
			if a.Why != "" {
				state += " (" + a.Why + ")"
			}
		}
		line := fmt.Sprintf("%s/%s %s/%s #%s %s %s", a.Ref.Group, a.Ref.Kind, a.Ref.Namespace, a.Ref.Name, a.Ref.SectionName, a.Controller, state)
		if a.Summary {
			line += " summary"
		}
		for _, r := range a.Readings {
			line += fmt.Sprintf(" [%s warning=%v note=%s %s]", r.Type, r.Warning, r.Note, r.Source)
		}
		line += " " + string(a.Source)
		s.Ancestors = append(s.Ancestors, line)
	}
	return s
}

const (
	gw    = "gateway.networking.k8s.io"
	kgw   = "gateway.kgateway.dev"
	cur   = kgwController + " current"
	ctrl  = " controller-reported"
	decl  = " declared"
	attWG = " [Attached warning=false note=gateway-wide inferred]"
)

// Read keeps targets in spec order, every ancestor and condition as written,
// and marks only what the guard and the pack decide (design §5, §6.4, §8).
func TestReadRealPolicies(t *testing.T) {
	cases := map[string]summary{
		"2.3 TrafficPolicy a/child-policy-filter": {Interpreted: true,
			Features:  []string{"extProc=External processing" + decl, "rateLimit=Rate limiting" + decl},
			Ancestors: []string{gw + "/Gateway infra/example-gateway # " + cur + attWG + ctrl}},
		"2.3 TrafficPolicy a/child-policy-targetref": {Interpreted: true,
			Targets:   []string{"0 " + gw + "/HTTPRoute child #"},
			Features:  []string{"extProc=External processing" + decl},
			Ancestors: []string{gw + "/Gateway infra/example-gateway # " + cur + attWG + ctrl}},
		"2.3 HTTPListenerPolicy default/accept-http10": {Interpreted: true,
			Targets:   []string{"0 " + gw + "/Gateway example-gateway #"},
			Features:  []string{"acceptHttp10=Accept HTTP/1.0" + decl},
			Ancestors: []string{gw + "/Gateway default/example-gateway # " + cur + ctrl}},
		"2.3 DirectResponse default/direct-response": {Interpreted: true,
			Features:  []string{"bodyFormat=Body format" + decl, "status=Status code" + decl},
			Ancestors: []string{gw + "/Gateway default/example-gateway # " + cur + ctrl}},
		"2.3 BackendTLSPolicy default/conflicted-without-section-name-2": {Interpreted: false,
			Targets:   []string{"0 /Service backendtlspolicy-conflicted-without-section-name-test #"},
			Features:  []string{"validation=" + decl},
			Ancestors: []string{gw + "/Gateway default/example-gateway # " + cur + ctrl}},
		"2.4 BackendConfigPolicy default/httpbin-policy": {Interpreted: true,
			Targets:   []string{"0 /Service httpbin #"},
			Features:  []string{"tls=TLS" + decl},
			Ancestors: []string{"/Service default/httpbin # " + cur + ctrl}},
		"2.4 TrafficPolicy default/listener-attachment-1": {Interpreted: true,
			Targets:     []string{"0 " + gw + "/Gateway test #http"},
			Features:    []string{"transformation=Transformations" + decl},
			Annotations: []string{"kgateway.dev/policy-weight=-1" + decl},
			Ancestors:   []string{gw + "/Gateway default/test # " + cur + ctrl}},
		"2.4 TrafficPolicy default/gateway-attachment-2": {Interpreted: true,
			Targets:     []string{"0 " + gw + "/Gateway test #"},
			Features:    []string{"transformation=Transformations" + decl},
			Annotations: []string{"kgateway.dev/policy-weight=5" + decl},
			Ancestors: []string{
				gw + "/Gateway default/test # " + kgwController + " stale (Status describes an older generation)" + ctrl,
				gw + "/Gateway default/test # example.com/other-controller other-controller" + ctrl}},
		"2.5 TrafficPolicy default/partial": {Interpreted: true,
			Targets:  []string{"0 " + gw + "/Gateway example-gateway #", "1 " + gw + "/HTTPRoute route-b-typo #"},
			Features: []string{"cors=CORS" + decl},
			Ancestors: []string{
				kgw + "/StatusSummary default/StatusSummary # " + cur + " summary" + ctrl,
				gw + "/Gateway default/example-gateway # " + cur + ctrl}},
		"2.5 ListenerPolicy default/accept-http10": {Interpreted: true,
			Targets:   []string{"0 " + gw + "/Gateway example-gateway #"},
			Features:  []string{"default=Listener defaults" + decl},
			Ancestors: []string{gw + "/Gateway default/example-gateway # " + cur + ctrl}},
		"2.5 BackendConfigPolicy default/service-partially-valid": {Interpreted: true,
			Targets:   []string{"0 /Service backend #"},
			Features:  []string{"loadBalancer=Load balancing" + decl, "sessionPersistence=Session persistence" + decl},
			Ancestors: []string{"/Service default/backend # " + cur + " [Accepted warning=true note= inferred]" + ctrl}},
		"2.5 BackendConfigPolicy default/backend-http-port": {Interpreted: true,
			Targets:   []string{"0 /Service backend #http"},
			Features:  []string{"connectTimeout=Connect timeout" + decl},
			Ancestors: []string{"/Service default/backend #http " + cur + " [Attached warning=false note=backend-port inferred]" + ctrl}},
		"2.5 DirectResponse default/direct-response": {Interpreted: true,
			Features: []string{"bodyFormat=Body format" + decl, "status=Status code" + decl}},
		"2.5 ListenerPolicy default/future-ancestor": {Interpreted: true,
			Targets:   []string{"0 " + gw + "/Gateway example-gateway #"},
			Features:  []string{"perPort=Per-port listener settings" + decl},
			Ancestors: []string{"example.com/Mesh default/mesh-a # " + cur + ctrl}},
	}
	seen := map[string]bool{}
	for _, v := range []string{"2.3", "2.4", "2.5"} {
		for _, u := range load(t, "kgateway-"+v+".yaml") {
			name := v + " " + u.GetKind() + " " + u.GetNamespace() + "/" + u.GetName()
			want, ok := cases[name]
			if !ok {
				t.Errorf("%s: no expectation", name)
				continue
			}
			seen[name] = true
			before := u.DeepCopy()
			p := policy.Read(u, kgateway.PolicyInterpreter(), guard())
			if got := summarize(p); !reflect.DeepEqual(got, want) {
				t.Errorf("%s:\n got %+v\nwant %+v", name, got, want)
			}
			if !reflect.DeepEqual(u, before) {
				t.Errorf("%s: Read changed its input", name)
			}
			// Conditions are copied verbatim, unknown types and reasons included.
			raw := objects.PolicyAncestors(u)
			for i, a := range p.Ancestors {
				if !reflect.DeepEqual(a.Conditions, raw[i].Conditions) {
					t.Errorf("%s ancestor %d: conditions %+v, want %+v", name, i, a.Conditions, raw[i].Conditions)
				}
			}
			if p.Ref != objects.RefOf(u) || p.Generation != u.GetGeneration() {
				t.Errorf("%s: ref %+v generation %d", name, p.Ref, p.Generation)
			}
		}
	}
	for name := range cases {
		if !seen[name] {
			t.Errorf("%s: fixture missing", name)
		}
	}
}

// Without a pack interpreter the features are the raw spec keys and nothing is
// interpreted: no labels, annotations, summary marker or readings (design §8).
func TestReadWithoutPack(t *testing.T) {
	for _, v := range []string{"2.3", "2.4", "2.5"} {
		for _, u := range load(t, "kgateway-"+v+".yaml") {
			p := policy.Read(u, nil, guard())
			name := v + " " + u.GetName()
			if p.Interpreted || len(p.Annotations) > 0 {
				t.Errorf("%s: interpreted %v annotations %+v", name, p.Interpreted, p.Annotations)
			}
			for _, f := range p.Features {
				if f.Label != "" {
					t.Errorf("%s: feature %s labelled %q", name, f.Key, f.Label)
				}
			}
			for _, a := range p.Ancestors {
				if a.Summary || len(a.Readings) > 0 {
					t.Errorf("%s: ancestor %+v interpreted", name, a.Ref)
				}
			}
		}
	}
}

// A Gateway ancestor the install does not own is kept and not current; a guard
// without functions makes nothing current.
func TestReadGuards(t *testing.T) {
	u := load(t, "kgateway-2.4.yaml")[1] // listener-attachment-1, ancestor Gateway default/test
	g := guard()
	g.Owns = func(r objects.Ref) bool { return r.Kind != "Gateway" }
	if a := policy.Read(u, nil, g).Ancestors[0]; a.Current || a.NotCurrent != policy.NotCurrentNotOwned {
		t.Errorf("not owned: %+v", a)
	}
	if a := policy.Read(u, nil, policy.Guard{ControllerNames: []string{kgwController}}).Ancestors[0]; a.Current || a.NotCurrent != policy.NotCurrentNotOwned {
		t.Errorf("nil Owns: %+v", a)
	}
	if a := policy.Read(u, nil, policy.Guard{}).Ancestors[0]; a.Current || a.NotCurrent != policy.NotCurrentOtherController {
		t.Errorf("zero guard: %+v", a)
	}
	g = guard()
	g.Fresh = nil
	if a := policy.Read(u, nil, g).Ancestors[0]; a.Current || a.NotCurrent != policy.NotCurrentNotFresh {
		t.Errorf("nil Fresh: %+v", a)
	}
}

// Every ancestor's derived fields carry their own source apart from its
// controller-reported conditions: Current and its reason inferred, Summary
// inferred from a pack or unknown without one (8.1 review #8).
func TestReadAncestorDerivedSources(t *testing.T) {
	n := 0
	for _, file := range []string{"kgateway-2.3.yaml", "kgateway-2.4.yaml", "kgateway-2.5.yaml"} {
		for _, u := range load(t, file) {
			for _, c := range []struct {
				interp  policy.Interpreter
				summary policy.Source
			}{{kgateway.PolicyInterpreter(), policy.SourceInferred}, {nil, policy.SourceUnknown}} {
				p := policy.Read(u, c.interp, guard())
				if !p.Interpreted && c.interp != nil {
					continue
				}
				for _, a := range p.Ancestors {
					n++
					if a.Source != policy.SourceControllerReported || a.CurrentSource != policy.SourceInferred || a.SummarySource != c.summary {
						t.Errorf("%s ancestor %s: sources %s, current %s, summary %s", p.Ref, a.Ref.Ref(), a.Source, a.CurrentSource, a.SummarySource)
					}
				}
			}
		}
	}
	if n == 0 {
		t.Fatal("no ancestors read")
	}
}

// Targets keep spec order, refs before selectors, with a selector's labels as written.
func TestReadSelectors(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": kgw + "/v1alpha1", "kind": "TrafficPolicy",
		"metadata": map[string]any{"name": "sel", "namespace": "shop"},
		"spec": map[string]any{
			"targetSelectors": []any{map[string]any{"group": gw, "kind": "HTTPRoute", "matchLabels": map[string]any{"app": "cart"}}},
			"targetRefs":      []any{map[string]any{"group": gw, "kind": "HTTPRoute", "name": "r1", "sectionName": "rule-a"}},
		},
	}}
	got := summarize(policy.Read(u, nil, guard())).Targets
	want := []string{"0 " + gw + "/HTTPRoute r1 #rule-a", "1 sel " + gw + "/HTTPRoute map[app:cart] #"}
	if !slices.Equal(got, want) {
		t.Errorf("targets %q, want %q", got, want)
	}
}
