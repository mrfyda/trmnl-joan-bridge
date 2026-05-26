package pv3

import (
	"encoding/binary"
	"hash/crc32"
	"testing"

	lz4 "github.com/pierrec/lz4/v4"
)

const fbSize = PanelW * PanelH / 2

// paintRegion sets the packed bytes of fb that back the panel rectangle (x,y,w,h)
// to a varied non-zero pattern, using the same scan-start column mapping as the
// encoder, so EncodePartial recovers exactly this rectangle.
func paintRegion(fb []byte, x, y, w, h int) {
	rowBytes := PanelW / 2
	for r := 0; r < h; r++ {
		for cb := 0; cb < w/2; cb++ {
			pc := (x/2 + cb - colStartByteOffset + rowBytes) % rowBytes
			fb[(y+r)*rowBytes+pc] = byte(0x10 + (r+cb)%200)
		}
	}
}

// extractRegion reads region (x,y,w,h) out of a packed framebuffer the same way
// the encoder does — the reference the decoded frame must reproduce.
func extractRegion(fb []byte, x, y, w, h int) []byte {
	rowBytes := PanelW / 2
	out := make([]byte, w*h/2)
	for r := 0; r < h; r++ {
		for cb := 0; cb < w/2; cb++ {
			pc := (x/2 + cb - colStartByteOffset + rowBytes) % rowBytes
			out[r*(w/2)+cb] = fb[(y+r)*rowBytes+pc]
		}
	}
	return out
}

func TestEncodePartialRoundTrip(t *testing.T) {
	const x, y, w, h = 256, 120, 256, 200 // even x/w; w*h/2 = 25600 > preHdrRawSz
	prev := make([]byte, fbSize)
	next := make([]byte, fbSize)
	copy(next, prev)
	paintRegion(next, x, y, w, h)

	frame, ok := EncodePartial(prev, next)
	if !ok {
		t.Fatal("EncodePartial returned ok=false for a valid mid-size change")
	}

	// Outer ProtocolHeader.
	if v := binary.LittleEndian.Uint32(frame[0:]); v != 3 {
		t.Fatalf("Version = %d, want 3", v)
	}
	body := frame[20:]
	if got := binary.LittleEndian.Uint32(frame[12:]); int(got) != len(body) {
		t.Fatalf("Length = %d, body = %d", got, len(body))
	}
	if got, want := binary.LittleEndian.Uint32(frame[16:]), crc32.ChecksumIEEE(body); got != want {
		t.Fatalf("CRC = %08x, want %08x", got, want)
	}

	nprim := binary.LittleEndian.Uint32(body[4:])
	opts := binary.LittleEndian.Uint32(body[8:])
	if pl := binary.LittleEndian.Uint32(body[12:]); pl != blockSz {
		t.Errorf("ImageHeader.PayloadLength = %d, want %d", pl, blockSz)
	}
	preHdrLen := int(opts) + 4
	pre := body[20 : 20+preHdrLen]

	// Preamble payload-size fields must track the region — the device validates
	// these and drops the frame if they still read the full-frame 388096.
	if got, want := binary.LittleEndian.Uint32(pre[32:]), uint32(w*h/2+44); got != want {
		t.Errorf("preamble[32] = %d, want %d", got, want)
	}
	if got, want := binary.LittleEndian.Uint32(pre[52:]), uint32(w*h/2+24); got != want {
		t.Errorf("preamble[52] = %d, want %d", got, want)
	}

	// RectangleHeader sits right after the 60-byte preamble.
	rh := pre[60:84]
	gotIT := binary.LittleEndian.Uint16(rh[0:])
	gotX := int(binary.LittleEndian.Uint16(rh[4:]))
	gotY := int(binary.LittleEndian.Uint16(rh[6:]))
	gotW := int(binary.LittleEndian.Uint16(rh[8:]))
	gotH := int(binary.LittleEndian.Uint16(rh[10:]))
	gotRUO := binary.LittleEndian.Uint16(rh[12:])
	gotEnc := binary.LittleEndian.Uint16(rh[16:])
	gotPL := binary.LittleEndian.Uint32(rh[20:])
	if gotIT != 1 || gotEnc != 4 || gotRUO != 0x0102 {
		t.Errorf("rect ImageType/Enc/RUO = %d/%d/0x%04x, want 1/4/0x0102", gotIT, gotEnc, gotRUO)
	}
	if gotX != x || gotY != y || gotW != w || gotH != h {
		t.Errorf("rect = (%d,%d,%d,%d), want (%d,%d,%d,%d)", gotX, gotY, gotW, gotH, x, y, w, h)
	}
	if gotPL != uint32(w*h/2) {
		t.Errorf("rect PayloadLength = %d, want %d", gotPL, w*h/2)
	}

	// Tail is the last preHdrRawSz bytes of the pre-header.
	tail := pre[84:]
	if len(tail) != preHdrRawSz {
		t.Fatalf("tail len = %d, want %d", len(tail), preHdrRawSz)
	}

	// Walk + decompress the blocks; region = blockData ++ tail must equal the
	// region extracted straight from next.
	blocks := body[20+preHdrLen:]
	var blockData []byte
	off := 0
	for i := 0; i < int(nprim); i++ {
		csz := int(binary.LittleEndian.Uint32(blocks[off+8:]))
		rsz := int(binary.LittleEndian.Uint32(blocks[off+12:]))
		comp := blocks[off+24 : off+24+csz]
		if csz == rsz {
			blockData = append(blockData, comp...)
		} else {
			dst := make([]byte, rsz)
			n, err := lz4.UncompressBlock(comp, dst)
			if err != nil {
				t.Fatalf("block %d decompress: %v", i, err)
			}
			blockData = append(blockData, dst[:n]...)
		}
		off += 24 + csz
	}
	if off != len(blocks) {
		t.Errorf("blocks ended at %d, region len %d", off, len(blocks))
	}

	region := append(append([]byte(nil), blockData...), tail...)
	want := extractRegion(next, x, y, w, h)
	if len(region) != len(want) {
		t.Fatalf("reassembled region %d bytes, want %d", len(region), len(want))
	}
	for i := range region {
		if region[i] != want[i] {
			t.Fatalf("region byte %d = %02x, want %02x", i, region[i], want[i])
		}
	}
}

func TestEncodePartialFallbacks(t *testing.T) {
	fb := make([]byte, fbSize)

	// No change → ok=false.
	if _, ok := EncodePartial(fb, fb); ok {
		t.Error("expected ok=false when nothing changed")
	}

	// Whole-screen change → too large → ok=false.
	big := make([]byte, fbSize)
	for i := range big {
		big[i] = 0xFF
	}
	if _, ok := EncodePartial(fb, big); ok {
		t.Error("expected ok=false for a whole-screen change")
	}

	// Wrong size → ok=false.
	if _, ok := EncodePartial(fb[:10], big); ok {
		t.Error("expected ok=false for mismatched framebuffer size")
	}
}
