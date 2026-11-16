package health

import (
	"reflect"
	"regexp"
	"testing"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// readSet is a viewer who may read exactly the refs set true.
type readSet map[objects.Ref]bool

func (s readSet) Ref(r objects.Ref) bool { return s[r] }

// readAll is a viewer without the full read who may still read every object.
type readAll struct{}

func (readAll) Ref(objects.Ref) bool { return true }

// An own-object finding needs its resource and every named ref readable; a
// derived one is never shown by Project; a collection one needs only its resource.
func TestShownToTable(t *testing.T) {
	res, gw, svc := objects.Ref{Kind: "HTTPRoute", Namespace: "shop", Name: "cart"}, testutil.GwRef("infra", "edge").Ref(),
		objects.Ref{Kind: "Service", Namespace: "shop", Name: "cart"}
	ownF := Finding{Resource: &res, Class: ClassOwn, Basis: Basis{Refs: []objects.Ref{gw, svc}}}
	all := readSet{res: true, gw: true, svc: true}
	without := func(r objects.Ref) readSet {
		s := readSet{res: true, gw: true, svc: true}
		delete(s, r)
		return s
	}
	cases := []struct {
		name string
		f    Finding
		rd   Readable
		want bool
	}{
		{"own, all readable", ownF, all, true},
		{"own, resource hidden", ownF, without(res), false},
		{"own, Gateway hidden", ownF, without(gw), false},
		{"own, Service hidden", ownF, without(svc), false},
		{"derived", Finding{Resource: &res}, readAll{}, false},
		{"collection without resource", Finding{Basis: Basis{Collection: true}}, readSet{}, true},
		{"collection, resource hidden", Finding{Resource: &res, Basis: Basis{Collection: true}}, readSet{}, false},
		{"collection, resource readable", Finding{Resource: &res, Basis: Basis{Collection: true}}, all, true},
	}
	for _, tc := range cases {
		if got := tc.f.ShownTo(tc.rd); got != tc.want {
			t.Errorf("%s: ShownTo = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A hidden error finding leaves state and counts as if it were absent.
func TestProjectCountsAndState(t *testing.T) {
	hidden, shown := objects.Ref{Kind: "HTTPRoute", Namespace: "a", Name: "x"}, objects.Ref{Kind: "HTTPRoute", Namespace: "b", Name: "y"}
	rep := Report{Install: "i", State: StateError, Counts: map[Severity]int{SeverityError: 1, SeverityWarning: 1},
		Findings: []Finding{
			{Code: CodeConditionFalse, Severity: SeverityError, Resource: &hidden, Class: ClassOwn, Group: "g1"},
			{Code: CodeRouteUnattached, Severity: SeverityWarning, Resource: &shown, Class: ClassOwn, Group: "g2"},
		},
		Checks: []CheckResult{{Name: "resource status", OK: true, Required: true}}}
	got := Project(rep, readSet{shown: true})
	if got.State != StateWarning || !reflect.DeepEqual(got.Counts, map[Severity]int{SeverityWarning: 1}) {
		t.Fatalf("state %s counts %v, want warning and one warning", got.State, got.Counts)
	}
	if len(got.Findings) != 1 || *got.Findings[0].Resource != shown || got.Findings[0].Group != "" {
		t.Fatalf("findings %+v, want the shown one with its group cleared", got.Findings)
	}
	if rep.Findings[1].Group != "g2" || rep.Counts[SeverityError] != 1 {
		t.Fatal("Project must not change the report it is given")
	}
}

// projectWorlds are fixture reports with collection failures, pending
// resources and own-object findings.
func projectWorlds(t *testing.T) map[string]Report {
	t.Helper()
	out := map[string]Report{}
	for name, w := range ownWorlds(t) {
		out[name] = Evaluate(w.in, Upstream(), w.rt)
	}
	return out
}

// For a viewer without the full read, every result other than a collection
// failure is "Not checked", whatever objects they may read; a full reader's
// report is never projected.
func TestResultsNotCheckedForRestricted(t *testing.T) {
	for name, rep := range projectWorlds(t) {
		for _, rd := range []Readable{readAll{}, readSet{}} {
			got := Project(rep, rd)
			for i, c := range got.Checks {
				orig := rep.Checks[i]
				if orig.Collection {
					if !reflect.DeepEqual(c, orig) {
						t.Errorf("%s: collection result %q changed: %+v", name, orig.Name, c)
					}
					continue
				}
				want := CheckResult{Name: orig.Name, Required: orig.Required, CollectedAt: orig.CollectedAt, OK: true, NotAssessed: true}
				if !reflect.DeepEqual(c, want) {
					t.Errorf("%s: result %q = %+v, want Not checked", name, orig.Name, c)
				}
			}
		}
	}
}

// A "Not checked" result never makes the state unknown.
func TestNotCheckedNeverUnknown(t *testing.T) {
	for name, rep := range projectWorlds(t) {
		got := Project(rep, readAll{})
		collection := false
		for _, c := range got.Checks {
			collection = collection || c.Collection && c.Required && !c.OK && c.Unavailable == ""
		}
		if got.State == StateUnknown && !collection {
			t.Errorf("%s: state unknown without a collection failure", name)
		}
	}
}

// A denied watch is shown to every viewer, with fixed text, and keeps the state unknown.
func TestCollectionFailureStays(t *testing.T) {
	w := GuardWorlds(t)["namespaces unreadable"]
	rep := Evaluate(w.In, Upstream(), w.RT)
	got := Project(rep, readSet{})
	var access *CheckResult
	for i := range got.Checks {
		if got.Checks[i].Name == (cacheCheck{}).Name() {
			access = &got.Checks[i]
		}
	}
	if access == nil || !access.Collection || access.NotAssessed || access.OK || access.Error != "1 kinds not readable" {
		t.Fatalf("cluster access = %+v, want the collection failure whole", access)
	}
	if got.State != StateUnknown {
		t.Fatalf("state %s, want unknown", got.State)
	}
	shown := false
	for _, f := range got.Findings {
		shown = shown || f.Code == CodeSignalUnavailable && f.Basis.Collection
	}
	if !shown {
		t.Fatal("the collection KGWUI-008 must be shown to every viewer")
	}
}

// No collection result's text names an object or namespace of its world.
func TestCollectionTextObjectFree(t *testing.T) {
	collections := 0
	for name, w := range ownWorlds(t) {
		rep := Evaluate(w.in, Upstream(), w.rt)
		var words []string
		for _, objs := range w.in.Snapshot.Objects {
			for _, u := range objs {
				words = append(words, u.GetName(), u.GetNamespace())
			}
		}
		for _, n := range w.in.Snapshot.Namespaces {
			words = append(words, n.Name)
		}
		for _, c := range rep.Checks {
			if !c.Collection {
				continue
			}
			collections++
			for _, word := range words {
				if len(word) > 2 && regexp.MustCompile(`\b`+regexp.QuoteMeta(word)+`\b`).MatchString(c.Error+" "+c.Detail) {
					t.Errorf("%s: collection result %q names %q: %s", name, c.Name, word, c.Error)
				}
			}
		}
	}
	if collections == 0 {
		t.Fatal("the fixtures have no collection result")
	}
}

// A nil viewer is the full read: Project returns the report unchanged.
func TestProjectNilIsIdentity(t *testing.T) {
	for name, rep := range projectWorlds(t) {
		if got := Project(rep, nil); !reflect.DeepEqual(got, rep) {
			t.Errorf("%s: Project with a nil viewer changed the report", name)
		}
	}
}

// A check outside runOwn, such as an extension's, raises derived findings:
// hidden from a viewer without the full read, shown to a full reader.
func TestExtensionFindingsDerived(t *testing.T) {
	res := objects.Ref{Kind: "HTTPRoute", Namespace: "shop", Name: "cart"}
	rep := Evaluate(Input{}, []Check{fixedCheck{{Code: "ACME-001", Severity: SeverityError, Resource: &res, Message: "m"}}}, Runtime{})
	if f := rep.Findings[0]; f.Class != ClassDerived || f.Basis.Collection || len(f.Basis.Refs) > 0 {
		t.Fatalf("extension finding %+v, want derived", f)
	}
	if got := Project(rep, readAll{}); len(got.Findings) != 0 || got.State != StateHealthy {
		t.Fatalf("restricted viewer sees %+v (state %s), want nothing", got.Findings, got.State)
	}
	if len(rep.Findings) != 1 || rep.State != StateError {
		t.Fatal("a full reader sees the extension finding")
	}
}

func TestRollUpOrder(t *testing.T) {
	warn := Finding{Severity: SeverityWarning}
	errF := Finding{Severity: SeverityError}
	info := Finding{Severity: SeverityInfo}
	missing := CheckResult{Name: "metrics", Required: true, OK: false}
	optional := CheckResult{Name: "verify", Required: false, OK: false}
	cases := []struct {
		name string
		fs   []Finding
		cs   []CheckResult
		want State
	}{
		{"nothing is healthy", nil, nil, StateHealthy},
		{"info only is healthy", []Finding{info}, nil, StateHealthy},
		{"warning", []Finding{warn}, nil, StateWarning},
		{"unknown beats warning", []Finding{warn}, []CheckResult{missing}, StateUnknown},
		{"error beats unknown", []Finding{errF}, []CheckResult{missing}, StateError},
		{"optional check failing is not unknown", nil, []CheckResult{optional}, StateHealthy},
	}
	for _, tc := range cases {
		if got := RollUp(tc.fs, tc.cs); got != tc.want {
			t.Errorf("%s: want %s got %s", tc.name, tc.want, got)
		}
	}
}

type fixedCheck []Finding

func (fixedCheck) Name() string     { return "fixed" }
func (fixedCheck) Inputs() InputSet { return InputSet{} }
func (c fixedCheck) Run(ctx RunContext) ([]Finding, CheckResult) {
	return c, CheckResult{Name: "fixed", OK: true, CollectedAt: ctx.Now}
}

// A downstream check cannot hide a finding behind a severity the roll-up does
// not know: Evaluate reports it as a warning and names the original severity.
func TestEvaluateFlagsUnrecognisedSeverity(t *testing.T) {
	r := Evaluate(Input{}, []Check{fixedCheck{{Code: "ACME-001", Severity: "critical", Message: "m"}, {Code: "ACME-002", Severity: SeverityInfo}}}, Runtime{})
	if r.State != StateWarning || r.Counts["critical"] != 0 || r.Counts[SeverityWarning] != 1 {
		t.Fatalf("want warning with the finding counted as a warning: state=%s counts=%v", r.State, r.Counts)
	}
	f := r.Findings[0]
	if f.Code != "ACME-001" || f.Severity != SeverityWarning || f.Detail != `unrecognised severity "critical"` {
		t.Fatalf("finding must be a flagged warning, ordered first: %+v", r.Findings)
	}
}

// Severities outside the known set order after info.
func TestUnknownSeverityRanksLast(t *testing.T) {
	if compareFindings(Finding{Severity: "critical"}, Finding{Severity: SeverityInfo}) <= 0 {
		t.Fatal("an unknown severity must order after info")
	}
}
