package api

import (
	"maps"
	"net/http"
	"net/url"
	"slices"

	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// namespaces is GET …/namespaces (D-100 item 8): the sorted namespaces of the
// install's route, Gateway and ListenerSet rows the caller can see. It
// prefetches the list pages' root buckets, then checks a row only while its
// namespace is not yet listed. It takes no parameters.
func (h *handler) namespaces(w http.ResponseWriter, r *http.Request) {
	p, i := h.find(w, r)
	if i == nil {
		return
	}
	if vals, err := url.ParseQuery(r.URL.RawQuery); err != nil || len(vals) > 0 {
		writeErr(w, http.StatusBadRequest, badParam("namespaces takes no parameters").Error())
		return
	}
	var refs []objects.Ref
	if ix := i.Index; ix != nil {
		for k := range ix.Routes {
			refs = append(refs, ix.Routes[k].Ref)
		}
		for k := range ix.Gateways {
			refs = append(refs, ix.Gateways[k].Ref)
		}
		for k := range ix.ListenerSets {
			refs = append(refs, ix.ListenerSets[k].Ref)
		}
	}
	v := h.view(r)
	v.prefetch(func(add func(objects.Ref)) {
		for _, ref := range refs {
			add(ref)
		}
	})
	seen := map[string]bool{}
	for _, ref := range refs {
		if !seen[ref.Namespace] && v.can(ref) {
			seen[ref.Namespace] = true
		}
	}
	ns := slices.Sorted(maps.Keys(seen))
	if ns == nil {
		ns = []string{}
	}
	writeJSON(w, NamespacesResponse{Install: i.Resolved.ID(), Namespaces: ns, CollectedAt: wireTime(p.ConfirmedAt)})
}
