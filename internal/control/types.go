// Package control holds the heating decision logic for one room.
//
// It does no I/O and never reads the clock: callers pass the current time in,
// so tests and simulations are fast and deterministic.
package control

import (
	"fmt"
	"strings"
	"time"
)

// Fan is a generic fan speed. Devices map these to their own names.
type Fan int

const (
	Quiet Fan = iota
	Low
	Medium
	MediumHigh
	High
)

// Fans lists all fan speeds from lowest to highest.
var Fans = []Fan{Quiet, Low, Medium, MediumHigh, High}

var fanNames = [...]string{"quiet", "low", "medium", "medium_high", "high"}

func (f Fan) String() string {
	if f < Quiet || f > High {
		return fmt.Sprintf("Fan(%d)", int(f))
	}
	return fanNames[f]
}

func (f Fan) MarshalText() ([]byte, error) { return []byte(f.String()), nil }

// ParseFan parses a generic fan speed name. Case, spaces and hyphens are
// ignored, so "Medium high" and "medium_high" are the same speed.
func ParseFan(s string) (Fan, bool) {
	s = strings.NewReplacer(" ", "_", "-", "_").Replace(strings.ToLower(strings.TrimSpace(s)))
	for i, name := range fanNames {
		if s == name {
			return Fan(i), true
		}
	}
	return 0, false
}

// Mode is the heat pump's HVAC mode. Only the modes used for heating exist.
type Mode int

const (
	Off Mode = iota
	FanOnly
	Heat
)

var modeNames = [...]string{"off", "fan_only", "heat"}

func (m Mode) String() string {
	if m < Off || m > Heat {
		return fmt.Sprintf("Mode(%d)", int(m))
	}
	return modeNames[m]
}

func (m Mode) MarshalText() ([]byte, error) { return []byte(m.String()), nil }

// ParseMode parses a Home Assistant HVAC mode name (case-insensitive).
func ParseMode(s string) (Mode, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	for i, name := range modeNames {
		if s == name {
			return Mode(i), true
		}
	}
	return 0, false
}

// PumpState is what the heat pump is set to, or what it is told to be set to.
type PumpState struct {
	Mode    Mode `json:"mode"`
	SetTemp int  `json:"setTemp"`
	Fan     Fan  `json:"fan"`
}

// Same reports whether a and b are equal in the fields Home Assistant applies:
// set temperature only matters in heat, and fan only when not off.
func (a PumpState) Same(b PumpState) bool {
	if a.Mode != b.Mode {
		return false
	}
	switch a.Mode {
	case Heat:
		return a.SetTemp == b.SetTemp && a.Fan == b.Fan
	case FanOnly:
		return a.Fan == b.Fan
	default:
		return true
	}
}

func (s PumpState) String() string {
	switch s.Mode {
	case Heat:
		return fmt.Sprintf("heat/%d/%s", s.SetTemp, s.Fan)
	case FanOnly:
		return "fan_only/" + s.Fan.String()
	default:
		return s.Mode.String()
	}
}

// Policy is what the household currently allows. It arrives with every
// request and may change at any time.
type Policy struct {
	MinSetTemp   int  `json:"minSetTemp"`
	MaxSetTemp   int  `json:"maxSetTemp"`
	MaxFan       Fan  `json:"maxFan"`
	CanUseFan    bool `json:"canUseFan"`
	CanSwitchOff bool `json:"canSwitchOff"`
}

func (p Policy) Validate() error {
	if p.MinSetTemp > p.MaxSetTemp {
		return fmt.Errorf("min set temperature %d is above max %d", p.MinSetTemp, p.MaxSetTemp)
	}
	if p.MaxFan < Quiet || p.MaxFan > High {
		return fmt.Errorf("invalid max fan %v", p.MaxFan)
	}
	return nil
}

// Params are the tuning values of the controller.
type Params struct {
	Window         time.Duration // how far back samples are used for the estimate
	Lookahead      time.Duration // how far ahead the trend is projected
	ColdBand       float64       // °C below target (predicted) before heating is stepped up
	WarmBand       float64       // °C above target (predicted) before heating is stepped down
	IdleBand       float64       // °C above target (actual) to jump straight to idle
	Dwell          time.Duration // minimum time between steps
	UrgentDwell    time.Duration // minimum time between steps when far from target
	UrgentError    float64       // |predicted error| from which UrgentDwell may apply (see Decide)
	ResumeSetTemp  int           // set temperature used when heating resumes from idle
	BaseFan        Fan           // fan speed at the bottom of the ladder
	CirculationFan Fan           // fan speed in fan-only mode
	FanFrom        FanCurve      // fan ceiling by set temperature, for steps up
}

// FanCurve gives, for each fan speed, the set temperature from which a step
// up may use that fan (see Ladder). Zero means not given: the speed then
// starts with the next faster speed that is given, or at the max set
// temperature. Speeds at or below the base fan are ignored.
type FanCurve [High + 1]int

// From returns the set temperature from which fan f is used, given the
// highest set temperature allowed.
func (c FanCurve) From(f Fan, maxSetTemp int) int {
	for ; f <= High; f++ {
		if c[f] != 0 {
			return min(c[f], maxSetTemp)
		}
	}
	return maxSetTemp
}

// Validate checks that the curve never steps down: a faster fan never starts
// at a lower set temperature than a slower one.
func (c FanCurve) Validate() error {
	prev := Fan(-1)
	for f, t := range c {
		switch {
		case t < 0:
			return fmt.Errorf("fan curve: negative set temperature %d for %s", t, Fan(f))
		case t == 0:
			continue
		case prev >= 0 && t < c[prev]:
			return fmt.Errorf("fan curve: %s from %d is below %s from %d", Fan(f), t, prev, c[prev])
		}
		prev = Fan(f)
	}
	return nil
}

// DefaultParams are the starting values; tune them with the simulation.
func DefaultParams() Params {
	return Params{
		Window:         30 * time.Minute,
		Lookahead:      20 * time.Minute,
		ColdBand:       0.2,
		WarmBand:       0.3,
		IdleBand:       0.5,
		Dwell:          15 * time.Minute,
		UrgentDwell:    5 * time.Minute,
		UrgentError:    0.5,
		ResumeSetTemp:  23,
		BaseFan:        Low,
		CirculationFan: Medium,
		FanFrom:        FanCurve{Medium: 23, MediumHigh: 26},
	}
}

func (p Params) Validate() error {
	switch {
	case p.Window <= 0:
		return fmt.Errorf("window must be positive")
	case p.Lookahead < 0:
		return fmt.Errorf("lookahead must not be negative")
	case p.ColdBand <= 0 || p.WarmBand <= 0:
		return fmt.Errorf("cold and warm bands must be positive")
	case p.IdleBand < p.WarmBand:
		return fmt.Errorf("idle band %.2f must not be below warm band %.2f", p.IdleBand, p.WarmBand)
	case p.UrgentDwell < 0 || p.Dwell < p.UrgentDwell:
		return fmt.Errorf("need 0 <= urgent dwell <= dwell")
	case p.UrgentError <= 0:
		return fmt.Errorf("urgent error must be positive")
	case p.BaseFan < Quiet || p.BaseFan > High:
		return fmt.Errorf("invalid base fan %v", p.BaseFan)
	case p.CirculationFan < Quiet || p.CirculationFan > High:
		return fmt.Errorf("invalid circulation fan %v", p.CirculationFan)
	}
	return p.FanFrom.Validate()
}
