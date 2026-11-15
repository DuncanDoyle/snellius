package app

import (
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/DuncanDoyle/snellius/pkg/auth/sar"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
)

// coreKind is a built-in kind the cache watches; off reports when its input is turned off.
type coreKind struct {
	gk  schema.GroupKind
	k   sar.Kind
	off func(Diagnostics) bool
}

func always(Diagnostics) bool { return false }

// coreKinds are the non-CRD kinds the cache watches (pkg/cache). Services stay
// in every mode because route backendRefs name them.
var coreKinds = []coreKind{
	{schema.GroupKind{Kind: "Namespace"}, sar.Kind{Resource: "namespaces"}, always},
	{schema.GroupKind{Kind: "Pod"}, sar.Kind{Resource: "pods", Namespaced: true}, always},
	{schema.GroupKind{Group: "apps", Kind: "Deployment"}, sar.Kind{Group: "apps", Resource: "deployments", Namespaced: true}, always},
	{schema.GroupKind{Kind: "Service"}, sar.Kind{Resource: "services", Namespaced: true}, always},
	{schema.GroupKind{Group: "discovery.k8s.io", Kind: "EndpointSlice"}, sar.Kind{Group: "discovery.k8s.io", Resource: "endpointslices", Namespaced: true},
		func(d Diagnostics) bool { return d.NoEndpoints }},
	{schema.GroupKind{Kind: "Event"}, sar.Kind{Resource: "events", Namespaced: true}, func(d Diagnostics) bool { return d.NoEvents }},
	{schema.GroupKind{Group: "apiextensions.k8s.io", Kind: "CustomResourceDefinition"}, sar.Kind{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"},
		func(d Diagnostics) bool { return d.NoCRDs }},
}

// sarKinds is the production sar.Kinds: the watched served kinds plus coreKinds.
// All drops a watched kind the catalog knows is no longer served, and its key
// is the catalog's, so the full-read key changes whenever that set does.
type sarKinds struct {
	byGK    map[schema.GroupKind]sar.Kind
	watched []schema.GroupKind
	core    []sar.Kind
	catalog func() kinds.Catalog

	mu   sync.Mutex
	key  string
	all  []sar.Kind
	done bool
}

var _ sar.Kinds = (*sarKinds)(nil)

// newSARKinds maps served and the core kinds the diagnostics d leave on; catalog is the monitor's.
func newSARKinds(served []kinds.Served, d Diagnostics, catalog func() kinds.Catalog) *sarKinds {
	s := &sarKinds{byGK: map[schema.GroupKind]sar.Kind{}, catalog: catalog}
	for _, c := range coreKinds {
		s.byGK[c.gk] = c.k
		if !c.off(d) {
			s.core = append(s.core, c.k)
		}
	}
	for _, sv := range served {
		gk := schema.GroupKind{Group: sv.Group, Kind: sv.Kind}
		if _, dup := s.byGK[gk]; dup {
			continue
		}
		s.byGK[gk] = sar.Kind{Group: sv.Group, Resource: sv.Resource, Namespaced: sv.Namespaced}
		s.watched = append(s.watched, gk)
	}
	return s
}

// Lookup implements sar.Kinds.
func (s *sarKinds) Lookup(group, kind string) (sar.Kind, bool) {
	k, ok := s.byGK[schema.GroupKind{Group: group, Kind: kind}]
	return k, ok
}

// All implements sar.Kinds. The list is rebuilt only when the catalog key changes.
func (s *sarKinds) All() ([]sar.Kind, string) {
	cat := s.catalog()
	key := cat.Key()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done && key == s.key {
		return s.all, s.key
	}
	all := make([]sar.Kind, 0, len(s.watched)+len(s.core))
	for _, gk := range s.watched {
		if cat.Known(gk.Group) && !cat.Served(gk.Group, gk.Kind) {
			continue
		}
		all = append(all, s.byGK[gk])
	}
	s.all, s.key, s.done = append(all, s.core...), key, true
	return s.all, s.key
}
