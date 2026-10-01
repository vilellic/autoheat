package control

import "fmt"

// Ladder is how heating steps up and down within a policy. Heat states are
// set temperature × fan; below them is idle (fan only or off, when allowed).
//
// The fan curve gives a fan ceiling for each set temperature, for example:
//
//	fan \ set    20   21   22   23   24   25   26
//	high                                       ●
//	medium_high                                ●
//	medium                     ●    ●    ●    ●
//	low          ●    ●    ●    ●    ●    ●    ●
//
// A step up raises the fan while it is below the ceiling, and otherwise the
// set temperature. A step down lowers the fan while it is above the base
// fan, and otherwise the set temperature, then goes idle. So heating climbs
// along the ceiling (23/low → 23/medium → 24/medium) and comes down along
// the base fan (24/medium → 24/low → 23/low): the fan is the first thing
// removed when there is too much heat.
//
// Every step changes one of set temperature and fan by one notch, so every
// step changes the heat the same way. States off the curve, such as a fan
// set by hand, follow the same rules. Every state a step returns is inside
// the policy.
type Ladder struct {
	policy  Policy
	hasIdle bool
	idle    Mode // FanOnly or Off, when hasIdle
	baseFan Fan
	circFan Fan
	curve   FanCurve
	resume  int // set temperature heating restarts from after idle
}

func BuildLadder(pol Policy, p Params) Ladder {
	l := Ladder{
		policy:  pol,
		baseFan: min(p.BaseFan, pol.MaxFan),
		circFan: min(p.CirculationFan, pol.MaxFan),
		curve:   p.FanFrom,
		resume:  clamp(p.ResumeSetTemp, pol.MinSetTemp, pol.MaxSetTemp),
	}
	switch {
	case pol.CanUseFan:
		l.hasIdle, l.idle = true, FanOnly
	case pol.CanSwitchOff:
		l.hasIdle, l.idle = true, Off
	}
	return l
}

func (l Ladder) HasIdle() bool { return l.hasIdle }

// Ceiling returns the highest fan a step up uses at set temperature t: the
// fastest speed the curve reaches by t, at least the base fan and at most
// the max fan. At the max set temperature it is the max fan.
func (l Ladder) Ceiling(t int) Fan {
	f := l.baseFan
	for f < l.policy.MaxFan && l.curve.From(f+1, l.policy.MaxSetTemp) <= t {
		f++
	}
	return f
}

// Up returns the state one step up from s, or false at the top (max set
// temperature at max fan). From idle it is the resume state. s must be
// valid for the policy (see Fix).
func (l Ladder) Up(s PumpState) (PumpState, bool) {
	switch {
	case s.Mode != Heat:
		return l.Resume(), true
	case s.Fan < l.Ceiling(s.SetTemp):
		s.Fan++
	case s.SetTemp < l.policy.MaxSetTemp:
		s.SetTemp++
	default:
		return s, false
	}
	return s, true
}

// Down returns the state one step down from s, or false at the bottom: idle,
// or min set temperature at the base fan when idle is not allowed. s must be
// valid for the policy (see Fix).
func (l Ladder) Down(s PumpState) (PumpState, bool) {
	switch {
	case s.Mode != Heat:
		return s, false
	case s.Fan > l.baseFan:
		s.Fan--
	case s.SetTemp > l.policy.MinSetTemp:
		s.SetTemp--
	case l.hasIdle:
		return l.Idle(s), true
	default:
		return s, false
	}
	return s, true
}

// Resume is the state heating restarts from after idle: the resume set
// temperature (clamped to the policy) at the base fan.
func (l Ladder) Resume() PumpState {
	return PumpState{Mode: Heat, SetTemp: l.resume, Fan: l.baseFan}
}

// Idle returns the idle command, fan only or off; HasIdle must be true.
// Fields Home Assistant ignores (set temperature when idle, fan when off)
// are copied from current.
func (l Ladder) Idle(current PumpState) PumpState {
	if l.idle == FanOnly {
		return PumpState{Mode: FanOnly, SetTemp: current.SetTemp, Fan: l.circFan}
	}
	return PumpState{Mode: Off, SetTemp: current.SetTemp, Fan: current.Fan}
}

// Direction reports how heating changes from a to b: +1 if b is a step or
// more up (out of idle, or one of set temperature and fan higher and neither
// lower), -1 if down, and 0 if the same or mixed.
func Direction(a, b PumpState) int {
	switch {
	case a.Mode != Heat && b.Mode != Heat:
		return 0
	case a.Mode != Heat:
		return 1
	case b.Mode != Heat:
		return -1
	}
	dt, df := sign(b.SetTemp-a.SetTemp), sign(int(b.Fan-a.Fan))
	switch {
	case dt*df < 0:
		return 0
	case dt != 0:
		return dt
	}
	return df
}

// Fix returns the nearest state that the policy allows, and why it had to
// change. A valid state is returned unchanged with an empty reason.
//
// A pump that is off stays off while off is allowed, even when fan only is
// allowed too (as in v1). Fan only is preferred only when going idle from
// heat.
func (l Ladder) Fix(s PumpState) (PumpState, string) {
	pol := l.policy
	switch s.Mode {
	case Heat:
		var why string
		if s.SetTemp < pol.MinSetTemp || s.SetTemp > pol.MaxSetTemp {
			why = fmt.Sprintf("set temp %d outside %d..%d", s.SetTemp, pol.MinSetTemp, pol.MaxSetTemp)
			s.SetTemp = clamp(s.SetTemp, pol.MinSetTemp, pol.MaxSetTemp)
		}
		if s.Fan > pol.MaxFan {
			why = join(why, fmt.Sprintf("fan %s above max %s", s.Fan, pol.MaxFan))
			s.Fan = pol.MaxFan
		}
		return s, why
	case FanOnly:
		if !pol.CanUseFan {
			return l.leaveIdle(s, "fan only not allowed")
		}
		if s.Fan > pol.MaxFan {
			return PumpState{Mode: FanOnly, SetTemp: s.SetTemp, Fan: pol.MaxFan},
				fmt.Sprintf("fan %s above max %s", s.Fan, pol.MaxFan)
		}
		return s, ""
	default: // Off
		if !pol.CanSwitchOff {
			return l.leaveIdle(s, "off not allowed")
		}
		return s, ""
	}
}

// leaveIdle moves a disallowed idle state to the allowed idle mode, or to the
// resume state when no idle mode is allowed.
func (l Ladder) leaveIdle(s PumpState, why string) (PumpState, string) {
	if l.hasIdle {
		return l.Idle(s), why
	}
	return l.Resume(), why
}

func clamp[T ~int](v, lo, hi T) T { return max(lo, min(v, hi)) }

func sign(v int) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}
