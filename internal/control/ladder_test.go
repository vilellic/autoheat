package control

import "testing"

func heat(t int, f Fan) PumpState { return PumpState{Mode: Heat, SetTemp: t, Fan: f} }

var testPolicy = Policy{MinSetTemp: 20, MaxSetTemp: 26, MaxFan: High, CanUseFan: true, CanSwitchOff: true}

func TestBuildLadderOrder(t *testing.T) {
	l := BuildLadder(testPolicy, DefaultParams())
	want := []PumpState{
		{Mode: FanOnly, SetTemp: 22, Fan: Medium}, // idle
		heat(20, Low), heat(21, Low), heat(22, Low), heat(23, Low), heat(24, Low), heat(25, Low), heat(26, Low),
		heat(26, Medium), heat(26, MediumHigh), heat(26, High),
	}
	if l.Top() != len(want)-1 {
		t.Fatalf("Top = %d, want %d", l.Top(), len(want)-1)
	}
	for pos, w := range want {
		if got := l.Level(pos, PumpState{SetTemp: 22}); got != w {
			t.Errorf("Level(%d) = %v, want %v", pos, got, w)
		}
	}
}

func TestBuildLadderIdleChoice(t *testing.T) {
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
		if c.hasIdle && l.Level(0, PumpState{}).Mode != c.idle {
			t.Errorf("%+v: idle mode = %v", c, l.Level(0, PumpState{}).Mode)
		}
		if !c.hasIdle && l.Level(0, PumpState{}) != heat(20, Low) {
			t.Errorf("%+v: bottom = %v, want heat/20/low", c, l.Level(0, PumpState{}))
		}
	}
}

func TestBuildLadderMaxFanBelowBase(t *testing.T) {
	pol := testPolicy
	pol.MaxFan = Quiet
	l := BuildLadder(pol, DefaultParams())
	if got := l.Level(l.Top(), PumpState{}); got != heat(26, Quiet) {
		t.Errorf("top = %v, want heat/26/quiet", got)
	}
	if got := l.Level(0, PumpState{}); got.Fan != Quiet {
		t.Errorf("circulation fan = %v, want capped to quiet", got.Fan)
	}
}

func TestLocate(t *testing.T) {
	l := BuildLadder(testPolicy, DefaultParams())
	cases := []struct {
		s    PumpState
		want int
	}{
		{PumpState{Mode: FanOnly, Fan: Medium}, 0},
		{PumpState{Mode: Off}, 0},
		{heat(20, Low), 1},
		{heat(24, Medium), 5}, // off-ladder fan maps by set temp
		{heat(26, Quiet), 7},  // below base at max maps to base
		{heat(26, MediumHigh), 9},
	}
	for _, c := range cases {
		if got := l.Locate(c.s); got != c.want {
			t.Errorf("Locate(%v) = %d, want %d", c.s, got, c.want)
		}
	}
}

func TestResumeIsClamped(t *testing.T) {
	for _, c := range []struct{ resume, want int }{{23, 23}, {30, 26}, {10, 20}} {
		p := DefaultParams()
		p.ResumeSetTemp = c.resume
		l := BuildLadder(testPolicy, p)
		if got := l.Level(l.Resume(), PumpState{}); got != heat(c.want, Low) {
			t.Errorf("resume %d: got %v, want heat/%d/low", c.resume, got, c.want)
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

func TestMoveKeepsFanDirection(t *testing.T) {
	l := BuildLadder(testPolicy, DefaultParams())
	s := heat(24, Medium) // off the ladder: fan above base below max
	pos := l.Locate(s)
	if got := l.Move(s, pos, pos+1); got != heat(25, Medium) {
		t.Errorf("step up = %v, want heat/25/medium (fan not lowered)", got)
	}
	if got := l.Move(s, pos, pos-1); got != heat(23, Low) {
		t.Errorf("step down = %v, want heat/23/low", got)
	}
	q := heat(24, Quiet)
	if got := l.Move(q, pos, pos-1); got != heat(23, Quiet) {
		t.Errorf("step down from quiet = %v, want heat/23/quiet (fan not raised)", got)
	}
}
