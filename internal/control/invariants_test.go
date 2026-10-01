package control

import (
	"math/rand/v2"
	"testing"
	"time"
)

// TestInvariants drives a room with random policies, manual changes, and
// room temperatures, and checks every decision against the rules that must
// always hold.
func TestInvariants(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	p := DefaultParams()
	randPolicy := func() Policy {
		lo := 18 + rng.IntN(5)
		return Policy{
			MinSetTemp:   lo,
			MaxSetTemp:   lo + rng.IntN(8),
			MaxFan:       Fans[rng.IntN(len(Fans))],
			CanUseFan:    rng.IntN(2) == 0,
			CanSwitchOff: rng.IntN(2) == 0,
		}
	}
	randState := func() PumpState {
		return PumpState{Mode: Mode(rng.IntN(3)), SetTemp: 15 + rng.IntN(15), Fan: Fans[rng.IntN(len(Fans))]}
	}

	for run := range 200 {
		var r Room
		now := t0
		var lastChange time.Time
		in := Input{Target: 21.5, RoomTemp: 21.5, Observed: randState(), Policy: randPolicy()}

		for step := range 300 {
			now = now.Add(time.Duration(10+rng.IntN(600)) * time.Second)
			in.RoomTemp += (rng.Float64() - 0.5) * 0.2
			switch rng.IntN(40) {
			case 0:
				in.Policy = randPolicy()
			case 1:
				in.Observed = randState() // changed by hand
			case 2:
				in.Target = 19 + rng.Float64()*5
			}

			start, fixed := BuildLadder(in.Policy, p).Fix(in.Observed)
			external := r.hasPrevObs && !in.Observed.Same(r.prevObs) &&
				!(r.hasCommand && in.Observed.Same(r.lastCommand))
			if external {
				lastChange = now
			}

			d := r.Decide(in, p, now)
			c, pol := d.Command, in.Policy
			fail := func(msg string) {
				t.Fatalf("run %d step %d: %s\npolicy %+v\nobserved %v -> command %v (%s)", run, step, msg, pol, in.Observed, c, d.Reason)
			}

			switch c.Mode {
			case Heat:
				if c.SetTemp < pol.MinSetTemp || c.SetTemp > pol.MaxSetTemp {
					fail("set temp outside policy")
				}
				if c.Fan > pol.MaxFan {
					fail("fan above max")
				}
			case FanOnly:
				if !pol.CanUseFan {
					fail("fan only not allowed")
				}
				if c.Fan > pol.MaxFan {
					fail("fan above max")
				}
			case Off:
				if !pol.CanSwitchOff {
					fail("off not allowed")
				}
				if pol.CanUseFan && in.Observed.Mode != Off {
					fail("went idle to off although fan only is allowed")
				}
			}

			if d.Changed != !c.Same(in.Observed) {
				fail("Changed flag wrong")
			}
			if moved := !c.Same(start); moved {
				dir := Direction(start, c)
				if start.Mode == Heat && c.Mode == Heat &&
					abs(c.SetTemp-start.SetTemp)+abs(int(c.Fan-start.Fan)) != 1 {
					fail("heat step must change one of set temp and fan by one notch")
				}
				switch {
				case dir == 0:
					fail("step neither up nor down")
				case dir > 0 && d.Predicted >= -p.ColdBand:
					fail("stepped up although not too cold")
				case dir < 0 && d.Predicted <= p.WarmBand:
					fail("stepped down although not too warm")
				}
				if !lastChange.IsZero() && now.Sub(lastChange) < p.UrgentDwell {
					fail("stepped within dwell")
				}
			}
			if fixed == "" && !d.Changed && c != in.Observed {
				fail("hold must return the observed state exactly")
			}
			if d.Changed {
				lastChange = now
			}
			in.Observed = c // Home Assistant applies the command
		}
	}
}

func abs(v int) int { return max(v, -v) }
