# Setting up Autoheat

This takes you from nothing to a room controlled by Autoheat:

1. [Run the service](#1-run-the-service)
2. [Prepare Home Assistant](#2-prepare-home-assistant)
3. [Connect a room](#3-connect-a-room)
4. [Check that it works](#4-check-that-it-works)

It also covers [moving from v1](#moving-from-v1),
[updating](#updating), [tuning](#tuning) and
[troubleshooting](#troubleshooting).

**You need:**
- A machine on the same network as Home Assistant, running Docker (or Go
  1.25+ to run it without Docker)
- Home Assistant 2024.10 or newer
- For each room: the heat pump as a `climate` entity, and a room
  temperature sensor away from the heat pump

## 1. Run the service

### Configure your heat pump models

```bash
cp config.example.yaml config.yaml
```

Under `devices:`, each heat pump model maps five generic fan speeds to the
names that model uses. To find a model's names, open **Developer tools →
States**, select the climate entity, and look at the `fan_modes` attribute.

```yaml
devices:
  mitsubishi:            # the name you will give in Home Assistant
    fanModes:
      quiet: quiet       # generic speed: the model's own fan_mode name
      low: low
      medium: medium
      medium_high: medium_high
      high: high
```

- All five speeds must be listed, each with a different name.
- Modes like `auto` or `strong` can be left out.
- The `defaults` and `rooms` sections hold tuning values. The built-in values
  are a good start, so leave them for now.

### Start it

```bash
docker compose up -d --build
docker logs autoheat
```

You should see `msg=listening ... devices="[gree mitsubishi]"`. The service
listens on port **8090** and refuses to start if the config is invalid; the
log tells you why.

*Without Docker:*

```bash
go run ./cmd/autoheat -config config.yaml -addr :8090
```

### Test it

```bash
curl -s -X POST http://localhost:8090/ -H 'Content-Type: application/json' -d '{
  "room": "test", "device": "mitsubishi",
  "targetIndoorTemp": 21.5, "currentIndoorTemp": 21.0,
  "currentStateHvacMode": "heat", "currentStateTemperature": 23, "currentStateFanMode": "low",
  "heatMin": 20, "heatMax": 26, "maxAllowedFanSpeed": "high",
  "canUseFan": false, "canBeSwitchedOff": false}'
```

The answer should be `{"HVAC_mode":"heat","Temperature":24,"Fan_mode":"low"}`:
the room is 0.5 °C cold, so the service steps the set temperature up.

## 2. Prepare Home Assistant

### Helpers for each room

The automation reads these helpers, which you or your own automations can
change at any time. Create them in **Settings → Devices & services →
Helpers**, or in YAML:

```yaml
input_number:
  olohuone_target:            # wanted room temperature
    name: Living room target
    min: 16
    max: 25
    step: 0.5
    unit_of_measurement: "°C"
  olohuone_set_min:           # lowest set temperature Autoheat may use
    name: Living room min set temperature
    min: 10
    max: 31
    step: 1
    initial: 20
  olohuone_set_max:           # highest set temperature Autoheat may use
    name: Living room max set temperature
    min: 10
    max: 31
    step: 1
    initial: 26

input_select:
  olohuone_max_fan:           # noise limit, e.g. lower at night
    name: Living room max fan
    options: [quiet, low, medium, medium_high, high]

input_boolean:
  olohuone_fan_only_allowed:  # e.g. the fireplace is burning
    name: Living room fan only allowed
  olohuone_off_allowed:       # e.g. mild weather
    name: Living room off allowed
```

- **Target:** a `sensor` works too, such as a template that lowers the target
  at night.
- **The two switches** can be `binary_sensor`s driven by your own logic
  instead of `input_boolean`s, for example "fireplace temperature above 40
  °C" or "outdoor temperature above 10 °C".

### The REST command

Add this once to `configuration.yaml`; all rooms share it. Replace
`AUTOHEAT_HOST` with the IP address of the machine running Autoheat. The
complete file is [deploy/homeassistant/rest_command.yaml](deploy/homeassistant/rest_command.yaml).

```yaml
rest_command:
  autoheat:
    url: "http://AUTOHEAT_HOST:8090/"
    method: POST
    content_type: "application/json"
    payload: >-
      { "room": {{ room | tojson }}, "device": {{ device | tojson }}, ... }
```

Restart Home Assistant (**Developer tools → YAML → Check configuration**,
then restart).

### The blueprint

Copy [deploy/homeassistant/autoheat_room.yaml](deploy/homeassistant/autoheat_room.yaml)
to `/config/blueprints/automation/autoheat/autoheat_room.yaml` in Home
Assistant. You can use the File editor or Studio Code Server add-on, Samba,
or SSH:

```bash
scp deploy/homeassistant/autoheat_room.yaml root@HA_HOST:/config/blueprints/automation/autoheat/
```

Then reload automations (**Developer tools → YAML → Automations**), or
restart.

## 3. Connect a room

Go to **Settings → Automations & scenes → Blueprints → Autoheat room →
Create automation**, and fill in:

| Field | Example |
|---|---|
| Room name | `olohuone` (unique per room; matches `rooms:` in the config) |
| Device | `mitsubishi` (a name under `devices:` in the config) |
| Heat pump | `climate.olohuone` |
| Room temperature sensor | `sensor.olohuone_temperature` |
| Target temperature | `input_number.olohuone_target` |
| Minimum / Maximum set temperature | `input_number.olohuone_set_min` / `…_set_max` |
| Maximum fan speed | `input_select.olohuone_max_fan` |
| Fan only allowed / Off allowed | `input_boolean.olohuone_fan_only_allowed` / `…_off_allowed` |
| Apply the answer | on (off only for a [shadow run](#moving-from-v1)) |

The automation calls Autoheat when the room temperature changes, every 5
minutes, and when a helper changes. It applies only values that differ from
the pump's current state, and only after a successful answer.

Repeat for each room.

## 4. Check that it works

- **Logs:** `docker logs -f autoheat` shows one line per decision with its
  reason, e.g. `command=heat/25/low changed=true reason="too cold: step up"`.
- **Status:** `curl http://AUTOHEAT_HOST:8090/status/olohuone` shows the last
  input, the temperature estimate and the last decision.
- **Automation traces:** in Home Assistant, open the automation → ⋮ →
  **Traces** to see each run and what was applied.

Right after a start, Autoheat has no history for about 10 minutes. It then
acts on the current temperature alone, without a trend.

## Moving from v1

The v1 automation, its stored `lastDelta` helper, the derivative sensor and
the throttle template are no longer needed. Try v2 alongside v1 first:

1. **Give v2 its own port and name** if v1 runs on the same machine. In
   `docker-compose.yml`, set `container_name: autoheat-v2` and ports
   `"8091:8080"`, and use port 8091 in the `rest_command` URL.
   `rest_command.autoheat` does not clash with v1's `autoheat_2go`.
2. **Shadow run:** create the room's automation with **Apply the answer**
   off. v2 then decides and logs, but changes nothing.
3. **Compare** `docker logs autoheat-v2` with v1's log for a few days.
4. **Cut over:** switch **Apply the answer** on and disable the v1
   automation.

## Updating

- **Code:** pull the new version, then run `docker compose up -d --build`.
- **Config:** edit `config.yaml`, then run `docker compose restart autoheat`;
  no rebuild is needed.
- **Blueprint:** copy the new file over the old one and reload automations.
  Existing automations pick up the change.

## Tuning

The defaults suit a typical living room. To tune one room, add overrides
under `rooms:` in `config.yaml`:

```yaml
rooms:
  olohuone:
    dwellMinutes: 20      # wait longer between changes
    baseFan: quiet        # quieter while the set temperature has headroom
```

[config.example.yaml](config.example.yaml) explains every value. To see how a
change behaves before trying it on the house, run it through the simulated
scenarios:

```bash
go run ./tools/sim -params '{dwellMinutes: 20}'
```

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| Log: `config.yaml is a directory` | `config.yaml` did not exist when the container first started, so Docker created a directory. Run `docker compose down`, `rmdir config.yaml`, `cp config.example.yaml config.yaml`, then start again. |
| `docker compose up`: `bind source path does not exist` | Same cause, now caught early: create `config.yaml` from the example. |
| Answer `400 unknown device "…"` | The automation's *Device* doesn't match a name under `devices:`. The error lists the configured names. |
| Answer `400 … missing or unavailable` | A sensor or the heat pump is unavailable. Nothing is applied, and the next call after it recovers works normally. |
| Log warning: `fan mode "…" unknown for device` | The pump reports a fan name that isn't in `fanModes` (e.g. `auto`). It works, but check the mapping. |
| Nothing changes on the pump | Check the automation traces. Common causes: **Apply the answer** is off, or the answer was not `200`. The status page's `reason` may also say `waiting`, which is normal: it waits between changes. |
| `docker compose up`: port is already allocated | Another service (often v1) uses 8090. Change the host port in `docker-compose.yml` and in the `rest_command` URL. |
| Build error after editing files on another machine | A folder sync may still be in progress, leaving old and new files mixed. Let it finish and build again. |
