package control

import "fmt"

// Ladder is the ordered list of heating levels a policy allows, from least to
// most heat:
//
//	idle (fan only or off, when allowed)
//	heat MinSetTemp..MaxSetTemp at the base fan
//	heat MaxSetTemp with the fan above base, up to MaxFan
//
// The set temperature goes up before the fan, and the fan comes down before
// the set temperature. Every level is inside the policy.
type Ladder struct {
	policy  Policy
	hasIdle bool
	idle    Mode // FanOnly or Off, when hasIdle
	baseFan Fan
	circFan Fan
	heat    []PumpState
	resume  int // position heating restarts from after idle
}

func BuildLadder(pol Policy, p Params) Ladder {
	l := Ladder{
		policy:  pol,
		baseFan: min(p.BaseFan, pol.MaxFan),
		circFan: min(p.CirculationFan, pol.MaxFan),
	}
	switch {
	case pol.CanUseFan:
		l.hasIdle, l.idle = true, FanOnly
	case pol.CanSwitchOff:
		l.hasIdle, l.idle = true, Off
	}
	for t := pol.MinSetTemp; t <= pol.MaxSetTemp; t++ {
		l.heat = append(l.heat, PumpState{Mode: Heat, SetTemp: t, Fan: l.baseFan})
	}
	for f := l.baseFan + 1; f <= pol.MaxFan; f++ {
		l.heat = append(l.heat, PumpState{Mode: Heat, SetTemp: pol.MaxSetTemp, Fan: f})
	}
	l.resume = l.Locate(PumpState{Mode: Heat, SetTemp: p.ResumeSetTemp, Fan: l.baseFan})
	return l
}

// Positions run from 0 (bottom) to Top(). With idle, position 0 is idle.

func (l Ladder) HasIdle() bool       { return l.hasIdle }
func (l Ladder) IsIdle(pos int) bool { return l.hasIdle && pos == 0 }
func (l Ladder) Top() int            { return l.offset() + len(l.heat) - 1 }
func (l Ladder) offset() int {
	if l.hasIdle {
		return 1
	}
	return 0
}

// Locate returns the position of a state that is valid for the policy (see
// Fix). A heat state between levels, such as a fan other than the base fan
// below max set temperature, maps to the level with the same set temperature.
func (l Ladder) Locate(s PumpState) int {
	if s.Mode != Heat {
		return 0
	}
	span := l.policy.MaxSetTemp - l.policy.MinSetTemp
	t := clamp(s.SetTemp, l.policy.MinSetTemp, l.policy.MaxSetTemp)
	if t < l.policy.MaxSetTemp {
		return l.offset() + t - l.policy.MinSetTemp
	}
	f := clamp(s.Fan, l.baseFan, l.policy.MaxFan)
	return l.offset() + span + int(f-l.baseFan)
}

// Resume is the position heating restarts from after idle: the resume set
// temperature (clamped to the policy) at the base fan.
func (l Ladder) Resume() int { return l.resume }

// Level returns the command for a position. Fields Home Assistant ignores
// (set temperature when idle, fan when off) are copied from current.
func (l Ladder) Level(pos int, current PumpState) PumpState {
	if l.IsIdle(pos) {
		if l.idle == FanOnly {
			return PumpState{Mode: FanOnly, SetTemp: current.SetTemp, Fan: l.circFan}
		}
		return PumpState{Mode: Off, SetTemp: current.SetTemp, Fan: current.Fan}
	}
	return l.heat[pos-l.offset()]
}

// Move returns the command for stepping from state s at position pos to
// position next. If s is a heat state off the ladder (e.g. a fan set by
// hand), the fan never moves against the step: a step up never lowers it and
// a step down never raises it.
func (l Ladder) Move(s PumpState, pos, next int) PumpState {
	cmd := l.Level(next, s)
	if cmd.Mode == Heat && s.Mode == Heat {
		if next > pos {
			cmd.Fan = max(cmd.Fan, s.Fan)
		} else {
			cmd.Fan = min(cmd.Fan, s.Fan)
		}
	}
	return cmd
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
// resume level when no idle mode is allowed.
func (l Ladder) leaveIdle(s PumpState, why string) (PumpState, string) {
	if l.hasIdle {
		return l.Level(0, s), why
	}
	return l.Level(l.resume, s), why
}

func clamp[T ~int](v, lo, hi T) T { return max(lo, min(v, hi)) }

func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}
