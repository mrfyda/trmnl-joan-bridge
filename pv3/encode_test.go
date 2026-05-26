package pv3

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"testing"
)

func gradient(w, h int) image.Image {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetGray(x, y, color.Gray{Y: uint8((x + y) % 256)})
		}
	}
	return img
}

func TestEncodeFrameStructure(t *testing.T) {
	out := EncodeFrame(gradient(300, 200))

	if len(out) < headerLen+20+len(preamble)+24 {
		t.Fatalf("frame too short: %d", len(out))
	}
	// Outer ProtocolHeader: [Version=3, Security=0, Compression=1, Length, CRC32].
	if got := binary.LittleEndian.Uint32(out[0:]); got != 3 {
		t.Errorf("Version = %d, want 3", got)
	}
	body := out[headerLen:]
	if got := binary.LittleEndian.Uint32(out[12:16]); int(got) != len(body) {
		t.Errorf("Length = %d, actual body = %d", got, len(body))
	}
	if got, want := binary.LittleEndian.Uint32(out[16:]), crc32.ChecksumIEEE(body); got != want {
		t.Errorf("Checksum = %08x, want %08x", got, want)
	}
	// ImageHeader: [Checksum, NrPrimitives, Options, PayloadLength, Reserved].
	if got := binary.LittleEndian.Uint32(body[4:]); got != numBlocks {
		t.Errorf("NrPrimitives = %d, want %d", got, numBlocks)
	}
	// Options = pre-header region length − 4 = (preamble + RectangleHeader + raw tail) − 4.
	wantOpts := uint32(len(preamble)+24+preHdrRawSz) - 4
	if got := binary.LittleEndian.Uint32(body[8:]); got != wantOpts {
		t.Errorf("Options = %d, want %d", got, wantOpts)
	}
	if got := binary.LittleEndian.Uint32(body[12:]); got != blockSz {
		t.Errorf("PayloadLength = %d, want %d", got, blockSz)
	}
}

// EncodeFrame is fully deterministic for identical input (no per-frame nonce).
func TestEncodeFrameDeterministic(t *testing.T) {
	img := gradient(64, 64)
	if a, b := EncodeFrame(img), EncodeFrame(img); !bytes.Equal(a, b) {
		t.Error("EncodeFrame not deterministic for identical input")
	}
}

// The full-frame pre-header (preamble + RectangleHeader) built by buildPreHeader
// must stay byte-identical to the 84-byte header captured from a real VSS frame —
// the device validates the size fields, and the rest is device-specific. Guards
// the region-encoder refactor against drifting the on-the-wire full frame.
func TestFullFramePreHeaderGolden(t *testing.T) {
	golden := mustDecodeHex(
		"0000000000000000420028000d513731373439340000000005000000" +
			"b3f1a71a2cec050000000000b0e099a0010000000000000018ec0500" +
			"00000000010000000000000000" +
			"04f602020100000400000000ec0500")
	if got := buildPreHeader(fullScreen, nil); !bytes.Equal(got, golden) {
		t.Errorf("full-frame pre-header changed:\n got  %x\n want %x", got, golden)
	}
}
