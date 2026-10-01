package control

import (
	"fmt"
	"math"
	"time"
)

// Input is everything one request tells us about a room.
type Input struct {
	Target   float64   `json:"target"`
	RoomTemp float64   `json:"roomTemp"`
	Observed PumpState `json:"observed"`
	Policy   Policy    `json:"policy"`
}

// Decision is the answer for one request.
type Decision struct {
	Command   PumpState `json:"command"`
	Changed   bool      `json:"changed"` // Command differs from the observed state
	Reason    string    `json:"reason"`
	Estimate  Estimate  `json:"estimate"`
	Error     float64   `json:"error"`     // smoothed room temp − target
	Predicted float64   `json:"predicted"` // Error projected Lookahead ahead
}

// Room is the controller's memory of one room between requests.
// It is not safe for concurrent use; callers serialise access per room.
type Room struct {
	samples     []Sample
	lastChange  time.Time // last change of the pump, commanded or observed
	prevObs     PumpState
	hasPrevObs  bool
	lastCommand PumpState
	hasCommand  bool
	lastStep    int     // direction of the last ladder step: +1 up, -1 down, 0 none
	lastPredict float64 // predicted error when the pump last changed

	lastAt       time.Time
	lastInput    Input
	lastDecision Decision
}

// Decide works out what the pump should be set to now.
func (r *Room) Decide(in Input, p Params, now time.Time) Decision {
	var notes string
	if r.noteObserved(in.Observed, now) {
		notes = "pump changed outside autoheat"
	}

	r.samples = addSample(r.samples, Sample{At: now, Temp: in.RoomTemp}, p.Window)
	est := estimate(r.samples, now, p.Window/3)
	e := est.Level - in.Target
	pe := e + est.SlopePerHour*p.Lookahead.Hours()

	lad := BuildLadder(in.Policy, p)
	start, fixed := lad.Fix(in.Observed)
	if fixed != "" {
		notes = join(notes, "policy fix: "+fixed)
	}
	idle := start.Mode != Heat
	up, canUp := lad.Up(start)
	down, canDown := lad.Down(start)

	next, dir, why := start, 0, ""
	switch {
	case pe < -p.ColdBand && idle:
		next, dir, why = up, 1, "too cold: resume heating"
	case pe < -p.ColdBand && !canUp:
		why = "too cold, already at max heat"
	case pe < -p.ColdBand:
		next, dir, why = up, 1, "too cold: step up"
	case pe > p.WarmBand && e >= p.IdleBand && lad.HasIdle() && !idle:
		next, dir, why = lad.Idle(start), -1, "clearly too warm: go idle"
	case pe > p.WarmBand && !canDown:
		why = "too warm, already at min"
	case pe > p.WarmBand:
		next, dir, why = down, -1, "too warm: step down"
	default:
		why = "within band: hold"
	}

	cmd := start
	if dir != 0 {
		// A further step the same way waits until the whole estimate window
		// comes after the last change, so the trend shows its effect.
		//
		// A large predicted error shortens the wait, but only on new
		// evidence: the room is that cold now, or the prediction has got
		// worse since the last change. Until the trend shows a step, the
		// prediction still says what it said when the step was taken, and
		// stepping on it again sweeps the whole ladder before the room
		// responds.
		urgent := math.Abs(pe) >= p.UrgentError &&
			(e <= -p.UrgentError || float64(dir)*(r.lastPredict-pe) > 0)
		dwell := p.Dwell
		switch {
		case urgent:
			dwell = p.UrgentDwell
		case dir == r.lastStep:
			dwell = max(p.Dwell, p.Window)
		}
		if wait := dwell - now.Sub(r.lastChange); !r.lastChange.IsZero() && wait > 0 {
			why += fmt.Sprintf(", waiting %s", wait.Round(time.Second))
		} else {
			cmd = next
			r.lastStep = dir
		}
	}

	d := Decision{
		Command:   cmd,
		Changed:   !cmd.Same(in.Observed),
		Reason:    join(notes, why),
		Estimate:  est,
		Error:     e,
		Predicted: pe,
	}
	if d.Changed {
		r.lastChange, r.lastPredict = now, pe
		r.lastCommand, r.hasCommand = cmd, true
	}
	r.lastAt, r.lastInput, r.lastDecision = now, in, d
	return d
}

// noteObserved tracks the pump state between calls and reports whether it
// changed in a way Autoheat did not command (by hand, another automation, or
// a command only partly applied). Such a change restarts the dwell timer.
func (r *Room) noteObserved(obs PumpState, now time.Time) bool {
	external := r.hasPrevObs && !obs.Same(r.prevObs) &&
		!(r.hasCommand && obs.Same(r.lastCommand))
	if external {
		r.lastChange, r.lastPredict = now, 0
		r.lastStep = 0
	}
	r.prevObs, r.hasPrevObs = obs, true
	return external
}

// Status is a snapshot of a room for debugging.
type Status struct {
	LastCall     time.Time `json:"lastCall"`
	LastChange   time.Time `json:"lastChange"`
	LastInput    Input     `json:"lastInput"`
	LastDecision Decision  `json:"lastDecision"`
}

func (r *Room) Status() Status {
	return Status{
		LastCall:     r.lastAt,
		LastChange:   r.lastChange,
		LastInput:    r.lastInput,
		LastDecision: r.lastDecision,
	}
}
