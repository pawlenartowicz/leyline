package metrics

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

func TestCounter_IncAndRender(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("test_calls_total", "Test calls.")
	c.Inc()
	c.Inc()
	c.Inc()

	out := render(t, r)
	wantLines := []string{
		"# HELP test_calls_total Test calls.",
		"# TYPE test_calls_total counter",
		"test_calls_total 3",
	}
	for _, want := range wantLines {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestCounterVec_TwoLabelsSorted(t *testing.T) {
	r := NewRegistry()
	cv := r.CounterVec("test_ops_total", "Ops.", "vault", "op")
	cv.With("alpha", "push").Inc()
	cv.With("alpha", "push").Inc()
	cv.With("beta", "pull").Inc()

	out := render(t, r)
	// Both labelled lines must be present, sorted by labels lexicographically.
	idxAlpha := strings.Index(out, `test_ops_total{vault="alpha",op="push"} 2`)
	idxBeta := strings.Index(out, `test_ops_total{vault="beta",op="pull"} 1`)
	if idxAlpha < 0 || idxBeta < 0 {
		t.Fatalf("missing expected lines\nfull output:\n%s", out)
	}
	if idxAlpha >= idxBeta {
		t.Errorf("expected alpha line before beta line (sort by labels); got alpha=%d beta=%d", idxAlpha, idxBeta)
	}
}

func TestCounterVec_With_ArityPanics(t *testing.T) {
	r := NewRegistry()
	cv := r.CounterVec("test_arity_total", "", "a", "b")
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on arity mismatch")
		}
	}()
	cv.With("only-one").Inc()
}

func TestCounter_RaceFree(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("test_race_total", "")
	const G, N = 100, 1000
	var wg sync.WaitGroup
	wg.Add(G)
	for i := 0; i < G; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < N; j++ {
				c.Inc()
			}
		}()
	}
	wg.Wait()
	if got := c.Value(); got != G*N {
		t.Fatalf("got %d, want %d", got, G*N)
	}
}

func TestLabelEscaping(t *testing.T) {
	r := NewRegistry()
	cv := r.CounterVec("test_esc_total", "", "vault")
	// Vault ID that exercises all three escape rules.
	cv.With("a\\b\"c\nd").Inc()

	out := render(t, r)
	wantLine := `test_esc_total{vault="a\\b\"c\nd"} 1`
	if !strings.Contains(out, wantLine) {
		t.Fatalf("output missing escaped line %q\nfull output:\n%s", wantLine, out)
	}

	// The output must still parse cleanly with the tolerant parser below.
	samples, err := parseProm(out)
	if err != nil {
		t.Fatalf("parse error: %v\n%s", err, out)
	}
	if len(samples) != 1 || samples[0].Name != "test_esc_total" {
		t.Fatalf("unexpected parsed samples: %+v", samples)
	}
	if got := samples[0].Labels["vault"]; got != "a\\b\"c\nd" {
		t.Errorf("round-trip label = %q, want %q", got, "a\\b\"c\nd")
	}
}

func TestGaugeFunc_EmitsAtScrapeTime(t *testing.T) {
	r := NewRegistry()
	calls := 0
	r.GaugeFunc("test_active_clients", "", []string{"vault"}, func(emit func([]string, int64)) {
		calls++
		emit([]string{"a"}, 3)
		emit([]string{"b"}, 7)
	})

	out1 := render(t, r)
	out2 := render(t, r)
	if calls != 2 {
		t.Errorf("GaugeFunc fn called %d times across 2 scrapes, want 2", calls)
	}
	for _, want := range []string{
		`test_active_clients{vault="a"} 3`,
		`test_active_clients{vault="b"} 7`,
		"# TYPE test_active_clients gauge",
	} {
		if !strings.Contains(out1, want) {
			t.Errorf("scrape 1 missing %q\n%s", want, out1)
		}
		if !strings.Contains(out2, want) {
			t.Errorf("scrape 2 missing %q\n%s", want, out2)
		}
	}
}

