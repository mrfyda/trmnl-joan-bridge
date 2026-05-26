# Joan 6 device identity

Captured from Joan's own logs (via Joan Configurator app, "Get Device
Information" feature) on 2026-05-24.

## Hardware

```
UUID:              42002800-0d51-3731-3734-393400000000
HW name id:        0x5 ("V Tablet 2 v1.0, BOM 2, APP: Joan")
HW version:        1.0.2
HW firmware iface: 0x0
GTIN:              3830065460078
```

## Display (EPD)

```
Size:              6.0"
Resolution:        1024 x 758
Waveform:          6.0_C276_U2
Driver IC:         6.0_p47000cd0502
Encoding (panel):  0x4 = 4-bit grayscale
                   (from internal `l:` log line — confirms what Joan renders)
```

## Firmware & bootloader

```
FW:        4.12.2775   (build 2019-11-29)
FW CRC32:  0x5E0B9C17
FW SHA-ish hash: 0x3D4CB69B
FW length: 363260 bytes
BL:        4.12.2775   (build 2019-11-29)
```

## Network behavior

```
Protocol:               PV3 (version 3)
Heartbeat interval:     3 minutes
Connectivity stack:     CC3100 WiFi module (TI)
TCP target:             Whatever IP:port is configured in Joan Configurator
                        → Advanced Connectivity → Server IP / Server Port
Local IP (current AP):  192.168.1.218  (DHCP, will vary)
```

## Touch

```
Type:                   0x2
Touch FW version:       18.243
EVT count:              15
```

## Battery

```
Level:                  100% (charging)
Voltage:                4196 mV
Current:                61 mA
```

## File system (on-device)

```
Total:                  130988 bytes
Free:                   130988 bytes  (empty as of S09)
```

That's only ~128 KB of file storage. So image files pushed via the File
protocol must be small enough to fit alongside firmware reserved space.

## Important error code

```
PV2 error: 0x00210000   = Joan's rejection of our 3-packet sequence
                          (session 09; cause still being investigated)
PV2 error: 0x00220000   = variant seen once; likely a sibling protocol error
```

## The 88-byte "introduction" packet — what it is

When Joan boots fresh, it sometimes sends an 88-byte packet to the server
before the regular 456-byte Status hello. The 88-byte payload contains the
**FW CRC inline** (bytes 8..11 = `17 9c 0b 5e` LE = `0x5e0b9c17`, matches
`PV2_FW_CRC` from logs). So it's a **device identity / capability
announcement**. We previously treated it as "mystery."
