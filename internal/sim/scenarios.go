package sim

import (
	"math"
	"time"

	"github.com/vilellic/autoheat/internal/control"
)

const h = time.Hour

func constant(v float64) func(time.Duration) float64 { return func(time.Duration) float64 { return v } }

// between returns v from t0 to t1 and 0 otherwise.
func between(t0, t1 time.Duration, v float64) func(time.Duration) float64 {
	return func(t time.Duration) float64 {
		if t >= t0 && t < t1 {
			return v
		}
		return 0
	}
}

func policy(p control.Policy) func(time.Duration) control.Policy {
	return func(time.Duration) control.Policy { return p }
}

var heatOnly = control.Policy{MinSetTemp: 20, MaxSetTemp: 26, MaxFan: control.High}

func base(name string, dur time.Duration) Scenario {
	return Scenario{
		Name:     name,
		Duration: dur,
		House:    DefaultHouse(),
		Params:   control.DefaultParams(),
		RoomTemp: 21.5,
		Pump:     control.PumpState{Mode: control.Heat, SetTemp: 24, Fan: control.Low},
		Target:   constant(21.5),
		Outdoor:  constant(0),
		Policy:   policy(heatOnly),
	}
}

// Scenarios are the standard runs used by the tests and tools/sim.
func Scenarios() []Scenario {
	coldStart := base("cold-start", 12*h)
	coldStart.RoomTemp = 19
	coldStart.Pump = control.PumpState{Mode: control.Off, SetTemp: 20, Fan: control.Low}

	coldSnap := base("cold-snap", 12*h)
	coldSnap.Outdoor = func(t time.Duration) float64 { // 0 °C to −15 °C between 1 h and 3 h
		return -15 * math.Min(1, math.Max(0, (t-1*h).Hours()/2))
	}

	targetRaised := base("target-raised", 10*h)
	targetRaised.Target = func(t time.Duration) float64 {
		if t < 3*h {
			return 21.0
		}
		return 22.5
	}
	targetRaised.RoomTemp = 21.0

	fireplace := base("fireplace", 12*h)
	fireplace.Outdoor = constant(-5)
	fireplace.Pump = control.PumpState{Mode: control.Heat, SetTemp: 25, Fan: control.Low}
	fireplace.Fire = between(2*h, 5*h, 5)
	fireplace.Policy = func(t time.Duration) control.Policy {
		p := heatOnly
		p.CanUseFan = t >= 2*h && t < 6*h // fireplace warm
		return p
	}

	mildDay := base("mild-day", 24*h)
	mildDay.Outdoor = func(t time.Duration) float64 { return 8 + 4*math.Sin((t-9*h).Hours()/24*2*math.Pi) }
	mildDay.Sun = func(t time.Duration) float64 { // up to 2.5 kW between 9 and 17
		if t < 9*h || t > 17*h {
			return 0
		}
		return 2.5 * math.Sin((t-9*h).Hours()/8*math.Pi)
	}
	mildDay.Policy = policy(control.Policy{MinSetTemp: 20, MaxSetTemp: 26, MaxFan: control.Medium, CanSwitchOff: true})

	manual := base("manual-override", 10*h)
	manual.Manual = []Override{{At: 4 * h, State: control.PumpState{Mode: control.Heat, SetTemp: 20, Fan: control.Quiet}}}

	return []Scenario{coldStart, coldSnap, targetRaised, fireplace, mildDay, manual}
}
