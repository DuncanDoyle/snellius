package policy_test

import (
	"context"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/cache"
	"github.com/DuncanDoyle/snellius/pkg/computed"
	"github.com/DuncanDoyle/snellius/pkg/computed/rulesets"
	"github.com/DuncanDoyle/snellius/pkg/kgateway"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/policy"
)

// snapshotLookup answers from a one-shot snapshot, as a CLI does: what the
// user's own credentials listed is what they may read.
type snapshotLookup struct {
	snap   *cache.Snapshot
	served []kinds.Served
}

func (l snapshotLookup) gvr(gk objects.GroupKind) (schema.GroupVersionResource, bool) {
	for _, s := range l.served {
		if s.Group == gk.Group && s.Kind == gk.Kind {
			return s.GVR(), true
		}
	}
	return schema.GroupVersionResource{}, false
}

func (l snapshotLookup) Object(r objects.Ref) (*unstructured.Unstructured, bool) {
	gvr, ok := l.gvr(objects.GroupKind{Group: r.Group, Kind: r.Kind})
	if !ok {
		return nil, false
	}
	i := slices.IndexFunc(l.snap.Objects[gvr], func(u *unstructured.Unstructured) bool {
		return u.GetNamespace() == r.Namespace && u.GetName() == r.Name
	})
	if i < 0 {
		return nil, false
	}
	return l.snap.Objects[gvr][i], true
}

func (l snapshotLookup) Watched(gk objects.GroupKind, ns string) bool {
	gvr, ok := l.gvr(gk)
	if !ok || !l.snap.Access[gvr].OK {
		return false
	}
	return len(l.snap.RequestedNamespaces) == 0 || l.snap.NamespaceAccess[gvr][ns].OK
}

func (l snapshotLookup) CanGet(r objects.Ref) bool {
	return l.Watched(objects.GroupKind{Group: r.Group, Kind: r.Kind}, r.Namespace)
}

func (l snapshotLookup) CanList(gk objects.GroupKind, ns string) bool { return l.Watched(gk, ns) }

// The model builds from a cache.ListOnce snapshot with no pkg/api import, as
// kgwctl's one-shot flow does (design §11, E7).
func TestModelFromListOnce(t *testing.T) {
	w, us := relationWorld()
	var objs []runtime.Object
	for _, u := range w.objs {
		if u.GetKind() != "Service" { // Services are typed objects in the cache, not served kinds
			objs = append(objs, u)
		}
	}
	for _, u := range us {
		objs = append(objs, u)
	}
	objs = append(objs, load(t, "kgateway-2.5.yaml")[0]) // default/partial, with the summary ancestor
	served := testutil.ServedUpstream()
	dyn, kube := testutil.Clients(t, objs...)
	snap, err := cache.ListOnce(context.Background(), cache.Config{Dynamic: dyn, Kube: kube, Served: served})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Mode != cache.ModeList || !snap.Synced {
		t.Fatalf("snapshot mode %q synced %v", snap.Mode, snap.Synced)
	}

	// Every policy kind in the snapshot, read with the kgateway pack.
	g := policy.Guard{ControllerNames: []string{kgwController}, Owns: func(objects.Ref) bool { return true }, Fresh: fresh}
	var ps []policy.Policy
	for _, s := range served {
		if s.Category != kinds.CategoryPolicy {
			continue
		}
		for _, u := range snap.Objects[s.GVR()] {
			ps = append(ps, policy.Read(u, kgateway.PolicyInterpreter(), g))
		}
	}
	if len(ps) != len(us)+1 {
		t.Fatalf("read %d policies, want %d", len(ps), len(us)+1)
	}
	in := policy.Inputs{Policies: ps, Lookup: snapshotLookup{snap: snap, served: served},
		Targeter: computed.Selection{Rules: rulesets.Select("v2.4.7").Rules}.PolicyTargeter(), ScopeKnown: true}

	var got []string
	for _, r := range policy.Relations(policy.Subject{Ref: objects.Ref{Group: gw, Kind: "HTTPRoute", Namespace: "shop", Name: "r-ls"}}, in) {
		got = append(got, rel(r))
	}
	want := []string{
		"targets-parent infra/p-gw declared -> HTTPRoute shop/r-ls via ListenerSet infra/ls, Gateway infra/gw",
		"targets-parent infra/p-ls declared -> HTTPRoute shop/r-ls via ListenerSet infra/ls"}
	if !slices.Equal(got, want) {
		t.Errorf("relations:\n got %q\nwant %q", got, want)
	}

	outcome := func(ns, name string) string {
		i := slices.IndexFunc(ps, func(p policy.Policy) bool { return p.Ref.Namespace == ns && p.Ref.Name == name })
		return policy.CheckTargets(ps[i], in)[0].Outcome
	}
	if o := outcome("infra", "p-gw-https"); o != computed.TargetFound {
		t.Errorf("p-gw-https: %s, want found", o)
	}
	if o := outcome("shop", "p-gw-shop"); o != computed.TargetNotFound {
		t.Errorf("p-gw-shop: %s, want not found", o)
	}
	i := slices.IndexFunc(ps, func(p policy.Policy) bool { return p.Ref.Name == "partial" })
	if a := ps[i].Ancestors[0]; !a.Summary || !a.Current {
		t.Errorf("partial's summary ancestor: %+v", a)
	}
}
