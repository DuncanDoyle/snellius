package health

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"hash/fnv"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/internal/testutil/scale"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/scopeverify"
)

var updateOwnToday = flag.Bool("update-own-today", false, "rewrite testdata/own_today.json.gz from the current code")

// ownWorld is one fixture world for the own-object tests.
type ownWorld struct {
	in Input
	rt Runtime
}

// scaleInput is the Input of tier t's scale world, with a complete catalog of
// the served kinds and every seventh object inside the grace period.
func scaleInput(t *testing.T, tier scale.Tier, mutate func(u *unstructured.Unstructured, n uint32)) Input {
	t.Helper()
	snap := scale.Snapshot(tier, 1)
	if mutate != nil {
		for _, objs := range snap.Objects {
			for _, u := range objs {
				mutate(u, nameHash(u))
			}
		}
	}
	served := testutil.ServedUpstream()
	inst, _ := install.UpstreamDetector{}.Detect(snap.Deployments[0])
	inst.Pods = snap.Pods
	res := install.Resolved{Install: inst, Scope: install.ParseSelectors("[]"), ExperimentalOnly: install.UpstreamExperimentalOnly}
	a := install.Assign(snap, served, []install.Resolved{res}, nil)
	gks := []schema.GroupKind{{Kind: "Service"}, {Kind: "Secret"}}
	for _, s := range served {
		gks = append(gks, schema.GroupKind{Group: s.Group, Kind: s.Kind})
	}
	young := func(u *unstructured.Unstructured) time.Time {
		if nameHash(u)%7 == 0 {
			return testutil.Now.Add(-10 * time.Second)
		}
		return u.GetCreationTimestamp().Time
	}
	return Input{Install: res, Owned: a.PerInstall[res.ID()], Assignment: a, Snapshot: snap, Served: served, Now: testutil.Now,
		Verify: scopeverify.Result{Available: true}, Catalog: kinds.NewCatalog(gks, nil), FirstSeen: young}
}

// nameHash is FNV-32a of u's namespace and name.
func nameHash(u *unstructured.Unstructured) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(u.GetNamespace() + "/" + u.GetName()))
	return h.Sum32()
}

// statusFaults makes some objects stale, drops some objects' status and
// rejects or overrides some policy ancestors, by name hash n.
func statusFaults(u *unstructured.Unstructured, n uint32) {
	switch n % 11 {
	case 0:
		u.SetGeneration(u.GetGeneration() + 1)
	case 1:
		unstructured.RemoveNestedField(u.Object, "status")
	case 2, 3:
		as, _, _ := unstructured.NestedSlice(u.Object, "status", "ancestors")
		for _, a := range as {
			cs, _, _ := unstructured.NestedSlice(a.(map[string]any), "conditions")
			for _, c := range cs {
				if m := c.(map[string]any); m["type"] == "Accepted" {
					m["status"], m["reason"] = "False", "Invalid"
					if n%11 == 3 {
						m["status"], m["reason"] = "True", "Overridden"
					}
				}
			}
			_ = unstructured.SetNestedSlice(a.(map[string]any), cs, "conditions")
		}
		if len(as) > 0 {
			_ = unstructured.SetNestedSlice(u.Object, as, "status", "ancestors")
		}
	case 4:
		setFalse(u, "Accepted", "Invalid", "status", "conditions")
	case 5:
		ls, _, _ := unstructured.NestedSlice(u.Object, "status", "listeners")
		if len(ls) > 0 {
			setFalse(&unstructured.Unstructured{Object: ls[0].(map[string]any)}, "Programmed", "Invalid", "conditions")
			_ = unstructured.SetNestedSlice(u.Object, ls, "status", "listeners")
		}
	}
}

// setFalse sets the condition typ at path of u False with reason.
func setFalse(u *unstructured.Unstructured, typ, reason string, path ...string) {
	cs, _, _ := unstructured.NestedSlice(u.Object, path...)
	for _, c := range cs {
		if m := c.(map[string]any); m["type"] == typ {
			m["status"], m["reason"] = "False", reason
		}
	}
	if len(cs) > 0 {
		_ = unstructured.SetNestedSlice(u.Object, cs, path...)
	}
}

// ownWorlds are the guard worlds and the scale worlds, by name.
func ownWorlds(t *testing.T) map[string]ownWorld {
	t.Helper()
	out := map[string]ownWorld{}
	for name, w := range GuardWorlds(t) {
		out["guard/"+name] = ownWorld{w.In, w.RT}
	}
	for _, tier := range []scale.Tier{scale.Typical, scale.Typical.Worst(), scale.Typical.Contradicting()} {
		out["scale/"+tier.Name] = ownWorld{scaleInput(t, tier, nil), Runtime{}}
	}
	out["scale/typical-worst-faults"] = ownWorld{scaleInput(t, scale.Typical.Worst(), statusFaults), Runtime{}}
	return out
}

// todayFinding is a finding with the fields JSON leaves out.
type todayFinding struct {
	Finding
	EvidenceRefs []objects.Ref `json:"evidenceRefs,omitempty"`
	ResSince     *time.Time    `json:"resSince,omitempty"`
}

// todayReport is the resource status check's report on one world.
type todayReport struct {
	State    State          `json:"state"`
	Findings []todayFinding `json:"findings"`
	Checks   []CheckResult  `json:"checks"`
	Next     time.Time      `json:"next"`
}

// conditionsToday evaluates the resource status check on w.
func conditionsToday(w ownWorld) todayReport {
	r := Evaluate(w.in, []Check{conditionsCheck{}}, w.rt)
	out := todayReport{State: r.State, Checks: r.Checks, Next: r.Next}
	for _, f := range r.Findings {
		out.Findings = append(out.Findings, todayFinding{f, f.Evidence, f.ResourceSince})
	}
	return out
}

// For a full reader, the resource status check's findings through the
// own-object boundary equal those it produced before the boundary existed.
func TestOwnFindingsMatchToday(t *testing.T) {
	worlds := ownWorlds(t)
	got := map[string]todayReport{}
	for name, w := range worlds {
		got[name] = conditionsToday(w)
	}
	path := filepath.Join("testdata", "own_today.json.gz")
	if *updateOwnToday {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if err := json.NewEncoder(zw).Encode(got); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]json.RawMessage
	if err := json.NewDecoder(zr).Decode(&want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(got) {
		t.Fatalf("%d worlds, want %d", len(got), len(want))
	}
	for name, r := range got {
		g, _ := json.Marshal(r)
		if !bytes.Equal(g, want[name]) {
			var w todayReport
			_ = json.Unmarshal(want[name], &w)
			t.Errorf("world %s: findings differ from today's:\n%s", name, firstDiff(r, w))
		}
	}
}

// firstDiff describes the first finding, or the results, where got and want differ.
func firstDiff(got, want todayReport) string {
	for i := range max(len(got.Findings), len(want.Findings)) {
		var g, w []byte
		if i < len(got.Findings) {
			g, _ = json.Marshal(got.Findings[i])
		}
		if i < len(want.Findings) {
			w, _ = json.Marshal(want.Findings[i])
		}
		if !bytes.Equal(g, w) {
			return "finding " + strconv.Itoa(i) + ":\n got " + string(g) + "\nwant " + string(w)
		}
	}
	got.Findings, want.Findings = nil, nil
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	return " got " + string(g) + "\nwant " + string(w)
}
