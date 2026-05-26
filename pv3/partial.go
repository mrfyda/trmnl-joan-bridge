package pv3

// colStartByteOffset is colStartOffset expressed in packed bytes (2 px/byte): a
// region's panel column maps to a packed-framebuffer column shifted by this,
// wrapping at the row width — the inverse of the scan shift toPackedGray4 applies.
const colStartByteOffset = colStartOffset / 2 // 112

// maxPartialFraction is the largest dirty area (as a fraction of the framebuffer,
// over 10) still worth sending as a partial; above it a full frame is simpler and
// no larger.
const maxPartialFraction = 6

// EncodePartial builds a partial-update PV3 frame that repaints only the bounding
// box of the bytes that changed between prev and next — both packed gray4
// framebuffers (PanelW*PanelH/2 bytes) as produced by Pack. It is encodeRegion
// over the dirty rectangle: the same frame as EncodeFrame but with a sub-region
// RectangleHeader and only the region's pixels, which the device composites onto
// its current framebuffer with a flicker-free waveform.
//
// Returns ok=false — the caller falls back to EncodeFrame — when nothing changed,
// when the dirty box spans too much of the screen, or when the region is too small
// to fill the fixed tail. See docs/partial-updates.md.
func EncodePartial(prev, next []byte) (frame []byte, ok bool) {
	r, ok := dirtyRect(prev, next)
	if !ok {
		return nil, false
	}
	return encodeRegion(readRegion(next, r), r), true
}

// dirtyRect returns the panel-space bounding box of the bytes that differ between
// prev and next, or ok=false when there is no change worth a partial (nothing
// changed, too large, or too small to fill the tail). A packed byte at index i
// sits at row i/rowBytes and packed column i%rowBytes; the panel byte-column is
// (packed column + colStartByteOffset) mod rowBytes — undoing the scan shift.
func dirtyRect(prev, next []byte) (rect, bool) {
	fbSize := PanelW * PanelH / 2
	rowBytes := PanelW / 2 // 512
	if len(prev) != fbSize || len(next) != fbSize {
		return rect{}, false
	}
	minRow, maxRow, minCol, maxCol := PanelH, -1, rowBytes, -1
	for i := 0; i < fbSize; i++ {
		if prev[i] == next[i] {
			continue
		}
		row := i / rowBytes
		col := (i%rowBytes + colStartByteOffset) % rowBytes
		if row < minRow {
			minRow = row
		}
		if row > maxRow {
			maxRow = row
		}
		if col < minCol {
			minCol = col
		}
		if col > maxCol {
			maxCol = col
		}
	}
	if maxRow < 0 {
		return rect{}, false // nothing changed
	}
	r := rect{x: minCol * 2, y: minRow, w: (maxCol - minCol + 1) * 2, h: maxRow - minRow + 1}
	if p := r.payload(); p > fbSize*maxPartialFraction/10 || p <= preHdrRawSz {
		return rect{}, false
	}
	return r, true
}

// readRegion extracts rectangle r's packed pixels from a framebuffer, undoing the
// scan-start shift so region byte (row, cb) reads framebuffer[(y+row)*rowBytes +
// ((x/2 + cb − colStartByteOffset) mod rowBytes)]. Returns r.payload() bytes,
// row-major with stride r.w/2.
func readRegion(packed []byte, r rect) []byte {
	rowBytes := PanelW / 2
	wb, xb := r.w/2, r.x/2
	out := make([]byte, r.payload())
	for row := 0; row < r.h; row++ {
		base, dst := (r.y+row)*rowBytes, row*wb
		for cb := 0; cb < wb; cb++ {
			pc := (xb + cb - colStartByteOffset + rowBytes) % rowBytes
			out[dst+cb] = packed[base+pc]
		}
	}
	return out
}
