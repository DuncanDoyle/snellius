package api

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/DuncanDoyle/snellius/pkg/obs"
)

// Ready reports whether the process can serve; the reason is for the log only.
type Ready func() (ready bool, reason string)

// ModelReady is ready once the model is synced and no install was rejected (D-016).
// A rejected install's ID goes in the reason, never into a response.
func ModelReady(src ModelSource) Ready {
	return func() (bool, string) {
		p := src.Current()
		if p == nil || !synced(p.Model) {
			return false, "model not synced"
		}
		if len(p.Model.Rejected) > 0 {
			return false, "installs rejected: " + strings.Join(p.Model.Rejected, "; ")
		}
		return true, ""
	}
}

// ProbeHandler serves /healthz, /readyz and /metrics, and nothing else.
func ProbeHandler(ready Ready, reg *obs.Registry) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /readyz", readyzHandler(ready))
	if reg != nil {
		mux.Handle("GET /metrics", reg)
	}
	return mux
}

func healthz(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

// readyzHandler answers 200 or a bare 503; a changed reason is logged once.
func readyzHandler(ready Ready) http.HandlerFunc {
	var last atomic.Value
	return func(w http.ResponseWriter, _ *http.Request) {
		ok, reason := ready()
		if ok {
			last.Store("")
			w.WriteHeader(http.StatusOK)
			return
		}
		if prev, _ := last.Load().(string); prev != reason {
			last.Store(reason)
			slog.Warn("not ready", "reason", reason)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "not ready")
	}
}
