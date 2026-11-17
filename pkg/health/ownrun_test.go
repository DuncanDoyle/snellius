package health

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/internal/testutil/scale"
	"github.com/DuncanDoyle/snellius/pkg/health/own"
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

// ownKindOf is the analyzer that reports f, false for a derived code or resource (D-106 item 2).
func ownKindOf(in Input, f Finding) (own.Kind, bool) {
	if f.Resource == nil {
		return 0, false
	}
	i := slices.IndexFunc(in.Served, func(s kinds.Served) bool { return s.Group == f.Resource.Group && s.Kind == f.Resource.Kind })
	if i < 0 {
		return 0, false
	}
	switch c := in.Served[i].Category; {
	case f.Code == CodeConditionFalse && slices.Contains([]kinds.Category{kinds.CategoryGateway, kinds.CategoryListenerSet, kinds.CategoryRoute, kinds.CategoryPolicy}, c):
		return own.Status, true
	case c != kinds.CategoryRoute:
		return 0, false
	case f.Code == CodeRouteUnattached:
		return own.Parents, true
	case f.Code == CodeRefUnresolved || f.Code == CodeRefNotPermitted:
		return own.Refs, true
	case f.Code == CodeRuleReplaced:
		return own.Rules, true
	}
	return 0, false
}

// Every finding the fixtures raise is own-object exactly when its code and
// resource are in the own-object set, and every other one is derived.
func TestEveryCodeClassified(t *testing.T) {
	owned := 0
	for name, w := range ownWorlds(t) {
		for _, f := range Evaluate(w.in, Upstream(), w.rt).Findings {
			_, ownable := ownKindOf(w.in, f)
			switch {
			case f.class == ClassOwn && (!ownable || f.Basis.Collection):
				t.Errorf("%s: %s on %v is own-object but not in the own-object set", name, f.Code, f.Resource)
			case f.class == ClassOwn:
				owned++
			case f.class != ClassDerived || ownable || len(f.Basis.Refs) > 0:
				t.Errorf("%s: %s on %v is class %d, want derived", name, f.Code, f.Resource, f.class)
			case f.Basis.Collection && f.Code != CodeSignalUnavailable:
				t.Errorf("%s: %s marked collection", name, f.Code)
			}
		}
	}
	if owned == 0 {
		t.Fatal("the fixtures raise no own-object finding")
	}
}

// A finding's class is set only in fromOwn (other packages cannot: it is
// unexported), and own.Evaluate, own.InstallContext and
// own.Named are used or implemented only in ownrun.go. fromOwn refuses a
// Finding that own did not build.
func TestOwnFindingsOnlyFromOwn(t *testing.T) {
	if bad := ownUseViolations(t, "."); len(bad) > 0 {
		t.Errorf("own-object marks outside fromOwn and ownrun.go:\n%s", strings.Join(bad, "\n"))
	}
	if bad := ownUseViolations(t, filepath.Join("testdata", "badclass")); len(bad) != 3 {
		t.Errorf("the check must reject own.InstallContext, own.Evaluate and a Named type: %v", bad)
	}
	// Runtime writes inside package health: a conversion, an increment, a copy, an
	// op-assignment, a composite literal and a pointer, one each.
	bad := ownUseViolations(t, ".", filepath.Join("testdata", "ownwrite", "writes.go.txt"))
	if got := slices.DeleteFunc(bad, func(b string) bool { return !strings.HasPrefix(b, "writes.go.txt:") }); len(got) != 6 {
		t.Errorf("the check must reject each runtime write of a finding's class: %v", got)
	}
	if _, ok := fromOwn(own.Finding{}, nil); ok {
		t.Fatal("fromOwn must refuse a Finding own did not build")
	}
}

