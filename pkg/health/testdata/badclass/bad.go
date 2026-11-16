// Package badclass marks findings own-object and runs analyzers outside
// ownrun.go, in the ways a name-based check misses.
package badclass

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	h "github.com/DuncanDoyle/snellius/pkg/health"
	o "github.com/DuncanDoyle/snellius/pkg/health/own"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// Mark sets the class through a variable, a conversion and arithmetic.
func Mark(f *h.Finding) {
	var c = h.ClassOwn
	f.Class = c
	f.Class = h.Class(1)
	f.Class = h.ClassDerived + 1
}

// Run calls the analyzers with its own install context.
func Run(ic o.InstallContext) o.Result { return o.Evaluate(o.Status, ic, nil, reader{}) }

// reader implements own.Named.
type reader struct{}

func (reader) Object(objects.Ref) (*unstructured.Unstructured, bool) { return nil, false }

func (reader) FirstSeen(*unstructured.Unstructured) time.Time { return time.Time{} }
