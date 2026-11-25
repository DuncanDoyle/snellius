package api

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/install"
)

// sameNameCheck is a downstream check that shares the kgateway backend check's name.
type sameNameCheck struct{}

func (sameNameCheck) Name() string            { return "backend status" }
func (sameNameCheck) Inputs() health.InputSet { return health.InputSet{} }
func (sameNameCheck) Run(health.RunContext) ([]health.Finding, health.CheckResult) {
	return nil, health.CheckResult{Name: "backend status", OK: true}
}

// overriddenReading is the KGWUI-002 severity and "has problems" of a TrafficPolicy
// with a current Attached=False/Overridden, served by NewHandler without options.
func overriddenReading(t *testing.T, s setup) (health.Severity, bool) {
	t.Helper()
	tp := testutil.TrafficPolicy("shop", "tp", testutil.PolicyAncestor{Ref: edgeRef, Controller: testutil.Controller, Conds: []metav1.Condition{
		testutil.Cond("Accepted", metav1.ConditionTrue, "Valid"), testutil.Cond("Attached", metav1.ConditionFalse, "Overridden")}})
	h := newHarness(t, s, world(tp)...)
	var sev health.Severity
	for _, f := range get[HealthResponse](t, h.srv, base+"/health", 200).Findings {
		if f.Code == health.CodeConditionFalse && f.Resource != nil && f.Resource.String() == "shop/tp" {
			sev = f.Severity
		}
	}
	rows := get[PoliciesResponse](t, h.srv, base+"/policies?limit=200", 200)
	for _, r := range rows.Items {
		if r.Ref.Name == "tp" {
			return sev, r.Problems.Has
		}
	}
	t.Fatalf("tp not listed: %+v", rows.Items)
	return "", false
}

// The engine resolves one interpreter, which /findings and "Has problems" share: the
// default kgateway engine reads Overridden as info, with no handler option (8.4 re-review, new 2).
func TestDefaultEngineAgreesOnOverridden(t *testing.T) {
	if sev, has := overriddenReading(t, setup{}); sev != health.SeverityInfo || has {
		t.Errorf("finding %q, has problems %v; want info, false", sev, has)
	}
}

// A downstream check named like a kgateway check supplies no kgateway reading
// (8.4 re-review, new 1): the policy gets the generic error and has problems.
func TestSameNameCheckReadsNothing(t *testing.T) {
	hooks := func(i install.Install) engine.Hooks {
		h := packHooks(i)
		h.Checks = append(health.Core(), sameNameCheck{})
		return h
	}
	sev, has := overriddenReading(t, setup{detectors: []install.Detector{kgwDetector{}}, hooks: hooks})
	if sev != health.SeverityError || !has {
		t.Errorf("finding %q, has problems %v; want error, true", sev, has)
	}
}
