# Autoheat — Plan

**Fixed decisions**
- Called from Home Assistant over HTTP, with the same response shape as today.
- One instance serves multiple rooms, each with its own heat pump.
- Heating only.

---

## 1. Goals

1. Keep each room close to its target temperature with **few, calm changes**.
2. Use **one simple mechanism for each concern**, so behaviour is easy to
   understand and tune.
3. The service **owns all control state and timing**. Home Assistant only
   triggers the call and applies the answer.
4. Keep the decision logic a **pure, testable function**, so it can be run
   against unit tests, a room simulation and replays of recorded logs.

Non-goals: cooling, talking to Home Assistant directly, and any UI.

## 2. HTTP contract

### `POST /`: decide for one room

The path is kept so the existing `rest_command` URL keeps working.

**Request**: the current field names are kept, so migrating Home Assistant is
small. `room` is new; `lastDelta` is removed (and ignored if sent).

```json
{
  "room": "olohuone",
  "device": "mitsubishi",
  "targetIndoorTemp": 21.5,
  "currentIndoorTemp": 21.25,
  "currentStateHvacMode": "heat",
  "currentStateTemperature": 24,
  "currentStateFanMode": "medium",
  "heatMin": 20,
  "heatMax": 26,
  "maxAllowedFanSpeed": "high",
  "canUseFan": false,
  "canBeSwitchedOff": false
}
```

- `currentStateFanMode` uses the **device's own name**. It is mapped back to
  the generic fan-speed ladder through the device config. Today this mapping
  is missing, which is a bug.
- `maxAllowedFanSpeed` uses a generic speed name. All names are
  case-insensitive.

**Response**: the same shape as today. `Fan_mode` uses the device's own name.

```json
{"HVAC_mode": "heat", "Temperature": 25, "Fan_mode": "low"}
```

**Errors**: return `400` with a plain-text reason, and Home Assistant applies
nothing. Causes:
- missing `room` or `device`, or an unknown `device`
- room or target temperature missing, `unavailable`, or not a number
- heat pump entity `unavailable`/`unknown`
- set temperature missing while in heat, or fan mode missing while the pump
  is on. Neither is needed when off, where Home Assistant ignores them.
- `heatMin > heatMax`
- unknown `maxAllowedFanSpeed`

Tolerated, with a warning in the log:
- An observed mode other than heat / fan_only / off (for example `cool` or
  `auto`) is treated as **off**.
- An observed fan name not in the mapping is treated as the **base fan**. It
  is echoed back unchanged until Autoheat changes the fan, so for example
  `auto` is left alone while holding.

This keeps control running if someone changes the pump by hand.

### `GET /status` and `GET /status/{room}`

These return each room's internal state for debugging: the estimate, trend,
last decision and its reason, and the time of the last change.

## 3. Control design

### 3.1 The heating ladder

The idea: one question, *"how much heat do we want?"*, answered as a position
on an ordered ladder. The ladder is rebuilt on every call from the request's
policy fields, so policy changes take effect immediately.

```
 idle        fan_only if canUseFan, else off if canBeSwitchedOff, else not present
 heat 20  low       ┐
 heat 21  low       │ set temperature goes up first (quiet)
 ...                │
 heat 26  low       ┘
 heat 26  medium    ┐
 heat 26  medium_high│ fan goes up only once the set temperature is at heatMax
 heat 26  high      ┘ (capped by maxAllowedFanSpeed)
```

This single structure covers several of today's rules:
- It is always inside the policy limits.
- It raises the set temperature before the fan on the way up.
- It lowers the fan before the set temperature on the way down.
- It uses idle only when allowed.

The *base fan* (`low` above) is set per room in config.

*Changed after v2 was in use:* keeping the fan at base up to `heatMax` meant a
room needing a set temperature of 24 ran at 24/low when 23/medium would
spread the heat better. The single list became steps through set
temperature × fan, with a per-room **fan curve** (`fanFrom`) giving the
highest fan a step up uses at each set temperature:

```
 fan \ set    20   21   22   23   24   25   26      fanFrom: {medium: 23, medium_high: 26}
 high                                       ●
 medium_high                                ●
 medium                     ●    ●    ●    ●
 low          ●    ●    ●    ●    ●    ●    ●
```

