package sim

import (
	"testing"
	"time"

	"github.com/vilellic/autoheat/internal/control"
)

// Each scenario runs with several noise seeds and must stay within these
// limits. They leave some margin over the current results, so a change that
// makes control clearly worse fails here.
func TestScenarios(t *testing.T) {
	type limits struct {
		settleAfter    time.Duration // comfort is checked from here on
		maxAbsError    float64       // true room temp − target, once settled
		minError       float64       // never colder than this, once settled
		changesPerHour float64       // over the whole run
		check          func(t *testing.T, r Result)
	}
	want := map[string]limits{
		"cold-start":    {settleAfter: 4 * h, maxAbsError: 0.4, minError: -0.4, changesPerHour: 2},
		"cold-snap":     {settleAfter: 0, maxAbsError: 0.45, minError: -0.45, changesPerHour: 1.8},
		"target-raised": {settleAfter: 5 * h, maxAbsError: 0.45, minError: -0.45, changesPerHour: 2.5},
		"fireplace": {settleAfter: 6*h + 30*time.Minute, maxAbsError: 1.5, minError: -0.6, changesPerHour: 3.2,
			check: func(t *testing.T, r Result) {
				if d := r.ModeTime(control.FanOnly, 0); d < 2*h {
					t.Errorf("fan only for %s, want it used while the fire burns", d)
				}
			}},
		"mild-day": {settleAfter: 0, maxAbsError: 2.5, minError: -0.4, changesPerHour: 1.6,
			check: func(t *testing.T, r Result) {
				if d := r.ModeTime(control.Off, 0); d < 4*h {
					t.Errorf("off for %s, want it off while the sun heats", d)
				}
			}},
		"manual-override": {settleAfter: 6 * h, maxAbsError: 0.4, minError: -0.4, changesPerHour: 3.3},
	}

	for _, s := range Scenarios() {
		lim, ok := want[s.Name]
		if !ok {
			t.Errorf("scenario %s has no limits", s.Name)
			continue
		}
		t.Run(s.Name, func(t *testing.T) {
			for seed := range uint64(5) {
				r := Run(s, seed)
				settled := r.After(lim.settleAfter)
				if settled.MaxAbsError > lim.maxAbsError || settled.MinError < lim.minError {
					t.Errorf("seed %d: error %+.2f..%+.2f after %s, want within ±%.2f and above %+.2f",
						seed, settled.MinError, settled.MaxError, lim.settleAfter, lim.maxAbsError, lim.minError)
				}
				if c := r.After(0).ChangesPerHour; c > lim.changesPerHour {
					t.Errorf("seed %d: %.2f changes/h, want at most %.2f", seed, c, lim.changesPerHour)
				}
				if lim.check != nil {
					lim.check(t, r)
				}
			}
		})
	}
}

func TestPumpModel(t *testing.T) {
	h := DefaultHouse()
	// With no heating the room cools towards outdoors.
	temp, _ := h.step(21, 0, control.PumpState{Mode: control.Off}, 0, 0, 0)
	if temp >= 21 {
		t.Errorf("off: %v, want cooling", temp)
	}
	// The unit's own sensor reads warm, so a set temp at room temperature
	// gives no heat...
	_, kw := h.step(21, 0, control.PumpState{Mode: control.Heat, SetTemp: 21, Fan: control.Low}, 0, 0, 0)
	if kw != 0 {
		t.Errorf("set temp 21 in a 21 °C room: %v kW, want 0", kw)
	}
	// ...while a high set temp does, more at higher fan.
	_, low := h.step(21, 0, control.PumpState{Mode: control.Heat, SetTemp: 26, Fan: control.Low}, 0, 0, 0)
	_, high := h.step(21, 0, control.PumpState{Mode: control.Heat, SetTemp: 26, Fan: control.High}, 0, 0, 0)
	if !(0 < low && low < high) {
		t.Errorf("heat output low fan %v, high fan %v", low, high)
	}
}
