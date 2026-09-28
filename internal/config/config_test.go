package config

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vilellic/autoheat/internal/control"
)

const sample = `
defaults:
  dwellMinutes: 20
  baseFan: quiet
devices:
  Gree:
    fanModes: {quiet: low, low: medium low, medium: medium, medium_high: medium high, high: high}
  mitsubishi:
    fanModes: {quiet: quiet, low: low, medium: medium, medium_high: medium_high, high: high}
rooms:
  olohuone:
    dwellMinutes: 10
    circulationFan: Medium High
`

func TestParse(t *testing.T) {
	c, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}

	def := c.Params("unknown room")
	if def.Dwell != 20*time.Minute || def.BaseFan != control.Quiet {
		t.Errorf("defaults not applied: %+v", def)
	}
	if def.Window != control.DefaultParams().Window {
		t.Errorf("unset default changed: %v", def.Window)
	}

	room := c.Params("olohuone")
	if room.Dwell != 10*time.Minute || room.CirculationFan != control.MediumHigh {
		t.Errorf("room override not applied: %+v", room)
	}
	if room.BaseFan != control.Quiet {
		t.Errorf("room should inherit file defaults, got base fan %v", room.BaseFan)
	}

	if got := c.DeviceNames(); strings.Join(got, ",") != "Gree,mitsubishi" {
		t.Errorf("DeviceNames = %v", got)
	}
}

func TestDeviceMapping(t *testing.T) {
	c, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	d, ok := c.Device("gree")
	if !ok {
		t.Fatal("device lookup should ignore case")
	}
	if got := d.FanName(control.Quiet); got != "low" {
		t.Errorf("FanName(quiet) = %q, want the device's own name %q", got, "low")
	}
	for name, want := range map[string]control.Fan{"low": control.Quiet, "medium low": control.Low, "Medium High": control.MediumHigh} {
		if got, ok := d.ParseFan(name); !ok || got != want {
			t.Errorf("ParseFan(%q) = %v %v, want %v", name, got, ok, want)
		}
	}
	if _, ok := d.ParseFan("auto"); ok {
		t.Error("ParseFan(auto) should be unknown")
	}
}

func TestParseErrors(t *testing.T) {
	mitsu := "devices:\n  m:\n    fanModes: {quiet: q, low: l, medium: m, medium_high: mh, high: h}\n"
	dev := func(fanModes string) string { return "devices:\n  m:\n    fanModes: {" + fanModes + "}\n" }
	cases := []struct{ in, want string }{
		{"", "empty"},
		{"defaults:\n  dwelMinutes: 5\n" + mitsu, "dwelMinutes"},
		{"defaults:\n  baseFan: turbo\n" + mitsu, "turbo"},
		{"defaults:\n  idleBand: 0.1\n" + mitsu, "idle band"},
		{"rooms:\n  x:\n    warmBand: -1\n" + mitsu, `room "x"`},
		{"defaults: {}\n", "no devices"},
		{dev("quiet: q, low: l, medium: m, medium_high: mh"), "missing high"},
		{dev("quiet: q, low: q, medium: m, medium_high: mh, high: h"), "used for both"},
		{dev("quiet: q, low: l, medium: m, medium_high: mh, turbo: h"), "turbo"},
		{mitsu + "  M:\n    fanModes: {quiet: q, low: l, medium: m, medium_high: mh, high: h}\n", "twice"},
	}
	for _, c := range cases {
		_, err := Parse([]byte(c.in))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Parse(%q) error = %v, want it to mention %q", c.in, err, c.want)
		}
	}
}

func TestExampleConfigIsValid(t *testing.T) {
	if _, err := os.Stat("../../config.example.yaml"); os.IsNotExist(err) {
		t.Skip("config.example.yaml not written yet")
	}
	if _, err := Load("../../config.example.yaml"); err != nil {
		t.Fatal(err)
	}
}

func TestApplyTunables(t *testing.T) {
	p, err := ApplyTunables([]byte("{dwellMinutes: 25, baseFan: quiet}"), control.DefaultParams())
	if err != nil || p.Dwell != 25*time.Minute || p.BaseFan != control.Quiet || p.Window != control.DefaultParams().Window {
		t.Errorf("got %+v, %v", p, err)
	}
	if _, err := ApplyTunables([]byte("{dwell: 5}"), control.DefaultParams()); err == nil {
		t.Error("unknown key should fail")
	}
	if p, err := ApplyTunables(nil, control.DefaultParams()); err != nil || p != control.DefaultParams() {
		t.Errorf("empty overrides: %+v, %v", p, err)
	}
}

func TestLoadDirectoryExplains(t *testing.T) {
	_, err := Load(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("err = %v, want an explanation that the path is a directory", err)
	}
}
