package api_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/cache"
	"github.com/DuncanDoyle/snellius/pkg/ctrlmetrics"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// Handler tests of what secure mode shows a viewer without the full read
// (D-106): own-object findings whose basis they can read, collection
// failures, and "Not checked" for every other result.

// findingsOf is every finding row user sees.
func findingsOf(t *testing.T, srv *httptest.Server, user string) []api.FindingRow {
	t.Helper()
	return decode[api.Page[api.FindingRow]](t, srv, user, ruleBase+"/findings?limit=200").Items
}

// on is the codes of rows whose resource is ns/name.
func on(rows []api.FindingRow, ns, name string) []string {
	var out []string
	for _, f := range rows {
		if f.Resource != nil && f.Resource.Namespace == ns && f.Resource.Name == name {
			out = append(out, f.Code)
		}
	}
	return out
}

// codes is the set of codes in rows.
func codes(rows []api.FindingRow) map[string]bool {
	out := map[string]bool{}
	for _, f := range rows {
		out[f.Code] = true
	}
	return out
}

// same fails unless user's responses to paths are byte-equal on a and b.
func same(t *testing.T, a, b *httptest.Server, user string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		ca, ba := fetch(t, a, user, ruleBase+p)
		cb, bb := fetch(t, b, user, ruleBase+p)
		if ca != cb || ba != bb {
			t.Errorf("%s as %s differs:\n%d %.400s\n  vs %d %.400s", p, user, ca, ba, cb, bb)
		}
	}
}

// A visible route's ref finding whose named object is hidden is absent for the
// restricted viewer, from the rows, counts and verdict; the full viewer sees it.
func TestFindingBasisHidesFinding(t *testing.T) {
	withRef := ruleServe(t, ruleWorld(t, ruleChange{name: "ref", apply: func(s ruleSnap) {
		u := routeTo(ns0, "basis-route", gw0(), hiddenNS, "absent-be", true)
		failRefs(u)
		s.add(u)
	}}))
	without := ruleServe(t, ruleWorld(t, ruleChange{name: "none", apply: func(s ruleSnap) {
		s.add(routeTo(ns0, "basis-route", gw0(), "", "", true))
	}}))
	full := on(findingsOf(t, withRef, fullUser), ns0, "basis-route")
	if !slices.Contains(full, health.CodeRefUnresolved) && !slices.Contains(full, health.CodeRefNotPermitted) {
		t.Fatalf("full viewer: %v on the route, want its 013 or 014", full)
	}
	if got := on(findingsOf(t, withRef, restrictedUser), ns0, "basis-route"); len(got) != 0 {
		t.Errorf("restricted viewer sees %v on the route", got)
	}
	same(t, withRef, without, restrictedUser, "/health", "/findings?limit=200", "/diagnostics")
}

// An extension check's findings and result need the full read.
func TestExtensionFindingsHiddenFromRestricted(t *testing.T) {
	srv := ruleServe(t, ruleWorld(t, ruleChange{name: "ext", apply: func(s ruleSnap) {
		s.get("Backend", ns0, "be-0000").SetLabels(map[string]string{extLabel: "fail"})
	}}))
	if !codes(findingsOf(t, srv, fullUser))["EXT-001"] {
		t.Fatal("the full viewer lacks the extension finding")
	}
	if codes(findingsOf(t, srv, restrictedUser))["EXT-001"] {
		t.Error("the restricted viewer sees the extension finding")
	}
	for _, c := range decode[api.HealthResponse](t, srv, restrictedUser, ruleBase+"/health").Checks {
		if c.Name == "extension check" && (!c.NotAssessed || !c.OK || c.Error != "") {
			t.Errorf("restricted extension result %+v, want Not checked", c)
		}
	}
}

// stuckIn adds n routes of ns on gw0 whose status is for an older generation.
func stuckIn(s ruleSnap, ns string, n int) {
	for i := range n {
		u := routeTo(ns, fmt.Sprintf("stuck-%02d", i), gw0(), ns, "be-0000", true)
		u.SetGeneration(2)
		s.add(u)
	}
}

