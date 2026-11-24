package health

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/DuncanDoyle/snellius/pkg/cache"
	"github.com/DuncanDoyle/snellius/pkg/health/own"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// namedRecorder records the refs one resource's analyzer named, in order,
// and the objects Object returned. base is shared by one runOwn.
type namedRecorder struct {
	base     *ownBase
	resource *unstructured.Unstructured
	refs     []objects.Ref
	returned []*unstructured.Unstructured
}

// ownBase is what newOwnInput builds once per runOwn: the install context,
// membership, the Namespaces and an index of the snapshot by ref.
type ownBase struct {
	in       *Input
	ic       own.InstallContext
	owns     map[objects.Ref]bool
	nss      map[string]int // index in the snapshot's Namespaces
	exp      map[objects.GroupKind]bool
	byGVR    map[schema.GroupVersionResource]map[objects.Ref]*unstructured.Unstructured
	services map[objects.Ref]*unstructured.Unstructured
	// serviceAt indexes the snapshot's Service facts by ref.
	serviceAt map[objects.Ref]int
}

// Object implements own.Named: it reads the snapshot by ref and records each ref asked.
func (rec *namedRecorder) Object(r objects.Ref) (*unstructured.Unstructured, bool) {
	if !slices.Contains(rec.refs, r) {
		rec.refs = append(rec.refs, r)
	}
	u := rec.base.object(rec.base.in, r)
	if u != nil {
		rec.returned = append(rec.returned, u)
	}
	return u, u != nil
}

// FirstSeen implements own.Named: grace by first sight, for the resource or an object Object returned.
func (rec *namedRecorder) FirstSeen(u *unstructured.Unstructured) time.Time {
	if testing.Testing() && u != rec.resource && !slices.Contains(rec.returned, u) {
		panic(fmt.Sprintf("own: FirstSeen of %s, which the analyzer did not name", objects.RefOf(u)))
	}
	return rec.base.in.firstSeen(u)
}

// object returns the snapshot object r names, or nil. A core Service is
// built from the cache's Service facts: its name and ports.
func (b *ownBase) object(in *Input, r objects.Ref) *unstructured.Unstructured {
	if in.Snapshot == nil {
		return nil
	}
	if r.Group == "" && r.Kind == "Service" {
		if b.services == nil {
			b.services = map[objects.Ref]*unstructured.Unstructured{}
			b.serviceAt = make(map[objects.Ref]int, len(in.Snapshot.Services))
			for i, s := range in.Snapshot.Services {
				b.serviceAt[objects.Ref{Kind: "Service", Namespace: s.Namespace, Name: s.Name}] = i
			}
		}
		// Built on first ask and kept, so one ref returns one object for the run.
		u, ok := b.services[r]
		if i, found := b.serviceAt[r]; !ok && found {
			u = serviceObject(in.Snapshot.Services[i])
			b.services[r] = u
		}
		return u
	}
	i := slices.IndexFunc(in.Served, func(s kinds.Served) bool { return s.Group == r.Group && s.Kind == r.Kind })
	if i < 0 {
		return nil
	}
	gvr := in.Served[i].GVR()
	idx, ok := b.byGVR[gvr]
	if !ok {
		idx = make(map[objects.Ref]*unstructured.Unstructured, len(in.Snapshot.Objects[gvr]))
		for _, u := range in.Snapshot.Objects[gvr] {
			idx[objects.Ref{Group: r.Group, Kind: r.Kind, Namespace: u.GetNamespace(), Name: u.GetName()}] = u
		}
		if b.byGVR == nil {
			b.byGVR = map[schema.GroupVersionResource]map[objects.Ref]*unstructured.Unstructured{}
		}
		b.byGVR[gvr] = idx
	}
	return idx[r]
}

// serviceObject is Service fact s as an object with its name and spec.ports.
func serviceObject(s cache.ServiceFact) *unstructured.Unstructured {
	ports := make([]any, 0, len(s.Ports))
	for _, p := range s.Ports {
		ports = append(ports, map[string]any{"name": p.Name, "port": int64(p.Port), "protocol": p.Protocol})
	}
	u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"type": s.Type, "ports": ports}}}
	u.SetAPIVersion("v1")
	u.SetKind("Service")
	u.SetNamespace(s.Namespace)
	u.SetName(s.Name)
	return u
}

