package control

import (
	"strings"
	"testing"
	"time"
)

// harness drives one Room like Home Assistant would: every call reports the
// pump in whatever state the previous command left it.
type harness struct {
	t   *testing.T
	r   Room
	p   Params
	now time.Time
	in  Input
}

func newHarness(t *testing.T, pump PumpState) *harness {
	return &harness{t: t, p: DefaultParams(), now: t0, in: Input{
		Target:   21.5,
		RoomTemp: 21.5,
		Observed: pump,
		Policy:   testPolicy,
	}}
}

// call advances the clock, decides, and applies the command to the pump.
func (h *harness) call(after time.Duration, roomTemp float64) Decision {
	h.now = h.now.Add(after)
	h.in.RoomTemp = roomTemp
	d := h.r.Decide(h.in, h.p, h.now)
	h.in.Observed = d.Command
	return d
}

// history fills the samples for the 30 minutes before now without deciding.
// temp gets the minutes before now.
func (h *harness) history(temp func(minAgo int) float64) {
	for m := 30; m >= 5; m -= 5 {
		at := h.now.Add(-time.Duration(m) * time.Minute)
		h.r.samples = addSample(h.r.samples, Sample{At: at, Temp: temp(m)}, h.p.Window)
	}
}

func constant(t float64) func(int) float64 { return func(int) float64 { return t } }

func (h *harness) expect(d Decision, want PumpState, reason string) {
	h.t.Helper()
	if d.Command != want {
		h.t.Errorf("command = %v, want %v (reason %q)", d.Command, want, d.Reason)
	}
	if !strings.Contains(d.Reason, reason) {
		h.t.Errorf("reason = %q, want it to contain %q", d.Reason, reason)
	}
}

func TestHoldInBandReturnsObserved(t *testing.T) {
	h := newHarness(t, heat(24, Medium))
	h.history(constant(21.4))
	d := h.call(0, 21.4)
	h.expect(d, heat(24, Medium), "within band")
	if d.Changed {
		t.Error("hold must not report a change")
	}
}

func TestStepUpRespectsDwell(t *testing.T) {
	h := newHarness(t, heat(23, Low))
	h.history(constant(21.5))
	d := h.call(0, 21.2) // falling below target
	h.expect(d, heat(24, Low), "too cold: step up")

	d = h.call(5*time.Minute, 21.2)
	h.expect(d, heat(24, Low), "waiting")

	d = h.call(10*time.Minute, 21.2) // 15 min after the step
	h.expect(d, heat(25, Low), "step up")
}

func TestUrgentDwellWhenFarFromTarget(t *testing.T) {
	h := newHarness(t, heat(23, Low))
	h.history(constant(21.5))
	h.in.Target = 22.5 // target raised by a degree
	d := h.call(0, 21.5)
	h.expect(d, heat(24, Low), "step up")
	d = h.call(5*time.Minute, 21.5)
	h.expect(d, heat(25, Low), "step up")
}

func TestFanGoesUpOnlyAtMaxSetTemp(t *testing.T) {
	h := newHarness(t, heat(26, Low))
	h.history(constant(21.1))
	h.expect(h.call(0, 21.1), heat(26, Medium), "step up")

	h = newHarness(t, heat(25, Low))
	h.history(constant(21.1))
	h.expect(h.call(0, 21.1), heat(26, Low), "step up")
}

func TestAtMaxHeatHolds(t *testing.T) {
	h := newHarness(t, heat(26, High))
	h.history(constant(20.5))
	h.expect(h.call(0, 20.5), heat(26, High), "already at max")
}

func TestStepDownFanBeforeSetTemp(t *testing.T) {
	h := newHarness(t, heat(26, MediumHigh))
	h.history(constant(21.9))
	h.expect(h.call(0, 21.9), heat(26, Medium), "too warm: step down")
}

func TestClearlyTooWarmGoesIdle(t *testing.T) {
	h := newHarness(t, heat(25, Low))
	h.history(constant(22.2))
	h.expect(h.call(0, 22.2), PumpState{Mode: FanOnly, SetTemp: 25, Fan: Medium}, "go idle")
}

func TestClearlyTooWarmWithoutIdleStepsDown(t *testing.T) {
	h := newHarness(t, heat(25, Low))
	h.in.Policy.CanUseFan, h.in.Policy.CanSwitchOff = false, false
	h.history(constant(22.2))
	h.expect(h.call(0, 22.2), heat(24, Low), "step down")
}