// A namespace-scoped viewer sees the own-object findings on their objects
// (002, 003, 013) and none of the derived ones (012, 018, 022). A finding whose
// analyzer named a hidden parent is hidden too, as its basis is per resource (D-108 item 4).
func TestOwnObjectShownDerivedNot(t *testing.T) {
	srv := ruleServe(t, ruleWorld(t, ruleChange{name: "mixed", apply: func(s ruleSnap) {
		stuckIn(s, ns0, 12)
		missing := routeTo(ns0, "missing-ref", gw0(), ns0, "absent-be", true)
		failRefs(missing)
		s.add(missing)
		two := routeTo(ns0, "two-parents", append(gw0(), hiddenGW("hidden-gw")...), "", "", true)
		setParents(two, "Accepted", "NotAllowedByListeners")
		s.add(two)
		// Mixed Gateway API CRD bundles raise a 022.
		s.CRDs[0].BundleVersion = "v1.3.0"
	}}))
	full, restricted := findingsOf(t, srv, fullUser), findingsOf(t, srv, restrictedUser)
	fc, rc := codes(full), codes(restricted)
	for _, c := range []string{health.CodeConditionFalse, health.CodeRouteUnattached, health.CodeRefUnresolved, health.CodeComputedDisagrees,
		health.CodeStatusNotProgressing, health.CodeVersionSkew} {
		if !fc[c] {
			t.Errorf("the full viewer lacks %s; the fixture does not reach it", c)
		}
	}
	for _, c := range []string{health.CodeConditionFalse, health.CodeRouteUnattached, health.CodeRefUnresolved} {
		if !rc[c] {
			t.Errorf("the restricted viewer lacks own-object %s", c)
		}
	}
	for c := range rc {
		if !slices.Contains([]string{health.CodeConditionFalse, health.CodeRouteUnattached, health.CodeRefUnresolved, health.CodeRefNotPermitted, health.CodeRuleReplaced}, c) {
			t.Errorf("the restricted viewer sees derived %s", c)
		}
	}
	if !slices.Contains(on(restricted, ns0, "missing-ref"), health.CodeRefUnresolved) {
		t.Errorf("the restricted viewer lacks the 013 on its route: %v", on(restricted, ns0, "missing-ref"))
	}
	if len(on(full, ns0, "two-parents")) == 0 {
		t.Fatal("no finding on the route with a hidden parent; the fixture does not reach it")
	}
	if got := on(restricted, ns0, "two-parents"); len(got) != 0 {
		t.Errorf("the restricted viewer sees %v on a route whose findings name a hidden parent", got)
	}
}

// The verdict counts and state the findings the viewer is shown.
func TestVerdictOverShownFindings(t *testing.T) {
	srv := ruleServe(t, ruleWorld(t, ruleChange{name: "rejecting", rejects: []ctrlmetrics.GatewayKey{{Namespace: hiddenNS, Name: "hidden-gw"}},
		apply: func(s ruleSnap) {
			s.get("Backend", hiddenNS, "hidden-be").SetLabels(map[string]string{extLabel: "fail"})
		}}))
	for _, user := range []string{fullUser, restrictedUser} {
		d := decode[api.Diagnostics](t, srv, user, ruleBase+"/diagnostics")
		h := decode[api.HealthResponse](t, srv, user, ruleBase+"/health")
		want := map[health.Severity]int{}
		var fs []health.Finding
		for _, f := range findingsOf(t, srv, user) {
			want[f.Severity]++
			fs = append(fs, health.Finding{Severity: f.Severity})
		}
		if fmt.Sprint(d.Verdict.Counts) != fmt.Sprint(want) || fmt.Sprint(h.Counts) != fmt.Sprint(want) {
			t.Errorf("%s: verdict counts %v, health counts %v, want the rows' %v", user, d.Verdict.Counts, h.Counts, want)
		}
		if st := health.RollUp(fs, h.Checks); d.Verdict.State != st || h.State != st {
			t.Errorf("%s: verdict %s, health %s, want %s from the rows and checks", user, d.Verdict.State, h.State, st)
		}
		if d.Verdict.Problems != want[health.SeverityError]+want[health.SeverityWarning] {
			t.Errorf("%s: problems %d, want %v", user, d.Verdict.Problems, want)
		}
	}
	full := decode[api.Diagnostics](t, srv, fullUser, ruleBase+"/diagnostics").Verdict
	restricted := decode[api.Diagnostics](t, srv, restrictedUser, ruleBase+"/diagnostics").Verdict
	if full.Counts[health.SeverityError] <= restricted.Counts[health.SeverityError] {
		t.Errorf("full errors %d, restricted %d: the hidden errors do not reach the full viewer", full.Counts[health.SeverityError], restricted.Counts[health.SeverityError])
	}
}

