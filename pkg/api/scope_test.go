package api_test

import (
	"encoding/json"
	"flag"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/auth/sar"
	"github.com/DuncanDoyle/snellius/pkg/cache"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// Handler tests of the namespace scope (Task 37, D-100 item 8).

var updateScope = flag.Bool("update-scope", false, "rewrite the unscoped goldens under testdata/scope")

// scopeList is one list the scope applies to: where its rows are, their
// count when it has one, and the namespace a row is scoped by.
type scopeList struct {
	name, path   string
	items, total []string
	ns           func(row map[string]any) string
}

// refNS is the namespace of the ref at key of row; "" when absent.
func refNS(key string) func(map[string]any) string {
	return func(row map[string]any) string {
		ref, _ := row[key].(map[string]any)
		ns, _ := ref["namespace"].(string)
		return ns
	}
}

// rootNS is the namespace of a path row's root route.
func rootNS(row map[string]any) string {
	routes, _ := row["routes"].([]any)
	if len(routes) == 0 {
		return ""
	}
	ns, _ := routes[0].(map[string]any)["namespace"].(string)
	return ns
}

var scopeLists = []scopeList{
	{name: "routes", path: "/routes?limit=200", items: []string{"items"}, total: []string{"total"}, ns: refNS("ref")},
	{name: "gateways", path: "/gateways?limit=200", items: []string{"items"}, total: []string{"total"}, ns: refNS("ref")},
	{name: "listenersets", path: "/listenersets?limit=200", items: []string{"items"}, total: []string{"total"}, ns: refNS("ref")},
	{name: "paths", path: "/paths?limit=200", items: []string{"rows"}, total: []string{"summary", "paths"}, ns: rootNS},
	{name: "backends", path: "/backends?limit=200", items: []string{"rows"}, ns: refNS("ref")},
	{name: "findings", path: "/findings?limit=200", items: []string{"items"}, total: []string{"total"}, ns: refNS("resource")},
	{name: "processes", path: "/processes?limit=200", items: []string{"proxies", "items"}, total: []string{"proxies", "total"}, ns: refNS("gateway")},
}

// at is the value at path in v, nil when absent.
func at(v any, path ...string) any {
	for _, k := range path {
		m, _ := v.(map[string]any)
		v = m[k]
	}
	return v
}

// rows is the rows of l in v.
func rows(l scopeList, v map[string]any) []map[string]any {
	var out []map[string]any
	items, _ := at(v, l.items...).([]any)
	for _, it := range items {
		out = append(out, it.(map[string]any))
	}
	return out
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestScopeParamValidation: a bad namespaces parameter is a 400 on every list.
func TestScopeParamValidation(t *testing.T) {
	srv := ruleServe(t, ruleWorld(t, ruleChange{name: "base"}))
	var many []string
	for i := range 101 {
		many = append(many, testutil.ScaleNamespace(i))
	}
	bad := map[string]string{
		"empty":      "namespaces=",
		"empty name": "namespaces=" + ns0 + ",",
		"repeated":   "namespaces=" + ns0 + "&namespaces=" + hiddenNS,
		"bad label":  "namespaces=Not_A_Label",
		"too long":   "namespaces=" + strings.Repeat("a", 64),
		"101 names":  "namespaces=" + strings.Join(many, ","),
	}
	paths := []string{"/routes", "/gateways", "/listenersets", "/paths", "/backends", "/findings", "/findings?view=groups", "/processes"}
	for _, p := range paths {
		sep := "?"
		if strings.Contains(p, "?") {
			sep = "&"
		}
		for name, q := range bad {
			if code, b := fetch(t, srv, fullUser, ruleBase+p+sep+q); code != http.StatusBadRequest {
				t.Errorf("%s %s: %d %.200s, want 400", p, name, code, b)
			}
		}
		if code, b := fetch(t, srv, fullUser, ruleBase+p+sep+"namespaces="+strings.Join(many[:100], ",")); code != http.StatusOK {
			t.Errorf("%s with 100 names: %d %.200s", p, code, b)
		}
	}
	m := decode[api.MetaResponse](t, srv, fullUser, "/api/v1/meta")
	for _, l := range []string{"routes", "gateways", "listenersets", "paths", "backends", "findings", "processes"} {
		if !slices.Contains(m.Filters[l], "namespaces") {
			t.Errorf("/meta filters do not list namespaces for %s: %v", l, m.Filters[l])
		}
	}
}

// scopeNS is the scope the row tests use: two namespaces the restricted viewer reads.
var scopeNS = []string{ns0, testutil.ScaleNamespace(4)}

// TestScopeLimitsRowsCountsFacets: a scoped list holds exactly the unscoped
// rows in scope, unchanged, with counts and facets of those rows only.
func TestScopeLimitsRowsCountsFacets(t *testing.T) {
	srv := ruleServe(t, ruleWorld(t, ruleChange{name: "base"}))
	q := "&namespaces=" + strings.Join(scopeNS, ",")
	for _, l := range scopeLists {
		all, _ := pages(t, srv, l, l.path)
		in, scoped := pages(t, srv, l, l.path+q)
		var want, got []string
		for _, r := range in {
			got = append(got, mustMarshal(t, r))
		}
		for _, r := range all {
			if slices.Contains(scopeNS, l.ns(r)) {
				want = append(want, mustMarshal(t, r))
			}
		}
		if len(want) == 0 || len(want) == len(all) {
			t.Fatalf("%s: %d of %d rows in scope; the fixture does not test the scope", l.name, len(want), len(all))
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: scoped rows differ from the unscoped rows in scope:\n got %v\nwant %v", l.name, got, want)
		}
		if l.total != nil && at(scoped, l.total...) != float64(len(want)) {
			t.Errorf("%s: total %v, want %d", l.name, at(scoped, l.total...), len(want))
		}
	}
	// Facets come from the rows in scope only.
	p := decodeAny(t, srv, fullUser, ruleBase+"/paths?limit=200"+q)
	nss := map[string]bool{}
	for _, r := range rows(scopeLists[3], p) {
		for _, rt := range r["routes"].([]any) {
			nss[rt.(map[string]any)["namespace"].(string)] = true
		}
	}
	for _, f := range at(p, "summary", "facets", "namespaces").([]any) {
		if n := f.(map[string]any)["name"].(string); !nss[n] {
			t.Errorf("/paths namespace facet %s has no scoped row", n)
		}
	}
	b := decodeAny(t, srv, fullUser, ruleBase+"/backends?limit=200"+q)
	for _, f := range at(b, "facets", "namespaces").([]any) {
		if n := f.(map[string]any)["name"].(string); !slices.Contains(scopeNS, n) {
			t.Errorf("/backends namespace facet %s is outside the scope", n)
		}
	}
	// The groups view lists the groups with a member in scope, each row unchanged.
	inScope := map[string]bool{}
	for _, f := range findingsOf(t, srv, fullUser) {
		if f.Resource != nil && slices.Contains(scopeNS, f.Resource.Namespace) {
			inScope[f.Group] = true
		}
	}
	var want, got []string
	for _, g := range decode[api.Page[api.GroupRow]](t, srv, fullUser, ruleBase+"/findings?view=groups&limit=200").Items {
		if inScope[g.ID] {
			want = append(want, mustMarshal(t, g))
		}
	}
	gs := decode[api.Page[api.GroupRow]](t, srv, fullUser, ruleBase+"/findings?view=groups&limit=200"+q)
	for _, g := range gs.Items {
		got = append(got, mustMarshal(t, g))
	}
	if len(want) == 0 || !slices.Equal(got, want) || gs.Total != len(want) {
		t.Errorf("groups view: got %d %v, want %v", gs.Total, got, want)
	}
}

// pages is every row of l at path for the full viewer, following next, and the first page.
func pages(t *testing.T, srv *httptest.Server, l scopeList, path string) ([]map[string]any, map[string]any) {
	t.Helper()
	var out []map[string]any
	var first map[string]any
	for p := path; p != ""; {
		v := decodeAny(t, srv, fullUser, ruleBase+p)
		if first == nil {
			first = v
		}
		out = append(out, rows(l, v)...)
		next, _ := at(v, append(l.items[:len(l.items)-1:len(l.items)-1], "next")...).(string)
		p = ""
		if next != "" {
			p = path + "&cursor=" + url.QueryEscape(next)
		}
	}
	return out, first
}

// decodeAny is GET path as user as a JSON object; it must answer 200.
func decodeAny(t *testing.T, srv *httptest.Server, user, path string) map[string]any {
	t.Helper()
	return decode[map[string]any](t, srv, user, path)
}

// TestScopeExcludesUnnamespacedFindings: a finding with no resource, or on a
// cluster-scoped one, is outside every scope and listed without one.
func TestScopeExcludesUnnamespacedFindings(t *testing.T) {
	srv := ruleServe(t, ruleWorld(t, ruleChange{name: "denied", apply: func(s ruleSnap) {
		s.Access[cache.PodsGVR] = cache.Access{Err: "pods is forbidden", Denied: true}
	}}))
	unnamespaced := func(rows []api.FindingRow) (none, cluster bool) {
		for _, f := range rows {
			none = none || f.Resource == nil
			cluster = cluster || f.Resource != nil && f.Resource.Namespace == ""
		}
		return none, cluster
	}
	if none, cluster := unnamespaced(findingsOf(t, srv, fullUser)); !none || !cluster {
		t.Fatalf("the fixture lacks a finding without a resource (%v) or on a cluster-scoped one (%v)", none, cluster)
	}
	for _, user := range []string{fullUser, restrictedUser} {
		rows := decode[api.Page[api.FindingRow]](t, srv, user, ruleBase+"/findings?limit=200&namespaces="+ns0).Items
		if none, cluster := unnamespaced(rows); none || cluster {
			t.Errorf("%s: a scoped list holds an unnamespaced finding: %+v", user, rows)
		}
	}
}

// lists are the list SARs l answered.
func (l *sarLog) lists() []sar.Attributes {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []sar.Attributes
	for _, a := range l.calls {
		if a.Verb == "list" {
			out = append(out, a)
		}
	}
	return out
}

// rootResources are, per list, the resources of its root rows.
var rootResources = map[string]func(resource string) bool{
	"routes":       func(r string) bool { return strings.HasSuffix(r, "routes") },
	"paths":        func(r string) bool { return strings.HasSuffix(r, "routes") },
	"gateways":     func(r string) bool { return r == "gateways" },
	"processes":    func(r string) bool { return r == "gateways" },
	"listenersets": func(r string) bool { return strings.HasSuffix(r, "listenersets") },
	"backends":     func(r string) bool { return r == "services" || r == "backends" },
}

// scopeOne is, per list, a readable namespace whose rows there depend on
// nothing elsewhere, so every bucket asked outside it would be a root's.
func scopeOne(list string) string {
	switch list {
	case "listenersets":
		return ns0
	case "findings":
		return testutil.ScaleNamespace(4)
	}
	return testutil.ScaleNamespace(3)
}

// TestScopeAsksNoOutsideRootBucket: a scoped list asks no list SAR for a root
// kind in a namespace outside the scope; findings ask none for a bucket of a
// finding outside it. TestScopeCrossNamespaceDependency covers dependencies.
func TestScopeAsksNoOutsideRootBucket(t *testing.T) {
	src := ruleWorld(t, ruleChange{name: "base"})
	all := findingsOf(t, ruleServe(t, src), fullUser)
	for _, l := range scopeLists {
		ns := scopeOne(l.name)
		// The resources of the findings outside the scope, by namespace.
		outside := map[[2]string]bool{}
		for _, f := range all {
			if f.Resource != nil && f.Resource.Namespace != "" && f.Resource.Namespace != ns {
				k, _ := ruleKinds{}.Lookup(f.Resource.Group, f.Resource.Kind)
				outside[[2]string{k.Resource, f.Resource.Namespace}] = true
			}
		}
		for _, path := range []string{l.path, strings.Replace(l.path, "?", "?view=groups&", 1)} {
			if path != l.path && l.name != "findings" {
				continue
			}
			c := &sarLog{}
			decodeAny(t, ruleServeWith(t, src, c), restrictedUser, ruleBase+path+"&namespaces="+ns)
			asked := false
			for _, a := range c.lists() {
				asked = asked || a.Namespace == ns
				if a.Namespace == "" || a.Namespace == ns {
					continue
				}
				if root := rootResources[l.name]; root != nil && root(a.Resource) || l.name == "findings" && outside[[2]string{a.Resource, a.Namespace}] {
					t.Errorf("%s: asked list %s in %s outside the scope", path, a.Resource, a.Namespace)
				}
			}
			if !asked {
				t.Errorf("%s: asked nothing in %s; the test is vacuous", path, ns)
			}
		}
	}
}

// crossRoute is a route in ns0 on the Gateway of another readable namespace.
const crossRoute = "cross-ns"

// TestScopeCrossNamespaceDependency: a scoped route's Gateway in another
// namespace is still checked and shown; one the viewer may not read stays
// omitted, as without the scope.
func TestScopeCrossNamespaceDependency(t *testing.T) {
	ns1 := testutil.ScaleNamespace(1)
	src := worldPlus(t, func(s ruleSnap) {
		s.add(routeTo(ns0, crossRoute, []objects.ParentRef{testutil.GwRef(ns1, testutil.ScaleGateway(1))}, ns0, "be", true))
	}, ruleChange{name: "base"})
	for _, user := range []string{restrictedUser, fullUser} {
		c := &sarLog{}
		srv := ruleServeWith(t, src, c)
		scoped := decode[api.Page[api.RouteRow]](t, srv, user, ruleBase+"/routes?limit=200&namespaces="+ns0).Items
		all := decode[api.Page[api.RouteRow]](t, srv, user, ruleBase+"/routes?limit=200").Items
		cross := rowOf(t, scoped, routeRowRef, ns0, crossRoute)
		if len(cross.Parents) != 1 || cross.Parents[0].Ref.Namespace != ns1 {
			t.Errorf("%s: the cross-namespace Gateway is not shown: %+v", user, cross.Parents)
		}
		hidden := rowOf(t, scoped, routeRowRef, ns0, namesHidden)
		if a, b := mustMarshal(t, hidden), mustMarshal(t, rowOf(t, all, routeRowRef, ns0, namesHidden)); a != b {
			t.Errorf("%s: a route on a hidden Gateway differs under the scope:\n%s\n%s", user, a, b)
		}
		if user == restrictedUser && !slices.ContainsFunc(c.lists(), func(a sar.Attributes) bool { return a.Resource == "gateways" && a.Namespace == ns1 }) {
			t.Errorf("the Gateway in %s was not checked", ns1)
		}
	}
}

// TestScopeEmptyNamespace: a scope on a namespace with hidden objects answers
// as one on a namespace with none: 200 and identical bodies.
func TestScopeEmptyNamespace(t *testing.T) {
	srv := ruleServe(t, ruleWorld(t, ruleChange{name: "base"}))
	for _, l := range scopeLists {
		paths := []string{l.path}
		if l.name == "findings" {
			paths = append(paths, "/findings?view=groups&limit=200")
		}
		for _, p := range paths {
			hc, hidden := fetch(t, srv, restrictedUser, ruleBase+p+"&namespaces="+hiddenNS)
			ac, absent := fetch(t, srv, restrictedUser, ruleBase+p+"&namespaces=no-such-namespace")
			if hc != http.StatusOK || hc != ac || hidden != absent {
				t.Errorf("%s: hidden %d %.300s\n  vs absent %d %.300s", p, hc, hidden, ac, absent)
			}
			if _, full := fetch(t, srv, fullUser, ruleBase+p+"&namespaces="+hiddenNS); full == hidden && l.name != "listenersets" {
				t.Errorf("%s: the full viewer sees nothing in %s either; the case is vacuous", p, hiddenNS)
			}
		}
	}
}

// TestNoScopeUnchanged: without the parameter every list answers byte-equal
// to its golden, recorded before the scope existed.
func TestNoScopeUnchanged(t *testing.T) {
	srv := ruleServe(t, worldPlus(t, neighbours, ruleChange{name: "base"}))
	for _, user := range []string{fullUser, restrictedUser} {
		for _, l := range scopeLists {
			paths := []string{l.path}
			if l.name == "findings" {
				paths = append(paths, "/findings?view=groups&limit=200")
			}
			for _, p := range paths {
				_, got := fetch(t, srv, user, ruleBase+p)
				name := filepath.Join("testdata", "scope", user+"-"+strings.NewReplacer("/", "", "?", "-", "&", "-", "=", "-").Replace(p)+".json")
				if *updateScope {
					if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(name, []byte(got), 0o600); err != nil {
						t.Fatal(err)
					}
					continue
				}
				want, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				if got != string(want) {
					t.Errorf("%s as %s changed without a scope:\n got %.400s\nwant %.400s", p, user, got, want)
				}
			}
		}
	}
}

// TestScopedFewerColdChecks: scoped to one readable namespace, a cold /routes
// asks fewer SARs than unscoped.
func TestScopedFewerColdChecks(t *testing.T) {
	src := ruleWorld(t, ruleChange{name: "base"})
	cold := func(q string) int {
		c := &sarLog{}
		decodeAny(t, ruleServeWith(t, src, c), restrictedUser, ruleBase+"/routes?limit=200"+q)
		return len(c.calls)
	}
	if scoped, all := cold("&namespaces="+ns0), cold(""); scoped >= all {
		t.Errorf("scoped /routes asked %d SARs cold, unscoped %d", scoped, all)
	}
}

// TestNamespacesVisibleOnly: /namespaces lists the sorted namespaces of the
// route, Gateway and ListenerSet rows the viewer can see.
func TestNamespacesVisibleOnly(t *testing.T) {
	srv := ruleServe(t, ruleWorld(t, ruleChange{name: "base"}))
	want := func(user string) []string {
		set := map[string]bool{}
		for _, l := range scopeLists[:3] {
			for _, r := range rows(l, decodeAny(t, srv, user, ruleBase+l.path)) {
				set[l.ns(r)] = true
			}
		}
		return slices.Sorted(maps.Keys(set))
	}
	for _, user := range []string{fullUser, restrictedUser, nobodyUser} {
		got := decode[api.NamespacesResponse](t, srv, user, ruleBase+"/namespaces")
		if w := want(user); !slices.Equal(got.Namespaces, w) || got.Install != ruleInstall || got.Namespaces == nil {
			t.Errorf("%s: %v, want %v", user, got.Namespaces, w)
		}
	}
	if got := decode[api.NamespacesResponse](t, srv, restrictedUser, ruleBase+"/namespaces"); slices.Contains(got.Namespaces, hiddenNS) {
		t.Errorf("the restricted viewer sees %s", hiddenNS)
	}
}
