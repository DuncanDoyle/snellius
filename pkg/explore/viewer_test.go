package explore_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/internal/testutil/scale"
	"github.com/DuncanDoyle/snellius/pkg/cache"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/explore"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/scopeverify"
)

type fixedSnap struct{ s *cache.Snapshot }

func (f fixedSnap) Snapshot() *cache.Snapshot { return f.s }

// scaleInstall computes the typical scale world once and returns its install.
func scaleInstall(t *testing.T) *engine.InstallModel {
	t.Helper()
	snap := scale.Snapshot(scale.Typical, 1)
	snap.TakenAt = testutil.Now
	e := engine.New(engine.Config{Snapshots: fixedSnap{snap}, Served: testutil.ServedUpstream(), Detectors: []install.Detector{install.UpstreamDetector{}},
		Scope: install.NamespaceSelectorScope{}, Verifier: scopeverify.NewManagedCountVerifier(time.Hour), Checks: health.Upstream(),
		Now: func() time.Time { return testutil.Now }})
	m := e.Compute()
	if len(m.Installs) != 1 || m.Installs[0].Index == nil {
		t.Fatalf("%d installs", len(m.Installs))
	}
	return &m.Installs[0]
}

// unknownOf is the install-wide unknown of rep: a required check not OK.
func unknownOf(rep health.Report) bool {
	for _, c := range rep.Checks {
		if c.Required && !c.OK {
			return true
		}
	}
	return false
}

// TestStateOfAllShownEqualsPrecomputed: with every finding shown, the viewer
// forms equal the precomputed states, flags and reasons on every row.
func TestStateOfAllShownEqualsPrecomputed(t *testing.T) {
	i := scaleInstall(t)
	ix := i.Index
	vw := explore.Viewer{Shown: func(*health.Finding) bool { return true }, Unknown: unknownOf(i.Health)}
	problems := 0
	for k := range ix.Routes {
		id := explore.RowID(k) // #nosec G115 -- a row count fits in int32
		st, f, r := ix.RouteStateOf(id, vw)
		wst, wf, wr := ix.RouteStateOf(id, explore.Viewer{})
		if st != wst || f != wf || !reflect.DeepEqual(r, wr) || st != ix.Routes[k].State || !reflect.DeepEqual(ix.ProblemBackend(id, vw), ix.Routes[k].Backend) {
			t.Errorf("route %s: %s %v %+v, want %s %v %+v", ix.Routes[k].Ref, st, f, r, wst, wf, wr)
		}
		if st != health.StateHealthy {
			problems++
		}
	}
	for k := range ix.Gateways {
		g := &ix.Gateways[k]
		if st := ix.StateOf(g.Ref, vw); st != g.State {
			t.Errorf("Gateway %s: %s, want %s", g.Ref, st, g.State)
		}
		if n := ix.Disagreements(g.Ref, g.Disagreements, vw); n != g.Disagreements {
			t.Errorf("Gateway %s: %d disagreements, want %d", g.Ref, n, g.Disagreements)
		}
	}
	for k := range ix.ListenerSets {
		if st := ix.ListenerSetStateOf(explore.RowID(k), vw); st != ix.ListenerSets[k].State { // #nosec G115 -- a row count fits in int32
			t.Errorf("ListenerSet %s: %s, want %s", ix.ListenerSets[k].Ref, st, ix.ListenerSets[k].State)
		}
	}
	if problems == 0 {
		t.Fatal("every route is healthy; the fixture reaches no finding")
	}
	// The viewer query forms page the same rows.
	for _, q := range []explore.Query{{Health: []health.State{health.StateError}, Limit: explore.MaxLimit}, {Sort: explore.Sort{Key: "health", Desc: true}, Limit: explore.MaxLimit}} {
		a, _ := ix.QueryRoutes(q, nil)
		b, _ := ix.QueryRoutesAs(q, vw)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("QueryRoutesAs %+v differs", q)
		}
	}
}

// TestMemberCountAsVisible: the As counts count visible member routes only;
// nil counts every member, as the precomputed counts do.
func TestMemberCountAsVisible(t *testing.T) {
	ix := scaleInstall(t).Index
	hidden := testutil.ScaleNamespace(0)
	// Only routes are hidden: a member's dependencies include its Gateway.
	visible := func(r objects.Ref) bool { return r.Kind != "HTTPRoute" || r.Namespace != hidden }
	checked := 0
	for k := range ix.Gateways {
		id := explore.RowID(k) // #nosec G115 -- a row count fits in int32
		all, ok := ix.GatewayMemberCount(id)
		if !ok {
			continue
		}
		if n, _ := ix.GatewayMemberCountAs(id, nil); n != all {
			t.Errorf("%s: nil count %d, want %d", ix.Gateways[k].Ref, n, all)
		}
		want := 0
		for _, m := range ix.Gateways[k].Members {
			if visible(ix.Routes[m].Ref) {
				want++
			}
		}
		if n, ok := ix.GatewayMemberCountAs(id, visible); !ok || n != want {
			t.Errorf("%s: visible count %d %v, want %d", ix.Gateways[k].Ref, n, ok, want)
		}
		if want < all {
			checked++
		}
		for _, l := range ix.Gateways[k].View.Listeners {
			key := explore.ListenerKey{Resource: ix.Gateways[k].Ref, Listener: l.Name}
			if n := ix.ListenerCountAs(key, visible); n > ix.Listeners[key].Count {
				t.Errorf("%s: visible listener count %d over %d", key, n, ix.Listeners[key].Count)
			}
		}
	}
	for k := range ix.ListenerSets {
		id := explore.RowID(k) // #nosec G115 -- a row count fits in int32
		all, ok := ix.ListenerSetMemberCount(id)
		if n, ok2 := ix.ListenerSetMemberCountAs(id, func(objects.Ref) bool { return true }); ok != ok2 || ok && n != all {
			t.Errorf("%s: all-visible count %d, want %d", ix.ListenerSets[k].Ref, n, all)
		}
	}
	if checked == 0 {
		t.Fatalf("no Gateway has a member in %s; the fixture does not reach the filter", hidden)
	}
}
