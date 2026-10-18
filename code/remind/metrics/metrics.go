// Package metrics keeps counters, gauges, and histograms and writes them
// in the Prometheus text exposition format.
package metrics

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefBuckets are the histogram buckets used when none are given, in
// seconds, sized for HTTP request durations.
var DefBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

// A Registry holds named metrics. It is safe for concurrent use.
type Registry struct {
	// Now is the clock Instrument uses to time requests; nil means
	// time.Now. Set it before the registry is used.
	Now func() time.Time

	mu       sync.Mutex
	families map[string]*family
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{families: make(map[string]*family)}
}

type kind string

const (
	kindCounter   kind = "counter"
	kindGauge     kind = "gauge"
	kindHistogram kind = "histogram"
)

// A family is one metric name with a child per set of label values.
type family struct {
	name    string
	help    string
	kind    kind
	labels  []string
	order   []int     // label indexes sorted by name
	buckets []float64 // histograms only

	mu       sync.Mutex
	children map[string]*child
}

type child struct {
	values []string
	value  atomicFloat // counters and gauges
	hist   *histogram  // histograms
}

// register returns the family called name, creating it if needed.
// Registering a name again with a different kind or labels panics.
func (r *Registry) register(name, help string, k kind, buckets []float64, labels []string) *family {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.families[name]; ok {
		if f.kind != k || !slices.Equal(f.labels, labels) {
			panic(fmt.Sprintf("metrics: %s registered again as a different metric", name))
		}
		return f
	}
	f := &family{
		name:     name,
		help:     help,
		kind:     k,
		labels:   slices.Clone(labels),
		order:    make([]int, len(labels)),
		buckets:  buckets,
		children: make(map[string]*child),
	}
	for i := range f.order {
		f.order[i] = i
	}
	slices.SortFunc(f.order, func(a, b int) int { return strings.Compare(labels[a], labels[b]) })
	if len(labels) == 0 {
		f.with(nil)
	}
	r.families[name] = f
	return f
}

// with returns the child for values, creating it if needed.
func (f *family) with(values []string) *child {
	if len(values) != len(f.labels) {
		panic(fmt.Sprintf("metrics: %s takes %d label values, got %d", f.name, len(f.labels), len(values)))
	}
	key := strings.Join(values, "\xff")
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.children[key]
	if !ok {
		c = &child{values: slices.Clone(values)}
		if f.kind == kindHistogram {
			c.hist = &histogram{bounds: f.buckets, counts: make([]uint64, len(f.buckets)+1)}
		}
		f.children[key] = c
	}
	return c
}

// A CounterVec is a counter partitioned by labels.
type CounterVec struct{ f *family }

// A Counter is a value that only goes up.
type Counter struct{ c *child }

// Counter registers a counter, or returns the one already registered
// under name. It panics if name is registered as another kind of metric
// or with other labels.
func (r *Registry) Counter(name, help string, labels ...string) *CounterVec {
	return &CounterVec{r.register(name, help, kindCounter, nil, labels)}
}

// With returns the counter for the given label values, in the order the
// labels were registered. It panics if the number of values is wrong.
func (v *CounterVec) With(values ...string) Counter { return Counter{v.f.with(values)} }

// Inc adds one.
func (c Counter) Inc() { c.c.value.add(1) }

// Add adds n. Negative values are ignored.
func (c Counter) Add(n float64) {
	if n > 0 {
		c.c.value.add(n)
	}
}

// A GaugeVec is a gauge partitioned by labels.
type GaugeVec struct{ f *family }

// A Gauge is a value that goes up and down.
type Gauge struct{ c *child }

// Gauge registers a gauge, or returns the one already registered under
// name. It panics on a conflicting registration, as Counter does.
func (r *Registry) Gauge(name, help string, labels ...string) *GaugeVec {
	return &GaugeVec{r.register(name, help, kindGauge, nil, labels)}
}

// With returns the gauge for the given label values. It panics if the
// number of values is wrong.
func (v *GaugeVec) With(values ...string) Gauge { return Gauge{v.f.with(values)} }

// Set sets the gauge to n.
func (g Gauge) Set(n float64) { g.c.value.store(n) }

// Inc adds one.
func (g Gauge) Inc() { g.c.value.add(1) }

// Add adds n, which may be negative.
func (g Gauge) Add(n float64) { g.c.value.add(n) }

// A HistogramVec is a histogram partitioned by labels.
type HistogramVec struct{ f *family }

// A Histogram counts observations into buckets.
type Histogram struct{ c *child }

