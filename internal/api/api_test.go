package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vilellic/autoheat/internal/config"
)

// The tests step from 24/low, so a fan curve that keeps the fan at base below
// max set temperature makes every step below max a set temperature step.
const testConfig = `
defaults:
  fanFrom: {}
devices:
  mitsubishi:
    fanModes: {quiet: quiet, low: low, medium: medium, medium_high: medium_high, high: high}
  gree:
    fanModes: {quiet: low, low: medium low, medium: medium, medium_high: medium high, high: high}
`

type testServer struct {
	t   *testing.T
	srv *Server
	h   http.Handler
	now time.Time
}

func newTestServer(t *testing.T) *testServer {
	cfg, err := config.Parse([]byte(testConfig))
	if err != nil {
		t.Fatal(err)
	}
	ts := &testServer{t: t, now: time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)}
	ts.srv = New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts.srv.now = func() time.Time { return ts.now }
	ts.h = ts.srv.Handler()
	return ts
}

func (ts *testServer) do(method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	ts.h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

// body builds a request like the Home Assistant rest_command sends, with
// fields overridden by kv pairs (raw JSON values).
func body(kv ...string) string {
	fields := map[string]string{
		"room":                    `"olohuone"`,
		"device":                  `"mitsubishi"`,
		"targetIndoorTemp":        `21.5`,
		"currentIndoorTemp":       `21.5`,
		"currentStateHvacMode":    `"heat"`,
		"currentStateTemperature": `24`,
		"currentStateFanMode":     `"low"`,
		"heatMin":                 `20`,
		"heatMax":                 `26`,
		"maxAllowedFanSpeed":      `"High"`,
		"canUseFan":               `false`,
		"canBeSwitchedOff":        `true`,
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			delete(fields, kv[i])
		} else {
			fields[kv[i]] = kv[i+1]
		}
	}
	var parts []string
	for k, v := range fields {
		parts = append(parts, `"`+k+`":`+v)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func TestResponseShapeMatchesV1(t *testing.T) {
	ts := newTestServer(t)
	rec := ts.do("POST", "/", body())
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if got, want := rec.Body.String(), `{"HVAC_mode":"heat","Temperature":24,"Fan_mode":"low"}`+"\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestDeviceFanNamesBothWays(t *testing.T) {
	ts := newTestServer(t)
	// Gree reports "medium low" for generic low; cold room steps up to 25.
	rec := ts.do("POST", "/", body("device", `"gree"`, "currentStateFanMode", `"medium low"`, "currentIndoorTemp", `20.9`))
	var r response
	json.Unmarshal(rec.Body.Bytes(), &r)
	if rec.Code != 200 || r != (response{"heat", 25, "medium low"}) {
		t.Fatalf("status %d, got %+v", rec.Code, r)
	}

	// Stepping the fan up at max set temp returns Gree's own name.
	rec = ts.do("POST", "/", body("room", `"b"`, "device", `"gree"`, "currentStateTemperature", `26`, "currentStateFanMode", `"medium low"`, "currentIndoorTemp", `20.9`))
	json.Unmarshal(rec.Body.Bytes(), &r)
	if r != (response{"heat", 26, "medium"}) {
		t.Fatalf("got %+v", r)
	}
}

func TestRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"invalid JSON", `{"room": "x", "targetIndoorTemp": unavailable}`, "invalid JSON"},
		{"no room", body("room", `""`), "room"},
		{"unknown device", body("device", `"daikin"`), "daikin"},
		{"target missing", body("targetIndoorTemp", `null`), "targetIndoorTemp"},
		{"room temp missing", body("currentIndoorTemp", ""), "currentIndoorTemp"},
		{"pump unavailable", body("currentStateHvacMode", `"unavailable"`), "unavailable"},
		{"set temp missing in heat", body("currentStateTemperature", `null`), "currentStateTemperature"},
		{"fan missing while on", body("currentStateFanMode", `"None"`), "currentStateFanMode"},
		{"min above max", body("heatMin", `27`), "above max"},
		{"bad max fan", body("maxAllowedFanSpeed", `"turbo"`), "turbo"},
	}
	ts := newTestServer(t)
	for _, c := range cases {
		rec := ts.do("POST", "/", c.body)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: status %d body %q, want 400 mentioning %q", c.name, rec.Code, rec.Body, c.want)
		}
	}
}

func TestToleratedInput(t *testing.T) {
	ts := newTestServer(t)
	cases := []struct {
		name string
		body string
		want response
	}{
		{"v1 lastDelta ignored", body("lastDelta", `-0.2`), response{"heat", 24, "low"}},
		{"float set temp truncated", body("currentStateTemperature", `24.5`), response{"heat", 24, "low"}},
		{"unknown fan echoed on hold", body("currentStateFanMode", `"auto"`), response{"heat", 24, "auto"}},
		{"cooling mode treated as off", body("currentStateHvacMode", `"cool"`), response{"off", 24, "low"}},
		{"off without set temp or fan", body("currentStateHvacMode", `"off"`, "currentStateTemperature", `null`, "currentStateFanMode", `"None"`), response{"off", 20, "low"}},
	}
	for i, c := range cases {
		b := strings.Replace(c.body, `"olohuone"`, `"room`+string(rune('a'+i))+`"`, 1)
		rec := ts.do("POST", "/", b)
		var r response
		json.Unmarshal(rec.Body.Bytes(), &r)
		if rec.Code != 200 || r != c.want {
			t.Errorf("%s: status %d got %+v, want %+v (%s)", c.name, rec.Code, r, c.want, rec.Body)
		}
	}
}

