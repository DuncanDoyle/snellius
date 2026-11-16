package api

import "github.com/DuncanDoyle/snellius/pkg/objects"

// incompletePermissions adapts a view to explore.Permissions: Get is the
// named check, List the list check.
type incompletePermissions struct{ v *view }

func (p incompletePermissions) Get(ref objects.Ref) bool { return p.v.canNamed(ref) }

func (p incompletePermissions) List(gk objects.GroupKind, namespace string) bool {
	return p.v.canList(gk, namespace)
}