// newOwnInput returns the install context and the named-object reader for
// one resource; the context is built once per rec.base. It is the only place
// in pkg/health that builds either.
func newOwnInput(in Input, rec *namedRecorder) (own.InstallContext, own.Named) {
	b := rec.base
	if b.in != nil {
		return b.ic, rec
	}
	cp := in
	b.in = &cp
	b.owns = map[objects.Ref]bool{}
	if o := in.Owned; o != nil {
		for _, u := range slices.Concat(o.Gateways, o.ListenerSets, objectsOf(o.Routes), objectsOf(o.Policies)) {
			b.owns[objects.RefOf(u)] = true
		}
	}
	var policies []objects.GroupKind
	for _, s := range kinds.ByCategory(in.Served, kinds.CategoryPolicy) {
		if !s.ListOnly {
			policies = append(policies, objects.GroupKind{Group: s.Group, Kind: s.Kind})
		}
	}
	ts := in.Rules.TargetStatusReporter()
	b.ic = own.InstallContext{
		Install:          in.Install.ID(),
		ControllerNames:  in.Install.ControllerNames,
		Owns:             func(r objects.Ref) bool { return b.owns[r] },
		InScope:          func(ns string) bool { return b.inScope(*b.in, ns) },
		Version:          in.Install.Version,
		Conditions:       in.conditions(),
		Delegation:       !in.Install.NoDelegation,
		Catalog:          in.Catalog,
		PolicyKinds:      policies,
		Views:            in.views(),
		Now:              in.Now,
		Served:           in.Served,
		Experimental:     b.experimental,
		Collected:        func(gk objects.GroupKind, ns string) bool { return b.in.collected(gk, ns) },
		TargetFromStatus: ts != nil && ts.ReportsTargetNotFound(),
	}
	return b.ic, rec
}

// experimental is the install's Experimental for k, decided once per kind.
func (b *ownBase) experimental(k kinds.Served) bool {
	key := objects.GroupKind{Group: k.Group, Kind: k.Kind}
	e, ok := b.exp[key]
	if !ok {
		if b.exp == nil {
			b.exp = map[objects.GroupKind]bool{}
		}
		e = b.in.Install.Experimental(k)
		b.exp[key] = e
	}
	return e
}

// inScope reports whether the install's scope contains namespace ns, its labels included.
func (b *ownBase) inScope(in Input, ns string) bool {
	if in.Snapshot == nil {
		return in.Install.Scope.Contains(ns, nil)
	}
	if b.nss == nil {
		b.nss = make(map[string]int, len(in.Snapshot.Namespaces))
		for i, n := range in.Snapshot.Namespaces {
			b.nss[n.Name] = i
		}
	}
	if i, ok := b.nss[ns]; ok {
		return in.Install.Scope.Contains(ns, in.Snapshot.Namespaces[i])
	}
	return in.Install.Scope.Contains(ns, nil)
}

// collected reports whether the cache read kind gk and, for a namespace other
// than "" in a scoped snapshot, read gk there. A kind the UI does not watch counts as read.
func (in Input) collected(gk objects.GroupKind, ns string) bool {
	return Collected(in.Snapshot, in.Served, gk, ns)
}

// Collected reports whether snap read kind gk, one of served: everywhere for
// namespace "", else in ns. A scoped snapshot answers ns from that namespace's
// access alone, so a failure elsewhere does not count. A kind served does not list counts as read.
func Collected(snap *cache.Snapshot, served []kinds.Served, gk objects.GroupKind, ns string) bool {
	if snap == nil {
		return false
	}
	gvr := cache.ServicesGVR
	if gk != cache.ServicesKind {
		i := slices.IndexFunc(served, func(s kinds.Served) bool { return s.Group == gk.Group && s.Kind == gk.Kind })
		if i < 0 {
			return true
		}
		gvr = served[i].GVR()
	}
	if ns == "" || len(snap.RequestedNamespaces) == 0 {
		return snap.Access[gvr].OK
	}
	return snap.NamespaceAccess[gvr][ns].OK
}

