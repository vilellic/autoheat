# Autoheat — Core Description

This describes what the program *is* and the principles behind it. It is meant
as the starting point for a fresh rewrite, so it covers the ideas and leaves
out the implementation details.

---

## 1. What it is

Autoheat is a **room-temperature controller for an air-to-air heat pump**.
It keeps one room (e.g. a living room) at a wanted temperature. It does this
by deciding, over and over, how the heat pump should be set right now:

- **Mode**: heat, fan only, or off
- **Set temperature**: the heat pump's own set point
- **Fan speed**: from quiet to high

It does not drive the hardware. It works next to **Home Assistant**, which
reads the sensors, asks Autoheat what to do, and applies the answer to the heat
pump's climate entity.

## 2. The problem it solves

The heat pump's own thermostat does a poor job of holding a room temperature:

- It measures temperature **at the indoor unit** (warm air, high on the wall),
  not where people are. Its set point has no reliable link to the actual room
  temperature.
- It is coarse: whole-degree set points, and on its own it tends to overshoot
  and cycle.
- It knows nothing about the household: noise tolerance, a fireplace that is
  burning, or a mild day when heating could stop.

Autoheat fixes this with an **external room sensor** and a control loop that
sits **on top of** the heat pump's own control.

## 3. Core idea: an outer control loop

```
  wanted room temp ─┐
                    ▼
  room sensor ──▶ [ Autoheat ] ──▶ mode / set point / fan ──▶ [ heat pump's own thermostat ] ──▶ room
       ▲                                                                                        │
       └────────────────────────────────────────────────────────────────────────────────────────┘
```

The key insight: **the heat pump's set point is a power lever, not a target.**
When the room is too cold, the set point is pushed up so the pump works harder.
When the room is warm enough, it is pulled down. The actual goal (the wanted
room temperature) lives only in Autoheat.

## 4. Levers and constraints

**Levers**, from least to most noticeable:

| Lever | What it changes | Notes |
|---|---|---|
| Set temperature | Heating power | Whole degrees, within an allowed min–max range |
| Fan speed | How fast heat spreads, and noise | Ordered steps (quiet → high), capped by a maximum |
| Mode | Heat / fan only / off | Fan only and off are used only when allowed |

**Constraints / policies** come from the household (via Home Assistant helpers)
and can change at any time:

- **Allowed set-point range**: never command below the minimum or above the maximum.
- **Maximum fan speed**: noise limit (e.g. lower at night).
- **Fan only allowed**: for example, the fireplace is burning. The heat pump
  then just moves the fireplace's heat around the house instead of heating.
- **Off allowed**: for example, mild weather, when the room can hold
  temperature without the pump running.

## 5. How it decides (principles)

1. **Error = room temperature − wanted temperature.** Everything depends on
   this value and on which way it is trending.
2. **Three zones:** *too cold*, *near target*, *too warm*. The zones are
   **asymmetric**: a slight overshoot is tolerated more than being cold,
   because comfort comes first and stopping/starting is costly.
3. **Hold when close and stable.** If the error is small and the room
   temperature is not moving, change nothing.
4. **Too cold → add heat:** make sure the mode is heat and raise the set point.
   **Keep the fan quiet at low set points**, because the fan is noisier. The
   fan goes up with the set point along a configured curve (for example,
   medium from a set point of 23), and it is the first thing lowered when
   there is too much heat.
5. **Too warm → remove heat,** using the gentlest allowed option first:
   switch to fan only if allowed, otherwise off if allowed, otherwise lower the
   set point and the fan speed.
6. **Near target → small nudges, driven by the trend.** Act against the trend
   (drifting down → nudge up, drifting up → nudge down). Don't push if the room
   is already moving the right way.
7. **Returning to heat** from fan only or off starts from a known, moderate
   baseline setting.
8. **Always respect the limits:** set point inside its range, fan speed at or
   below the maximum. If the current setting is outside the limits, fix that
   first, regardless of anything else.

## 6. Stability principles

A heat pump reacts slowly and the room reacts even more slowly, so the most
important non-functional goal is **few, calm changes**:

- **Smooth the measurement** so sensor noise does not trigger actions.
- **Dead band** around the target where nothing happens.
- **Minimum time between changes**, set per lever: the set point changes less
  often than the fan.
- **Override the minimum time when needed:** a large error, or a current
  setting outside the allowed limits.
- **Trend awareness:** look at the direction and speed of the temperature
  change, not only its value.
- **Detect a persistent offset:** if the room stays slightly off target for a
  long time, act even inside the "near" zone.

## 7. System context (current)

