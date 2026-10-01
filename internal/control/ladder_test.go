package control

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
)

func heat(t int, f Fan) PumpState { return PumpState{Mode: Heat, SetTemp: t, Fan: f} }

var testPolicy = Policy{MinSetTemp: 20, MaxSetTemp: 26, MaxFan: High, CanUseFan: true, CanSwitchOff: true}

// walk steps from s until step fails and returns the states passed through.
func walk(s PumpState, step func(PumpState) (PumpState, bool)) []PumpState {
	path := []PumpState{s}
	for next, ok := step(s); ok; next, ok = step(s) {
		s = next
		path = append(path, s)
	}
	return path
}

func TestLadderPaths(t *testing.T) {
	idle := PumpState{Mode: FanOnly, SetTemp: 20, Fan: Medium}
	medium := testPolicy
	medium.MaxFan = Medium
	narrow := testPolicy
	narrow.MinSetTemp, narrow.MaxSetTemp = 24, 25
	quiet := testPolicy
	quiet.MaxFan = Quiet
	cases := []struct {
		name     string
		pol      Policy
		curve    FanCurve
		base     Fan
		up, down []PumpState // from the bottom heat state, and from the top
	}{
		{"default curve", testPolicy, DefaultParams().FanFrom, Low,
			[]PumpState{heat(20, Low), heat(21, Low), heat(22, Low), heat(23, Low), heat(23, Medium), heat(24, Medium),
				heat(25, Medium), heat(26, Medium), heat(26, MediumHigh), heat(26, High)},
			[]PumpState{heat(26, High), heat(26, MediumHigh), heat(26, Medium), heat(26, Low), heat(25, Low),
				heat(24, Low), heat(23, Low), heat(22, Low), heat(21, Low), heat(20, Low), idle}},
		{"no curve: fan only at max", testPolicy, FanCurve{}, Low,
			[]PumpState{heat(20, Low), heat(21, Low), heat(22, Low), heat(23, Low), heat(24, Low), heat(25, Low),
				heat(26, Low), heat(26, Medium), heat(26, MediumHigh), heat(26, High)},
			nil},
		{"max fan caps the fan, not the set temp", medium, FanCurve{Medium: 23, MediumHigh: 25}, Low,
			[]PumpState{heat(20, Low), heat(21, Low), heat(22, Low), heat(23, Low), heat(23, Medium), heat(24, Medium),
				heat(25, Medium), heat(26, Medium)},
			nil},
		{"two fans at one set temp", testPolicy, FanCurve{Medium: 24, MediumHigh: 24, High: 25}, Low,
			[]PumpState{heat(20, Low), heat(21, Low), heat(22, Low), heat(23, Low), heat(24, Low), heat(24, Medium),
				heat(24, MediumHigh), heat(25, MediumHigh), heat(25, High), heat(26, High)},
			nil},
		{"curve outside the policy moves to its edges", narrow, FanCurve{Medium: 20, MediumHigh: 30}, Low,
			[]PumpState{heat(24, Low), heat(24, Medium), heat(25, Medium), heat(25, MediumHigh), heat(25, High)},
			[]PumpState{heat(25, High), heat(25, MediumHigh), heat(25, Medium), heat(25, Low), heat(24, Low),
				{Mode: FanOnly, SetTemp: 24, Fan: Medium}}},
		{"unset speed starts with the next given one", testPolicy, FanCurve{Medium: 23}, Quiet,
			[]PumpState{heat(20, Quiet), heat(21, Quiet), heat(22, Quiet), heat(23, Quiet), heat(23, Low), heat(23, Medium),
				heat(24, Medium), heat(25, Medium), heat(26, Medium), heat(26, MediumHigh), heat(26, High)},
			nil},
		{"speeds at or below base are ignored", testPolicy, FanCurve{Low: 21, Medium: 23}, Medium,
			[]PumpState{heat(20, Medium), heat(21, Medium), heat(22, Medium), heat(23, Medium), heat(24, Medium),
				heat(25, Medium), heat(26, Medium), heat(26, MediumHigh), heat(26, High)},
			nil},
		{"max fan below base", quiet, DefaultParams().FanFrom, Low,
			[]PumpState{heat(20, Quiet), heat(21, Quiet), heat(22, Quiet), heat(23, Quiet), heat(24, Quiet),
				heat(25, Quiet), heat(26, Quiet)},
			nil},
	}
	for _, c := range cases {
		p := DefaultParams()
		p.FanFrom, p.BaseFan = c.curve, c.base
		l := BuildLadder(c.pol, p)
		if got := walk(c.up[0], l.Up); !slices.Equal(got, c.up) {
			t.Errorf("%s: up\n got %v\nwant %v", c.name, got, c.up)
		}
		if c.down != nil {
			if got := walk(c.down[0], l.Down); !slices.Equal(got, c.down) {
				t.Errorf("%s: down\n got %v\nwant %v", c.name, got, c.down)
			}
		}
	}
}