// runOwn evaluates the analyzers ks on each resource through package own, in
// one walk per resource, and passes each result's wake time to ctx; out[i] are
// ks[i]'s findings. It is the only caller of own.EvaluateRoute.
// One check run shares its base across calls, so membership is indexed once.
func runOwn(ctx RunContext, ks []own.Kind, resources []*unstructured.Unstructured) [][]Finding {
	base := &ownBase{}
	if ctx.sink != nil {
		if ctx.sink.own == nil {
			ctx.sink.own = base
		}
		base = ctx.sink.own
	}
	// Each analyzer names through its own recorder, so a basis is per resource and analyzer (D-108),
	// unless a finding narrows it to the objects it alone rests on (policy ancestors).
	recs := make([]*namedRecorder, len(ks))
	named := make([]own.Named, len(ks))
	var ic own.InstallContext
	for i := range ks {
		recs[i] = &namedRecorder{base: base}
		ic, named[i] = newOwnInput(ctx.Input, recs[i])
	}
	out := make([][]Finding, len(ks))
	id := ctx.Install.ID()
	for _, u := range resources {
		for _, rec := range recs {
			rec.resource, rec.refs, rec.returned = u, rec.refs[:0], rec.returned[:0]
		}
		var res *objects.Ref // the resource's findings share it; nothing writes through it
		for i, r := range own.EvaluateRoute(ks, ic, u, named) {
			if at := r.WakeAt(); !at.IsZero() {
				ctx.WakeAt(at)
			}
			var basis []objects.Ref
			if len(r.Findings()) > 0 {
				basis = slices.Clone(recs[i].refs) // findings keep it; the recorder is reused
			}
			for _, of := range r.Findings() {
				b := basis
				if fb := of.Basis(); fb != nil {
					// A finding that names its own basis rests on that alone; it must be named.
					if testing.Testing() && slices.ContainsFunc(fb, func(r objects.Ref) bool { return !slices.Contains(basis, r) }) {
						panic(fmt.Sprintf("own: %s basis %v was not named", of.Code(), fb))
					}
					b = fb
				}
				f, ok := fromOwn(of, b)
				if !ok {
					continue
				}
				if res == nil {
					res = ref(u)
				}
				f.Install, f.Resource = id, res
				if ctx.sink != nil {
					if ctx.sink.owned == nil {
						ctx.sink.owned = &ownedSet{}
					}
					ctx.sink.owned.add(&f)
				}
				out[i] = append(out[i], f)
			}
		}
	}
	return out
}

// fromOwn converts an own-object finding with basis, the refs its analyzer
// named. It refuses one own did not build. It is the only code that sets ClassOwn.
func fromOwn(f own.Finding, basis []objects.Ref) (Finding, bool) {
	if !f.Sealed() {
		return Finding{}, false
	}
	if testing.Testing() {
		for _, e := range f.Evidence() {
			if !slices.Contains(basis, e) {
				panic(fmt.Sprintf("own: %s evidence %s was not named", f.Code(), e))
			}
		}
	}
	out := Finding{Code: f.Code(), Slug: f.Slug(), Severity: Severity(f.Severity()), Message: f.Message(), Detail: f.Detail(),
		Since: f.Since(), Key: f.Key(), Evidence: f.Evidence(), parent: f.Parent(), class: ClassOwn, Basis: &Basis{Refs: basis, Strict: f.StrictBasis()}}
	if ev, ok := f.Facts().(Evidence); ok {
		out.Facts = ev
	}
	return out, true
}

// objectsOf returns the objects of os.
func objectsOf(os []install.OwnedObject) []*unstructured.Unstructured {
	out := make([]*unstructured.Unstructured, len(os))
	for i := range os {
		out[i] = os[i].Obj
	}
	return out
}

// listedPolicies returns the install's list-only policies (DirectResponse) whose
// watch is collected in their namespace. Only their ancestor 002s are judged (design §6.6).
func (in Input) listedPolicies() []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	for _, o := range in.Owned.Listed {
		if o.Kind.Category == kinds.CategoryPolicy && in.collected(objects.GroupKind{Group: o.Kind.Group, Kind: o.Kind.Kind}, o.Obj.GetNamespace()) {
			out = append(out, o.Obj)
		}
	}
	return out
}

// ownRoutes returns the owned routes, then the unattached routes not owned,
// each once: the resources of own.Parents.
func ownRoutes(o *install.Owned) []*unstructured.Unstructured {
	list := objectsOf(o.Routes)
	n := len(list)
	for _, u := range o.Unattached {
		if !slices.Contains(list[n:], u.Route.Obj) {
			if _, owned := o.Route(objects.RefOf(u.Route.Obj)); !owned {
				list = append(list, u.Route.Obj)
			}
		}
	}
	return list
}