// ownUseViolations type-checks the non-test files of the package in dir and
// returns, with extra files parsed into that package, each write of a
// finding's class outside fromOwn other than a reset to ClassDerived, and each use of own.Evaluate,
// own.InstallContext or a type implementing own.Named outside ownrun.go.
// Types resolve, so aliased imports, variables and conversions are caught.
func ownUseViolations(t *testing.T, dir string, extra ...string) []string {
	t.Helper()
	const healthPath, ownPath = "github.com/DuncanDoyle/snellius/pkg/health", "github.com/DuncanDoyle/snellius/pkg/health/own"
	cmd := exec.Command("go", "list", "-export", "-deps", "-f", "{{.ImportPath}}={{.Export}}", ".")
	cmd.Dir = dir
	listed, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list in %s: %v", dir, err)
	}
	exports := map[string]string{}
	var self string
	for _, l := range strings.Split(strings.TrimSpace(string(listed)), "\n") {
		path, file, _ := strings.Cut(l, "=")
		exports[path], self = file, path // the package itself is listed last
	}
	fset := token.NewFileSet()
	names, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	names = append(names, extra...)
	var files []*ast.File
	for _, n := range names {
		if strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, n, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	imp := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) { return os.Open(exports[path]) })
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}, Defs: map[*ast.Ident]types.Object{}}
	pkg, err := (&types.Config{Importer: imp}).Check(self, fset, files, info)
	if err != nil {
		t.Fatal(err)
	}
	var healthPkg, ownPkg *types.Package
	for _, p := range append(pkg.Imports(), pkg) {
		switch p.Path() {
		case healthPath:
			healthPkg = p
		case ownPath:
			ownPkg = p
		}
	}
	var bad []string
	at := func(n ast.Node) string {
		p := fset.Position(n.Pos())
		return filepath.Base(p.Filename) + ":" + strconv.Itoa(p.Line)
	}
	if healthPkg != nil {
		finding := healthPkg.Scope().Lookup("Finding").Type().Underlying().(*types.Struct)
		var field *types.Var
		for i := range finding.NumFields() {
			if finding.Field(i).Name() == "class" {
				field = finding.Field(i)
			}
		}
		derived := healthPkg.Scope().Lookup("ClassDerived").(*types.Const)
		// clears reports whether e is the constant ClassDerived: writing it never marks a finding own-object.
		clears := func(e ast.Expr) bool {
			tv, ok := info.Types[e]
			return ok && tv.Value != nil && constant.Compare(tv.Value, token.EQL, derived.Val())
		}
		for _, f := range files {
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "fromOwn" && pkg == healthPkg {
					continue
				}
				var stack []ast.Node
				ast.Inspect(d, func(n ast.Node) bool {
					if n == nil {
						stack = stack[:len(stack)-1]
						return true
					}
					stack = append(stack, n)
					id, ok := n.(*ast.Ident)
					if !ok || field == nil || info.Uses[id] != field {
						return true
					}
					if writes(stack, clears) {
						bad = append(bad, at(n)+": a finding's class written outside fromOwn")
					}
					return true
				})
			}
		}
	}
	inRun := func(n ast.Node) bool {
		return pkg == healthPkg && filepath.Base(fset.Position(n.Pos()).Filename) == "ownrun.go"
	}
	if ownPkg != nil {
		for id, obj := range info.Uses {
			if obj.Pkg() == ownPkg && slices.Contains([]string{"Evaluate", "InstallContext"}, obj.Name()) && !inRun(id) {
				bad = append(bad, at(id)+": own."+obj.Name()+" outside ownrun.go")
			}
		}
		named := ownPkg.Scope().Lookup("Named").Type().Underlying().(*types.Interface)
		for id, obj := range info.Defs {
			if tn, ok := obj.(*types.TypeName); ok && !inRun(id) &&
				(types.Implements(tn.Type(), named) || types.Implements(types.NewPointer(tn.Type()), named)) {
				bad = append(bad, at(id)+": "+tn.Name()+" implements own.Named outside ownrun.go")
			}
		}
	}
	slices.Sort(bad)
	return bad
}

// writes reports whether the class field named by the last node of stack is
// written there: assigned, incremented, set in a literal or addressed. An
// assignment or literal value that clears accepts reports a reset, not a write.
func writes(stack []ast.Node, clears func(ast.Expr) bool) bool {
	id := stack[len(stack)-1]
	parent := stack[len(stack)-2]
	if kv, ok := parent.(*ast.KeyValueExpr); ok && kv.Key == id {
		return !clears(kv.Value)
	}
	sel, ok := parent.(*ast.SelectorExpr)
	if !ok || sel.Sel != id || len(stack) < 3 {
		return false
	}
	switch g := stack[len(stack)-3].(type) {
	case *ast.AssignStmt:
		i := slices.Index(g.Lhs, ast.Expr(sel))
		if i < 0 {
			return false
		}
		return g.Tok != token.ASSIGN || len(g.Rhs) != len(g.Lhs) || !clears(g.Rhs[i])
	case *ast.IncDecStmt:
		return true
	case *ast.UnaryExpr:
		return g.Op == token.AND
	}
	return false
}

