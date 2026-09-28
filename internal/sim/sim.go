// Package sim is a simple room model for tuning and scenario tests.
//
// The room is one thermal mass losing heat to the outdoors. The heat pump
// runs its own thermostat against a sensor at the indoor unit, which reads
// warmer than the room, and more so at low fan speeds. That is why the set
// temperature acts as a power lever rather than a room target.
package sim

import (
	"math"
	"math/rand/v2"
	"time"

	"github.com/vilellic/autoheat/internal/control"
)

// House holds the physical constants of the model.
type House struct {
	Capacity       float64 // kWh per °C
	Loss           float64 // kW per °C of indoor−outdoor difference
	PumpGain       float64 // kW per °C between set temp and the unit's reading
	PumpLag        time.Duration
	FireShareStill float64 // share of fireplace heat reaching the room with no fan running
}

var (
	unitBias = [...]float64{2.5, 2.0, 1.5, 1.2, 1.0} // unit sensor above room, by fan
	fanMaxKW = [...]float64{1.5, 2.5, 3.5, 4.5, 5.5} // max heat output, by fan
)

func DefaultHouse() House {
	return House{Capacity: 2, Loss: 0.12, PumpGain: 2, PumpLag: 10 * time.Minute, FireShareStill: 0.5}
}

// Scenario is one simulated run. Functions get the time since the start.
type Scenario struct {
	Name     string
	Duration time.Duration
	House    House
	Params   control.Params
	RoomTemp float64
	Pump     control.PumpState
	Target   func(time.Duration) float64
	Outdoor  func(time.Duration) float64
	Fire     func(time.Duration) float64 // fireplace output, kW
	Sun      func(time.Duration) float64 // solar gain, kW
	Policy   func(time.Duration) control.Policy
	Manual   []Override // changes made by hand
}

type Override struct {
	At    time.Duration
	State control.PumpState
}

// Point is one call to the controller.
type Point struct {
	At       time.Duration
	RoomTemp float64 // true room temperature
	Sensor   float64 // what Home Assistant reported
	Target   float64
	Decision control.Decision
}

type Result struct {
	Points []Point
}

const (
	step         = 10 * time.Second
	callInterval = 5 * time.Minute // the automation's periodic trigger
	sensorStep   = 0.1             // sensor resolution, °C
	sensorNoise  = 0.03            // ± °C before rounding
)

var start = time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)

// Run simulates a scenario. Like the Home Assistant automation, it calls the
// controller when the sensor value changes, when the target or policy
// changes, and every 5 minutes, and applies each command at once.
func Run(s Scenario, seed uint64) Result {
	rng := rand.New(rand.NewPCG(seed, 0))
	var room control.Room
	var res Result
	temp, pumpKW, pump := s.RoomTemp, 0.0, s.Pump
	lastCall := -callInterval
	var lastSensor, lastTarget float64
	var lastPolicy control.Policy
	manual := s.Manual

	for t := time.Duration(0); t <= s.Duration; t += step {
		for len(manual) > 0 && manual[0].At <= t {
			pump, manual = manual[0].State, manual[1:]
		}

		temp, pumpKW = s.House.step(temp, pumpKW, pump, s.Outdoor(t), orZero(s.Fire, t), orZero(s.Sun, t))
		sensor := math.Round((temp+(rng.Float64()*2-1)*sensorNoise)/sensorStep) * sensorStep
		target, pol := s.Target(t), s.Policy(t)

		if t-lastCall >= callInterval || sensor != lastSensor || target != lastTarget || pol != lastPolicy {
			in := control.Input{Target: target, RoomTemp: sensor, Observed: pump, Policy: pol}
			d := room.Decide(in, s.Params, start.Add(t))
			pump = d.Command
			res.Points = append(res.Points, Point{At: t, RoomTemp: temp, Sensor: sensor, Target: target, Decision: d})
			lastCall, lastSensor, lastTarget, lastPolicy = t, sensor, target, pol
		}
	}
	return res
}

func (h House) step(temp, pumpKW float64, pump control.PumpState, outdoor, fire, sun float64) (float64, float64) {
	want := 0.0
	if pump.Mode == control.Heat {
		unit := temp + unitBias[pump.Fan]
		want = math.Max(0, math.Min(h.PumpGain*(float64(pump.SetTemp)-unit), fanMaxKW[pump.Fan]))
	}
	pumpKW += (want - pumpKW) * math.Min(1, step.Seconds()/h.PumpLag.Seconds())

	share := 1.0
	if pump.Mode == control.Off {
		share = h.FireShareStill
	}
	heat := pumpKW + share*fire + sun - h.Loss*(temp-outdoor)
	return temp + heat/h.Capacity*step.Hours(), pumpKW
}

func orZero(f func(time.Duration) float64, t time.Duration) float64 {
	if f == nil {
		return 0
	}
	return f(t)
}

// Stats summarise a result from a given time on, using the true room
// temperature.
type Stats struct {
	MaxAbsError    float64
	RMSError       float64
	MinError       float64
	MaxError       float64
	ChangesPerHour float64
}

func (r Result) After(from time.Duration) Stats {
	st := Stats{MinError: math.Inf(1), MaxError: math.Inf(-1)}
	var sum2 float64
	var n, changes int
	var first, last time.Duration = -1, 0
	for _, p := range r.Points {
		if p.At < from {
			continue
		}
		if first < 0 {
			first = p.At
		}
		last = p.At
		e := p.RoomTemp - p.Target
		st.MinError, st.MaxError = math.Min(st.MinError, e), math.Max(st.MaxError, e)
		sum2 += e * e
		n++
		if p.Decision.Changed {
			changes++
		}
	}
	if n == 0 {
		return Stats{}
	}
	st.MaxAbsError = math.Max(-st.MinError, st.MaxError)
	st.RMSError = math.Sqrt(sum2 / float64(n))
	if hours := (last - first).Hours(); hours > 0 {
		st.ChangesPerHour = float64(changes) / hours
	}
	return st
}

// ModeTime returns how long the pump spent in a mode from a given time on.
func (r Result) ModeTime(m control.Mode, from time.Duration) time.Duration {
	var total time.Duration
	for i, p := range r.Points {
		if p.At < from || i+1 == len(r.Points) {
			continue
		}
		if p.Decision.Command.Mode == m {
			total += r.Points[i+1].At - p.At
		}
	}
	return total
}
