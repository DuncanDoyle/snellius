package own

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const module = "github.com/DuncanDoyle/snellius/"

// directAllowed are the packages outside the standard library own may import.
var directAllowed = []string{
	module + "pkg/objects",
	module + "pkg/kinds",
	"k8s.io/apimachinery/pkg/apis/meta/v1",
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured",
	"k8s.io/apimachinery/pkg/runtime/schema",
}

// closureAllowed is the audited transitive closure of directAllowed for the
// current module versions. A dependency bump that adds a package fails
// TestOwnImportsAllowlisted until the package is audited and listed here.
var closureAllowed = []string{
	module + "pkg/trigger",
	"k8s.io/apimachinery/pkg/api/errors",
	"k8s.io/apimachinery/pkg/api/operation",
	"k8s.io/apimachinery/pkg/api/resource",
	"k8s.io/apimachinery/pkg/api/validate/constraints",
	"k8s.io/apimachinery/pkg/api/validate/content",
	"k8s.io/apimachinery/pkg/conversion",
	"k8s.io/apimachinery/pkg/conversion/queryparams",
	"k8s.io/apimachinery/pkg/fields",
	"k8s.io/apimachinery/pkg/labels",
	"k8s.io/apimachinery/pkg/runtime",
	"k8s.io/apimachinery/pkg/runtime/serializer/cbor/direct",
	"k8s.io/apimachinery/pkg/runtime/serializer/cbor/internal/modes",
	"k8s.io/apimachinery/pkg/selection",
	"k8s.io/apimachinery/pkg/types",
	"k8s.io/apimachinery/pkg/util/errors",
	"k8s.io/apimachinery/pkg/util/intstr",
	"k8s.io/apimachinery/pkg/util/json",
	"k8s.io/apimachinery/pkg/util/naming",
	"k8s.io/apimachinery/pkg/util/net",
	"k8s.io/apimachinery/pkg/util/runtime",
	"k8s.io/apimachinery/pkg/util/sets",
	"k8s.io/apimachinery/pkg/util/validation",
	"k8s.io/apimachinery/pkg/util/validation/field",
	"k8s.io/apimachinery/pkg/watch",
	"k8s.io/apimachinery/third_party/forked/golang/reflect",
	"k8s.io/klog/v2",
	"k8s.io/klog/v2/internal/buffer",
	"k8s.io/klog/v2/internal/clock",
	"k8s.io/klog/v2/internal/dbg",
	"k8s.io/klog/v2/internal/serialize",
	"k8s.io/klog/v2/internal/severity",
	"k8s.io/klog/v2/internal/sloghandler",
	"k8s.io/klog/v2/internal/verbosity",
	"k8s.io/klog/v2/textlogger",
	"k8s.io/kube-openapi/pkg/util",
	"k8s.io/utils/net",
	"k8s.io/utils/ptr",
	"k8s.io/utils/internal/third_party/forked/golang/net",
	"sigs.k8s.io/json",
	"sigs.k8s.io/json/internal/golang/encoding/json",
	"sigs.k8s.io/randfill",
	"sigs.k8s.io/randfill/bytesource",
	"sigs.k8s.io/structured-merge-diff/v6/value",
	"github.com/go-logr/logr",
	"github.com/fxamacker/cbor/v2",
	"github.com/x448/float16",
	"github.com/json-iterator/go",
	"github.com/modern-go/concurrent",
	"github.com/modern-go/reflect2",
	"go.yaml.in/yaml/v2",
	"gopkg.in/inf.v0",
	"golang.org/x/net/http/httpguts",
	"golang.org/x/net/http2",
	"golang.org/x/net/http2/hpack",
	"golang.org/x/net/idna",
	"golang.org/x/net/internal/httpcommon",
	"golang.org/x/net/internal/httpsfv",
	"golang.org/x/text/secure/bidirule",
	"golang.org/x/text/transform",
	"golang.org/x/text/unicode/bidi",
	"golang.org/x/text/unicode/norm",
}

