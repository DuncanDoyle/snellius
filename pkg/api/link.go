package api

import (
	"sync"

	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/explore"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/paths"
)

// linker labels references for one request against one model (design
// §7.1). Build one per request with h.linker, so the detail kinds and the
// listed backends are computed once, not per reference.
type linker struct {
	v      *view
	m      *engine.Model
	i      *engine.InstallModel
	detail map[objects.GroupKind]bool
	// policy are the policy kinds, whose links are their detail endpoint paths (D-136).
	policy map[objects.GroupKind]bool
	// backend reports whether backendDetail serves this caller a ref, named as
	// its subject or else as a list row; nil links no backend.
	backend func(ref objects.Ref, named bool) bool
}

// linkerFor returns the linker for the caller of v and install i of m, without backend links.
func linkerFor(v *view, m *engine.Model, i *engine.InstallModel) linker {
	detail, pol := map[objects.GroupKind]bool{}, map[objects.GroupKind]bool{}
	for _, gk := range explore.DetailKinds(m.Served) {
		detail[gk] = true
	}
	for _, k := range m.Served {
		if k.Category == kinds.CategoryPolicy {
			pol[objects.GroupKind{Group: k.Group, Kind: k.Kind}] = true
		}
	}
	return linker{v: v, m: m, i: i, detail: detail, policy: pol}
}

// linker returns the linker for the caller of v and install i of p. Its
// backend links come from t and proj when given, else from h.pathModel.
func (h *handler) linker(v *view, p *engine.Published, i *engine.InstallModel, t *paths.Topology, proj *paths.Projection) linker {
	l := linkerFor(v, p.Model, i)
	listed := sync.OnceValue(func() func(objects.Ref, bool) bool {
		if t == nil {
			t, _, _ = h.pathModel(p, i, v)
		}
		return h.listedBackends(p, i, v, t)
	})
	l.backend = func(ref objects.Ref, named bool) bool { return listed()(ref, named) }
	return l
}

// link is admit, except that a ref backendDetail serves the caller is
// backend unless it is detail. Detail kinds are never backends in practice;
// should a route write one, its detail page wins. ref is a population member (D-100).
func (l linker) link(ref objects.Ref) Link {
	return l.linkWith(ref, false)
}

// linkNamed is link for a named ref: a detail page's subject, or a ref
// written in an object the caller may read (D-100).
func (l linker) linkNamed(ref objects.Ref) Link {
	return l.linkWith(ref, true)
}

func (l linker) linkWith(ref objects.Ref, named bool) Link {
	k := l.admitWith(ref, named)
	if k != LinkDetail && !k.isPolicy() && l.backend != nil && l.check(named)(ref) && l.backend(ref, named) {
		return LinkBackend
	}
	return k
}

// check is the view's predicate for a named or a population ref.
func (l linker) check(named bool) func(objects.Ref) bool {
	if named {
		return l.v.canNamed
	}
	return l.v.can
}

// admit is admit(install, caller, ref) of design §7.1, the one place that
// joins admission, existence and the Authorizer. It is none when ref is
// denied, absent, never served or not admitted; detail when it is admitted
// and its kind has a detail view; else yaml. A referenced or selected
// candidate needs a referrer the caller may view. Admission never follows chains.
// ref is a population member (D-100).
func (l linker) admit(ref objects.Ref) Link {
	return l.admitWith(ref, false)
}

// admitNamed is admit for a named ref; its referrers stay population checks.
func (l linker) admitNamed(ref objects.Ref) Link {
	return l.admitWith(ref, true)
}

func (l linker) admitWith(ref objects.Ref, named bool) Link {
	if l.i.Index == nil || !l.check(named)(ref) {
		return LinkNone
	}
	a, ok := l.i.Index.Admission(ref)
	if !ok {
		return LinkNone
	}
	// A Backend joins through its consumer routes, which are not install context (D-107).
	owned := a.Owned && (!a.Consumed || !l.v.omits() || l.v.readsAll() || l.v.anyOf(a.Referrers))
	finding := a.Reason == explore.ReasonFinding && l.findingShown(ref, named)
	byReferrer := a.Reason == explore.ReasonReferenced || a.Reason == explore.ReasonSelected || a.Reason == explore.ReasonFinding && !finding
	candidate := owned || finding || (byReferrer && l.v.anyOf(a.Referrers))
	if !candidate && !a.NoParent {
		return LinkNone
	}
	if u, d := stored(l.m, ref); u == nil && d == nil {
		return LinkNone
	}
	gk := objects.GroupKind{Group: ref.Group, Kind: ref.Kind}
	if candidate && l.policy[gk] {
		return policyLink(l.i.Resolved.ID(), ref)
	}
	if candidate && l.detail[gk] {
		return LinkDetail
	}
	return LinkYAML
}

// findingShown reports whether a finding on ref is shown to the caller: a
// finding admits an object only then (design §7.2), so a hidden basis that
// raises or clears it never opens or closes a readable object. A named ref
// is read by name, its basis as population members.
func (l linker) findingShown(ref objects.Ref, named bool) bool {
	if !l.v.projects() {
		// A full reader without Events or CRDs is not shown the findings from them.
		return !l.v.withholds() || l.i.Index.FindingShown(ref, l.v.shown)
	}
	var rd health.Readable = l.v
	if named {
		rd = namedSubject{v: l.v, subject: ref}
	}
	return l.i.Index.FindingShown(ref, func(f *health.Finding) bool { return f.ShownTo(rd) })
}

// linked is ref with its link, as a population member.
func (l linker) linked(ref objects.Ref) LinkedRef {
	return LinkedRef{Ref: ref, Link: l.link(ref)}
}

// linkedNamed is ref with its link, as a named ref.
func (l linker) linkedNamed(ref objects.Ref) LinkedRef {
	return LinkedRef{Ref: ref, Link: l.linkNamed(ref)}
}
