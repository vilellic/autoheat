# Autoheat v2 — Implementation Plan

This turns [PLAN.md](PLAN.md) into ordered steps. Each step ends with passing
tests (`go test ./...`) before the next one starts.

## Steps

- [x] **1. Scaffold**: Go module, directory layout, YAML dependency.
- [x] **2. Control core** (`internal/control`), pure logic with no I/O:
  - generic types: fan speed ladder, modes, pump state, policy, tunables
  - heating ladder: build it from the policy, locate the observed state on
    it, apply policy fixes
  - estimator: time-based level and slope from samples
  - `Decide` plus per-room state (change tracking, dwell, reason)
  - unit tests, plus invariant tests over generated inputs
- [x] **3. Config** (`internal/config`): load and validate YAML with defaults,
  devices (two-way fan name maps) and per-room overrides, and fail at startup
  on bad config. Tests.
- [x] **4. HTTP API** (`internal/api`):
  - `POST /` with request validation, name mapping, the room registry and a
    structured decision log
  - `GET /status` and `GET /status/{room}`
  - `httptest` tests, including that the response shape exactly matches v1
- [x] **5. Binary** (`cmd/autoheat`): flags, config loading, graceful
  shutdown. Provide `config.example.yaml` and smoke-test it with curl.
- [x] **6. Simulation** (`internal/sim`, `tools/sim`):
  - a simple room model with sensor quantization and HA-like call timing
  - scenario tests: cold start, target raised, fireplace, mild day, manual
    override (two rooms at once is covered by the API tests instead)
  - tune the defaults if the scenarios show a need
- [x] **7. Replay** (`tools/replay`): run the v1 `autoheat.log` inputs
  through `Decide` and summarise the result as a sanity check.
- [x] **8. Packaging**: Dockerfile, docker-compose, the Home Assistant
  `rest_command` and a blueprint, and a README.

Done by hand afterwards (not in code): a shadow run next to v1, then cut
over room by room (PLAN.md §8).

## Outcome

- `go test ./...` covers:
  - **unit tests**
  - **invariants**: 60,000 random decisions, all inside the policy and
    stepping at most one level per dwell
  - **API tests**: the exact v1 response bytes, errors, tolerated input,
    rooms in parallel under `-race`
  - **simulation scenarios** with limits on comfort and number of changes
- **The simulation changed the design:** a further step in the same
  direction now waits a full estimate window. Without it, lag between the
  pump and the room caused double steps. With it, changes fell by about half
  (for example, a raised target went from 3.6 to 1.8 changes/h, a cold snap
  from 2.2 to 1.0) at the same comfort. PLAN.md §3.3 is updated. No new
  tuning value was needed; the defaults are unchanged.
- **Replaying v1's log** (101 calls, open loop): v2 never moves the opposite
  way from v1, changes less often (8 vs 18), and raises the set temperature
  where v1 raised the fan.
- **Docker image:** about 10 MB, `scratch` base, time zone data embedded.
  Verified with a real container.
- **Not verified here:** the Home Assistant blueprint and `rest_command`.
  Their YAML parses and every blueprint input is used, but they need a real
  Home Assistant, ideally during the shadow run.

## Conventions

- Standard library plus one YAML library.
- `log/slog`, one line per decision.
- The control package takes `now time.Time` as an argument and never reads
  the clock, so tests and simulations run instantly and deterministically.
- Temperatures are in °C as `float64`. Set temperatures are whole degrees
  (`int`), truncated like Home Assistant's `| int`.
