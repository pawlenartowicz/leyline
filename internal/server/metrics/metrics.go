// Package metrics is a zero-dependency Prometheus text-format emitter.
//
// Design constraints:
//   - No third-party deps; binary growth must be negligible.
//   - Counter/Gauge are atomic; the registry is a package-level singleton.
//   - Metric names mirror prometheus/client_golang defaults so a future swap
//     doesn't rename series out from under existing dashboards.
//   - Output is sorted and stable across scrapes for diffable smoke tests.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Counter is a monotonically increasing int64. Identity (name, labels) lives
// in the registry entry, not on the counter itself, so a single Counter type
// serves both bare counters and CounterVec children.
type Counter struct{ v atomic.Int64 }

func (c *Counter) Inc()         { c.v.Add(1) }
func (c *Counter) Add(d int64)  { c.v.Add(d) }
func (c *Counter) Value() int64 { return c.v.Load() }

// Gauge is a settable int64. Use GaugeFunc when the value is cheap to compute
// at scrape time and you want to avoid drift from Inc/Dec bookkeeping.
type Gauge struct{ v atomic.Int64 }

func (g *Gauge) Set(x int64)  { g.v.Store(x) }
func (g *Gauge) Value() int64 { return g.v.Load() }

// CounterVec is a counter family keyed by an ordered label tuple. The label
// names are declared at construction; With(...) selects (or lazily creates)
// the child for a specific tuple.
type CounterVec struct {
	name   string
	help   string
	labels []string

	mu sync.RWMutex
	m  map[string]*Counter
}

// With returns the Counter for the given label values. The arity must match
// the declared label count, otherwise With panics — a programmer error caught
// in tests, not a runtime condition.
func (cv *CounterVec) With(values ...string) *Counter {
	if len(values) != len(cv.labels) {
		panic(fmt.Sprintf("metrics: %s: got %d label values, want %d", cv.name, len(values), len(cv.labels)))
	}
	key := encodeKey(values)
	cv.mu.RLock()
	c, ok := cv.m[key]
	cv.mu.RUnlock()
	if ok {
		return c
	}
	cv.mu.Lock()
	defer cv.mu.Unlock()
	if c, ok = cv.m[key]; ok {
		return c
	}
	c = &Counter{}
	cv.m[key] = c
	return c
}

// GaugeFunc emits one or more gauge values computed at scrape time. The
// callback is invoked with an `emit` function that the producer calls once
// per (labelValues, value) pair. Producers don't need to know about Prom
// text format — they just enumerate their state.
type GaugeFunc struct {
	name   string
	help   string
	labels []string
	fn     func(emit func(values []string, v int64))
}

// counterEntry is a registered bare counter with its identity.
type counterEntry struct {
	name string
	help string
	c    *Counter
}

// gaugeEntry is a registered bare gauge with its identity.
type gaugeEntry struct {
	name string
	help string
	g    *Gauge
}

// Registry holds the registered metric set and emits Prom text on demand.
type Registry struct {
	mu       sync.RWMutex
	counters []*counterEntry
	cvecs    []*CounterVec
	gauges   []*gaugeEntry
	gfns     []*GaugeFunc
}

// NewRegistry returns an empty registry. Use Default for the package-wide one.
func NewRegistry() *Registry { return &Registry{} }

// Default is the process-wide registry. Wired by exports.go and read by the
// /metrics HTTP handler.
var Default = NewRegistry()

// Counter registers a new bare counter against r.
func (r *Registry) Counter(name, help string) *Counter {
	c := &Counter{}
	r.mu.Lock()
	r.counters = append(r.counters, &counterEntry{name: name, help: help, c: c})
	r.mu.Unlock()
	return c
}

// CounterVec registers a labeled counter family against r.
func (r *Registry) CounterVec(name, help string, labels ...string) *CounterVec {
	cv := &CounterVec{name: name, help: help, labels: labels, m: make(map[string]*Counter)}
	r.mu.Lock()
	r.cvecs = append(r.cvecs, cv)
	r.mu.Unlock()
	return cv
}

// Gauge registers a bare gauge against r.
func (r *Registry) Gauge(name, help string) *Gauge {
	g := &Gauge{}
	r.mu.Lock()
	r.gauges = append(r.gauges, &gaugeEntry{name: name, help: help, g: g})
	r.mu.Unlock()
	return g
}

// GaugeFunc registers a scrape-time gauge against r. Pass nil for labels for
// an unlabeled gauge.
func (r *Registry) GaugeFunc(name, help string, labels []string, fn func(emit func(values []string, v int64))) {
	gf := &GaugeFunc{name: name, help: help, labels: labels, fn: fn}
	r.mu.Lock()
	r.gfns = append(r.gfns, gf)
	r.mu.Unlock()
}