func TestAtMinHolds(t *testing.T) {
	h := newHarness(t, PumpState{Mode: FanOnly, SetTemp: 20, Fan: Medium})
	h.history(constant(22.5))
	h.expect(h.call(0, 22.5), PumpState{Mode: FanOnly, SetTemp: 20, Fan: Medium}, "already at min")
}

func TestResumeFromIdle(t *testing.T) {
	h := newHarness(t, PumpState{Mode: FanOnly, SetTemp: 20, Fan: Medium})
	h.history(constant(21.1))
	h.expect(h.call(0, 21.1), heat(23, Low), "resume heating")
}

func TestTrendPreventsPush(t *testing.T) {
	// 0.25 below target but rising 0.9 °C/h: predicted to reach it, so hold.
	h := newHarness(t, heat(24, Low))
	h.history(func(m int) float64 { return 21.25 - 0.015*float64(m) })
	d := h.call(0, 21.25)
	if d.Error > -0.2 || d.Predicted < -0.2 {
		t.Fatalf("setup: error %.2f predicted %.2f", d.Error, d.Predicted)
	}
	h.expect(d, heat(24, Low), "within band")
}

func TestTrendActsEarly(t *testing.T) {
	// At target but falling 0.9 °C/h: step up before it gets cold.
	h := newHarness(t, heat(24, Low))
	h.history(func(m int) float64 { return 21.5 + 0.015*float64(m) })
	h.expect(h.call(0, 21.5), heat(25, Low), "too cold: step up")
}

func TestPolicyFixIgnoresDwell(t *testing.T) {
	h := newHarness(t, heat(24, Low))
	h.history(constant(21.0))
	h.expect(h.call(0, 21.0), heat(25, Low), "step up") // starts the dwell
	h.in.Policy.MaxSetTemp = 23
	h.expect(h.call(time.Minute, 21.0), heat(23, Low), "policy fix")
}

func TestOffNotAllowedResumes(t *testing.T) {
	h := newHarness(t, PumpState{Mode: Off, SetTemp: 20, Fan: Low})
	h.in.Policy.CanUseFan, h.in.Policy.CanSwitchOff = false, false
	h.expect(h.call(0, 21.5), heat(23, Low), "off not allowed")
}

func TestExternalChangeRestartsDwell(t *testing.T) {
	h := newHarness(t, heat(24, Low))
	h.history(constant(21.5))
	h.call(0, 21.5)
	h.in.Observed = heat(22, Low) // someone turned it down by hand
	d := h.call(5*time.Minute, 21.0)
	h.expect(d, heat(22, Low), "outside autoheat")
	if !strings.Contains(d.Reason, "waiting") {
		t.Errorf("reason = %q, want to wait after a manual change", d.Reason)
	}
}

func TestCommandedChangeIsNotExternal(t *testing.T) {
	h := newHarness(t, heat(24, Low))
	h.history(constant(21.0))
	h.call(0, 21.0) // commands heat/25/low, the harness applies it
	d := h.call(5*time.Minute, 21.0)
	if strings.Contains(d.Reason, "outside") {
		t.Errorf("reason = %q, applied command counted as an external change", d.Reason)
	}
}

func TestSameDirectionWaitsForTrend(t *testing.T) {
	h := newHarness(t, heat(24, Low))
	h.history(constant(21.2))
	h.expect(h.call(0, 21.2), heat(25, Low), "step up")

	// Still too cold, but not urgent: a second step up waits a full window.
	h.expect(h.call(20*time.Minute, 21.2), heat(25, Low), "waiting 10m0s")
	h.expect(h.call(10*time.Minute, 21.2), heat(26, Low), "step up")
}

func TestReversalOnlyWaitsDwell(t *testing.T) {
	h := newHarness(t, heat(24, Low))
	h.history(constant(21.2))
	h.expect(h.call(0, 21.2), heat(25, Low), "step up")
	// Overshoot: stepping back down only needs the normal dwell.
	h.r.samples = nil
	h.expect(h.call(15*time.Minute, 21.9), heat(24, Low), "too warm: step down")
}

func TestOffStaysOffWhenFanOnlyBecomesAllowed(t *testing.T) {
	off := PumpState{Mode: Off, SetTemp: 22, Fan: Low}
	h := newHarness(t, off) // testPolicy allows both fan only and off
	h.history(constant(21.5))
	d := h.call(0, 21.5)
	h.expect(d, off, "within band")
	if d.Changed {
		t.Error("an allowed off must not be changed")
	}

	h.history(constant(22.5)) // fireplace heats the room: still off
	h.expect(h.call(time.Minute, 22.5), off, "already at min")
}
