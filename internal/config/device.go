package config

import (
	"fmt"
	"strings"

	"github.com/vilellic/autoheat/internal/control"
)

// Device maps generic fan speeds to one heat pump model's own fan mode
// names, in both directions.
type Device struct {
	Name   string
	toName map[control.Fan]string
	toFan  map[string]control.Fan // by lower-case name
}

func newDevice(name string, fanModes map[string]string) (*Device, error) {
	d := &Device{Name: name, toName: map[control.Fan]string{}, toFan: map[string]control.Fan{}}
	for key, devName := range fanModes {
		f, ok := control.ParseFan(key)
		if !ok {
			return nil, fmt.Errorf("fanModes: unknown fan speed %q", key)
		}
		if _, dup := d.toName[f]; dup {
			return nil, fmt.Errorf("fanModes: %s given twice", f)
		}
		if strings.TrimSpace(devName) == "" {
			return nil, fmt.Errorf("fanModes: empty name for %s", f)
		}
		lower := strings.ToLower(devName)
		if other, dup := d.toFan[lower]; dup {
			return nil, fmt.Errorf("fanModes: %q used for both %s and %s", devName, other, f)
		}
		d.toName[f], d.toFan[lower] = devName, f
	}
	for _, f := range control.Fans {
		if _, ok := d.toName[f]; !ok {
			return nil, fmt.Errorf("fanModes: missing %s", f)
		}
	}
	return d, nil
}

// FanName returns the device's name for a fan speed.
func (d *Device) FanName(f control.Fan) string { return d.toName[f] }

// ParseFan maps a device fan mode name, as Home Assistant reports it, back to
// a generic speed. Case is ignored.
func (d *Device) ParseFan(name string) (control.Fan, bool) {
	f, ok := d.toFan[strings.ToLower(name)]
	return f, ok
}
