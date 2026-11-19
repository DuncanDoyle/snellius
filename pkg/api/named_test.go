package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// D-100: populations (list rows, counts, facets) are decided by list
// permission alone; detail subjects and refs written in a readable object by
// list, then a named get.

// listKey is a list grant: a kind in a namespace, "" for a cluster-scoped kind.
type listKey struct{ group, kind, ns string }

// rbac answers as the SAR authoriser does: a population check is the list
// grant (plus the named get with scan), a named check the list grant or the
// named get. It records the refs asked by name.
type rbac struct {
	lists map[listKey]bool
	gets  map[objects.Ref]bool
	scan  bool
	mu    sync.Mutex
	named []objects.Ref
}

func grantOf(lists []listKey, gets ...objects.Ref) *rbac {
	a := &rbac{lists: map[listKey]bool{}, gets: map[objects.Ref]bool{}}
	for _, l := range lists {
		a.lists[l] = true
	}
	for _, g := range gets {
		a.gets[g] = true
	}
	return a
}

func (a *rbac) listed(ref objects.Ref) bool {
	return a.lists[listKey{ref.Group, ref.Kind, ref.Namespace}]
}

func (a *rbac) Authorize(r *http.Request, ref objects.Ref) bool {
	ok, _ := a.AuthorizeErr(r, ref)
	return ok
}

func (a *rbac) AuthorizeErr(_ *http.Request, ref objects.Ref) (bool, error) {
	return a.listed(ref) || a.scan && a.gets[ref], nil
}

func (a *rbac) AuthorizeNamedErr(_ *http.Request, ref objects.Ref) (bool, error) {
	a.mu.Lock()
	a.named = append(a.named, ref)
	a.mu.Unlock()
	return a.listed(ref) || a.gets[ref], nil
}

func (a *rbac) askedNamed(ref objects.Ref) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Contains(a.named, ref)
}

var (
	_ NamedAuthorizer = (*rbac)(nil)

	backGw       = objects.Ref{Group: objects.GatewayAPIGroup, Kind: "Gateway", Namespace: "infra", Name: "back"}
	shopRoutes   = listKey{objects.GatewayAPIGroup, "HTTPRoute", "shop"}
	infraGws     = listKey{objects.GatewayAPIGroup, "Gateway", "infra"}
	readableShop = []listKey{shopRoutes, infraGws}
)

func service(name string) objects.Ref {
	return objects.Ref{Kind: "Service", Namespace: "shop", Name: name}
}

func kgwBackend(name string) objects.Ref {
	return objects.Ref{Group: kinds.KgatewayGroup, Kind: "Backend", Namespace: "shop", Name: name}
}

// namedWorld is backendsWorld with shop/to-back also sending to the absent Service missing-svc.
func namedWorld() []runtime.Object {
	objs := backendsWorld()
	for _, o := range objs {
		if u, ok := o.(*unstructured.Unstructured); ok && u.GetKind() == "HTTPRoute" && u.GetName() == "to-back" {
			testutil.WithRules(u, backendRule([]any{svcRef("ext-svc")}), backendRule([]any{svcRef("named-svc")}), backendRule([]any{beRef("be-used")}),
				backendRule([]any{svcRef("missing-svc")}))
		}
	}
	return objs
}

func namedServer(t *testing.T, a Authorizer) *httptest.Server {
	t.Helper()
	_, srv, _ := startWith(t, backendsSetup(a, nil), namedWorld()...)
	return srv
}

const toBackURL = listBase + "/routes/gateway.networking.k8s.io/HTTPRoute/shop/to-back"

// writtenRef is the backendRef of to-back that names ref.
func writtenRef(t *testing.T, d RouteDetail, ref objects.Ref) BackendRefDTO {
	t.Helper()
	for _, r := range d.Rules {
		for _, b := range r.BackendRefs {
			if b.Ref.Ref == ref {
				return b
			}
		}
	}
	t.Fatalf("to-back writes no %v", ref)
	return BackendRefDTO{}
}

func backendRow(resp BackendsResponse, ref objects.Ref) bool {
	return slices.ContainsFunc(resp.Rows, func(r BackendRowDTO) bool { return r.Ref.Ref == ref })
}

// A Service granted only by name opens its backend detail; the backends list,
// a population, leaves it out. Without the get it is a 404.
func TestNamedOnlyServiceDetail(t *testing.T) {
	svc := service("named-svc")
	srv := namedServer(t, grantOf(readableShop, svc))
	d := get[BackendDetail](t, srv, detailURL("-", "Service", "named-svc"), http.StatusOK)
	if d.Ref.Link != LinkBackend {
		t.Errorf("detail ref %+v", d.Ref)
	}
	if list := get[BackendsResponse](t, srv, backendsURL+"?limit=200", http.StatusOK); backendRow(list, svc) || !list.Hidden {
		t.Errorf("the backends list shows %v or hides nothing: %+v", svc, list.Rows)
	}
	get[map[string]any](t, namedServer(t, grantOf(readableShop)), detailURL("-", "Service", "named-svc"), http.StatusNotFound)
}

