// Package badclass runs analyzers outside ownrun.go, in the ways a
// name-based check misses. It cannot mark a finding: the class is unexported.
package badclass

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	o "github.com/DuncanDoyle/snellius/pkg/health/own"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// Run calls the analyzers with its own install context.
func Run(ic o.InstallContext) o.Result { return o.Evaluate(o.Status, ic, nil, reader{}) }

// reader implements own.Named.
type reader struct{}

func (reader) Object(objects.Ref) (*unstructured.Unstructured, bool) { return nil, false }

func (reader) FirstSeen(*unstructured.Unstructured) time.Time { return time.Time{} }