```
 Home Assistant                                   Autoheat (Go, HTTP, Docker on LAN)
 ─────────────────────────────────────            ───────────────────────────────────
 automation triggers on sensor change   ──POST──▶  receives: target, room temp, current
 (with its own debounce / throttle)                 pump state, limits, permissions
                                                    keeps in-memory history (smoothing,
                                                    timers, trend)
 applies returned mode / set point / fan ◀─JSON──   returns: wanted mode / set point / fan
 to the climate entity, stores last delta
```

- The service is **advisory and request-driven**. It only runs when Home
  Assistant calls it.
- The service logs every decision (inputs, derived metrics, what changed and
  why). A status endpoint exposes its internal state for debugging.
- It runs as a small container on the home network.

## 8. The Home Assistant automation

The automation that calls the service is part of the system's behaviour, so its
logic is recorded here. The copy in this repo (`ha_autoheat.yaml`) is older than
the live version: it lacks the triggers for the two permission switches.

### 8.1 What it does today

**When it calls the service:**

| Event | Behaviour |
|---|---|
| Room temperature changes | Throttled: runs only if at least 5 min have passed since the last run, **or** the temperature is changing fast (per a Home Assistant derivative sensor) |
| Target temperature changes | Always runs, once the value has been stable for 10 s |
| Limits change (min/max set temp, max fan) | Always runs, once the value has been stable for 10 s |
| Permissions change (fan only allowed, off allowed) | Always runs, immediately |

- There is **no periodic trigger**. While the room temperature is steady,
  nothing calls the service; the logs show gaps of up to about 45 minutes.
- It skips the call if the room temperature or the target is unavailable.
  The heat pump entity is **not** checked.
- It runs **one call at a time**: triggers that arrive while a run is in
  progress are dropped.

**What it sends:** measurements (room temperature, target), the pump's
observed state (mode, set temperature, fan name as the pump reports it), and
the policy (limits and permissions). The room and entities are hard-coded, and
the automation even refers to itself by name in its throttle template.

**How it applies the answer**, in this order:
1. **Mode**, if it differs from the pump's current mode.
2. **Set temperature**, if it differs (compared as whole degrees) and the new
   mode is heat. It is never sent in fan_only or off.
3. **Fan**, if it differs from the pump's current fan name and the new mode is
   not off.

The response status is **not checked**. An error response makes the actions
fail partway through.

**Bookkeeping:** after each run it stores the raw
`room temperature − target`, so the next call can see the trend.

### 8.2 What the new program must support

1. **Output values must match what Home Assistant compares against.**
   - Mode: a Home Assistant mode name (`heat`, `fan_only`, `off`).
   - Fan: the pump's own fan name, exactly as Home Assistant reports it.
     Otherwise it is resent on every call.
   - Set temperature: whole degrees.
2. **"No change" means returning the observed state.** Home Assistant writes
   only differences, so holding costs nothing.
3. **Some output fields are ignored:** the set temperature in fan_only/off,
   and the fan in off. Return the observed values there, so the answer and
   the logs stay stable.
4. **The observed state is the truth.** An answer may be skipped, only partly
   applied, or overridden by hand. Manual changes to the pump do not trigger a
   call, so they are seen only at the next call.
5. **Call timing is irregular**, from a few seconds apart (bursts of policy
   changes) to nearly an hour apart (steady room).
   - All time-based logic must use elapsed time.
   - Calling more often must not change the behaviour.
   - The service cannot act on its own. Anything that waits, such as a
     minimum time between changes, happens only at the next call. The
     automation therefore needs a **periodic trigger** (for example, every
     5 min).
6. **Policy changes must take effect on that call.** The user expects an
   immediate reaction. At minimum, a setting that breaks the new policy is
   corrected right away, without waiting.
7. **The service owns the trend.** The stored delta, the derivative sensor and
   the throttle template can all go from Home Assistant.
8. **Bad input must fail safely.** Home Assistant checks only the room
   temperature and the target. The pump entity can be unavailable, its fan
   name missing, or its set temperature silently replaced by a default (21).
   - The service must reject such input explicitly.
   - The automation must check the response status before applying anything.
9. **Each room needs its own set of entities:**
   - room temperature sensor
   - target
   - heat pump (climate) entity
   - min and max set temperature
   - max fan speed
   - fan-only-allowed and off-allowed switches

   For multiple rooms, the automation should be a reusable template (for
   example a blueprint), not one copy per room with hard-coded names.
10. **Concurrency:** each room sends one call at a time, but different rooms
    call in parallel. Per-room state must be safe under concurrent requests.

## 9. Device abstraction

Heat pump brands name their fan speeds differently. The controller reasons
with a **generic, ordered fan-speed ladder**, and a per-device mapping turns it
into the device's own names. A new device should need only a small config
file, not code changes.
