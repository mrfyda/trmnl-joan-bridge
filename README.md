# trmnl-joan-bridge

A standalone Go server that drives a **Joan 6** e-ink display from a
[TRMNL](https://github.com/usetrmnl/byos_hanami) (BYOS) server — with
**no Visionect cloud (VSS) dependency**.

The Joan 6 is a 13" 1024×758 4-bit grayscale e-ink panel with a capacitive
touchscreen. Out of the box it only talks to Visionect's hosted software. This
shim reimplements the device-side wire protocol so the panel can be pointed at a
server you control and show anything you can render to an image — a Home
Assistant dashboard, a TRMNL plugin, a clock, whatever.

```
┌─────────┐   PV3 / TCP:11112   ┌───────────────────┐   HTTP /api/display   ┌──────────┐
│ Joan 6  │ ◀─────────────────▶ │ trmnl-joan-bridge │ ◀───────────────────▶ │  TRMNL   │
│ e-ink   │   image frames      │    (this repo)    │   image_url + rate    │  (BYOS)  │
└─────────┘                     └───────────────────┘                       └──────────┘
```

## How it works

1. **Polls TRMNL** on the standard TRMNL device protocol: `GET /api/display`
   with `ID` (the device MAC) and `Access-Token` headers. TRMNL replies with
   an `image_url` and a `refresh_rate`.
2. **Fetches and encodes** the image into a Visionect **PV3** frame: resize to
   1024×758, convert to 4-bit grayscale, pack 2 px/byte, split into 80
   LZ4-compressed blocks, and wrap with the device descriptor and headers.
3. **Serves the panel** over raw TCP on port 11112. Joan opens a connection and
   sends a status "hello" roughly every 3 minutes; the shim replies with a
   session ACK and, when the image is new, the frame. When only part of the
   screen changed it sends a **partial update** — just the changed rectangle, so
   the panel does a fast, flicker-free local refresh instead of a full repaint; a
   whole-screen change (or the first push after a (re)connect) sends a full frame.
   See [`docs/partial-updates.md`](docs/partial-updates.md).
4. **Reports device health.** The status hello also carries battery voltage and
   WiFi RSSI; the shim parses them and forwards them to TRMNL as the standard
   `Battery-Voltage` and `RSSI` headers, so battery and signal appear in the
   TRMNL device dashboard. See [`docs/status-hello.md`](docs/status-hello.md).

The PV3 wire format, block layout, and session handshake were reverse-engineered
from captured device traffic; see `docs/` for the protocol notes.

## Quick start

The image is published multi-arch (arm64 + amd64) to the GitHub Container
Registry by CI on every push to `main`.

### Docker

```bash
docker run -d --name trmnl-joan-bridge \
  -p 11112:11112 \
  -e TRMNL_SERVER="http://your-trmnl-host:2300" \
  -e DEVICE_ID="AA:BB:CC:DD:EE:FF" \
  -e ACCESS_TOKEN="your-trmnl-device-token" \
  ghcr.io/mrfyda/trmnl-joan-bridge:latest
```

### Portainer / Docker Compose

```yaml
services:
  trmnl-joan-bridge:
    image: ghcr.io/mrfyda/trmnl-joan-bridge:latest
    restart: unless-stopped
    ports:
      - "11112:11112"
    environment:
      TRMNL_SERVER: http://your-trmnl-host:2300
      DEVICE_ID: AA:BB:CC:DD:EE:FF        # Joan MAC, UPPERCASE
      ACCESS_TOKEN: your-trmnl-device-token
```

Then point the panel at the shim with the **Joan Configurator** app: set the
server address to `your-shim-host:11112`.

## Configuration

All configuration is via environment variables (or the equivalent flags).

| Variable           | Required | Default   | Description                                                        |
| ------------------ | -------- | --------- | ------------------------------------------------------------------ |
| `TRMNL_SERVER`     | yes      | —         | TRMNL base URL, e.g. `http://192.168.1.10:2300`                 |
| `DEVICE_ID`        | yes      | —         | Joan MAC address, **uppercase**, e.g. `AA:BB:CC:DD:EE:FF`          |
| `ACCESS_TOKEN`     | yes      | —         | TRMNL device access token                                       |
| `REFRESH_INTERVAL` | no       | `60s`     | Fallback re-fetch interval when TRMNL omits `refresh_rate`      |
| `LISTEN_ADDR`      | no       | `:11112`  | TCP address the panel connects to                                  |

> The `DEVICE_ID` must be uppercase — TRMNL rejects lowercase MACs with
> `Invalid device ID`.

## Building from source

```bash
# Local binary (requires Go 1.22+)
go build -o bin/trmnl-joan-bridge .

# Or via the Makefile, using a container toolchain
make build         # native dev binary  → bin/trmnl-joan-bridge-local
make build-arm     # static linux/arm64 binary → bin/trmnl-joan-bridge

# Docker image
docker build -t trmnl-joan-bridge .
```

## Hardware notes

- **Panel:** Joan 6 — 1024×758, 4-bit grayscale e-ink, capacitive touch.
- **Transport:** Visionect PV3 over TCP port 11112.
- **Rotation:** the encoder applies a fixed 180° rotation to match this panel's
  scan orientation.

## Repository layout

```
main.go          TCP server, TRMNL polling, frame store, heartbeat loop
pv3/             PV3 wire protocol: framing + decode, full & partial frame encode, session ACK
docs/            reverse-engineered protocol notes
Dockerfile       multi-stage build → scratch runtime image
.github/         CI: gofmt/vet/test + multi-arch build & push to ghcr.io
```

## Acknowledgements

Built by reverse-engineering the Visionect device protocol purely for
interoperability, to keep a perfectly good display out of a landfill.