func TestIdleChoice(t *testing.T) {
	cases := []struct {
		useFan, switchOff bool
		hasIdle           bool
		idle              Mode
	}{
		{true, true, true, FanOnly},
		{true, false, true, FanOnly},
		{false, true, true, Off},
		{false, false, false, 0},
	}
	for _, c := range cases {
		pol := testPolicy
		pol.CanUseFan, pol.CanSwitchOff = c.useFan, c.switchOff
		l := BuildLadder(pol, DefaultParams())
		if l.HasIdle() != c.hasIdle {
			t.Errorf("%+v: HasIdle = %v", c, l.HasIdle())
			continue
		}
		got, ok := l.Down(heat(20, Low))
		switch {
		case c.hasIdle && (!ok || got.Mode != c.idle):
			t.Errorf("%+v: down from the bottom = %v %v, want %v", c, got, ok, c.idle)
		case !c.hasIdle && ok:
			t.Errorf("%+v: down from the bottom = %v, want none", c, got)
		}
	}
	l := BuildLadder(testPolicy, DefaultParams())
	if s, ok := l.Down(l.Idle(heat(20, Low))); ok {
		t.Errorf("down from idle = %v, want none", s)
	}
	if s, ok := l.Up(PumpState{Mode: Off, SetTemp: 20}); !ok || s != l.Resume() {
		t.Errorf("up from idle = %v %v, want resume %v", s, ok, l.Resume())
	}
}

func TestOffCurveStates(t *testing.T) {
	l := BuildLadder(testPolicy, DefaultParams())
	cases := []struct {
		s, up, down PumpState
	}{
		{heat(21, Medium), heat(22, Medium), heat(21, Low)}, // fan above the ceiling, set by hand
		{heat(24, Quiet), heat(24, Low), heat(23, Quiet)},   // fan below base: down never raises it
		{heat(26, Quiet), heat(26, Low), heat(25, Quiet)},
		{heat(23, MediumHigh), heat(24, MediumHigh), heat(23, Medium)},
	}
	for _, c := range cases {
		if got, _ := l.Up(c.s); got != c.up {
			t.Errorf("up from %v = %v, want %v", c.s, got, c.up)
		}
		if got, _ := l.Down(c.s); got != c.down {
			t.Errorf("down from %v = %v, want %v", c.s, got, c.down)
		}
	}
}

// TestStepsAreOneNotch checks random policies, curves and states: every step
// changes one of set temperature and fan by one notch in its direction, and
// walking up always ends at max set temp and max fan.
func TestStepsAreOneNotch(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for range 1000 {
		lo := 18 + rng.IntN(5)
		pol := Policy{MinSetTemp: lo, MaxSetTemp: lo + rng.IntN(8), MaxFan: Fans[rng.IntN(len(Fans))],
			CanUseFan: rng.IntN(2) == 0, CanSwitchOff: rng.IntN(2) == 0}
		p := DefaultParams()
		p.BaseFan = Fans[rng.IntN(len(Fans))]
		p.FanFrom = FanCurve{}
		at := 16
		for f := range p.FanFrom {
			if rng.IntN(3) > 0 {
				at += rng.IntN(4)
				p.FanFrom[f] = at
			}
		}
		l := BuildLadder(pol, p)
		s := heat(pol.MinSetTemp+rng.IntN(pol.MaxSetTemp-pol.MinSetTemp+1), Fans[rng.IntN(int(pol.MaxFan)+1)])
		fail := func(msg string) {
			t.Fatalf("%s\npolicy %+v base %s curve %v from %v", msg, pol, p.BaseFan, p.FanFrom, s)
		}
		check := func(a, b PumpState, dir int) {
			if fixed, why := l.Fix(b); why != "" || fixed != b {
				fail(fmt.Sprintf("step %v -> %v leaves the policy", a, b))
			}
			if Direction(a, b) != dir {
				fail(fmt.Sprintf("step %v -> %v goes the wrong way", a, b))
			}
			if b.Mode == Heat && a.Mode == Heat && abs(b.SetTemp-a.SetTemp)+abs(int(b.Fan-a.Fan)) != 1 {
				fail(fmt.Sprintf("step %v -> %v is not one notch", a, b))
			}
		}
		up := walk(s, l.Up)
		for i := 1; i < len(up); i++ {
			check(up[i-1], up[i], 1)
		}
		if top := up[len(up)-1]; top != heat(pol.MaxSetTemp, pol.MaxFan) {
			fail(fmt.Sprintf("walking up ends at %v", top))
		}
		down := walk(s, l.Down)
		for i := 1; i < len(down); i++ {
			check(down[i-1], down[i], -1)
		}
		if bottom := down[len(down)-1]; bottom.Mode == Heat && (l.HasIdle() || bottom.SetTemp != pol.MinSetTemp) {
			fail(fmt.Sprintf("walking down ends at %v", bottom))
		}
	}
}

