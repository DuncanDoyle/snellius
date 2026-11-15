package sar

import (
	"time"

	"github.com/DuncanDoyle/snellius/pkg/obs"
)

// metrics are the authoriser's series on the shared registry.
type metrics struct {
	checks     *obs.CounterVec
	seconds    *obs.CounterVec
	cache      *obs.CounterVec
	identities *obs.GaugeVec
	bytes      *obs.GaugeVec
	evictions  *obs.CounterVec
}

func newMetrics(r *obs.Registry) *metrics {
	if r == nil {
		r = obs.NewRegistry()
	}
	return &metrics{
		checks:     r.Counter("snellius_authz_checks_total", "SubjectAccessReviews sent, by verb and outcome.", "verb", "outcome"),
		seconds:    r.Counter("snellius_authz_check_seconds_total", "Wall time spent in SubjectAccessReviews, in seconds."),
		cache:      r.Counter("snellius_authz_cache_total", "Authorisation cache lookups, by result.", "result"),
		identities: r.Gauge("snellius_authz_identities", "Identities in the authorisation cache."),
		bytes:      r.Gauge("snellius_authz_cache_bytes", "Accounted bytes of the authorisation cache, against its budget."),
		evictions:  r.Counter("snellius_authz_cache_evictions_total", "Authorisation cache entries evicted to stay within the byte budget."),
	}
}

// checked records one SAR: its outcome and wall time.
func (m *metrics) checked(verb string, allowed bool, err error, took time.Duration) {
	outcome := "denied"
	switch {
	case err != nil:
		outcome = "error"
	case allowed:
		outcome = "allowed"
	}
	m.checks.Inc(verb, outcome)
	m.seconds.Add(took.Seconds())
}
