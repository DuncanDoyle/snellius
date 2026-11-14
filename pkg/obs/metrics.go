// Package obs is a dependency-free metrics registry that serves the
// Prometheus text exposition format 0.0.4.
package obs

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Registry holds metric families and serves them over HTTP.
type Registry struct {
	mu       sync.Mutex
	families map[string]*family
}

type family struct {
	name, help, kind string
	labels           []string
	series           map[string]*series
}

type series struct {
	values []string
	val    float64
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry { return &Registry{families: map[string]*family{}} }

// CounterVec is a counter family keyed by label values.
type CounterVec struct {
	r *Registry
	f *family
}

// GaugeVec is a gauge family keyed by label values.
type GaugeVec struct {
	r *Registry
	f *family
}

// Counter registers a counter family. It panics on a duplicate name.
func (r *Registry) Counter(name, help string, labels ...string) *CounterVec {
	return &CounterVec{r, r.register(name, help, "counter", labels)}
}

// Gauge registers a gauge family. It panics on a duplicate name.
func (r *Registry) Gauge(name, help string, labels ...string) *GaugeVec {
	return &GaugeVec{r, r.register(name, help, "gauge", labels)}
}

func (r *Registry) register(name, help, kind string, labels []string) *family {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.families[name]; dup {
		panic("obs: duplicate metric " + name)
	}
	f := &family{name: name, help: help, kind: kind, labels: labels, series: map[string]*series{}}
	r.families[name] = f
	return f
}

// Inc adds 1 to the series for the label values.
func (c *CounterVec) Inc(values ...string) { c.Add(1, values...) }

// Add adds v to the series for the label values.
func (c *CounterVec) Add(v float64, values ...string) {
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	c.f.at(values).val += v
}

// Set sets the series for the label values to v.
func (g *GaugeVec) Set(v float64, values ...string) {
	g.r.mu.Lock()
	defer g.r.mu.Unlock()
	g.f.at(values).val = v
}

func (f *family) at(values []string) *series {
	if len(values) != len(f.labels) {
		panic(fmt.Sprintf("obs: %s wants %d label values, got %d", f.name, len(f.labels), len(values)))
	}
	key := strings.Join(values, "\xff")
	s := f.series[key]
	if s == nil {
		s = &series{values: append([]string(nil), values...)}
		f.series[key] = s
	}
	return s
}

// ServeHTTP writes every family, sorted by name, with series sorted by label values.
func (r *Registry) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_ = r.write(w)
}

func (r *Registry) write(w io.Writer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.families))
	for n := range r.families {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		f := r.families[n]
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", f.name, escapeHelp(f.help), f.name, f.kind)
		keys := make([]string, 0, len(f.series))
		for k := range f.series {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			s := f.series[k]
			b.WriteString(f.name)
			if len(f.labels) > 0 {
				b.WriteByte('{')
				for i, l := range f.labels {
					if i > 0 {
						b.WriteByte(',')
					}
					fmt.Fprintf(&b, "%s=\"%s\"", l, escapeLabel(s.values[i]))
				}
				b.WriteByte('}')
			}
			b.WriteByte(' ')
			b.WriteString(strconv.FormatFloat(s.val, 'g', -1, 64))
			b.WriteByte('\n')
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

var (
	labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	helpEscaper  = strings.NewReplacer(`\`, `\\`, "\n", `\n`)
)

func escapeLabel(s string) string { return labelEscaper.Replace(s) }
func escapeHelp(s string) string  { return helpEscaper.Replace(s) }