- **Up:** raise the fan while it is below the curve, otherwise the set
  temperature. At `heatMax` the curve is `maxAllowedFanSpeed`.
- **Down:** lower the fan while it is above base, otherwise the set
  temperature, then go idle. So the fan is removed first when there is too
  much heat, and the way down is not the way up.
- Every step changes one of set temperature and fan by one notch, so every
  step changes the heat the same way, whatever the pump's characteristics.
- A curve left empty gives the original ladder, apart from the way down.
- *Why the way down differs:* with the same path both ways, the simulation
  settled between levels such as 24/medium and 25/medium, whose output
  straddles the room's need, and oscillated (about twice the changes). Going
  down through 25/low and 24/low adds levels in between, and changes fell back
  to the original ladder's level.

### 3.2 Estimating the room temperature

For each room, keep the **samples from the last N minutes** (time, temperature).
A linear fit over them gives:
- **level**: the smoothed temperature now
- **slope**: °C per hour

The fit handles irregular call timing naturally. With too few samples, use the
latest reading and slope 0.

- **Error:** `e = level − target`
- **Predicted error:** `ê = e + slope × lookahead`

Using the predicted error lets one number cover several of today's mechanisms:
the zones, the direction check, velocity, and the dead band.
- It does not push when the room is already moving toward the target.
- It acts early when the room is drifting away.

### 3.3 Decision (per call)

1. **Policy fix:** if the observed state breaks the policy, correct only
   what is wrong, **immediately** and without waiting for the dwell. Examples:
   set temperature out of range, fan above the maximum, or fan_only/off no
   longer allowed. A pump that is off stays off while off is allowed, even
   if fan only becomes allowed too (as in v1). Fan only is preferred only
   when going idle from heat.
2. **Continue from the (fixed) state,** so a fix and a step can happen in
   the same call.
3. **Want:**
   - `ê < −coldBand` → one step **up**
   - `ê > +warmBand` → one step **down**
   - otherwise **hold**
4. **Shortcuts:**
   - When idle and wanting heat, go straight to the **resume level**
     (`resumeSetTemp`, base fan). A step up from it waits only the normal
     dwell, not a full window. *Changed after the fan curve:* the resume
     level and the step after it (23/low, 23/medium) can both give almost
     no heat, and waiting a window between them let the room dip too far
     on the evening of `mild-day`.
   - When clearly too warm (`e ≥ idleBand`) and idle is allowed, go straight to
     **idle**. This is the fireplace / sunny-day case.
5. **Dwell:**
   - Take at most one step per `dwell` period, counted from the pump's last
     change (commanded, or observed from outside, such as by hand).
   - Use a shorter `urgentDwell` when `|ê|` is large (for example, the target
     was just raised), and there is new evidence: the room is that cold now
     (`e ≤ −urgentError`), or `ê` has got worse since the last change.
     *Changed after the simulation:* with `|ê|` alone, a slope that the
     pump had not yet answered kept `|ê|` large for several calls. Each
     urgent step then went the same way before the room responded, sweeping
     the whole ladder (e.g. 24/low → 26/high → 24/low about every 100 min
     in `target-raised`). Requiring new evidence leaves one urgent step per
     reading of the trend. Being too warm is not urgent by itself, because a
     slight overshoot is tolerated (§5 of CORE.md).
   - **A further step in the same direction** waits a full estimate window
     (`window`), so the trend reflects the previous step before taking
     another. *Added after the simulation:* without it, the lag between pump
     and room caused double steps and roughly twice as many changes.
   - Steps from a state off the curve (e.g. a fan set by hand) follow the
     same rules, so up never lowers the fan and down never raises it.
6. **Hold** returns the observed state unchanged, so there are no pointless
   writes.
7. **Fan in fan_only mode** is the room's `circulationFan`, capped by the
   maximum fan speed. In off mode, the set temperature and fan are passed
   through as observed; Home Assistant ignores them.

*Optional, added only if real use shows a need:* an **offset timer**. If the
error stays on the same side of the target for a long time, count it as outside
the band.

### 3.4 Per-room state

- the samples window
- the last observed pump state
- the time of the last change (commanded or observed)
- the last decision and its reason

State is kept in memory, in a map with a mutex. After a restart there is a
short warm-up while samples build up. Persisting state to disk is left for
later.

