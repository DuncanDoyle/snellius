package app

import (
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/DuncanDoyle/snellius/pkg/auth/sar"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
)

// coreKind is a built-in kind the cache watches. fullRead marks a kind the API
// authorises refs of, so the full-read path needs list on it.
type coreKind struct {
	gk       schema.GroupKind
	k        sar.Kind
	fullRead bool
}

// coreKinds are the non-CRD kinds the cache watches (pkg/cache). EndpointSlices,
// Events and CRDs are inputs read after another ref is authorised, never refs themselves.
var coreKinds = []coreKind{
	{schema.GroupKind{Kind: "Namespace"}, sar.Kind{Resource: "namespaces"}, true},
	{schema.GroupKind{Kind: "Pod"}, sar.Kind{Resource: "pods", Namespaced: true}, true},
	{schema.GroupKind{Group: "apps", Kind: "Deployment"}, sar.Kind{Group: "apps", Resource: "deployments", Namespaced: true}, true},
	{schema.GroupKind{Kind: "Service"}, sar.Kind{Resource: "services", Namespaced: true}, true},
	{schema.GroupKind{Group: "discovery.k8s.io", Kind: "EndpointSlice"}, sar.Kind{Group: "discovery.k8s.io", Resource: "endpointslices", Namespaced: true}, false},
	{schema.GroupKind{Kind: "Event"}, sar.Kind{Resource: "events", Namespaced: true}, false},
	{schema.GroupKind{Group: "apiextensions.k8s.io", Kind: "CustomResourceDefinition"}, sar.Kind{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"}, false},
}

// sarKinds is the production sar.Kinds: the watched served kinds plus coreKinds.
// All lists the kinds the per-object path checks, minus watched kinds the
// catalog knows are no longer served; its key is built from that list.
type sarKinds struct {
	byGK    map[schema.GroupKind]sar.Kind
	watched []schema.GroupKind
	core    []sar.Kind
	catalog func() kinds.Catalog

	mu     sync.Mutex
	catKey string
	key    string
	all    []sar.Kind
	done   bool
}

var _ sar.Kinds = (*sarKinds)(nil)

// newSARKinds maps served and the core kinds; catalog is the served-kinds monitor's.
func newSARKinds(served []kinds.Served, catalog func() kinds.Catalog) *sarKinds {
	s := &sarKinds{byGK: map[schema.GroupKind]sar.Kind{}, catalog: catalog}
	for _, c := range coreKinds {
		s.byGK[c.gk] = c.k
		if c.fullRead {
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

// All implements sar.Kinds. The list is rebuilt only when the catalog changes;
// the key changes only when the list does.
func (s *sarKinds) All() ([]sar.Kind, string) {
	cat := s.catalog()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done && cat.Key() == s.catKey {
		return s.all, s.key
	}
	all := make([]sar.Kind, 0, len(s.watched)+len(s.core))
	for _, gk := range s.watched {
		if cat.Known(gk.Group) && !cat.Served(gk.Group, gk.Kind) {
			continue
		}
		all = append(all, s.byGK[gk])
	}
	all = append(all, s.core...)
	parts := make([]string, len(all))
	for i, k := range all {
		parts[i] = k.Resource + "." + k.Group
	}
	s.all, s.key, s.catKey, s.done = all, strings.Join(parts, ","), cat.Key(), true
	return s.all, s.key
}
