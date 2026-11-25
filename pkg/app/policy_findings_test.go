package app

import (
	"net/http"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// policyAt is policy ns/name of kind at generation gen with one ancestor on gw, by kgateway's controller.
func policyAt(apiVersion, kind, name string, gen int64, gw objects.ParentRef, cs ...metav1.Condition) *unstructured.Unstructured {
	u := testutil.Policy(apiVersion, kind, "shop", name, testutil.PolicyAncestor{Ref: gw, Controller: testutil.Controller, Conds: cs})
	u.SetGeneration(gen)
	return u
}

// condAt is testutil.Cond observed at generation gen.
func condAt(typ string, status metav1.ConditionStatus, reason string, gen int64) metav1.Condition {
	c := testutil.Cond(typ, status, reason)
	c.ObservedGeneration = gen
	return c
}

// On the 2.5 rules, /findings and the policy list's "Has problems" agree per policy (phase-8.4
// brief rows 1 and 4): an Attached older than the policy raises no 016, and the kgateway reason
// readings apply to kgateway kinds only; a downstream kind with no interpreter gets none of them.
func TestPolicyFindingsAgreeWithHasProblems(t *testing.T) {
	d := testutil.ControllerDeployment("kgw", "kgateway", "cr.kgateway.dev/kgateway-dev/kgateway:v2.5.1", 1, 1)
	d.Labels["app.kubernetes.io/version"] = "2.5.1"
	gw := testutil.GwRef("infra", "edge")
	const tpV, wpV = "gateway.kgateway.dev/v1alpha1", "widgets.acme.example.com/v1"
	accepted := condAt("Accepted", metav1.ConditionTrue, "Valid", 2)
	objs := []runtime.Object{
		testutil.Namespace("infra", nil), testutil.Namespace("shop", nil), d, testutil.ControllerPod(d, "kgateway-0", true),
		testutil.GatewayClass("kgateway", testutil.Controller, true), testutil.Gateway("infra", "edge", "kgateway", nil),
		testutil.HTTPRoute("shop", "r", []objects.ParentRef{gw}, testutil.AcceptedBy(gw, testutil.Controller)),
		policyAt(tpV, "TrafficPolicy", "stale-attached", 2, gw, accepted, condAt("Attached", metav1.ConditionFalse, "TargetNotFound", 1)),
		policyAt(tpV, "TrafficPolicy", "target-missing", 2, gw, accepted, condAt("Attached", metav1.ConditionFalse, "TargetNotFound", 2)),
		policyAt(tpV, "TrafficPolicy", "tp-partial", 2, gw, condAt("Accepted", metav1.ConditionTrue, "PartiallyValid", 2)),
		policyAt(tpV, "TrafficPolicy", "tp-overridden", 2, gw, accepted, condAt("Attached", metav1.ConditionTrue, "Overridden", 2)),
		policyAt(wpV, "WidgetPolicy", "wp-partial", 2, gw, condAt("Accepted", metav1.ConditionTrue, "PartiallyValid", 2)),
		policyAt(wpV, "WidgetPolicy", "wp-overridden", 2, gw, accepted, condAt("Attached", metav1.ConditionTrue, "Overridden", 2)),
		policyAt(wpV, "WidgetPolicy", "wp-rejected", 2, gw, condAt("Accepted", metav1.ConditionFalse, "Invalid", 2)),
	}
	_, srv := start(t, objs, []kinds.Descriptor{widgetKind}, Options{PollInterval: 50 * time.Millisecond, MetricsInterval: time.Hour, UI: ui},
		&stubScraper{}, extension.WithKinds(widgetKind))

	type want struct {
		code string // "" for no finding on the policy
		sev  health.Severity
		has  bool
	}
	wants := map[string]want{
		"stale-attached": {"", "", false},
		"target-missing": {"KGWUI-016", health.SeverityError, true},
		"tp-partial":     {"KGWUI-002", health.SeverityWarning, true},
		"tp-overridden":  {"KGWUI-002", health.SeverityInfo, false},
		"wp-partial":     {"", "", false},
		"wp-overridden":  {"", "", false},
		"wp-rejected":    {"KGWUI-002", health.SeverityError, true},
	}
	const base = "/api/v1/installs/kgw.kgateway"
	var rows api.PoliciesResponse
	eventually(t, "policies never listed", func() bool {
		rows = api.PoliciesResponse{}
		return getJSON(t, srv, base+"/policies?limit=100", &rows) == http.StatusOK && len(rows.Items) == len(wants)
	})
	var fs api.Page[api.FindingRow]
	eventually(t, "policy findings never appeared", func() bool {
		fs = api.Page[api.FindingRow]{}
		getJSON(t, srv, base+"/findings?limit=200", &fs)
		for _, f := range fs.Items {
			if f.Code == "KGWUI-016" {
				return true
			}
		}
		return false
	})
	got := map[string][]api.FindingRow{}
	for _, f := range fs.Items {
		if f.Resource != nil && f.Resource.Namespace == "shop" && f.Resource.Name != "r" {
			got[f.Resource.Name] = append(got[f.Resource.Name], f)
		}
	}
	for _, r := range rows.Items {
		w := wants[r.Ref.Name]
		if r.Problems.Has != w.has {
			t.Errorf("%s: has problems = %v, want %v", r.Ref.Name, r.Problems.Has, w.has)
		}
		f := got[r.Ref.Name]
		switch {
		case w.code == "" && len(f) != 0:
			t.Errorf("%s: want no finding, got %+v", r.Ref.Name, f)
		case w.code != "" && (len(f) != 1 || f[0].Code != w.code || f[0].Severity != w.sev):
			t.Errorf("%s: want one %s %s, got %+v", r.Ref.Name, w.code, w.sev, f)
		}
	}
}
