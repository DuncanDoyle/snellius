package api

import (
	"time"

	"github.com/DuncanDoyle/snellius/pkg/collect"
	"github.com/DuncanDoyle/snellius/pkg/ctrlmetrics"
)

// WithProjectionBypass turns every secure-mode projection step off: views are
// not secure, so the D-013 rows and counts come back. The rule harness uses it
// to show that its invariant fails without the projection.
func WithProjectionBypass() HandlerOption {
	return func(c *handlerConfig) { c.bypass = true }
}

// ruleMetrics serves install id's controller metrics, collected at now(),
// with an active reject on each Gateway in rejects.
type ruleMetrics struct {
	id      string
	now     func() time.Time
	rejects []ctrlmetrics.GatewayKey
}

func (m ruleMetrics) IDs() []string { return []string{m.id} }

func (m ruleMetrics) Get(id string) (ctrlmetrics.Result, bool) {
	if id != m.id {
		return ctrlmetrics.Result{}, false
	}
	r := map[ctrlmetrics.GatewayKey]float64{}
	for _, k := range m.rejects {
		r[k] = 1
	}
	return ctrlmetrics.Result{CollectedAt: m.now(), Metrics: ctrlmetrics.Metrics{Rejects: r, Managed: map[ctrlmetrics.ManagedKey]float64{}, ManagedPresent: true}}, true
}

// RuleCollected is the engine's Collected for the rule harness: base's values
// (none when nil) and install id's controller metrics at now(), rejecting on
// each Gateway named in rejects.
func RuleCollected(id string, now func() time.Time, base func() collect.Snapshot, rejects ...ctrlmetrics.GatewayKey) func() collect.Snapshot {
	return served(ruleMetrics{id: id, now: now, rejects: rejects}, 30*time.Second, base)
}
