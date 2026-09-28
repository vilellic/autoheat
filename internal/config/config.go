// Package config loads the YAML configuration: tuning defaults, heat pump
// devices, and per-room overrides.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/vilellic/autoheat/internal/control"
)

// Config is a validated configuration.
type Config struct {
	defaults control.Params
	rooms    map[string]control.Params
	devices  map[string]*Device // by lower-case name
}

// Params returns the tuning for a room: the defaults plus its overrides.
func (c *Config) Params(room string) control.Params {
	if p, ok := c.rooms[room]; ok {
		return p
	}
	return c.defaults
}

// Device looks up a device by name, ignoring case.
func (c *Config) Device(name string) (*Device, bool) {
	d, ok := c.devices[strings.ToLower(strings.TrimSpace(name))]
	return d, ok
}

// DeviceNames lists the configured devices, sorted.
func (c *Config) DeviceNames() []string {
	var names []string
	for _, d := range c.devices {
		names = append(names, d.Name)
	}
	slices.Sort(names)
	return names
}

type file struct {
	Defaults tunables              `yaml:"defaults"`
	Devices  map[string]deviceFile `yaml:"devices"`
	Rooms    map[string]tunables   `yaml:"rooms"`
}

type deviceFile struct {
	FanModes map[string]string `yaml:"fanModes"`
}

// tunables are optional overrides of control.Params; unset fields keep the
// value they override.
type tunables struct {
	WindowMinutes      *float64 `yaml:"windowMinutes"`
	LookaheadMinutes   *float64 `yaml:"lookaheadMinutes"`
	ColdBand           *float64 `yaml:"coldBand"`
	WarmBand           *float64 `yaml:"warmBand"`
	IdleBand           *float64 `yaml:"idleBand"`
	DwellMinutes       *float64 `yaml:"dwellMinutes"`
	UrgentDwellMinutes *float64 `yaml:"urgentDwellMinutes"`
	UrgentError        *float64 `yaml:"urgentError"`
	ResumeSetTemp      *int     `yaml:"resumeSetTemp"`
	BaseFan            *string  `yaml:"baseFan"`
	CirculationFan     *string  `yaml:"circulationFan"`
}

// Load reads and validates a config file.
func Load(path string) (*Config, error) {
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		return nil, fmt.Errorf("%s is a directory, not a file: with Docker this usually means the host file did not exist when the container started (create it from config.example.yaml)", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Parse validates a config from YAML. Unknown keys are errors, so typos do
// not go unnoticed.
func Parse(data []byte) (*Config, error) {
	var f file
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("config is empty")
		}
		return nil, err
	}

	defaults, err := f.Defaults.apply(control.DefaultParams())
	if err != nil {
		return nil, fmt.Errorf("defaults: %w", err)
	}
	c := &Config{defaults: defaults, rooms: map[string]control.Params{}, devices: map[string]*Device{}}

	for room, t := range f.Rooms {
		if c.rooms[room], err = t.apply(defaults); err != nil {
			return nil, fmt.Errorf("room %q: %w", room, err)
		}
	}

	if len(f.Devices) == 0 {
		return nil, errors.New("no devices configured")
	}
	for name, df := range f.Devices {
		d, err := newDevice(name, df.FanModes)
		if err != nil {
			return nil, fmt.Errorf("device %q: %w", name, err)
		}
		key := strings.ToLower(name)
		if _, dup := c.devices[key]; dup {
			return nil, fmt.Errorf("device %q defined twice (names ignore case)", name)
		}
		c.devices[key] = d
	}
	return c, nil
}

// ApplyTunables applies tuning overrides written like a room entry in the
// config file (e.g. "{dwellMinutes: 20}") on top of p.
func ApplyTunables(data []byte, p control.Params) (control.Params, error) {
	var t tunables
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&t); err != nil && !errors.Is(err, io.EOF) {
		return p, err
	}
	return t.apply(p)
}

func (t tunables) apply(p control.Params) (control.Params, error) {
	minutes := func(dst *time.Duration, v *float64) {
		if v != nil {
			*dst = time.Duration(*v * float64(time.Minute))
		}
	}
	number := func(dst *float64, v *float64) {
		if v != nil {
			*dst = *v
		}
	}
	fan := func(dst *control.Fan, v *string, key string) error {
		if v == nil {
			return nil
		}
		f, ok := control.ParseFan(*v)
		if !ok {
			return fmt.Errorf("%s: unknown fan speed %q", key, *v)
		}
		*dst = f
		return nil
	}

	minutes(&p.Window, t.WindowMinutes)
	minutes(&p.Lookahead, t.LookaheadMinutes)
	number(&p.ColdBand, t.ColdBand)
	number(&p.WarmBand, t.WarmBand)
	number(&p.IdleBand, t.IdleBand)
	minutes(&p.Dwell, t.DwellMinutes)
	minutes(&p.UrgentDwell, t.UrgentDwellMinutes)
	number(&p.UrgentError, t.UrgentError)
	if t.ResumeSetTemp != nil {
		p.ResumeSetTemp = *t.ResumeSetTemp
	}
	if err := fan(&p.BaseFan, t.BaseFan, "baseFan"); err != nil {
		return p, err
	}
	if err := fan(&p.CirculationFan, t.CirculationFan, "circulationFan"); err != nil {
		return p, err
	}
	return p, p.Validate()
}