// Histogram registers a histogram with the given bucket upper bounds
// (DefBuckets if empty), or returns the one already registered under
// name. A +Inf bucket is always added. It panics on a conflicting
// registration, as Counter does.
func (r *Registry) Histogram(name, help string, buckets []float64, labels ...string) *HistogramVec {
	if len(buckets) == 0 {
		buckets = DefBuckets
	}
	b := slices.Clone(buckets)
	slices.Sort(b)
	b = slices.Compact(b)
	b = slices.DeleteFunc(b, func(x float64) bool { return math.IsInf(x, 1) })
	return &HistogramVec{r.register(name, help, kindHistogram, b, labels)}
}

// With returns the histogram for the given label values. It panics if
// the number of values is wrong.
func (v *HistogramVec) With(values ...string) Histogram { return Histogram{v.f.with(values)} }

// Observe records one observation.
func (h Histogram) Observe(x float64) { h.c.hist.observe(x) }

type histogram struct {
	bounds []float64

	mu     sync.Mutex
	counts []uint64 // per bucket, not cumulative; the last is +Inf
	sum    float64
	count  uint64
}

func (h *histogram) observe(x float64) {
	i, _ := slices.BinarySearch(h.bounds, x)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.counts[i]++
	h.sum += x
	h.count++
}

func (h *histogram) snapshot() (counts []uint64, sum float64, count uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.counts), h.sum, h.count
}

type atomicFloat struct{ bits atomic.Uint64 }

func (f *atomicFloat) load() float64   { return math.Float64frombits(f.bits.Load()) }
func (f *atomicFloat) store(v float64) { f.bits.Store(math.Float64bits(v)) }

func (f *atomicFloat) add(v float64) {
	for {
		old := f.bits.Load()
		next := math.Float64bits(math.Float64frombits(old) + v)
		if f.bits.CompareAndSwap(old, next) {
			return
		}
	}
}

// WriteText writes every metric in the Prometheus text format, sorted
// by name and then by labels.
func (r *Registry) WriteText(w io.Writer) error {
	r.mu.Lock()
	fams := make([]*family, 0, len(r.families))
	for _, f := range r.families {
		fams = append(fams, f)
	}
	r.mu.Unlock()
	slices.SortFunc(fams, func(a, b *family) int { return strings.Compare(a.name, b.name) })

	var buf bytes.Buffer
	for _, f := range fams {
		f.write(&buf)
	}
	if _, err := buf.WriteTo(w); err != nil {
		return fmt.Errorf("write metrics: %w", err)
	}
	return nil
}

// Handler serves the registry's metrics.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_ = r.WriteText(w)
	})
}

type series struct {
	labels string // rendered label pairs, without braces
	c      *child
}

func (f *family) write(buf *bytes.Buffer) {
	f.mu.Lock()
	all := make([]series, 0, len(f.children))
	for _, c := range f.children {
		all = append(all, series{labels: f.renderLabels(c.values), c: c})
	}
	f.mu.Unlock()
	if len(all) == 0 {
		return
	}
	slices.SortFunc(all, func(a, b series) int { return strings.Compare(a.labels, b.labels) })

	fmt.Fprintf(buf, "# HELP %s %s\n", f.name, escapeHelp(f.help))
	fmt.Fprintf(buf, "# TYPE %s %s\n", f.name, f.kind)
	for _, s := range all {
		if f.kind != kindHistogram {
			writeSample(buf, f.name, s.labels, formatFloat(s.c.value.load()))
			continue
		}
		counts, sum, count := s.c.hist.snapshot()
		var cum uint64
		for i, n := range counts {
			cum += n
			le := "+Inf"
			if i < len(f.buckets) {
				le = formatFloat(f.buckets[i])
			}
			writeSample(buf, f.name+"_bucket", joinLabels(s.labels, `le="`+le+`"`), strconv.FormatUint(cum, 10))
		}
		writeSample(buf, f.name+"_sum", s.labels, formatFloat(sum))
		writeSample(buf, f.name+"_count", s.labels, strconv.FormatUint(count, 10))
	}
}

// renderLabels formats label pairs sorted by label name.
func (f *family) renderLabels(values []string) string {
	pairs := make([]string, len(values))
	for i, j := range f.order {
		pairs[i] = f.labels[j] + `="` + escapeLabel(values[j]) + `"`
	}
	return strings.Join(pairs, ",")
}

func joinLabels(a, b string) string {
	if a == "" {
		return b
	}
	return a + "," + b
}

func writeSample(buf *bytes.Buffer, name, labels, value string) {
	buf.WriteString(name)
	if labels != "" {
		buf.WriteString("{" + labels + "}")
	}
	buf.WriteString(" " + value + "\n")
}

func formatFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

var (
	helpEscaper  = strings.NewReplacer(`\`, `\\`, "\n", `\n`)
	labelEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)
)

func escapeHelp(s string) string  { return helpEscaper.Replace(s) }
func escapeLabel(s string) string { return labelEscaper.Replace(s) }
