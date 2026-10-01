# Autoheat

Room-temperature control for air-to-air heat pumps, driven from Home Assistant.

A heat pump's built-in thermostat measures the air at the indoor unit, not in
the room, so it tends to overshoot and cycle. Autoheat puts a separate room
sensor in charge instead. On each call it decides the pump's **mode**,
**set temperature** and **fan speed**, and Home Assistant applies the answer.

- Heating, with fan-only and off modes when the household allows them
  (e.g. fireplace burning, mild weather)
- Few, calm changes: it acts on the room's trend, not just its current
  temperature
- Quiet first: the fan stays at a quiet base speed at low set temperatures,
  steps up along a configurable curve, and is the first thing lowered
- Respects limits: set temperature range, maximum fan speed, manual changes
- Any number of rooms and heat pump models from one small service
- A single Go binary with one YAML config file, in a ~10 MB Docker image

## How it works

Heating moves one step at a time through set temperature × fan, with idle
(fan only or off, when allowed) below. A fan curve (`fanFrom`) caps the fan
by set temperature:

```
fan \ set    20   21   22   23   24   25   26
high                                       ●
medium_high                                ●
medium                     ●    ●    ●    ●
low          ●    ●    ●    ●    ●    ●    ●
```

A step up raises the fan up to the curve, then the set temperature
(23/low → 23/medium → 24/medium). A step down lowers the fan to the base
speed first, then the set temperature (24/medium → 24/low → 23/low). Each
step changes one of them by one notch, always within the allowed limits.

Autoheat estimates the room temperature and its trend from recent readings.
It projects the trend a little ahead and moves one step up or down when the
room is heading out of a comfort band. Otherwise it holds and returns the
pump's current state, so Home Assistant changes nothing.

## Quick start

```bash
cp config.example.yaml config.yaml    # list your heat pump models' fan names
docker compose up -d --build          # listens on port 8090
```

Then add the `rest_command` and the blueprint to Home Assistant and create an
automation for each room. **[SETUP.md](SETUP.md)** walks through every step.

## API in one line

`POST /` takes the room's temperatures, the pump's current state and the
household's limits, and answers:

```json
{"HVAC_mode":"heat","Temperature":24,"Fan_mode":"low"}
```

`GET /status/{room}` shows the last decision and why it was made.

## Documentation

- [SETUP.md](SETUP.md): installation, Home Assistant, troubleshooting
- [CORE.md](CORE.md): what the program is and the ideas behind it
- [PLAN.md](PLAN.md): the design, including the HTTP contract
- [IMPLEMENTATION.md](IMPLEMENTATION.md): how it was built and verified

## Development

```bash
go test ./...                 # unit, invariant, API and room-simulation tests
go run ./tools/sim            # simulated scenarios, for tuning
go run ./tools/replay v1.log  # compare against a log from the previous version
```

The code is split into:
- `internal/control`: the decision logic, which does no I/O
- `internal/api`: HTTP
- `internal/config`: YAML config
- `internal/sim`: the room model

## License

MIT, see [LICENSE](LICENSE).
