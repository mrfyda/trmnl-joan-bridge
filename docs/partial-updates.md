# Partial updates (e-ink dirty-rectangle) — reverse-engineering notes

**Status: investigated, NOT implemented (S33).** Partial updates are confirmed to
work and the architecture is mapped, but a from-scratch partial encoder is a
multi-session reverse-engineering effort. Banked — revisit only if flicker-free
updates become a priority. The shim currently sends full frames (which already
compress well for sparse content).

## What a partial update is

Instead of repainting the whole 1024×758, VSS sends only the changed **dirty
rectangle**; Joan composites it onto the existing framebuffer using a fast,
flicker-free e-ink waveform (no full-screen flash). The device config flag
"Enable partial updates" must be on (it was, by default).

## Confirmed from MITM captures

- **It's real and huge.** On an incompressible (random-noise) background, a full
  push is ~390 KB; changing a small box and re-pushing sends only **~4 KB**
  (~100×). Proof: `captures/partial2.log` (`noise_A` full 390 KB → `noise_B`
  partial 4 KB). On a *simple* (white) background you can't tell partial from
  full because both compress tiny — that earlier red herring is why we used noise.
- **Trigger:** a small region differs from the *previously displayed* frame.
  Whole-screen changes → full update; small changes → partial.
- **Region = the box bounding box** (only with the *correct display rotation* set
  on VSS — wrong orientation made the dirty-region tiling land oddly). Rounded
  outward to a tile grid (e.g. box 240×200 → region ~264×216).
- **Wire shape:** fewer blocks than full (e.g. `num_blocks` 1/4/5/9/10 instead of
  80), same `[idx:u32][num_blocks:u32][comp:u32][raw:u32][pad:8]` block-header
  format as full frames. Region width sets bytes/row; `block_size`=4800; the
  blocks tile the region. `field2` (pre-header size) varies with region content.

## Binary analysis (the deterministic source)

Extract the VSS binaries (unstripped, DWARF, Delve shipped — see
[vss-binary-recon.md](vss-binary-recon.md)):

```sh
container run --rm --entrypoint /bin/sh \
  --mount type=bind,source=/tmp/vss-analysis,target=/host \
  vss-patched:local \
  -c 'cp /opt/visionect/vss/bin/{gateway,engine,networkmanager} /host/'
# disassemble in a golang container:
go tool objdump -s '<symbol-regex>' gateway
```

Key symbols (in `gateway`):

| Symbol | Source | What it does |
|--------|--------|--------------|
| `vss/common.Display.RectangleTranslateToReal` / `...ToSession` | `common@v1.1.0/display-translations.go` | session↔panel coordinate transform. **Rotation-aware:** swaps w/h for rotation 1/3 (90°/270°), not 0/2 (0°/180°); applies offset + clamps to display bounds. Our panel is 180° → flip, no swap. |
| `vss/proto.CreateRectangles` | `vss/proto` | computes the dirty rectangle(s) — tiling/merging/alignment |
| `vss/imgproc.CutRectangle` | `vss/imgproc` | crops a region out of an image |
| `vss/proto.RectangleHeader` (+ `.String`) | `vss/proto` | the **PV3-side** rectangle header — disassemble its marshalling next |
| `vss/proto/v2/packet.(*RectangleHeader).MarshalBinaryTo` / `.Size` / `.UnmarshalBinary` | `proto/v2@v2.1.5/packet/image.go` | a **v2** RectangleHeader: 9× u16 (bytes 0–17) + 2 pad + u32 datalen (20–23); byte 12 = bit-depth flag (1→0x101, 4→0x102). ⚠️ **The captured PV3 frames do NOT use this format** — their block headers are `[idx][n][comp][raw]`. v2 ≠ PV3. |

## Still blocked (needs a dedicated session)

- Exact **PV3 wire offset** of the rectangle (x, y, w, h). Not at a fixed
  descriptor offset; serial-relative alignment shifts between frames; solid-color
  vs noise regions appear to encode differently. The clean `serial+56` read that
  worked for one noise partial — `(392,38,264,216)` for box `(380,480,240,200)` —
  did not generalize.
- The **full transform formula** (needs the complete `RectangleTranslateToReal`
  trace + the `helpers.go` min/max clamps).

## Next steps for a future effort

1. Disassemble `vss/proto.RectangleHeader` marshalling + `vss/proto.CreateRectangles`
   (the PV3 path, not v2).
2. Fully trace `Display.RectangleTranslateToReal` → exact formula.
3. Find the PV3 frame builder (grep symbols for the PV3 image/state encoder that
   assembles ProtocolHeader + descriptor + blocks) — that's where the rectangle
   field is written.
4. Implement: diff successive images → dirty rect → transform → crop & encode the
   region as blocks → write the rectangle into the frame.

## Captures (local, gitignored)

- `captures/partial2.log` — noise full (390 KB) + noise+box partial (4 KB): the proof.
- `captures/partial3.log`, `captures/partial4.log` — box sequences at known
  positions (A/B/C) for decoding the rectangle fields.

## Value assessment

Niche. The win (flicker-free + ~100× smaller) only matters for a **busy/complex
background with small changes** (e.g. a photo wallpaper + live clock). For typical
sparse dashboards, full frames already compress well and a full refresh every
~15 min is fine.