// importViolations walks the full transitive import graph of the package in
// dir and returns every package outside the standard library and the lists.
// A standard-library package imports only the standard library, so the walk stops there.
func importViolations(t *testing.T, dir string) []string {
	t.Helper()
	root, err := build.ImportDir(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	var bad []string
	seen := map[string]bool{}
	type edge struct {
		path   string
		direct bool
	}
	var queue []edge
	for _, p := range root.Imports {
		queue = append(queue, edge{p, true})
	}
	for len(queue) > 0 {
		e := queue[0]
		queue = queue[1:]
		if seen[e.path] || e.path == "C" || e.path == "unsafe" {
			continue
		}
		seen[e.path] = true
		pkg, err := build.Import(e.path, root.Dir, 0)
		if err != nil {
			bad = append(bad, e.path+": "+err.Error())
			continue
		}
		if pkg.Goroot {
			continue
		}
		inModule := strings.HasPrefix(e.path, module)
		n := len(bad)
		switch {
		case e.direct && !slices.Contains(directAllowed, e.path):
			bad = append(bad, "direct import "+e.path)
		case !e.direct && !slices.Contains(directAllowed, e.path) && !slices.Contains(closureAllowed, e.path):
			bad = append(bad, "unaudited dependency "+e.path)
		case inModule && !slices.Contains([]string{module + "pkg/objects", module + "pkg/kinds", module + "pkg/trigger"}, e.path):
			bad = append(bad, "module package "+e.path)
		}
		if len(bad) > n {
			continue // a rejected package's own imports add nothing to the verdict
		}
		for _, p := range pkg.Imports {
			queue = append(queue, edge{p, false})
		}
	}
	return bad
}

// own reaches only the standard library, objects, kinds and their audited
// closure, so cache, engine, install, computed, health and client-go are unreachable.
func TestOwnImportsAllowlisted(t *testing.T) {
	if bad := importViolations(t, "."); len(bad) > 0 {
		t.Fatalf("package own reaches packages outside its allowlist:\n%s", strings.Join(bad, "\n"))
	}
	if bad := importViolations(t, filepath.Join("testdata", "badimport")); len(bad) == 0 {
		t.Fatal("the check must reject a package that imports the cache")
	}
}

// parseDir parses the non-test Go files of dir.
func parseDir(t *testing.T, dir string) []*ast.File {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var out []*ast.File
	for _, n := range names {
		if strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), n, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
	return out
}

// packageVars returns the package-level variables declared in dir.
func packageVars(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, f := range parseDir(t, dir) {
		for _, d := range f.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.VAR {
				for _, s := range g.Specs {
					for _, n := range s.(*ast.ValueSpec).Names {
						out = append(out, n.Name)
					}
				}
			}
		}
	}
	return out
}

// own declares constants only, so an analyzer cannot reach state through a global.
func TestOwnHasNoPackageState(t *testing.T) {
	if vs := packageVars(t, "."); len(vs) > 0 {
		t.Fatalf("package own declares package-level variables: %v", vs)
	}
	if vs := packageVars(t, filepath.Join("testdata", "badvar")); len(vs) == 0 {
		t.Fatal("the check must reject a package-level variable")
	}
}

// entryViolations returns the exported function types in dir, and the
// exported functions that take or return a function.
func entryViolations(t *testing.T, dir string) []string {
	t.Helper()
	hasFunc := func(fl *ast.FieldList) bool {
		found := false
		if fl != nil {
			for _, f := range fl.List {
				ast.Inspect(f.Type, func(n ast.Node) bool {
					_, ok := n.(*ast.FuncType)
					found = found || ok
					return true
				})
			}
		}
		return found
	}
	var out []string
	for _, f := range parseDir(t, dir) {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				for _, s := range d.Specs {
					if ts, ok := s.(*ast.TypeSpec); ok && ts.Name.IsExported() {
						if _, fn := ts.Type.(*ast.FuncType); fn {
							out = append(out, "function type "+ts.Name.Name)
						}
					}
				}
			case *ast.FuncDecl:
				if d.Name.IsExported() && (hasFunc(d.Type.Params) || hasFunc(d.Type.Results)) {
					out = append(out, "function "+d.Name.Name)
				}
			}
		}
	}
	return out
}

// Evaluate is the only entry point: no exported function type exists and no
// exported function takes or returns an analyzer. Unknown kinds evaluate to nothing.
func TestOwnSingleEntryPoint(t *testing.T) {
	if bad := entryViolations(t, "."); len(bad) > 0 {
		t.Fatalf("package own exports analyzer-shaped API: %v", bad)
	}
	if bad := entryViolations(t, filepath.Join("testdata", "badentry")); len(bad) != 2 {
		t.Fatalf("the check must reject an exported function type and a function taking one: %v", bad)
	}
	for _, k := range []Kind{0, Rules + 1, 255} {
		if r := Evaluate(k, InstallContext{}, nil, nil); len(r.Findings()) != 0 || !r.WakeAt().IsZero() {
			t.Fatalf("kind %d must evaluate to an empty result", k)
		}
	}
	if (Finding{}).Sealed() {
		t.Fatal("a Finding built outside own must not be sealed")
	}
}