// A 015 inside its hint window wakes the report at the hint's deadline, as before the boundary.
func TestOwnRulesWakeAt(t *testing.T) {
	since := testutil.Now.Add(-2 * time.Minute)
	r := ruleReport(t, ruleRoute(ruleCond(kgatewayProgrammed, "RouteRuleReplaced", "Replaced Rule (0): bad", since)), policyCatalog())
	f, _ := onlyRule(t, r)
	if f.class != ClassOwn || strings.Contains(f.Detail, RuleReplacedHint) {
		t.Fatalf("015 %+v, want own-object without the hint yet", f)
	}
	if want := since.Add(RuleReplacedHintAge + time.Nanosecond); !r.Next.Equal(want) {
		t.Fatalf("Report.Next = %v, want the hint deadline %v", r.Next, want)
	}
}

// newOwnInput's callbacks are local: Views decodes the object it is given,
// FirstSeen answers for the resource and objects Object returned only, and
// Owns and InScope are the install's Assign membership and scope.
func TestOwnInputCallbacksLocal(t *testing.T) {
	w := healthyWorld()
	w.scope = `[{"matchLabels":{"team":"a"}}]`
	w.nss = []*corev1.Namespace{testutil.Namespace("infra", map[string]string{"team": "a"}), testutil.Namespace("shop", map[string]string{"team": "a"})}
	in := w.input(t)
	route, gw := in.Owned.Routes[0].Obj, in.Owned.Gateways[0]
	rec := &namedRecorder{base: &ownBase{}, resource: route}
	ic, named := newOwnInput(in, rec)
	before, _ := json.Marshal(ic.Views.HTTPRoute(route))
	gw.Object["status"] = map[string]any{}
	if after, _ := json.Marshal(ic.Views.HTTPRoute(route)); !bytes.Equal(before, after) {
		t.Fatal("Views must decode the route alone")
	}
	if !named.FirstSeen(route).Equal(in.firstSeen(route)) {
		t.Fatal("FirstSeen of the resource must be the engine's first sight of it")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("FirstSeen of an object the analyzer did not name must panic under test")
			}
		}()
		named.FirstSeen(gw)
	}()
	if _, ok := named.Object(objects.RefOf(gw)); !ok || !named.FirstSeen(gw).Equal(in.firstSeen(gw)) {
		t.Fatal("FirstSeen of an object Object returned must be the engine's")
	}
	for _, objs := range in.Snapshot.Objects {
		for _, u := range objs {
			r := objects.RefOf(u)
			member := slices.Contains(slices.Concat(in.Owned.Gateways, in.Owned.ListenerSets, objectsOf(in.Owned.Routes), objectsOf(in.Owned.Policies)), u)
			if ic.Owns(r) != member {
				t.Errorf("Owns(%s) = %v, want Assign's %v", r, ic.Owns(r), member)
			}
		}
	}
	if !ic.InScope("shop") || ic.InScope("elsewhere") {
		t.Fatal("InScope must follow the selector scope")
	}
	// A Namespace label and a GatewayClass's controllerName move membership; nothing else does.
	w.nss[1] = testutil.Namespace("shop", nil)
	w.objs[0] = testutil.GatewayClass("kgateway", "example.com/other", true)
	moved, _ := newOwnInput(w.input(t), &namedRecorder{base: &ownBase{}, resource: route})
	if moved.InScope("shop") || moved.Owns(objects.RefOf(gw)) {
		t.Fatal("InScope and Owns must follow Namespace labels and the GatewayClass")
	}
}

// ownFindings evaluates analyzer k on u as runOwn does, and returns its
// findings and the refs it named.
func ownFindings(in Input, k own.Kind, u *unstructured.Unstructured) ([]todayFinding, []objects.Ref) {
	rec := &namedRecorder{base: &ownBase{}, resource: u}
	ic, named := newOwnInput(in, rec)
	var out []todayFinding
	for _, of := range own.Evaluate(k, ic, u, named).Findings() {
		f, _ := fromOwn(of, rec.refs)
		out = append(out, todayFinding{f, f.Evidence, nil})
	}
	return out, rec.refs
}

// objectByRef returns the snapshot object r names.
func objectByRef(in Input, r objects.Ref) *unstructured.Unstructured {
	for _, objs := range in.Snapshot.Objects {
		for _, u := range objs {
			if objects.RefOf(u) == r {
				return u
			}
		}
	}
	return nil
}