// NewCounter registers a bare counter against Default.
func NewCounter(name, help string) *Counter { return Default.Counter(name, help) }

// NewCounterVec registers a labeled counter family against Default.
func NewCounterVec(name, help string, labels ...string) *CounterVec {
	return Default.CounterVec(name, help, labels...)
}

// NewGauge registers a bare gauge against Default.
func NewGauge(name, help string) *Gauge { return Default.Gauge(name, help) }

// RegisterGaugeFunc registers a scrape-time gauge against Default.
func RegisterGaugeFunc(name, help string, labels []string, fn func(emit func(values []string, v int64))) {
	Default.GaugeFunc(name, help, labels, fn)
}

// emittedLine is one rendered metric sample with its sort key.
type emittedLine struct {
	metric string // metric name (sort primary)
	labels string // rendered label block including braces, "" if none (sort secondary)
	value  int64
}

// WriteTo emits the registry contents in Prometheus text format v0.0.4.
// Output is sorted by (metric_name, labels) for stable diffs across scrapes.
// # HELP / # TYPE lines are emitted once per metric family.
func (r *Registry) WriteTo(w io.Writer) (int64, error) {
	r.mu.RLock()
	counters := append([]*counterEntry(nil), r.counters...)
	cvecs := append([]*CounterVec(nil), r.cvecs...)
	gauges := append([]*gaugeEntry(nil), r.gauges...)
	gfns := append([]*GaugeFunc(nil), r.gfns...)
	r.mu.RUnlock()

	type family struct {
		name  string
		help  string
		kind  string // "counter" | "gauge"
		lines []emittedLine
	}
	families := map[string]*family{}
	get := func(name, help, kind string) *family {
		f, ok := families[name]
		if !ok {
			f = &family{name: name, help: help, kind: kind}
			families[name] = f
		}
		return f
	}

	for _, ce := range counters {
		f := get(ce.name, ce.help, "counter")
		f.lines = append(f.lines, emittedLine{metric: ce.name, value: ce.c.v.Load()})
	}
	for _, ge := range gauges {
		f := get(ge.name, ge.help, "gauge")
		f.lines = append(f.lines, emittedLine{metric: ge.name, value: ge.g.v.Load()})
	}
	for _, cv := range cvecs {
		f := get(cv.name, cv.help, "counter")
		cv.mu.RLock()
		for key, c := range cv.m {
			f.lines = append(f.lines, emittedLine{
				metric: cv.name,
				labels: renderLabels(cv.labels, decodeKey(key)),
				value:  c.v.Load(),
			})
		}
		cv.mu.RUnlock()
	}
	for _, gf := range gfns {
		f := get(gf.name, gf.help, "gauge")
		gf.fn(func(values []string, v int64) {
			f.lines = append(f.lines, emittedLine{
				metric: gf.name,
				labels: renderLabels(gf.labels, values),
				value:  v,
			})
		})
	}

	names := make([]string, 0, len(families))
	for n := range families {
		names = append(names, n)
	}
	sort.Strings(names)

	var buf strings.Builder
	for _, n := range names {
		f := families[n]
		sort.Slice(f.lines, func(i, j int) bool {
			return f.lines[i].labels < f.lines[j].labels
		})
		if f.help != "" {
			fmt.Fprintf(&buf, "# HELP %s %s\n", f.name, escapeHelp(f.help))
		}
		fmt.Fprintf(&buf, "# TYPE %s %s\n", f.name, f.kind)
		for _, ln := range f.lines {
			fmt.Fprintf(&buf, "%s%s %d\n", ln.metric, ln.labels, ln.value)
		}
	}
	n, err := io.WriteString(w, buf.String())
	return int64(n), err
}

// encodeKey serializes a label-value tuple into a map key. Uses 0x1f (unit
// separator) so collisions across legal label values are impossible.
func encodeKey(values []string) string {
	return strings.Join(values, "\x1f")
}

func decodeKey(k string) []string {
	if k == "" {
		return nil
	}
	return strings.Split(k, "\x1f")
}

// renderLabels returns the `{a="x",b="y"}` block (or "") for the given
// label names + values. Values are escaped: backslash → \\, double quote → \", newline → \n.
func renderLabels(names, values []string) string {
	if len(names) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(n)
		b.WriteString(`="`)
		b.WriteString(escapeLabelValue(values[i]))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

func escapeLabelValue(s string) string {
	if !strings.ContainsAny(s, "\\\"\n") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// escapeHelp escapes a HELP-line value: backslash and newline only (quotes are allowed).
func escapeHelp(s string) string {
	if !strings.ContainsAny(s, "\\\n") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
