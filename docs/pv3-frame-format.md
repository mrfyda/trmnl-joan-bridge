# PV3 frame format (Joan 6 / Visionect)

The device-side PV3 wire protocol is not publicly documented. This is the frame
format the shim implements, reverse-engineered from the VSS binaries (which ship
unstripped, with DWARF) — Go modules `bill.vnct.xyz/vss/proto@v1.2.23` and
`bill.vnct.xyz/vss/lz4`. See [vss-binary-recon.md](vss-binary-recon.md) for how
the binaries were obtained and inspected.

## Frame layout

```
ProtocolHeader (20)  +  ImageHeader (20)  +  pre-header  +  80 × dataBlock
```

Everything after the ProtocolHeader is its `Length` bytes; the ProtocolHeader
`Checksum` is `CRC32(body)`.

## Structs (exact layouts from DWARF)

**ProtocolHeader** — 20 B, the outer/transport header
| off | field | value |
|----|-------|-------|
| 0 | Version | **3** (PV3) |
| 4 | Security | 0 (unencrypted) |
| 8 | Compression | **1** (LZ4) |
| 12 | Length | body length |
| 16 | Checksum | CRC32(body) |

**ImageHeader** — 20 B
| off | field | value |
|----|-------|-------|
| 0 | Checksum | 0 |
| 4 | NrPrimitives | 80 (dataBlocks) |
| 8 | Options | pre-header length − 4 |
| 12 | PayloadLength | 4800 (block/chunk size) |
| 16 | Reserved | 1 |

**dataBlock** — 24-B header + data: `BlockID`(u32, 1-based), `BlockLast`(u32,
= NrPrimitives), `compSize`(u32), `rawSize`(u32), 8 B pad, then `compSize` bytes
of LZ4 data. Walking 80 of these consumes exactly `Length`.

**RectangleHeader** — 24 B, the **last 24 bytes of the pre-header header** in
every frame (not just partials): `ImageType`(u16, 1=Gray), `ScreenID`(u16),
`X`(u16), `Y`(u16), `Width`(u16), `Height`(u16), `RectangleUpdateOptions`(u16,
0x0102 for 4-bit), `Options`(u16), `Encoding`(u16, 4), `Reserved`(u16),
`PayloadLength`(u32, = `Width*Height/2`). It is the region the payload paints —
the whole screen (`0,0,1024,758`) for a full frame, a sub-rectangle for a partial
update. See [partial-updates.md](partial-updates.md).

**DataHeader** — 36 B: `Priority`, `UUID` (16 B), `Type`, `ID`, `Length`.

## Compression

`vss/lz4.Lz4Compress` is a CGO wrapper over the stock LZ4 C library
(`LZ4_compress` / `LZ4_decompress_safe`) — plain **stateless LZ4, no dictionary**.

## Encode pipeline (in VSS)

```
CompressPacket(packet) → Marshall(packet) → ToBlocks: chunk into PayloadLength
(4800)-byte pieces, LZ4 each → dataBlocks.
```

## Block / pixel coverage

The panel is 1024×758 at 4-bit grayscale = **388096 bytes** (2 px/byte). The 80
`dataBlock`s cover `packed[0:383376]` (79×4800 + 4176 = the top 749 rows); the
**pre-header carries the remaining 4720 bytes** = `packed[383376:388096]`, which
the device places at the end of the framebuffer (the bottom ~9 rows after the
fixed 180° rotation).

## The pre-header

```
pre-header = 60-byte preamble + RectangleHeader (24 B) + tail pixels (raw, 4720 B)
```

The 60-byte preamble carries the device UUID
(`42 00 28 00 0d 51 37 31 37 34 39 34`), the `PacketImage` type (5), two nonce
fields the device does **not** validate, and two **payload-size fields it does
validate** (byte 32 = payload + 44, byte 52 = payload + 24; payload = the region
byte count). The `RectangleHeader` (above) is the region descriptor; the tail is
the last 4720 bytes of the region's pixels (the part the blocks don't cover).
`ImageHeader.Options` = the pre-header region length − 4.

## How the shim builds a frame

`pv3/encode.go` regenerates the pre-header every frame: `preHeaderHdr84` (the
84-byte header, fixed for this device) + the image's **real** tail pixels
`packed[383376:388096]` sent raw, with `Options = len − 4`. The 80 blocks are
LZ4-compressed; the tail is raw (the device accepts a raw tail — VSS itself sends
raw for incompressible content). This is why the bottom ~9 rows show live content
rather than a frozen blob.