// checkNamed is the result named name in cs.
func checkNamed(t *testing.T, cs []health.CheckResult, name string) health.CheckResult {
	t.Helper()
	i := slices.IndexFunc(cs, func(c health.CheckResult) bool { return c.Name == name })
	if i < 0 {
		t.Fatalf("no %q result", name)
	}
	return cs[i]
}

// A route in its grace period holds the resource status result: the full
// viewer sees it pending and unknown; the restricted viewer sees "Not checked",
// which raises no unknown.
func TestPendingResultNotCheckedForRestricted(t *testing.T) {
	srv := ruleServe(t, ruleWorld(t, ruleChange{name: "pending", late: func(s ruleSnap) {
		s.add(routeTo(ns0, "fresh", gw0(), ns0, "be-0000", false))
	}}))
	full := checkNamed(t, decode[api.HealthResponse](t, srv, fullUser, ruleBase+"/health").Checks, "resource status")
	if full.OK || !full.Pending {
		t.Fatalf("full resource status %+v, want pending", full)
	}
	h := decode[api.HealthResponse](t, srv, restrictedUser, ruleBase+"/health")
	if r := checkNamed(t, h.Checks, "resource status"); !r.NotAssessed || !r.OK || r.Pending || r.Error != "" || r.Hold != nil {
		t.Errorf("restricted resource status %+v, want Not checked", r)
	}
	if d := decode[api.Diagnostics](t, srv, restrictedUser, ruleBase+"/diagnostics"); d.Verdict.Reason != "" {
		t.Errorf("restricted verdict unknown because of %q", d.Verdict.Reason)
	}
	if fs := findingsOf(t, srv, restrictedUser); !slices.Contains(on(fs, ns0, "fresh"), health.CodeRouteUnattached) {
		t.Errorf("restricted viewer lacks the pending route's own 003: %v", on(fs, ns0, "fresh"))
	}
}

// A denied Pod watch is a collection failure: both viewers see the check
// and its 008, the restricted one with fixed text.
func TestCollectionFailureShownToAll(t *testing.T) {
	srv := ruleServe(t, ruleWorld(t, ruleChange{name: "denied", apply: func(s ruleSnap) {
		s.Access[cache.PodsGVR] = cache.Access{Err: `pods is forbidden: User "system:serviceaccount:ops:reader" cannot list resource "pods"`, Denied: true}
	}}))
	for _, user := range []string{fullUser, restrictedUser} {
		h := decode[api.HealthResponse](t, srv, user, ruleBase+"/health")
		if c := checkNamed(t, h.Checks, "cluster access"); c.OK || c.NotAssessed || c.Error == "" {
			t.Errorf("%s: cluster access %+v, want the failure", user, c)
		}
		if !codes(findingsOf(t, srv, user))[health.CodeSignalUnavailable] {
			t.Errorf("%s lacks the KGWUI-008", user)
		}
	}
}

// stuckWorld is twelve stuck routes in ns-000, with the controller ready
// (KGWUI-018 groups their 003s) or not (no 018).
func stuckWorld(t *testing.T, ready bool) *httptest.Server {
	return ruleServe(t, ruleWorld(t, ruleChange{name: "stuck", apply: func(s ruleSnap) {
		stuckIn(s, ns0, 12)
		if !ready {
			s.deployment("kgw", "kgateway").Status.ReadyReplicas = 0
		}
	}}))
}

// groupsOf maps each finding ID to its group in /health, /findings and the /diagnostics groups.
func groupsOf(t *testing.T, srv *httptest.Server, user string) (map[string]string, map[string]string, []string) {
	t.Helper()
	h, f := map[string]string{}, map[string]string{}
	for _, d := range decode[api.HealthResponse](t, srv, user, ruleBase+"/health").Findings {
		h[d.ID] = d.Group
	}
	for _, r := range findingsOf(t, srv, user) {
		f[r.ID] = r.Group
	}
	var ids []string
	for _, g := range decode[api.Diagnostics](t, srv, user, ruleBase+"/diagnostics").Groups {
		ids = append(ids, g.ID)
	}
	return h, f, ids
}

