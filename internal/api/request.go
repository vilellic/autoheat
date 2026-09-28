package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/vilellic/autoheat/internal/config"
	"github.com/vilellic/autoheat/internal/control"
)

// request is what the Home Assistant rest_command sends. Field names are
// kept from v1; unknown fields (such as v1's lastDelta) are ignored.
type request struct {
	Room                    value `json:"room"`
	Device                  value `json:"device"`
	TargetIndoorTemp        value `json:"targetIndoorTemp"`
	CurrentIndoorTemp       value `json:"currentIndoorTemp"`
	CurrentStateHvacMode    value `json:"currentStateHvacMode"`
	CurrentStateTemperature value `json:"currentStateTemperature"`
	CurrentStateFanMode     value `json:"currentStateFanMode"`
	HeatMin                 value `json:"heatMin"`
	HeatMax                 value `json:"heatMax"`
	MaxAllowedFanSpeed      value `json:"maxAllowedFanSpeed"`
	CanUseFan               value `json:"canUseFan"`
	CanBeSwitchedOff        value `json:"canBeSwitchedOff"`
}

// value is one JSON value kept as text. Home Assistant templates produce
// types loosely (21.0 or "21.0", true or "on"), so values are interpreted in
// parse, where an error can name the field.
type value struct {
	set  bool   // present and not null
	text string // strings unquoted, anything else as written
}

func (v *value) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	var s string
	if json.Unmarshal(b, &s) != nil {
		s = string(b)
	}
	*v = value{set: true, text: strings.TrimSpace(s)}
	return nil
}

// absent reports whether Home Assistant had no real value.
func (v value) absent() bool {
	if !v.set {
		return true
	}
	switch strings.ToLower(v.text) {
	case "", "none", "null", "unknown", "unavailable":
		return true
	}
	return false
}

func (v value) number(field string) (float64, error) {
	if v.absent() {
		return 0, fmt.Errorf("%s is missing or unavailable (%q)", field, v.text)
	}
	f, err := strconv.ParseFloat(v.text, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("%s: %q is not a number", field, v.text)
	}
	return f, nil
}

// boolean accepts true/false, on/off (a binary sensor's state), yes/no and
// 1/0. A missing or unavailable value is false, the safe choice for the
// permission switches.
func (v value) boolean(field string) (bool, error) {
	if v.absent() {
		return false, nil
	}
	switch strings.ToLower(v.text) {
	case "true", "on", "yes", "1":
		return true, nil
	case "false", "off", "no", "0":
		return false, nil
	}
	return false, fmt.Errorf("%s: %q is not true or false", field, v.text)
}

type parsed struct {
	input    control.Input
	device   *config.Device
	rawFan   string // the observed fan mode name as reported, if any
	warnings []string
}

// parse validates a request. Anything that would make the answer a guess is
// an error, so Home Assistant applies nothing. Oddities Autoheat can safely
// work around become warnings.
func (s *Server) parse(req request) (parsed, error) {
	var p parsed
	if req.Room.absent() {
		return p, errors.New("room is required")
	}
	dev, ok := s.cfg.Device(req.Device.text)
	if !ok {
		return p, fmt.Errorf("unknown device %q (configured: %s)", req.Device.text, strings.Join(s.cfg.DeviceNames(), ", "))
	}
	p.device = dev

	target, err := req.TargetIndoorTemp.number("targetIndoorTemp")
	if err != nil {
		return p, err
	}
	roomTemp, err := req.CurrentIndoorTemp.number("currentIndoorTemp")
	if err != nil {
		return p, err
	}
	heatMin, err := req.HeatMin.number("heatMin")
	if err != nil {
		return p, err
	}
	heatMax, err := req.HeatMax.number("heatMax")
	if err != nil {
		return p, err
	}
	maxFan, ok := control.ParseFan(req.MaxAllowedFanSpeed.text)
	if !ok {
		return p, fmt.Errorf("maxAllowedFanSpeed: unknown fan speed %q", req.MaxAllowedFanSpeed.text)
	}
	canUseFan, err := req.CanUseFan.boolean("canUseFan")
	if err != nil {
		return p, err
	}
	canSwitchOff, err := req.CanBeSwitchedOff.boolean("canBeSwitchedOff")
	if err != nil {
		return p, err
	}
	pol := control.Policy{
		MinSetTemp:   whole(heatMin),
		MaxSetTemp:   whole(heatMax),
		MaxFan:       maxFan,
		CanUseFan:    canUseFan,
		CanSwitchOff: canSwitchOff,
	}
	if err := pol.Validate(); err != nil {
		return p, err
	}

	obs, err := p.observed(req, pol, s.cfg.Params(req.Room.text).BaseFan)
	if err != nil {
		return p, err
	}
	p.input = control.Input{Target: target, RoomTemp: roomTemp, Observed: obs, Policy: pol}
	return p, nil
}

// observed parses the heat pump's reported state.
func (p *parsed) observed(req request, pol control.Policy, baseFan control.Fan) (control.PumpState, error) {
	var obs control.PumpState
	rawMode := req.CurrentStateHvacMode
	if rawMode.absent() {
		return obs, fmt.Errorf("heat pump unavailable (currentStateHvacMode %q)", rawMode.text)
	}
	mode, ok := control.ParseMode(rawMode.text)
	if !ok {
		mode = control.Off
		p.warn("mode %q is not used for heating; treated as off", rawMode.text)
	}
	obs.Mode = mode

	switch setTemp := req.CurrentStateTemperature; {
	case !setTemp.absent():
		t, err := setTemp.number("currentStateTemperature")
		if err != nil {
			return obs, err
		}
		obs.SetTemp = whole(t)
	case mode == control.Heat:
		return obs, errors.New("currentStateTemperature is required in heat mode")
	default:
		obs.SetTemp = pol.MinSetTemp // ignored by Home Assistant when not heating
	}

	rawFan := req.CurrentStateFanMode
	switch {
	case rawFan.absent() && mode != control.Off:
		return obs, fmt.Errorf("currentStateFanMode missing (%q) while the pump is on", rawFan.text)
	case rawFan.absent():
		obs.Fan = baseFan // ignored by Home Assistant when off
	default:
		fan, known := p.device.ParseFan(rawFan.text)
		if !known {
			fan = baseFan
			p.warn("fan mode %q unknown for device %s; treated as %s", rawFan.text, p.device.Name, baseFan)
		}
		obs.Fan, p.rawFan = fan, rawFan.text
	}
	return obs, nil
}

func (p *parsed) warn(format string, args ...any) {
	p.warnings = append(p.warnings, fmt.Sprintf(format, args...))
}

// whole truncates like Home Assistant's `| int`, which the automation uses
// when comparing set temperatures.
func whole(v float64) int { return int(math.Trunc(v)) }
