package own

import (
	"regexp"
	"slices"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// RuleReplacedHint ends a 015's detail once the rule has been replaced for
// longer than RuleReplacedHintAge (kgateway #14047).
const RuleReplacedHint = "If the cause was temporary, such as an identity provider outage, some versions keep the rule replaced until the controller restarts."

// RuleReplacedHintAge is how long a rule stays replaced before the hint shows.
const RuleReplacedHintAge = 10 * time.Minute

// Patterns of the controller's rule message, compiled per use: the package holds no state.
const (
	// ruleMessagePattern is "Replaced Rule (%d): …" or "Dropped Rule (%d): …".
	ruleMessagePattern = `(?s)^(Replaced|Dropped) Rule \((\d+)\): (.*)$`
	// policyRefPattern is a group/Kind/namespace/name ref in the text after the colon.
	policyRefPattern = `([a-z0-9][-a-z0-9.]*)/([A-Z][A-Za-z0-9]*)/([a-z0-9][-a-z0-9]*)/([a-z0-9][-a-z0-9.]*[a-z0-9]|[a-z0-9])`
)

// rules reports KGWUI-015 on a route: a current RouteRuleReplaced or
// RouteRuleDropped on one of the rule set's rejected condition types.
func rules(ic InstallContext, u *unstructured.Unstructured, named Named, o *out) {
	v := viewOf(ic.Views, u)
	if ignored(ic, u, v) {
		return
	}
	eachCurrentBlock(ic, u, v, func(_ objects.ParentRef, cond metav1.Condition, extra string, owner []string) {
		if _, _, refs := RefRuleFor(cond, false); refs || !ruleReplacedReason(cond, ic.Conditions.RouteRejected) {
			return
		}
		o.add(ruleFinding(ic, o, cond, extra, owner, named))
	})
}

// ruleFinding is the 015 for cond on a route parent; owner is the parent's
// key. The hint's due time is the evaluation's wake time.
func ruleFinding(ic InstallContext, o *out, cond metav1.Condition, extra string, owner []string, named Named) Finding {
	ev := &RuleReplacedEvidence{Reason: cond.Reason, Message: clipDisplay(cond.Message, MaxStatusBytes)}
	match := ""
	if m := regexp.MustCompile(ruleMessagePattern).FindStringSubmatch(cond.Message); m != nil {
		if n, err := strconv.Atoi(m[2]); err == nil {
			ev.MatchIndex, match = &n, m[2]
			ev.Policy = namedPolicy(ic, m[3])
		}
	}
	f := Finding{code: CodeRuleReplaced, slug: "rule-replaced", severity: severityError, message: ConditionMessage(cond),
		detail: ConditionDetail(cond, extra), since: since(cond), facts: ev, key: append(slices.Clone(owner), match, cond.Type)}
	if ev.Policy != nil {
		named.Object(*ev.Policy)
		f.evidence = []objects.Ref{*ev.Policy}
	}
	// The hint counts from since, the condition's lastTransitionTime (R77).
	if f.since != nil && cond.Reason == "RouteRuleReplaced" {
		due := f.since.Add(RuleReplacedHintAge)
		if ic.Now.After(due) {
			f.detail += ". " + RuleReplacedHint
		} else {
			o.wakeAt(due.Add(time.Nanosecond))
		}
	}
	return f
}

// namedPolicy returns the one served policy kind text names, or nil when it
// names none or several. Without complete discovery no kind is known served (R77).
func namedPolicy(ic InstallContext, text string) *objects.Ref {
	if !ic.Catalog.Complete() {
		return nil
	}
	var found *objects.Ref
	for _, m := range regexp.MustCompile(policyRefPattern).FindAllStringSubmatch(text, -1) {
		r := objects.Ref{Group: m[1], Kind: m[2], Namespace: m[3], Name: m[4]}
		if !slices.Contains(ic.PolicyKinds, objects.GroupKind{Group: r.Group, Kind: r.Kind}) || !ic.Catalog.Served(r.Group, r.Kind) {
			continue
		}
		if found != nil && *found != r {
			return nil
		}
		found = &r
	}
	return found
}
