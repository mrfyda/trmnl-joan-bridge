# Status hello — device telemetry (PV3 type 3)

Joan opens a TCP connection and sends a **Status hello** (PV3 type 3) on every
heartbeat (~3 min). Besides announcing the device, the hello body carries live
telemetry — battery, signal, temperature, filesystem — as a key-value list.

## Body layout

After the 20-byte outer header, the body is a flat list of `[key:u32][value:u32]`
little-endian pairs starting at **body offset `0x34`**, terminated by a key of
`0xffffffff` followed by a 4-byte CRC32 trailer. The 456- and 532-byte hello
variants share this layout; the 532-byte one just appends extra keys.

Parse **by key, not by fixed offset** — keys are sparse (14, 1c, 21 … are absent)
but always appear in ascending order.

## Field map

Identified by diffing 19 hellos across 6.8 h: constants stayed fixed, dynamic
sensors drifted. Cross-checked against the device's own "Get Device Information"
readout (see [device-identity.md](device-identity.md)).

| key (hex / dec) | field | notes |
| --- | --- | --- |
| 0x03 / 3 | firmware CRC | `0x5E0B9C17` — constant |
| 0x0a / 10 | **battery %** | 0..100. VSS dashboard "Battery 20%" = key 10 = 20 |
| 0x0c / 12 | **temperature (°C)** | VSS dashboard "Temperature 25°C" = key 12 = 25 |
| 0x0d / 13 | **RSSI** | value = `\|dBm\|`; report negative. VSS "Signal -54" = key 13 = 54 |
| 0x12, 0x15 / 18, 21 | firmware build | 2775 — constant (FW 4.12.2775) |
| **0x22 / 34** | **battery voltage (mV)** | 3898 → 3702 over the capture; *rose* while charging |
| 0x23 / 35 | charge current (mA) | 954 charging → 0 once unplugged |
| 0x26, 0x27 / 38, 39 | panel width, height | 1024, 758 — constant |
| 0x32–0x35 / 50–53 | GTIN | "3830065460078" as 4-char ASCII chunks |
| 0x3c, 0x3d / 60, 61 | filesystem free, total | 130988 bytes |

The voltage⇄current correlation pins down keys 34 and 35: voltage *climbs* while
current ≈ 950 mA (charging), then *falls* the instant current hits 0 (unplugged).

Keys 10, 12 and 13 were confirmed against the live VSS admin dashboard for this
device — Battery 20%, Temperature 25°C, Signal -54 — matching keys 10, 12, 13
exactly. (Time-series guesswork alone had keys 10 and 13 swapped: key 10's
apparent "fast swing" was the battery gauge correcting when the charger was
unplugged, the same instant key 35 current went 954→0.)

## Forwarding to TRMNL (BYOS)

TRMNL reads device telemetry from request headers on `GET /api/display`. The
contract, from byos_hanami `app/schemas/firmware/header.rb` and
`app/aspects/firmware/headers/model.rb`:

| HTTP header | type | persisted device field |
| --- | --- | --- |
| `ID` (**required**) | MAC | mac_address |
| `Access-Token` | string | api_key |
| `Battery-Voltage` | float (**volts**) | battery_voltage |
| `RSSI` | int (**dBm**) | wifi |
| `Percent-Charged` | float | battery_charge |
| `FW-Version` | version | firmware_version |
| `Width` / `Height` | int | width / height |
| `Model` | string | model_name |
| `Refresh-Rate` | int | refresh_rate |

**Validation is strict and fail-closed:** a header that fails its type check makes
the entire `/api/display` action return **404**, so the device would get no image.
The shim therefore only emits well-formed values, and only emits battery/RSSI once
a real hello has been parsed and range-checked (2000–5000 mV, RSSI ≤ 120).

**Sent by the shim:** `ID`, `Access-Token`, `Battery-Voltage` (key 34 ÷ 1000),
`RSSI` (−key 13), `Percent-Charged` (key 10), `Width` (1024), `Height` (758),
`Refresh-Rate` (poll cadence).

**Deliberately not sent:**
- `FW-Version` — risks the `Types::Version` check (→ 404) and feeds TRMNL's
  firmware-update comparison; cosmetic here, since the shim never forwards
  TRMNL's `firmware_url` to Joan (Joan only ever receives PV3 image frames).

## Touch events (type 6)

Tapping the screen sends a **76-byte device→server packet** — one per tap (no
separate down/up). It carries the device serial, an event-type marker **`6` at
body+20** (u16 LE), and coordinates **X at body+48, Y at body+52** (u16 LE):

| tap (user view) | X (body+48) | Y (body+52) |
| --- | --- | --- |
| top-left | ~970 | ~770–820 |
| top-right | ~50 | ~730 |
| bottom-left | ~930 | ~10 |
| bottom-right | ~67 | ~70 |
| centre | ~505 | ~420 |

Coordinates are in the panel's native orientation, **flipped 180°** from the
displayed image (high X = user left, high Y = user top): `userX ≈ Xmax − rawX`,
`userY ≈ Ymax − rawY`, with X ≈ 0–1024 and Y ≈ 0–~820 (the digitizer runs a
little taller than the 758 display). For touch *zones* the raw values suffice;
for exact pixels, linear-fit from corner taps.

**Tap → next playlist item:** TRMNL's `/api/display` rotator advances the
playlist on every poll (unless the playlist is `manual`). So the shim treats any
touch as "advance": on a 76-byte packet it re-polls TRMNL (rotating to the
next screen, re-encoding) and pushes the new frame on the same connection —
~2 s tap-to-render. The coordinates aren't used for this (any tap advances).