func TestRoundTripParser(t *testing.T) {
	r := NewRegistry()
	r.Counter("x_total", "").Inc()
	cv := r.CounterVec("y_total", "", "k")
	cv.With("a").Add(5)
	cv.With("b").Add(7)
	r.GaugeFunc("z_now", "", nil, func(emit func([]string, int64)) { emit(nil, 42) })

	samples, err := parseProm(render(t, r))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, s := range samples {
		key := s.Name
		if v, ok := s.Labels["k"]; ok {
			key += "{k=" + v + "}"
		}
		got[key] = s.Value
	}
	want := map[string]int64{
		"x_total":        1,
		"y_total{k=a}":  5,
		"y_total{k=b}":  7,
		"z_now":          42,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %d, want %d (got map: %v)", k, got[k], v, got)
		}
	}
}

func render(t *testing.T, r *Registry) string {
	t.Helper()
	var buf bytes.Buffer
	if _, err := r.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	return buf.String()
}

// parsedSample is what the tolerant parser yields. It's intentionally minimal
// — we're checking round-trip correctness, not full Prom feature coverage.
type parsedSample struct {
	Name   string
	Labels map[string]string
	Value  int64
}

// parseProm is a tolerant text-format parser. Handles only the subset this
// package emits: ints, no histograms/summaries, single-line label blocks.
// Skips # HELP / # TYPE / blank lines.
func parseProm(s string) ([]parsedSample, error) {
	var out []parsedSample
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Split into "<name>[{labels}]" and "<value>" on the LAST space, since
		// label values are quoted and may contain spaces.
		sp := strings.LastIndexByte(line, ' ')
		if sp < 0 {
			return nil, errExpect("space-separated value", line)
		}
		left, valStr := line[:sp], line[sp+1:]

		var name string
		labels := map[string]string{}
		if br := strings.IndexByte(left, '{'); br >= 0 {
			name = left[:br]
			if !strings.HasSuffix(left, "}") {
				return nil, errExpect("closing brace", line)
			}
			body := left[br+1 : len(left)-1]
			ls, err := parseLabels(body)
			if err != nil {
				return nil, err
			}
			labels = ls
		} else {
			name = left
		}
		v, err := atoi64(valStr)
		if err != nil {
			return nil, err
		}
		out = append(out, parsedSample{Name: name, Labels: labels, Value: v})
	}
	return out, nil
}

// parseLabels parses `a="x",b="y"` into a map, honoring the three escapes
// the emitter produces: \\, \", \n.
func parseLabels(s string) (map[string]string, error) {
	out := map[string]string{}
	i := 0
	for i < len(s) {
		// Read key up to '='.
		eq := strings.IndexByte(s[i:], '=')
		if eq < 0 {
			return nil, errExpect("`=` in label", s)
		}
		key := s[i : i+eq]
		i += eq + 1
		if i >= len(s) || s[i] != '"' {
			return nil, errExpect(`opening quote`, s)
		}
		i++ // consume opening quote
		var val strings.Builder
		for i < len(s) {
			c := s[i]
			if c == '\\' {
				if i+1 >= len(s) {
					return nil, errExpect("escape char", s)
				}
				switch s[i+1] {
				case '\\':
					val.WriteByte('\\')
				case '"':
					val.WriteByte('"')
				case 'n':
					val.WriteByte('\n')
				default:
					return nil, errExpect("known escape", s[i:i+2])
				}
				i += 2
				continue
			}
			if c == '"' {
				break
			}
			val.WriteByte(c)
			i++
		}
		if i >= len(s) || s[i] != '"' {
			return nil, errExpect("closing quote", s)
		}
		i++ // consume closing quote
		out[key] = val.String()
		if i < len(s) && s[i] == ',' {
			i++
		}
	}
	return out, nil
}

func errExpect(want, got string) error {
	return &parseErr{want: want, got: got}
}

type parseErr struct{ want, got string }

func (e *parseErr) Error() string { return "parse: expected " + e.want + ", got " + e.got }

func atoi64(s string) (int64, error) {
	if s == "" {
		return 0, errExpect("non-empty integer", s)
	}
	neg := false
	i := 0
	if s[0] == '-' {
		neg = true
		i = 1
	} else if s[0] == '+' {
		i = 1
	}
	var v int64
	for ; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, errExpect("digit", string(c))
		}
		v = v*10 + int64(c-'0')
	}
	if neg {
		v = -v
	}
	return v, nil
}