// A group whose cause the viewer cannot read does not form: its members are
// ungrouped, and the total counts the groups the viewer is shown.
func TestGroupsOverShownFindings(t *testing.T) {
	srv := stuckWorld(t, true)
	full := decode[api.Diagnostics](t, srv, fullUser, ruleBase+"/diagnostics")
	if !slices.ContainsFunc(full.Groups, func(g api.GroupRow) bool { return g.Cause != nil && g.Cause.Kind == "Deployment" }) {
		t.Fatal("the full viewer has no status-not-progressing group")
	}
	d := decode[api.Diagnostics](t, srv, restrictedUser, ruleBase+"/diagnostics")
	groups := decode[api.Page[api.GroupRow]](t, srv, restrictedUser, ruleBase+"/findings?view=groups&limit=200")
	for _, g := range append(slices.Clone(d.Groups), groups.Items...) {
		if g.Restricted || g.Title == "Details hidden" || g.Cause != nil && !readable(g.Cause.Ref) {
			t.Errorf("restricted group %+v has a hidden cause", g)
		}
	}
	attention := 0
	for _, g := range groups.Items {
		if g.Severity != health.SeverityInfo && g.Code != health.CodeSignalUnavailable {
			attention++
		}
	}
	if d.GroupTotal != attention || d.GroupTotal >= full.GroupTotal+12 {
		t.Errorf("restricted groupTotal %d, want the %d shown groups that need attention", d.GroupTotal, attention)
	}
}

// The 003 warnings the status-not-progressing family binds to a 018 the
// viewer is not shown keep the groups of a world without the 018, everywhere.
func TestGroupIDsWithoutHiddenProgress(t *testing.T) {
	with, without := stuckWorld(t, true), stuckWorld(t, false)
	fh, _, _ := groupsOf(t, with, fullUser)
	wh, wf, wd := groupsOf(t, with, restrictedUser)
	nh, nf, nd := groupsOf(t, without, restrictedUser)
	bound := 0
	for id, g := range wh {
		if fg, ok := fh[id]; ok && fg != g {
			bound++
		}
	}
	if bound == 0 {
		t.Fatal("no shown finding is grouped differently for the full viewer; the fixture does not bind a 018")
	}
	if fmt.Sprint(wh) != fmt.Sprint(nh) || fmt.Sprint(wf) != fmt.Sprint(nf) || !slices.Equal(wd, nd) {
		t.Errorf("groups differ from a world without the 018:\n/health %v\n   vs %v\n/findings %v\n   vs %v\n/diagnostics %v\n   vs %v", wh, nh, wf, nf, wd, nd)
	}
}

// The data plane counts readable Gateways and the rejects the viewer is shown.
func TestDataPlaneShownOnly(t *testing.T) {
	srv := ruleServe(t, ruleWorld(t, ruleChange{name: "rejecting", rejects: []ctrlmetrics.GatewayKey{{Namespace: hiddenNS, Name: "hidden-gw"}, {Namespace: ns0, Name: testutil.ScaleGateway(0)}}}))
	full := decode[api.Diagnostics](t, srv, fullUser, ruleBase+"/diagnostics").DataPlane
	if full.Rejecting != 2 || full.Gateways != 15 {
		t.Fatalf("full data plane %+v, want 2 of 15 rejecting", full)
	}
	// 001 is derived, so the restricted viewer counts no reject, only its eleven Gateways.
	if r := decode[api.Diagnostics](t, srv, restrictedUser, ruleBase+"/diagnostics").DataPlane; r.Rejecting != 0 || r.Withheld != 0 || r.Gateways != 11 {
		t.Errorf("restricted data plane %+v, want 0 of 11 rejecting", r)
	}
}

