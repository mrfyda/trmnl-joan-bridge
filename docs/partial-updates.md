# Partial updates (e-ink dirty-rectangle)

**Status: implemented and live.** The shim sends a partial update — only the
changed rectangle — when a new image differs from what the Joan currently shows in
a small region, and a full frame otherwise. `pv3.EncodePartial` builds the frame;
the `serve` loop decides partial-vs-full. Validated on the real panel: image-ACK +
flicker-free localized refresh, with a replayed real VSS partial as a control.

## The format

A partial update is the *same* frame as a full image, with a smaller embedded
rectangle. In VSS, full and partial go through one encoder
(`render.DisplaysEncoder.generateImagePacketUnlocked` → `isFullScreenUnlocked` /
`getRectsAndClear` / `forceFullScreen`); the only difference is the rectangle
list — a full update is one rectangle covering the whole screen.

The 84-byte "pre-header header" is a **60-byte preamble + a 24-byte
`RectangleHeader`** (see [pv3-frame-format.md](pv3-frame-format.md)). For a full
frame the rectangle is the whole screen:

```
ImageType=1(Gray) ScreenID=0 X=0 Y=0 Width=1024 Height=758
RUO=0x0102 Options=0 Encoding=4 Reserved=0 PayloadLength=388096   # = 1024*758/2
```

A partial just shrinks it. Compared to `EncodeFrame`, a partial frame differs in:

1. The `RectangleHeader` carries the sub-region `X,Y,Width,Height` with
   `PayloadLength = Width*Height/2`.
2. The blocks carry only the region's pixels, so far fewer than 80.
3. `ImageHeader.NrPrimitives` = that block count.
4. The preamble's payload-size fields are rewritten for the region (below).

Everything else — preamble shape, ImageHeader, ProtocolHeader, LZ4 block format
`[BlockID][BlockLast][compSize][rawSize][pad8]`, CRC32 — is identical.

### Region pixel layout

The region is **region-packed**: `H` rows × `W/2` bytes, row-major, stride `W/2`
(not the panel's 512). It is read from the same packed framebuffer a full frame
uses, applying the colStartOffset scan shift — region byte `(r, cb)` is:

```
framebuffer[(Y+r)*512 + ((X/2 + cb − 112) mod 512)]    # 112 = colStartOffset/2 = 224/2
```

Rows do not wrap; columns wrap at 512 (the −224 px shift). There is **no extra
rotation** between region and framebuffer — the 180° + colStartOffset is baked
equally into full and partial frames.

### The tail/block split (the "4720 constant")

For *every* frame, `PayloadLength (= W*H/2) == 4720 + Σ block rawSize`. The region
payload = pre-header tail (the last 4720 bytes) ++ the block stream (the rest),
chunked at rawSize 4800 — identical to the full frame (tail 4720, blocks cover the
first 383376). A multi-rect capture tiles three regions back-to-back in this
payload with **zero leftover**, confirming it byte-exactly.

### Coordinate transform (panel is 180°)

From `common.Display.RectangleTranslateToReal`, rotation 2 (180°), full-panel
display, a dirty rect `(rx,ry,rw,rh)` in image space maps to:

```
X = 1024 − rx − rw    Y = 758 − ry − rh    Width = rw    Height = rh
```

(a 180° point-reflection, clamped to panel bounds). The shim diffs directly in
packed/panel space, so this transform is implicit in `EncodePartial`.

### Field values

`ImageType=1` (Gray), `Encoding=4` (4-bit), `RectangleUpdateOptions=0x0102`
(`0x0101` for 1-bit), `Options=0`, `ScreenID=0`, `Reserved=0`.
`ImageHeader.Options = pre-header length − 4`.

**Preamble payload-size fields (mandatory):** the device validates these and
silently drops the frame if they are wrong. Byte 32 (u32) = `payload + 44`, byte
52 (u32) = `payload + 24` (`payload = W*H/2`); byte 44 (u32) = rect count (1).
Derived by diffing the full (1-rect) and multi-rect (3-rect) captured preambles —
the one non-obvious requirement, found by comparing a generated frame against a
replayed real partial on the device.

## How the shim uses it

`pv3.EncodePartial(prev, next)` diffs two packed framebuffers, finds the dirty
bounding box, and emits a region frame — or returns `ok=false` (→ full frame) for
a whole-screen change or a region too small to fill the 4720-byte tail. The
`serve` loop tracks the framebuffer the device has actually rendered (updated only
on an image-ACK, reset per connection), so the first push of a connection is full,
a reconnect re-syncs with a full, and a partial is only ever diffed against a frame
the device is known to be showing. Identical re-polls are skipped entirely, so
unchanged content no longer triggers a full-screen refresh.

## How it was reverse-engineered

The VSS binaries ship unstripped with DWARF (see
[vss-binary-recon.md](vss-binary-recon.md)). The `RectangleHeader` layout, the
coordinate transform, and the field values came from disassembling the symbols
below. The pixel layout, the 4720 split, and the preamble size-field requirement
were then confirmed by MITM-capturing real VSS partials (one full + four partials)
and decoding them; the captures and decode scripts are in
`exploration/captures-partial-re/` (gitignored).

| Symbol (in `gateway`) | What it does |
|---|---|
| `common.Display.RectangleTranslateToReal` | session↔panel coordinate transform (rotation-aware; 180° = flip, no w/h swap) |
| `vss/proto.CreateRectangles` | builds the rectangle list; sets RUO from encoding (4-bit → 0x0102) |
| `render.DisplaysEncoder.generateImagePacketUnlocked` | the real device-bound encoder; full-vs-partial dispatch |

## Value

The win — flicker-free, and a fraction of the bytes — matters most for a busy
background with small, localized changes (a dashboard with a live clock or a
single changing widget). When a change touches most of the screen `EncodePartial`
falls back to a full frame, and unchanged content is skipped, so the shim never
sends a partial that wouldn't help.