// An in-scope Backend granted only by name opens its detail; the list leaves it out.
func TestNamedOnlyBackendDetail(t *testing.T) {
	be := kgwBackend("be-unused")
	srv := namedServer(t, grantOf(nil, be))
	if d := get[BackendDetail](t, srv, detailURL(kinds.KgatewayGroup, "Backend", "be-unused"), http.StatusOK); d.Ref.Ref != be {
		t.Errorf("detail of %v", d.Ref)
	}
	if list := get[BackendsResponse](t, srv, backendsURL+"?limit=200", http.StatusOK); backendRow(list, be) {
		t.Errorf("the backends list shows %v", be)
	}
	get[map[string]any](t, namedServer(t, grantOf(nil)), detailURL(kinds.KgatewayGroup, "Backend", "be-unused"), http.StatusNotFound)
}

// A readable route's backendRefs to name-only backends link to their detail,
// which serves them; the backend list has no row for them.
func TestNamedOnlyBackendLink(t *testing.T) {
	svc, be := service("named-svc"), kgwBackend("be-used")
	srv := namedServer(t, grantOf(readableShop, svc, be))
	d := get[RouteDetail](t, srv, toBackURL, http.StatusOK)
	for _, ref := range []objects.Ref{svc, be} {
		if b := writtenRef(t, d, ref); b.Ref.Link != LinkBackend {
			t.Errorf("%v links %q, want backend", ref, b.Ref.Link)
		}
	}
	if b := writtenRef(t, d, be); b.Backend == nil || b.Backend.Restricted {
		t.Errorf("be-used state %+v, want shown", b.Backend)
	}
	if b := writtenRef(t, d, service("ext-svc")); b.Ref.Link != LinkNone || b.Problem != nil {
		t.Errorf("ext-svc, not granted, is %+v", b)
	}
	get[BackendDetail](t, srv, detailURL("-", "Service", "named-svc"), http.StatusOK)
	get[BackendDetail](t, srv, detailURL(kinds.KgatewayGroup, "Backend", "be-used"), http.StatusOK)
	list := get[BackendsResponse](t, srv, backendsURL+"?limit=200", http.StatusOK)
	if backendRow(list, svc) || backendRow(list, be) {
		t.Errorf("the backends list has rows for name-only backends: %+v", list.Rows)
	}
}

// A readable route's backendRef to a Service granted only by name shows its
// path state. An absent target the viewer may get shows as missing, like any
// other; one it may not get is withheld: no state and no link.
func TestNamedRefInReadableRoute(t *testing.T) {
	svc, missing := service("named-svc"), service("missing-svc")
	for _, getMissing := range []bool{true, false} {
		gets := []objects.Ref{svc}
		if getMissing {
			gets = append(gets, missing)
		}
		d := get[RouteDetail](t, namedServer(t, grantOf(readableShop, gets...)), toBackURL, http.StatusOK)
		if b := writtenRef(t, d, svc); b.Problem == nil || b.ShareState == "" {
			t.Errorf("named-svc shows no path state: %+v", b)
		}
		b := writtenRef(t, d, missing)
		shown := b.Problem != nil && slices.ContainsFunc(b.Problem.Signals, func(s SignalDTO) bool { return s.Code == "missing" })
		if shown != getMissing || (b.Ref.Link == LinkBackend) != getMissing {
			t.Errorf("get on missing-svc %v: problem %+v, link %q", getMissing, b.Problem, b.Ref.Link)
		}
	}
}

// listsRef reports whether the list body names ref in a row.
func listsRef(body []byte, ref objects.Ref) bool {
	return strings.Contains(string(body), `"namespace":"`+ref.Namespace+`","name":"`+ref.Name+`"`)
}

