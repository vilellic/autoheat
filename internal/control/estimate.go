package control

import "time"

// Sample is one room temperature reading.
type Sample struct {
	At   time.Time
	Temp float64
}

// Estimate is the smoothed room temperature now and its trend.
type Estimate struct {
	Level        float64 `json:"level"`        // °C, smoothed temperature now
	SlopePerHour float64 `json:"slopePerHour"` // °C per hour
	Samples      int     `json:"samples"`
	SpanMinutes  float64 `json:"spanMinutes"` // time from the oldest sample to now
}

// sampleSpacing is the minimum time between kept samples. A burst of calls
// replaces the newest sample instead of piling up, so calling more often
// does not give a moment more weight.
const sampleSpacing = time.Minute

// addSample records a reading and drops samples older than the window.
func addSample(samples []Sample, s Sample, window time.Duration) []Sample {
	if n := len(samples); n > 0 && s.At.Before(samples[n-1].At) {
		samples = samples[:0] // clock went backwards: start over
	}
	if n := len(samples); n > 0 && s.At.Sub(samples[n-1].At) < sampleSpacing {
		samples[n-1] = s
	} else {
		samples = append(samples, s)
	}
	cut := 0
	for cut < len(samples)-1 && s.At.Sub(samples[cut].At) > window {
		cut++
	}
	return samples[cut:]
}

// estimate fits a straight line through the samples (least squares over
// time, so irregular timing is fine) and reads it at now. With too little
// history the trend is unknown: it returns the latest reading with slope 0.
func estimate(samples []Sample, now time.Time, minSpan time.Duration) Estimate {
	n := len(samples)
	if n == 0 {
		return Estimate{}
	}
	e := Estimate{
		Level:       samples[n-1].Temp,
		Samples:     n,
		SpanMinutes: now.Sub(samples[0].At).Minutes(),
	}
	if n < 3 || samples[n-1].At.Sub(samples[0].At) < minSpan {
		return e
	}
	// x = hours relative to now (≤ 0), so the intercept is the level now.
	var sx, sy, sxx, sxy float64
	for _, s := range samples {
		x := s.At.Sub(now).Hours()
		sx += x
		sy += s.Temp
		sxx += x * x
		sxy += x * s.Temp
	}
	fn := float64(n)
	den := fn*sxx - sx*sx
	if den == 0 {
		return e
	}
	e.SlopePerHour = (fn*sxy - sx*sy) / den
	e.Level = (sy - e.SlopePerHour*sx) / fn
	return e
}
