# Packet types (the "type" field in the outer header)

Extracted by disassembling each gateway handler's `Type()` method in the
public `visionect/visionect-server-v3:8.5.5-arm` Docker image (one-instruction
returns of the type constant).

| Type | Handler                  | Source            | What it carries                                    |
|------|--------------------------|-------------------|----------------------------------------------------|
| **3** | `PV3StatusHandler`      | `status.go:83`    | Device → server status/announce (our hello)         |
| 6    | `pv3TouchHandler`        | `touch.go:54`     | Touchscreen events                                  |
| 7    | `pv3GPSHandler`          | `gps.go:67`       | GPS coordinates                                     |
| **8** | `PV3ParamHandler`       | `param.go:51`     | Get/set device parameters (uint16-keyed key-value)  |
| **10** | `PV3FileHandler`       | `filesystem.go:60`| File / image transfer — **stateful 4-step sequence**|
| 11   | `pv3ButtonHandler`       | `button.go:43`    | Physical button press                               |
| 12   | `pv3CBORHandler`         | `cbor.go:58`      | Modern CBOR-encoded protocol alternative            |

"PV3" = Protocol Version 3 (Visionect's internal name).

## Direction

The handler's `Type()` value is the type ID it *receives* on incoming frames.
By convention server-to-device packets use the same type ID. So:

- Joan announces itself with `type=3` → we receive a Status frame.
- We push an image to the Joan with `type=10` → it routes to its file handler.

## Source files

All under `/code/go/src/vss/cmd/gateway/handlers/` (path visible via DWARF in
the binary; not present on the image's filesystem).

## What we know works

- Joan reliably sends `type=3` Status packets (we have ~100 captures).
- Reply with `type=3, version=0, len=0` (header-only) is accepted as a no-op.
- Any reply with `len=0` for types 4, 8, 10 is rejected with a ~4-5s fast EOF.

## What we don't know yet

- The body schemas for types 6, 7, 8, 10, 11, 12. Type 10's stateful sequence
  is documented in `proto-package.md`.