// Every detail route admits its subject with a named check: list or get opens
// it, neither is a 404. The matching list, a population, shows it only with list.
func TestNamedCallSitesTable(t *testing.T) {
	_, all, _ := startWith(t, backendsSetup(nil, nil), namedWorld()...)
	var proxy string
	for _, f := range get[Page[FindingRow]](t, all, listBase+"/findings", http.StatusOK).Items {
		if f.Resource != nil && f.Resource.Ref == backGw {
			proxy = f.ID
		}
	}
	ls := objects.Ref{Group: objects.GatewayAPIGroup, Kind: "ListenerSet", Namespace: "infra", Name: "ls1"}
	toBack, api := route("shop", "to-back"), route("shop", "api")
	cases := []struct {
		name, detail, list string
		subject            objects.Ref
		// restricted marks a subject that answers 200 with its row restricted, not 404.
		restricted func(body []byte) bool
	}{
		{name: "route", detail: toBackURL, list: listBase + "/routes?limit=200", subject: toBack},
		{name: "delegation", detail: listBase + "/routes/gateway.networking.k8s.io/HTTPRoute/shop/api/delegation", list: listBase + "/routes?limit=200", subject: api},
		{name: "gateway", detail: listBase + "/gateways/infra/back", list: listBase + "/gateways", subject: backGw},
		{name: "listenerset", detail: listBase + "/listenersets/gateway.networking.k8s.io/ListenerSet/infra/ls1", list: listBase + "/listenersets", subject: ls},
		{name: "resource", detail: listBase + "/resources/gateway.networking.k8s.io/HTTPRoute/shop/to-back", list: listBase + "/routes?limit=200", subject: toBack},
		{name: "finding", detail: listBase + "/findings/" + proxy, list: listBase + "/gateways", subject: backGw,
			restricted: func(b []byte) bool {
				var d FindingDetail
				return json.Unmarshal(b, &d) == nil && d.Finding.Restricted
			}},
	}
	for _, c := range cases {
		for _, g := range []string{"none", "get", "list"} {
			t.Run(c.name+"/"+g, func(t *testing.T) {
				a := grantOf(nil)
				switch g {
				case "get":
					a = grantOf(nil, c.subject)
				case "list":
					a = grantOf([]listKey{{c.subject.Group, c.subject.Kind, c.subject.Namespace}})
				}
				srv := namedServer(t, a)
				status, body := raw(t, srv, c.detail)
				open := status == http.StatusOK && (c.restricted == nil || !c.restricted(body))
				if open != (g != "none") || status != http.StatusOK && status != http.StatusNotFound {
					t.Fatalf("detail %d, open %v: %s", status, open, body)
				}
				if g != "none" && !a.askedNamed(c.subject) {
					t.Errorf("the subject %v was not checked by name", c.subject)
				}
				_, list := raw(t, srv, c.list)
				if listsRef(list, c.subject) != (g == "list") {
					t.Errorf("list %s names %v: %v", c.list, c.subject, listsRef(list, c.subject))
				}
			})
		}
	}
}

// usePools swaps both memo pools for single-slot ones, so every borrowing
// request reuses the same two maps.
func usePools(t *testing.T) {
	usePool(t, newChanMemos(t, 1))
	saved := namedPool
	namedPool = newChanMemos(t, 1)
	t.Cleanup(func() { namedPool = saved })
}

// One request asks by name, then as a population, about the Gateway back
// (list denied, get allowed): the route detail links its parentRef (named) but
// shows no path through it (population). Its finding is left out of
// /findings and its total, while its own finding detail shows it.
func TestNamedThenPopulation(t *testing.T) {
	usePools(t)
	srv := namedServer(t, grantOf([]listKey{shopRoutes}, backGw, service("named-svc")))
	for range 3 {
		d := get[RouteDetail](t, srv, toBackURL, http.StatusOK)
		if len(d.ParentRefs) != 1 || d.ParentRefs[0].Target.Link != LinkDetail {
			t.Errorf("parentRef %+v, want a detail link", d.ParentRefs)
		}
		if len(d.Serves) != 0 {
			t.Errorf("a list-denied Gateway serves the route: %+v", d.Serves)
		}
		fs := get[Page[FindingRow]](t, srv, listBase+"/findings?q=Proxy", http.StatusOK)
		if fs.Total != 0 || len(fs.Items) != 0 {
			t.Errorf("/findings?q=Proxy: total %d, %+v", fs.Total, fs.Items)
		}
		rows := get[Page[FindingRow]](t, srv, listBase+"/findings", http.StatusOK)
		for _, f := range rows.Items {
			if f.Resource != nil && f.Resource.Ref == backGw {
				if !f.Restricted {
					t.Errorf("/findings shows the finding on %v: %+v", backGw, f)
				}
				if fd := get[FindingDetail](t, srv, listBase+"/findings/"+f.ID, http.StatusOK); fd.Finding.Restricted {
					t.Errorf("the finding detail withholds its named subject")
				}
			}
		}
		if gws := get[Page[GatewayListRow]](t, srv, listBase+"/gateways", http.StatusOK); len(gws.Items) != 0 {
			t.Errorf("/gateways lists %+v", gws.Items)
		}
	}
}

// The reverse order: the route detail of shop/api counts its children (a
// population) before its name delegation asks about shop/child by name. The
// count leaves the get-only child out; the delegation still links it.
func TestPopulationThenNamed(t *testing.T) {
	usePools(t)
	child := route("shop", "child")
	for _, scan := range []bool{false, true} {
		a := grantOf(nil, route("shop", "api"), child)
		a.scan = scan
		srv := namedServer(t, a)
		for range 3 {
			d := get[RouteDetail](t, srv, listBase+"/routes/gateway.networking.k8s.io/HTTPRoute/shop/api", http.StatusOK)
			b := writtenRef(t, d, child)
			if b.Ref.Link != LinkDetail || b.Delegation == nil || b.Delegation.State != DelegationReported {
				t.Errorf("scan %v: delegation to child %+v", scan, b)
			}
			if want := map[bool]int{false: 0, true: 1}[scan]; d.ChildCount != want {
				t.Errorf("scan %v: childCount %d, want %d", scan, d.ChildCount, want)
			}
			// A borrowing request between details reuses both pooled memos.
			if p := get[Page[RouteRow]](t, srv, listBase+"/routes?limit=200", http.StatusOK); (len(p.Items) > 0) != scan {
				t.Errorf("scan %v: /routes lists %d rows", scan, len(p.Items))
			}
		}
	}
}
