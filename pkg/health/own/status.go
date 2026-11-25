package own

import (
	"slices"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// DelegationNoChildMessage replaces "Backend not found" on a parent whose
// backendRefs all delegate (D-021 item 9).
const DelegationNoChildMessage = "A delegating rule has no accepted child route"

// DelegationNoChildDetail starts that finding's detail; the raw condition follows.
const DelegationNoChildDetail = "A delegating rule selects no child route the controller accepted"

// status reports KGWUI-002 from the resource's own status: a Gateway's or
// ListenerSet's conditions and listeners, a route's parents, a policy's ancestors.
// The 003, 013, 014, 015 and 017 that own a block or condition are reported elsewhere.
func status(ic InstallContext, u *unstructured.Unstructured, named Named, o *out, w *walk) {
	gvk := u.GroupVersionKind()
	k, ok := served(ic, gvk.Group, gvk.Kind)
	if !ok {
		return
	}
	self := []string{OwnerSelf}
	switch k.Category {
	case kinds.CategoryGateway:
		v := ic.Views.Gateway(u)
		if len(v.Conditions) > 0 {
			conditionBlock(o, u, v.Conditions, GatewayRequired(), nil, "", self, "")
		}
		listenerBlocks(o, u, v.ListenerStatus)
	case kinds.CategoryListenerSet:
		v := ic.Views.ListenerSet(u)
		if len(v.Conditions) == 0 {
			return
		}
		if why, _, _ := BlockFreshness(u, v.Conditions, GatewayRequired(), nil); why != "" {
			return
		}
		conditionBlock(o, u, v.Conditions, GatewayRequired(), nil, "", self, "")
		listenerBlocks(o, u, v.ListenerStatus)
	case kinds.CategoryRoute:
		w := w.of(ic, u)
		if w.ignored {
			return
		}
		w.eachCurrentBlock(func(p objects.ParentRef, cond metav1.Condition, extra string, owner []string) {
			if _, _, refs := RefRuleFor(cond, false); refs || ruleReplacedReason(cond, ic.Conditions.RouteRejected) {
				return
			}
			named.Object(p.Ref())
			f := conditionFinding(severityError, cond, extra, owner)
			delegationCondition(ic, &f, u, cond)
			o.add(f)
		})
	case kinds.CategoryPolicy:
		// A list-only policy (DirectResponse) gets these ancestor findings and nothing else (design §6.6).
		policyAncestors(ic, u, named, o)
	}
}

// conditionFinding is a KGWUI-002 for cond, keyed by owner and the condition type.
func conditionFinding(sev string, cond metav1.Condition, extra string, owner []string) Finding {
	return Finding{code: CodeConditionFalse, slug: "condition-false", severity: sev, message: ConditionMessage(cond),
		detail: ConditionDetail(cond, extra), since: since(cond), key: append(slices.Clip(owner), cond.Type)}
}

// conditionBlock reports each False condition of a current block. A
// listener's ResolvedRefs that KGWUI-013 or 014 owns, and the Accepted and
// Programmed that carry a listener conflict's reason (017), are left out.
func conditionBlock(o *out, u *unstructured.Unstructured, cs []metav1.Condition, required, optional []string, extra string, owner []string, conflict string) {
	if why, _, _ := BlockFreshness(u, cs, required, optional); why != "" {
		return
	}
	listener := len(owner) == 2 && owner[0] == OwnerListener
	for _, ts := range [2][]string{required, optional} {
		for _, t := range ts {
			cond := objects.FindCondition(cs, t)
			if cond == nil || cond.Status != metav1.ConditionFalse {
				continue
			}
			if conflict != "" && cond.Reason == conflict && (t == "Accepted" || t == "Programmed") {
				continue
			}
			if _, _, refs := RefRuleFor(*cond, true); listener && refs {
				continue
			}
			o.add(conditionFinding(severityError, *cond, extra, owner))
		}
	}
}

// listenerBlocks reports the listener blocks of Gateway or ListenerSet u that
// have status; a missing block is KGWUI-003's.
func listenerBlocks(o *out, u *unstructured.Unstructured, status []objects.ListenerStatus) {
	byName := map[string]objects.ListenerStatus{}
	for _, l := range status {
		byName[l.Name] = l
	}
	for _, name := range objects.GatewaySpecListeners(u) {
		l, ok := byName[name]
		if !ok {
			continue
		}
		conflict := ""
		if cf := objects.FindCondition(l.Conditions, "Conflicted"); cf != nil && cf.Status == metav1.ConditionTrue && !objects.IsStale(u, *cf) {
			conflict = cf.Reason
		}
		conditionBlock(o, u, l.Conditions, ListenerRequired(), ListenerOptional(), "listener "+l.Name, []string{OwnerListener, l.Name}, conflict)
	}
}

// policyAncestors reports the policy's ancestors written by this install's
// controller: a Gateway or ListenerSet one only when the install owns it.
func policyAncestors(ic InstallContext, u *unstructured.Unstructured, named Named, o *out) {
	required, optional := PolicyRequired(), PolicyOptional()
	gvk := u.GroupVersionKind()
	gk := objects.GroupKind{Group: gvk.Group, Kind: gvk.Kind}
	for _, a := range objects.PolicyAncestors(u) {
		if !slices.Contains(ic.ControllerNames, a.ControllerName) {
			continue
		}
		ar := a.AncestorRef.Ref()
		if !AncestorOwned(ic, ar) {
			continue
		}
		if why, _, _ := PolicyFreshness(u, a.Conditions); why != "" {
			continue
		}
		// 016 owns TargetNotFound where the rule set reads it from status (D-030); it never
		// judges a list-only policy (DirectResponse), which keeps its 002 (design §6.6).
		fromStatus := ic.TargetFromStatus && slices.Contains(ic.PolicyKinds, gk) && slices.ContainsFunc(append(slices.Clone(required), optional...), func(t string) bool {
			c := objects.FindCondition(a.Conditions, t)
			return c != nil && c.Status == metav1.ConditionFalse && c.Reason == "TargetNotFound" && !objects.IsStale(u, *c)
		})
		extra := "ancestor " + a.AncestorRef.Kind + " " + ar.String()
		owner := append([]string{OwnerAncestor}, RefKey(ar)...)
		owner = append(owner, a.AncestorRef.SectionName, strconv.Itoa(int(a.AncestorRef.Port)))
		for _, t := range append(slices.Clone(required), optional...) {
			// A condition older than the policy says nothing about it now (D-135).
			cond := objects.FindCondition(a.Conditions, t)
			if cond == nil || objects.IsStale(u, *cond) || fromStatus && cond.Reason == "TargetNotFound" {
				continue
			}
			if sev, report := policySeverity(ic, gk, a.AncestorRef, *cond); report {
				// Each ancestor's finding rests on that ancestor only, so another one never withholds it;
				// a ListenerSet's also on its parent Gateway, which its ownership comes from (D-134).
				f := conditionFinding(sev, *cond, extra, owner)
				f.basis, f.strict = []objects.Ref{ar}, true
				if ls, ok := named.Object(ar); ok && inCategory(ic, ar.Group, ar.Kind, kinds.CategoryListenerSet) {
					gw := objects.ListenerSetParent(ls).Ref()
					named.Object(gw)
					f.basis = append(f.basis, gw)
				}
				o.add(f)
			}
		}
	}
}

// AncestorOwned reports whether the install of ic may report on policy
// ancestor ar: false only for a Gateway or ListenerSet ancestor it does not own.
func AncestorOwned(ic InstallContext, ar objects.Ref) bool {
	return !inCategory(ic, ar.Group, ar.Kind, kinds.CategoryGateway, kinds.CategoryListenerSet) || ic.Owns(ar)
}

// policySeverity maps a current condition c on ancestor of a policy of kind gk to a severity.
// Other than Pending, a reason means something only through the pack's reading (design §8).
func policySeverity(ic InstallContext, gk objects.GroupKind, ancestor objects.ParentRef, c metav1.Condition) (string, bool) {
	if c.Reason == "Pending" { // an undecided Attached beside a definitive Accepted (PolicyFreshness)
		return "", false
	}
	if ic.PolicyReading != nil {
		switch warning, info := ic.PolicyReading(gk, ancestor, c); {
		case warning:
			return severityWarning, true
		case info:
			return severityInfo, true
		}
	}
	if c.Status == metav1.ConditionFalse {
		return severityError, true
	}
	return "", false
}

// DelegationOnly reports whether u is an HTTPRoute whose every backendRef
// delegates and which has no ExtensionRef filter, so a BackendNotFound on it
// can only come from delegation (computed-rules §3.2, §8.2).
func DelegationOnly(u *unstructured.Unstructured) bool {
	if gvk := u.GroupVersionKind(); !isHTTPRoute(gvk.Group, gvk.Kind) {
		return false
	}
	extension := func(fs []objects.Filter) bool {
		return slices.ContainsFunc(fs, func(f objects.Filter) bool { return f.ExtensionRef != nil })
	}
	delegates := false
	for _, r := range objects.DecodeHTTPRoute(u).Rules {
		if extension(r.Filters) {
			return false
		}
		for _, b := range r.BackendRefs {
			if b.Delegation == nil || extension(b.Filters) {
				return false
			}
			delegates = true
		}
	}
	return delegates
}

// delegationCondition rewords a route's ResolvedRefs=False/BackendNotFound
// when only delegation can cause it (D-021 item 9); code and severity stay.
func delegationCondition(ic InstallContext, f *Finding, u *unstructured.Unstructured, c metav1.Condition) {
	if !ic.Delegation || c.Type != "ResolvedRefs" || c.Reason != "BackendNotFound" || !DelegationOnly(u) {
		return
	}
	f.message, f.detail = DelegationNoChildMessage, DelegationNoChildDetail+"; "+f.detail
}