## 4. Configuration

One YAML file, mounted into the container. New devices or rooms need no
rebuild.

```yaml
defaults:            # starting values; tune using the simulation and replay
  windowMinutes: 30
  lookaheadMinutes: 20
  coldBand: 0.2      # °C below target
  warmBand: 0.3      # °C above target
  idleBand: 0.5      # jump straight to idle above this
  dwellMinutes: 15
  urgentDwellMinutes: 5
  urgentError: 0.5
  resumeSetTemp: 23  # clamped into heatMin..heatMax
  baseFan: low
  circulationFan: medium
  fanFrom: {medium: 23, medium_high: 26}

devices:             # generic speed → the device's own fan-mode name
  mitsubishi:
    fanModes: {quiet: quiet, low: low, medium: medium, medium_high: medium_high, high: high}
  gree:
    fanModes: {quiet: low, low: medium low, medium: medium, medium_high: medium high, high: high}

rooms:               # optional per-room overrides of `defaults`
  olohuone: {baseFan: low}
  mokki:    {dwellMinutes: 20}
```

- A room that is not listed uses the defaults. A room's state is created on
  its first call.
- Config is validated at startup, and the service refuses to start if it is
  invalid.

## 5. Home Assistant side

- **One `rest_command`**, with templated fields filled from `data:`
  variables, reused by every room.
- **One automation per room.** Better still, ship a **blueprint** where you
  pick the climate entity, sensors and helpers.
- **Triggers:**
  - room sensor change (no throttle)
  - every 5 min (**new**: without it, nothing calls while the room is steady)
  - target or limit change, after a 10 s settle (as today)
  - permission switch change, immediately (as today)
- **Dropped:** the throttle templates, the derivative sensor and the stored
  `lastDelta` helper. The service rate-limits itself and can safely be called
  often.
- **Kept:** `mode: single`, and skipping the call when the room temperature
  or target is unavailable.
- **Apply step**: as today (only changed values; no set temperature in
  fan_only/off; no fan in off), plus a check that the call returned `200`.

## 6. Project layout

```
cmd/autoheat/main.go      wiring: config, HTTP server, graceful shutdown
internal/control/         pure logic, no I/O
  ladder.go               steps up and down within the policy, along the fan curve
  estimate.go             time-based level and slope from samples
  decide.go               Decide(state, input, now) → (output, state, reason)
internal/config/          load and validate the YAML
internal/api/             HTTP handlers, JSON, device name mapping, room registry
tools/sim/                simple room thermal model for tuning and scenario tests
tools/replay/             run old/new logs through Decide and compare
deploy/                   Dockerfile, compose, HA blueprint and rest_command example
```

- Dependencies: the standard library plus one YAML library.
- Logging: `log/slog`, one structured line per decision (room, inputs,
  estimate, ê, level before/after, reason).

## 7. Testing

- **Unit:** ladder steps and paths, the estimator with irregular
  samples, and a table of `Decide` cases.
- **Invariants,** checked for every generated input:
  - the output is always inside the policy limits
  - at most one ladder step per dwell period, except for the defined shortcuts
  - every heat step changes one of set temperature and fan by one notch
- **Scenarios** with the room simulation: cold start, target raised, fireplace
  lit, a mild day, a manual override, and two rooms at once. Check that each
  settles without oscillating.
- **Replay** the existing `autoheat.log` as a sanity check.

## 8. Milestones

1. `internal/control` with unit tests and invariants.
2. Config, the HTTP API, status and logging.
3. The simulation and replay tools; tune the defaults.
4. Packaging: Docker, the Home Assistant blueprint and README.
5. **Shadow run:** HA calls v2 alongside v1 but ignores v2's answer; compare
   the logs for a few days.
6. Cut over, room by room.

## 9. Choices made in this plan (easy to change)

| Choice | Picked | Alternative |
|---|---|---|
| Request field names | Keep today's names, add `room` | Rename to a cleaner schema |
| Device selection | `device` in the request | Fixed per room in config |
| Unknown room | Auto-created with defaults | Must be declared in config |
| State across restarts | Memory only, short warm-up | Persist to a JSON file |
| Unknown observed mode / fan | Tolerate and log | Reject with an error |