// A finding the viewer is not shown answers the 404 of an absent ID. A shown
// one answers whole, and one whose resource the viewer may only get by name
// answers whole and ungrouped, though the list leaves it out.
func TestFindingDetailHidden404(t *testing.T) {
	srv := ruleServe(t, ruleWorld(t, ruleChange{name: "faulty+named", apply: func(s ruleSnap) {
		ruleChanges[slices.IndexFunc(ruleChanges, func(c ruleChange) bool { return c.name == "faulty" })].apply(s)
		named := routeTo(hiddenNS, namedOnly, gw0(), "", "", true)
		setParents(named, "Accepted", "NotAllowedByListeners")
		s.add(named)
	}}))
	var hidden, byName string
	for _, f := range findingsOf(t, srv, fullUser) {
		switch {
		case f.Resource != nil && f.Resource.Name == namedOnly && f.Code == health.CodeConditionFalse:
			byName = f.ID
		case f.Resource != nil && f.Resource.Namespace == hiddenNS && hidden == "":
			hidden = f.ID
		}
	}
	shown := findingsOf(t, srv, restrictedUser)
	if hidden == "" || byName == "" || len(shown) == 0 {
		t.Fatalf("fixture: hidden %q, by name %q, %d shown", hidden, byName, len(shown))
	}
	if code, _ := fetch(t, srv, fullUser, ruleBase+"/findings/"+hidden); code != http.StatusOK {
		t.Errorf("full viewer: %d", code)
	}
	code, body := fetch(t, srv, restrictedUser, ruleBase+"/findings/"+hidden)
	absentCode, absent := fetch(t, srv, restrictedUser, ruleBase+"/findings/f1-aaaaaaaaaaaaaaaaaaaaaaaaaa")
	if code != http.StatusNotFound || absentCode != code || strings.ReplaceAll(body, hidden, "") != strings.ReplaceAll(absent, "f1-aaaaaaaaaaaaaaaaaaaaaaaaaa", "") {
		t.Errorf("hidden finding: %d %s, absent one: %d %s", code, body, absentCode, absent)
	}
	d := decode[api.FindingDetail](t, srv, restrictedUser, ruleBase+"/findings/"+shown[0].ID)
	if d.Finding.Restricted || d.Finding.EvidenceRestricted || d.Finding.Message == "" || d.Finding.Group != shown[0].Group {
		t.Errorf("shown finding detail %+v, want it whole, as listed", d.Finding)
	}
	if slices.ContainsFunc(shown, func(f api.FindingRow) bool { return f.ID == byName }) {
		t.Fatal("the list shows a finding whose resource the viewer may get by name only")
	}
	n := decode[api.FindingDetail](t, srv, restrictedUser, ruleBase+"/findings/"+byName)
	if n.Finding.Restricted || n.Finding.Message == "" || n.Finding.Group != "" || n.Affected.Total != 0 {
		t.Errorf("by-name finding detail %+v, want it whole and ungrouped", n.Finding)
	}
}

// nsAuthz is a registry authoriser outside secure mode: the first ten scale namespaces.
type nsAuthz struct{}

func (nsAuthz) Authorize(_ *http.Request, ref objects.Ref) bool { return readable(ref) }

// Outside secure mode a restricted authoriser keeps D-013's restricted rows,
// and state and counts over every finding.
func TestLocalRegistryAuthorizerKeepsRestrictedRows(t *testing.T) {
	src := ruleWorld(t, ruleChanges[slices.IndexFunc(ruleChanges, func(c ruleChange) bool { return c.name == "faulty" })])
	local := httptest.NewServer(api.NewHandler(src, fstest.MapFS{}, nsAuthz{}))
	t.Cleanup(local.Close)
	all := httptest.NewServer(api.NewHandler(src, fstest.MapFS{}, nil))
	t.Cleanup(all.Close)
	h := decode[api.HealthResponse](t, local, "", ruleBase+"/health")
	a := decode[api.HealthResponse](t, all, "", ruleBase+"/health")
	if len(h.Findings) != len(a.Findings) || fmt.Sprint(h.Counts) != fmt.Sprint(a.Counts) || h.State != a.State {
		t.Fatalf("restricted %d findings %v %s, all %d %v %s", len(h.Findings), h.Counts, h.State, len(a.Findings), a.Counts, a.State)
	}
	restricted := 0
	for _, f := range h.Findings {
		if f.Resource != nil && !readable(f.Resource.Ref) {
			if !f.Restricted || f.Message != "" {
				t.Errorf("hidden %s on %v not restricted: %+v", f.Code, f.Resource.Ref, f)
			}
			restricted++
		}
	}
	if restricted == 0 {
		t.Error("no restricted row")
	}
	for _, c := range h.Checks {
		if c.NotAssessed {
			t.Errorf("check %s is Not checked outside secure mode", c.Name)
		}
	}
}
