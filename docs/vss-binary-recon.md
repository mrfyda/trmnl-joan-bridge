# What we learned from static analysis of the VSS image

The public `visionect/visionect-server-v3:8.5.5-arm` Docker image is a goldmine for understanding how Joan devices talk to VSS. Three shipping mistakes that help us:

1. **Binaries are not stripped and include DWARF debug info** (`networkmanager`, `gateway`, `engine`). Symbol tables expose every Go package, function, and method name with full path. ~73k symbols in `gateway` alone.
2. **`/opt/visionect/vss/bin/dlv` is shipped** — the Delve source-level Go debugger, alongside the production binaries. Can step through any function, set breakpoints, inspect Go types live.
3. **Pre-rendered images at native panel resolutions** sit in `/opt/visionect/vss/images/blocked_device_*.png`. The 1024x758 variant (note: 758, not 768) is the Joan 6 panel — suggesting 10 px of header/footer chrome the protocol doesn't paint.

## Architecture

Wire path for a Joan-class device:

```
[Joan device] --TCP:11112--> [gateway] --gRPC--> [networkmanager] --DB/Redis--> [admin/engine/ac-render]
                                  |
                                  +-- HTTP --> [admin UI on https://*:8081]
```

`gateway` is the wire frontend; `networkmanager` owns device state. The internal gRPC service is `grpc_networkmanager.Networkmanager/DeviceGateway`.

## Two device protocols coexist

- **Legacy "VSS" protocol** — `vss/pkg/ac.HandleVSS`, framed by `vss/pkg/utils/vpacket`. Custom binary; would need reverse engineering.
- **Newer "AC" protocol** — `vss/pkg/ac.HandleCBOR`, `createCBORPacket`. Uses **CBOR (RFC 8949)** for body encoding. Documented format — no need to invent framing, just figure out the schema.

## Command vocabulary

Verbs the protocol understands (literal strings in `gateway`/`networkmanager`):

`SendImage`, `update_fw`, `update_bl`, `Sends device to sleep mode`, `Mobile power saving timeout`, `Mirroring`, `T2S speak`, `flashing`, `Firmware`, `checksum`, `wifi.json`, `status packet`, `heartbeat`, `proximity`, `Server IP`, `engineID`, `engineIP`.

Inferred device boot flow:
1. Device boots with NVRAM-stored `Server IP` / `engineIP`.
2. Fetches `config.json` from server (probably HTTP-style — `getVersion(): Getting config.json` appears in strings).
3. Opens TCP:11112 to gateway.
4. Sends a status packet announcing itself.
5. Server replies with either AC (CBOR) or legacy VSS framed commands.

## Reusable modules inside the image

- **`vss/pkg/driver`** — clean, small (~50 functions). Converts 8 bpp grayscale into panel-specific encoded byte streams. Registered drivers: `eInkGeneric`, `eInkFlip`, `eInk32InchColorMask{,Flip}`, `plGeneric`. Bit-depth converters: `encode_eight_to_one`, `encode_eight_to_four`. If we end up writing a custom server, this is the hardest part to recreate — and it's right there to read.
- **`vss/pkg/utils/vpacket/pkgutil`** — `prependHeader`, `Image.Dump`, `ImageToRectanges`. Wire-level packet format including dirty-rectangle partial updates for e-ink.

## Possible shortcut

The license check that blocked us live-running VSS lives inside the `networkmanager` binary. With Delve already shipped, the check could in principle be NOP'd or stepped over. We haven't pursued this — vendor TOS minefield — but it's a known option if a researcher wanted to capture an authoritative VSS↔Joan transcript.

## How to reproduce the extraction

```sh
mkdir -p /tmp/vss-analysis
container run --rm --entrypoint /bin/sh \
  --mount type=bind,source=/tmp/vss-analysis,target=/host \
  visionect/visionect-server-v3:8.5.5-arm \
  -c 'cp /opt/visionect/vss/bin/{networkmanager,gateway,engine} /host/'

# Functions in the `device` package, etc.
strings /tmp/vss-analysis/gateway | grep -E '^vss/pkg/[a-z]+\.' | sort -u
```
