package own

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// Messages of KGWUI-003 on a route.
const (
	notProcessedMessage   = "Not yet processed by the controller"
	beingProcessedMessage = "Being processed by the controller"
	unattachedMessage     = "Not attached: parent not found"
)

// parentsRun is one evaluation of parents on route u.
type parentsRun struct {
	ic       InstallContext
	u        *unstructured.Unstructured
	v        routeView
	named    Named
	o        *out
	reported map[objects.ParentRef]int // index in o.fs; nil until the first 003
}

// parents reports KGWUI-003 on a route: one per parent of this install whose
// status block is missing or not current (D-006), and one per parentRef that
// names a Gateway, ListenerSet or HTTPRoute that does not exist. Inside the
// grace period it is info; after it, a warning.
func parents(ic InstallContext, u *unstructured.Unstructured, named Named, o *out, w *walk) {
	w = w.of(ic, u)
	if w.ignored {
		return
	}
	r := parentsRun{ic: ic, u: u, v: w.v, named: named, o: o}
	hint := experimentalHint(ic, u)
	for i := range w.blocks {
		b := &w.blocks[i]
		switch {
		case b.st == nil:
			named.Object(b.p.Ref())
			r.notProcessed(b.p, notProcessedMessage, NoControllerStatus+" for "+b.desc()+hint, r.evidence(FreshnessNeverWritten, 0))
		case b.why != "":
			named.Object(b.p.Ref())
			r.notProcessed(b.p, notProcessedMessage, b.why+within(b.desc())+hint, r.evidence(b.kind, b.observed))
		}
	}
	// A parent of a kind the cache cannot read may only be missing from the
	// cache; cluster access already reports it. An owned parent exists.
	if !ic.InScope(u.GetNamespace()) {
		return
	}
	for _, p := range r.v.spec {
		if !routeParentKind(ic, p) || ic.Owns(p.Ref()) || !ic.Collected(objects.GroupKind{Group: p.Group, Kind: p.Kind}, "") {
			continue
		}
		if _, ok := named.Object(p.Ref()); ok {
			continue
		}
		r.notProcessed(p, unattachedMessage, "Parent "+ParentDesc(p)+" does not exist", r.evidence(FreshnessNeverWritten, 0))
	}
}

// notProcessed reports parent p once. A later block whose kind comes first in
// §10.1 order replaces the evidence.
func (r *parentsRun) notProcessed(p objects.ParentRef, msg, why string, ev *FreshnessEvidence) {
	if i, ok := r.reported[p]; ok {
		if prev := r.o.fs[i].facts.(*FreshnessEvidence); FreshnessRank(ev.Freshness) < FreshnessRank(prev.Freshness) {
			r.o.fs[i].detail, r.o.fs[i].facts = why, ev
		}
		return
	}
	if r.reported == nil {
		r.reported = map[objects.ParentRef]int{}
	}
	r.reported[p] = len(r.o.fs)
	sev := severityWarning
	if end := r.named.FirstSeen(r.u).Add(Grace); r.ic.Now.Before(end) {
		sev, msg = severityInfo, beingProcessedMessage
		r.o.wakeAt(end)
	}
	r.o.add(Finding{code: CodeRouteUnattached, slug: "route-unattached", severity: sev, message: msg, detail: why,
		key: parentKey(r.v, p), facts: ev, parent: &p})
}

// evidence is KGWUI-003's evidence; a stale one quotes the observed and
// current generations and when the UI first saw the current one.
func (r *parentsRun) evidence(kind string, observed int64) *FreshnessEvidence {
	ev := &FreshnessEvidence{Freshness: kind}
	if kind == FreshnessStale {
		current, seen := r.u.GetGeneration(), r.named.FirstSeen(r.u)
		ev.Observed, ev.Current, ev.Since = &observed, &current, &seen
	}
	return ev
}

// routeParentKind reports whether p names a served Gateway, ListenerSet or
// HTTPRoute kind: the kinds a route attaches to.
func routeParentKind(ic InstallContext, p objects.ParentRef) bool {
	k, ok := served(ic, p.Group, p.Kind)
	return ok && (k.Category == kinds.CategoryGateway || k.Category == kinds.CategoryListenerSet ||
		k.Category == kinds.CategoryRoute && isHTTPRoute(p.Group, p.Kind))
}
