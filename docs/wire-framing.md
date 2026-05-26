# Wire framing — Joan 6 ↔ server

## Transport

- **TCP, plaintext.** No TLS on the firmware we have (4.12.2775). First byte of every
  device-initiated frame is `0x03`, never `0x16` (TLS ClientHello).
- The device dials whatever IP:port is configured via the desktop **Visionect
  Configurator** under *Advanced connectivity → Server IP / Server port*. The
  Configurator's UI default port is `11113`, but we set it to `11112` to match our
  adapter's listener.
- Connection cycle when the server is unresponsive: open TCP, send a 456-byte
  "hello" packet, wait ~16s, EOF. The device retries every ~5-15s while it has
  battery.

## Outer 20-byte fixed header — ASYMMETRIC by direction

**Heads-up:** the device and the server use DIFFERENT 20-byte header formats.
This was discovered in session 06 by disassembling `vpacket/pkgutil.prependHeader`
and verifying against the captures.

### Device → Server (incoming)

```
offset  size  field             notes
------  ----  ----------------  ----------------------------------------
+0x00    4    type   (uint32 LE)  packet kind — see reference/packet-types.md
+0x04    4    version (uint32 LE) sub-type / protocol revision
+0x08    4    flags  (uint32 LE)  reserved (always 0 in observed packets)
+0x0c    4    length (uint32 LE)  number of payload bytes that follow
+0x10    4    dev_id (uint32 LE)  device-stable lo-32 of a session/device ID
                                  (constant across packets in same session)
```

### Server → Device (outgoing) — per `prependHeader` disassembly

```
offset  size  field             notes
------  ----  ----------------  ----------------------------------------
+0x00    4    uint32 LE = 2     (constant)
+0x04    4    uint32 LE = 0     (constant)
+0x08    4    uint32 LE = 1     (constant — protocol marker?)
+0x0c    4    body length
+0x10    4    CRC32-IEEE of body
```

The server's outgoing header uses constants `[2, 0, 1, len, CRC32(body)]`.
**There is no dev_id stamp in server→device packets** — and no separate "type"
field either. The device must dispatch on body content.

Both formats have total frame = `20 + length`.

### Why we got this wrong for sessions 2-5

We assumed the same format for both directions and built every reply with
`[type, ver, flags=0, len, dev_id_echo]`. Every empty-body reply got fast-rejected,
every File-formed reply got fast-rejected. Session 06's prependHeader analysis
suggests we were sending the wrong outer framing on every attempt.

In `joan-hello-v2.bin` the dev_id `0x56a14c5d` coincidentally equals
`CRC32-IEEE(header[0..16])`, which initially looked like evidence the field was
a CRC. Verified against `joan-first-connect-v1.bin` (same dev_id, different
header bytes, CRC doesn't match): the value is truly a device-static ID,
the hello-v2 CRC match was a 1-in-4-billion coincidence.

## CRC trailer

Some packet bodies end with a 4-byte CRC32 trailer.

- Algorithm: **standard CRC32-IEEE** (`hash/crc32.ChecksumIEEE` in Go terms).
  Confirmed by disassembling `vss/pkg/utils/vpacket/pkgutil.prependHeader` —
  it CALLs `hash/crc32.ChecksumIEEE` at `packet.go:39`.
- The Joan's 456-byte hello ends with `8a 43 3e 91` which fits this format.
- Empirically, *not* appending a CRC to our header-only replies didn't change the
  Joan's behavior — so the CRC may be optional or only required for certain
  packet types. Verify per-type.

## Key constants observed in captures

| Constant | Meaning |
|---|---|
| `0x56a14c5d` | This specific device's `dev_id_lo` (4-byte session ID — stable across reboots in our captures). |
| `0x5e0b9c17` | Appears in both the 456-byte hello body and the 88-byte mystery packet. Probably a Visionect firmware build hash / magic. |
| `0xffffffff` | "End-of-list" sentinel inside structured bodies. |

## Sender-side reference (server → device)

The VSS server queues outbound bytes through a per-device channel:
`xsync.Map[uuid [16]byte, chan []byte]`. A separate goroutine drains each
channel to the device's TCP socket. So "send a packet" at the application level
means "put bytes onto this channel".

## Reply behavior table (observed)

All replies tested were 20-byte headers with `flags=0, len=0, dev_id` echoed.

| Reply `(type, version)` | Disconnect after | Reading |
|---|---|---|
| (no reply) | 16s | Joan's idle "waiting for command" timeout |
| (3, 0) | **~12s** | Parsed as a valid status no-op, polite wait, normal close |
| (3, 1) | ~4.5s | Parsed, decode error, fast bail |
| (3, 2) | ~12s | Same as (3, 0) — version field tolerated |
| (4, 0) | ~4s | Unknown type, fast bail |
| (8, 0) | ~4s | Empty Param body, decode error, fast bail |
| (10, 0) | ~4s | Empty File body, decode error, fast bail |
| (10, 0) + CRC32 trailer | ~4s | Same as above — CRC presence didn't help an empty body |

**Conclusion**: header-only replies can't produce useful device behavior on any
type except 3-as-no-op. To trigger anything you need real body content matching
the type — see `reference/proto-package.md` for the body type names.
