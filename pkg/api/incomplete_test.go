package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DuncanDoyle/snellius/pkg/explore"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// listRBAC is an rbac that also answers list questions from its list grants.
type listRBAC struct{ *rbac }

func (a listRBAC) CanList(_ *http.Request, gk objects.GroupKind, ns string) (bool, error) {
	return a.lists[listKey{gk.Group, gk.Kind, ns}], nil
}

var _ explore.Permissions = incompletePermissions{}

// Get is the named check, so a resourceNames get grant makes a ref readable;
// List is the list check.
func TestIncompleteAdapterUsesNamedAndList(t *testing.T) {
	granted, other := service("named-svc"), service("other-svc")
	a := listRBAC{grantOf(readableShop, granted)}
	v := newView(httptest.NewRequest("GET", "/api/x", nil), a, true)
	p := incompletePermissions{v}
	if !p.Get(granted) || p.Get(other) {
		t.Errorf("get: granted %v, other %v", p.Get(granted), p.Get(other))
	}
	if !p.List(objects.GroupKind{Group: objects.GatewayAPIGroup, Kind: "Gateway"}, "infra") || p.List(objects.GroupKind{Kind: "Service"}, "shop") {
		t.Error("list answers not followed")
	}
}