// Each own-object finding's basis is exactly what its analyzer named, and its evidence is within it.
func TestOwnBasisIsWhatWasNamed(t *testing.T) {
	in := ownWorlds(t)["scale/typical-worst-faults"].in
	named := 0
	for _, f := range Evaluate(in, []Check{conditionsCheck{}}, Runtime{}).Findings {
		k, ok := ownKindOf(in, f)
		if !ok {
			continue
		}
		_, refs := ownFindings(in, k, objectByRef(in, *f.Resource))
		if !slices.Equal(f.Basis.Refs, refs) {
			t.Errorf("%s on %s: basis %v, named %v", f.Code, f.Resource, f.Basis.Refs, refs)
		}
		for _, e := range f.Evidence {
			if !slices.Contains(refs, e) {
				t.Errorf("%s on %s: evidence %s was not named", f.Code, f.Resource, e)
			}
		}
		if len(refs) > 0 {
			named++
		}
	}
	if named == 0 {
		t.Fatal("no own-object finding named an object")
	}
}

// Removing every object other than the resource and what it named, and
// adding unrelated ones, leaves its own-object findings byte-equal, for every
// resource each analyzer runs on, with findings or without; membership is
// install context and stays. This supplements the import boundary.
func TestOwnBoundaryIgnoresOthers(t *testing.T) {
	// A route on the install's Gateway that also names an existing Gateway of
	// another controller: it has no finding while that Gateway exists.
	w := healthyWorld()
	edge, other := testutil.GwRef("infra", "edge"), testutil.GwRef("infra", "other")
	w.objs = append(w.objs, testutil.GatewayClass("foreign", "example.com/other", true), testutil.Gateway("infra", "other", "foreign", nil),
		testutil.HTTPRoute("shop", "both", []objects.ParentRef{edge, other}, testutil.AcceptedBy(edge, testutil.Controller)))
	checked, withFindings := 0, 0
	for _, in := range []Input{ownWorlds(t)["scale/typical-worst-faults"].in, w.input(t)} {
		o := in.Owned
		routes := objectsOf(o.Routes)
		runs := []struct {
			k         own.Kind
			resources []*unstructured.Unstructured
		}{
			{own.Parents, ownRoutes(o)},
			{own.Status, slices.Concat(o.Gateways, o.ListenerSets, routes, objectsOf(o.Policies))},
			{own.Refs, routes},
			{own.Rules, routes},
		}
		for _, run := range runs {
			for _, u := range run.resources {
				checked++
				boundaryUnchanged(t, in, run.k, u, &withFindings)
			}
		}
	}
	if withFindings == 0 || withFindings == checked {
		t.Fatalf("%d of %d resources have findings; want some with and some without", withFindings, checked)
	}
}

// boundaryUnchanged checks analyzer k on u against a snapshot holding only u,
// what it named and an unrelated route.
func boundaryUnchanged(t *testing.T, in Input, k own.Kind, u *unstructured.Unstructured, withFindings *int) {
	t.Helper()
	want, refs := ownFindings(in, k, u)
	if len(want) > 0 {
		*withFindings++
	}
	keep := append([]objects.Ref{objects.RefOf(u)}, refs...)
	snap := *in.Snapshot
	snap.Objects = map[schema.GroupVersionResource][]*unstructured.Unstructured{}
	for gvr, objs := range in.Snapshot.Objects {
		for _, o := range objs {
			if slices.Contains(keep, objects.RefOf(o)) {
				snap.Objects[gvr] = append(snap.Objects[gvr], o)
			}
		}
	}
	other := testutil.HTTPRoute(u.GetNamespace(), "unrelated", []objects.ParentRef{testutil.GwRef(u.GetNamespace(), "elsewhere")})
	for _, s := range in.Served {
		if s.Group == objects.GatewayAPIGroup && s.Kind == "HTTPRoute" {
			snap.Objects[s.GVR()] = append(snap.Objects[s.GVR()], other)
		}
	}
	snap.Services = nil
	for _, s := range in.Snapshot.Services {
		if slices.Contains(keep, objects.Ref{Kind: "Service", Namespace: s.Namespace, Name: s.Name}) {
			snap.Services = append(snap.Services, s)
		}
	}
	perturbed := in
	perturbed.Snapshot = &snap
	got, _ := ownFindings(perturbed, k, u)
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if !bytes.Equal(g, w) {
		t.Errorf("analyzer %d on %s changed when other objects changed:\n got %s\nwant %s", k, objects.RefOf(u), g, w)
	}
}