func TestRoomsAreIndependent(t *testing.T) {
	ts := newTestServer(t)
	// Room a steps up, which starts its dwell timer.
	ts.do("POST", "/", body("room", `"a"`, "currentIndoorTemp", `20.9`))
	ts.now = ts.now.Add(time.Minute)
	// Room b is not affected by a's timer.
	rec := ts.do("POST", "/", body("room", `"b"`, "currentIndoorTemp", `20.9`))
	var r response
	json.Unmarshal(rec.Body.Bytes(), &r)
	if r.Temperature != 25 {
		t.Errorf("room b = %+v, want a step up to 25", r)
	}
}

func TestStatus(t *testing.T) {
	ts := newTestServer(t)
	ts.do("POST", "/", body())

	rec := ts.do("GET", "/status/olohuone", "")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var st struct {
		Room         string
		Device       string
		LastDecision struct {
			Command struct{ Mode, Fan string }
			Reason  string
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Room != "olohuone" || st.Device != "mitsubishi" || st.LastDecision.Command.Mode != "heat" || st.LastDecision.Reason == "" {
		t.Errorf("status = %+v", st)
	}

	rec = ts.do("GET", "/status", "")
	var all []json.RawMessage
	if json.Unmarshal(rec.Body.Bytes(), &all); len(all) != 1 {
		t.Errorf("/status = %s", rec.Body)
	}
	if rec := ts.do("GET", "/status/nowhere", ""); rec.Code != 404 {
		t.Errorf("unknown room status %d, want 404", rec.Code)
	}
}

func TestMethods(t *testing.T) {
	ts := newTestServer(t)
	if rec := ts.do("GET", "/", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET / = %d, want 405", rec.Code)
	}
	if rec := ts.do("POST", "/other", body()); rec.Code != 404 {
		t.Errorf("POST /other = %d, want 404", rec.Code)
	}
}

func TestConcurrentRooms(t *testing.T) {
	ts := newTestServer(t)
	ts.srv.now = time.Now // the fake clock is not safe for concurrent use
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			room := fmt.Sprintf(`"room%d"`, i%4)
			for range 50 {
				if rec := ts.do("POST", "/", body("room", room)); rec.Code != 200 {
					t.Errorf("status %d", rec.Code)
				}
				ts.do("GET", "/status", "")
			}
		})
	}
	wg.Wait()
}

func TestLooseTypesFromTemplates(t *testing.T) {
	ts := newTestServer(t)
	// The payload from a hand-written rest_command test: numbers and a
	// boolean as strings.
	rec := ts.do("POST", "/", `{
		"room": "olohuone", "device": "mitsubishi",
		"targetIndoorTemp": "21.0", "currentIndoorTemp": 21.5,
		"currentStateHvacMode": "off", "currentStateTemperature": 20, "currentStateFanMode": "medium",
		"heatMin": "20.0", "heatMax": "26.0", "maxAllowedFanSpeed": "Medium_high",
		"canUseFan": "false", "canBeSwitchedOff": true}`)
	if got, want := rec.Body.String(), `{"HVAC_mode":"off","Temperature":20,"Fan_mode":"medium"}`+"\n"; rec.Code != 200 || got != want {
		t.Fatalf("status %d body %q, want %q", rec.Code, got, want)
	}

	cases := []struct {
		name string
		body string
		want response
	}{
		{"binary sensor states", body("canUseFan", `"on"`, "canBeSwitchedOff", `"off"`, "currentIndoorTemp", `22.2`), response{"fan_only", 24, "medium"}},
		{"unavailable switch is false", body("canBeSwitchedOff", `"unavailable"`, "currentStateHvacMode", `"off"`), response{"heat", 23, "low"}},
		{"string set temp", body("currentStateTemperature", `"24.0"`), response{"heat", 24, "low"}},
		{"numeric room name", body("room", `42`), response{"heat", 24, "low"}},
	}
	for i, c := range cases {
		b := strings.Replace(c.body, `"olohuone"`, fmt.Sprintf(`"loose%d"`, i), 1)
		rec := ts.do("POST", "/", b)
		var r response
		json.Unmarshal(rec.Body.Bytes(), &r)
		if rec.Code != 200 || r != c.want {
			t.Errorf("%s: status %d got %+v, want %+v (%s)", c.name, rec.Code, r, c.want, rec.Body)
		}
	}

	errs := []struct{ name, body, want string }{
		{"unavailable target", body("targetIndoorTemp", `"unavailable"`), `targetIndoorTemp is missing or unavailable ("unavailable")`},
		{"not a number", body("heatMax", `"warm"`), `heatMax: "warm" is not a number`},
		{"not a boolean", body("canUseFan", `"maybe"`), `canUseFan: "maybe" is not true or false`},
		{"object as number", body("currentIndoorTemp", `{"v": 21}`), "currentIndoorTemp"},
	}
	for _, c := range errs {
		rec := ts.do("POST", "/", c.body)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: status %d body %q, want 400 with %q", c.name, rec.Code, rec.Body, c.want)
		}
	}
}
