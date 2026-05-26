# Context — domain glossary

The shared language of joan-shim. Definitions only — no implementation detail
(wire offsets and byte layouts live in [`docs/`](docs/)). When code names a type
or module after one of these terms, it means *this*.

## PV3

The Visionect device wire protocol the Joan 6 panel speaks over TCP. The shim
reimplements the device-facing side of PV3 so the panel needs no Visionect
cloud. Everything Joan sends or receives on the socket is a PV3 **Message** or a
PV3 **Frame**.

## Message

One thing Joan sends *to* the shim on its connection. Exactly one of:

- **Hello** — the status heartbeat Joan sends roughly every 3 minutes. Carries
  device telemetry (battery, signal, charge).
- **Touch** — a single screen-tap event, carrying panel coordinates. The shim
  treats any tap as "advance the playlist".
- **Image ACK** — Joan's acknowledgement that it rendered a pushed **Frame**.
- **Unknown** — a well-formed but unrecognised message; carried, not an error.

A read failure or a desynced stream is *not* a Message — it is an error.

## Frame

A complete rendered image encoded for the panel: the current display image
converted to the panel's grayscale pixel format and wrapped for PV3 delivery.
The shim pushes a Frame on first contact and whenever the image changes.

## Session ACK

The fixed reply the shim sends to a Joan **Hello** to (re)establish the session
before any **Frame**. Carries the device identity and display parameters.

## Heartbeat

Joan's ~3-minute **Hello** cadence. Between heartbeats the connection is idle;
the shim keeps it open and answers each Hello with a Session ACK (plus a Frame
when the image is new).

## Playlist

The ordered set of screens Terminus rotates through. Each Terminus poll advances
it, so a **Touch** — which triggers an extra poll — shows the next screen.

## Telemetry

The device-health readings carried in a **Hello** (battery voltage, signal
strength, charge percent) that the shim forwards to Terminus so they appear on
its device dashboard.
