package api_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/auth/sar"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// policyResources are the resources of the served policy kinds.
func policyResources() map[string]bool {
	out := map[string]bool{}
	for _, k := range testutil.ServedUpstream() {
		if k.Category == kinds.CategoryPolicy {
			out[k.Resource] = true
		}
	}
	return out
}

// TestPoliciesScope: /policies takes the namespace scope as the other lists do (D-100 item 8,
// Q8.4-3). For a secure viewer, rows, total, facets and checks cover the scoped namespaces only.
func TestPoliciesScope(t *testing.T) {
	src := policyBase(t, ruleChange{name: "base"})
	type run struct {
		resp  api.PoliciesResponse
		calls []sar.Attributes
	}
	cold := func(q string) run {
		c := &sarLog{}
		r := decode[api.PoliciesResponse](t, policyServeWith(t, src, c, nil), restrictedUser, policiesPath+"?limit=200"+q)
		return run{r, c.calls}
	}
	all, in := cold(""), cold("&namespaces="+ns0)
	var want []api.PolicyRow
	for _, r := range all.resp.Items {
		if r.Ref.Namespace == ns0 {
			want = append(want, r)
		}
	}
	if len(want) == 0 || len(want) == len(all.resp.Items) {
		t.Fatalf("%d of %d rows in %s; the fixture does not test the scope", len(want), len(all.resp.Items), ns0)
	}
	if got := mustMarshal(t, in.resp.Items); got != mustMarshal(t, want) {
		t.Errorf("scoped rows differ from the unscoped rows in scope:\n got %s\nwant %s", got, mustMarshal(t, want))
	}
	if in.resp.Total != len(want) {
		t.Errorf("total %d, want %d", in.resp.Total, len(want))
	}
	// Facets rebuilt from the scoped rows: no other filter is set.
	wantFacets := api.PolicyFacetsDTO{Kinds: []api.PolicyKindFacetDTO{}, Features: []api.PolicyFeatureFacetDTO{},
		Namespaces: []api.PolicyNamespaceFacetDTO{{Namespace: ns0, Policies: len(want)}}}
	kindsN, featuresN := map[objects.GroupKind]int{}, map[string]int{}
	for _, r := range want {
		kindsN[objects.GroupKind{Group: r.Ref.Group, Kind: r.Ref.Kind}]++
		for _, f := range r.Features {
			featuresN[f.Key]++
		}
		if r.Problems.Has {
			wantFacets.HasProblems++
		}
	}
	for _, f := range all.resp.Facets.Kinds {
		if n := kindsN[objects.GroupKind{Group: f.Group, Kind: f.Kind}]; n > 0 {
			wantFacets.Kinds = append(wantFacets.Kinds, api.PolicyKindFacetDTO{Group: f.Group, Kind: f.Kind, Policies: n})
		}
	}
	for _, f := range all.resp.Facets.Features {
		if n := featuresN[f.Key]; n > 0 {
			wantFacets.Features = append(wantFacets.Features, api.PolicyFeatureFacetDTO{Key: f.Key, Policies: n})
		}
	}
	if got := mustMarshal(t, in.resp.Facets); got != mustMarshal(t, wantFacets) {
		t.Errorf("facets %s, want %s", got, mustMarshal(t, wantFacets))
	}
	// No policy bucket outside the scope is asked, and the scope asks fewer checks.
	res, asked := policyResources(), false
	for _, a := range in.calls {
		if !res[a.Resource] {
			continue
		}
		asked = asked || a.Namespace == ns0
		if a.Namespace != ns0 {
			t.Errorf("scoped list asked %s %s in %q outside the scope", a.Verb, a.Resource, a.Namespace)
		}
	}
	if !asked {
		t.Error("the scoped list asked nothing in the scope; the test is vacuous")
	}
	if len(in.calls) >= len(all.calls) {
		t.Errorf("scoped list asked %d checks cold, unscoped %d", len(in.calls), len(all.calls))
	}
	// The page's Namespace filter narrows within the scope: the two intersect.
	both := ns0 + "," + kindLimitedNS
	if r := cold("&namespaces=" + ns0 + "&namespace=" + kindLimitedNS).resp; r.Total != 0 || len(r.Facets.Namespaces) != 1 {
		t.Errorf("a namespace filter outside the scope: total %d, namespace facets %+v", r.Total, r.Facets.Namespaces)
	}
	r := cold("&namespaces=" + both + "&namespace=" + kindLimitedNS).resp
	if r.Total == 0 || slices.ContainsFunc(r.Items, func(p api.PolicyRow) bool { return p.Ref.Namespace != kindLimitedNS }) || len(r.Facets.Namespaces) != 2 {
		t.Errorf("a namespace filter inside the scope: %d rows, namespace facets %+v", r.Total, r.Facets.Namespaces)
	}
	// A scope on a namespace with hidden policies reads as one on an absent namespace.
	srv := policyServeWith(t, src, &sarLog{}, nil)
	hc, hidden := fetch(t, srv, restrictedUser, policiesPath+"?limit=200&namespaces="+hiddenNS)
	ac, absent := fetch(t, srv, restrictedUser, policiesPath+"?limit=200&namespaces=no-such-namespace")
	if hc != http.StatusOK || hc != ac || hidden != absent {
		t.Errorf("hidden %d %.300s\n  vs absent %d %.300s", hc, hidden, ac, absent)
	}
	if _, full := fetch(t, srv, fullUser, policiesPath+"?limit=200&namespaces="+hiddenNS); full == hidden {
		t.Errorf("the full viewer sees nothing in %s either; the case is vacuous", hiddenNS)
	}
}