func TestResumeIsClamped(t *testing.T) {
	for _, c := range []struct{ resume, want int }{{23, 23}, {30, 26}, {10, 20}} {
		p := DefaultParams()
		p.ResumeSetTemp = c.resume
		if got := BuildLadder(testPolicy, p).Resume(); got != heat(c.want, Low) {
			t.Errorf("resume %d: got %v, want heat/%d/low", c.resume, got, c.want)
		}
	}
}

func TestDirection(t *testing.T) {
	off := PumpState{Mode: Off}
	cases := []struct {
		a, b PumpState
		want int
	}{
		{heat(23, Low), heat(23, Low), 0},
		{heat(23, Low), heat(24, Low), 1},
		{heat(23, Low), heat(23, Medium), 1},
		{heat(23, Low), heat(25, High), 1},
		{heat(24, Medium), heat(23, Low), -1},
		{heat(24, Low), heat(23, Medium), 0},
		{off, heat(20, Quiet), 1},
		{heat(20, Quiet), off, -1},
		{off, PumpState{Mode: FanOnly}, 0},
	}
	for _, c := range cases {
		if got := Direction(c.a, c.b); got != c.want {
			t.Errorf("Direction(%v, %v) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestFix(t *testing.T) {
	noIdle := testPolicy
	noIdle.CanUseFan, noIdle.CanSwitchOff = false, false
	offOnly := testPolicy
	offOnly.CanUseFan = false
	lowFan := testPolicy
	lowFan.MaxFan = Medium
	fanNoOff := testPolicy
	fanNoOff.CanSwitchOff = false

	cases := []struct {
		name    string
		pol     Policy
		in      PumpState
		want    PumpState
		changed bool
	}{
		{"valid heat", testPolicy, heat(24, Medium), heat(24, Medium), false},
		{"set temp above max", testPolicy, heat(28, Low), heat(26, Low), true},
		{"set temp below min", testPolicy, heat(17, Low), heat(20, Low), true},
		{"fan above max", lowFan, heat(24, High), heat(24, Medium), true},
		{"fan only allowed", testPolicy, PumpState{Mode: FanOnly, SetTemp: 22, Fan: MediumHigh}, PumpState{Mode: FanOnly, SetTemp: 22, Fan: MediumHigh}, false},
		{"fan only fan above max", lowFan, PumpState{Mode: FanOnly, SetTemp: 22, Fan: High}, PumpState{Mode: FanOnly, SetTemp: 22, Fan: Medium}, true},
		{"fan only not allowed, off is", offOnly, PumpState{Mode: FanOnly, SetTemp: 22, Fan: Medium}, PumpState{Mode: Off, SetTemp: 22, Fan: Medium}, true},
		{"fan only not allowed, no idle", noIdle, PumpState{Mode: FanOnly, SetTemp: 22, Fan: Medium}, heat(23, Low), true},
		{"off stays off when fan only is also allowed", testPolicy, PumpState{Mode: Off, SetTemp: 22, Fan: Low}, PumpState{Mode: Off, SetTemp: 22, Fan: Low}, false},
		{"off not allowed, fan only is", fanNoOff, PumpState{Mode: Off, SetTemp: 22, Fan: Low}, PumpState{Mode: FanOnly, SetTemp: 22, Fan: Medium}, true},
		{"off allowed", offOnly, PumpState{Mode: Off, SetTemp: 22, Fan: Low}, PumpState{Mode: Off, SetTemp: 22, Fan: Low}, false},
		{"off not allowed", noIdle, PumpState{Mode: Off, SetTemp: 22, Fan: Low}, heat(23, Low), true},
	}
	for _, c := range cases {
		got, why := BuildLadder(c.pol, DefaultParams()).Fix(c.in)
		if got != c.want || (why != "") != c.changed {
			t.Errorf("%s: Fix(%v) = %v %q, want %v changed=%v", c.name, c.in, got, why, c.want, c.changed)
		}
	}
}

func TestFanCurveValidate(t *testing.T) {
	cases := []struct {
		c  FanCurve
		ok bool
	}{
		{FanCurve{}, true},
		{FanCurve{Medium: 23, MediumHigh: 25}, true},
		{FanCurve{Medium: 23, MediumHigh: 23}, true},
		{FanCurve{Low: 21, High: 26}, true},
		{FanCurve{Medium: 25, MediumHigh: 23}, false},
		{FanCurve{Low: 24, High: 22}, false},
		{FanCurve{Medium: -1}, false},
	}
	for _, c := range cases {
		if err := c.c.Validate(); (err == nil) != c.ok {
			t.Errorf("%v: err %v, want ok=%v", c.c, err, c.ok)
		}
	}
}
