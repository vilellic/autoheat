package control

import (
	"math"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestEstimateLinearIrregular(t *testing.T) {
	// 0.6 °C/h rise, sampled at irregular times.
	var samples []Sample
	for _, m := range []int{0, 3, 11, 12, 20, 28} {
		at := t0.Add(time.Duration(m) * time.Minute)
		samples = append(samples, Sample{At: at, Temp: 21 + 0.6*float64(m)/60})
	}
	now := t0.Add(30 * time.Minute)
	e := estimate(samples, now, 10*time.Minute)
	if !near(e.SlopePerHour, 0.6) {
		t.Errorf("slope = %v, want 0.6", e.SlopePerHour)
	}
	if !near(e.Level, 21.3) {
		t.Errorf("level = %v, want 21.3 (line read at now)", e.Level)
	}
	if e.Samples != 6 || !near(e.SpanMinutes, 30) {
		t.Errorf("samples=%d span=%v", e.Samples, e.SpanMinutes)
	}
}

func TestEstimateTooLittleHistory(t *testing.T) {
	few := []Sample{{t0, 21.0}, {t0.Add(5 * time.Minute), 21.4}}
	if e := estimate(few, t0.Add(5*time.Minute), 10*time.Minute); e.Level != 21.4 || e.SlopePerHour != 0 {
		t.Errorf("two samples: %+v, want latest reading and no slope", e)
	}
	short := []Sample{{t0, 21.0}, {t0.Add(2 * time.Minute), 21.1}, {t0.Add(4 * time.Minute), 21.2}}
	if e := estimate(short, t0.Add(4*time.Minute), 10*time.Minute); e.Level != 21.2 || e.SlopePerHour != 0 {
		t.Errorf("short span: %+v, want latest reading and no slope", e)
	}
	if e := estimate(nil, t0, time.Minute); e != (Estimate{}) {
		t.Errorf("no samples: %+v", e)
	}
}

func TestAddSample(t *testing.T) {
	window := 30 * time.Minute
	var s []Sample
	s = addSample(s, Sample{t0, 21.0}, window)
	s = addSample(s, Sample{t0.Add(20 * time.Second), 21.1}, window) // burst: replaces
	if len(s) != 1 || s[0].Temp != 21.1 {
		t.Fatalf("burst: %v, want one sample 21.1", s)
	}
	s = addSample(s, Sample{t0.Add(10 * time.Minute), 21.2}, window)
	s = addSample(s, Sample{t0.Add(35 * time.Minute), 21.3}, window) // first falls out
	if len(s) != 2 || s[0].Temp != 21.2 {
		t.Fatalf("window: %v, want the last two", s)
	}
	s = addSample(s, Sample{t0.Add(2 * time.Hour), 21.4}, window) // long gap keeps only newest
	if len(s) != 1 || s[0].Temp != 21.4 {
		t.Fatalf("gap: %v", s)
	}
	s = addSample(s, Sample{t0, 20.0}, window) // clock went back
	if len(s) != 1 || s[0].Temp != 20.0 {
		t.Fatalf("clock back: %v", s)
	}
}
