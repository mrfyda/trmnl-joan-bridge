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

	if len(out) < headerLen+20+68 {
		t.Fatalf("frame too short: %d", len(out))
	}
	// Outer header: [3, 0, 1, body_len, CRC32(body)].
	if got := binary.LittleEndian.Uint32(out[0:]); got != 3 {
		t.Errorf("outer type = %d, want 3", got)
	}
	body := out[headerLen:]
	if got := binary.LittleEndian.Uint32(out[12:16]); int(got) != len(body) {
		t.Errorf("body_len = %d, actual body = %d", got, len(body))
	}
	if got, want := binary.LittleEndian.Uint32(out[16:]), crc32.ChecksumIEEE(body); got != want {
		t.Errorf("outer CRC = %08x, want %08x", got, want)
	}
	// ProtocolHeader inside body: [0, num_blocks, field2, block_size, 0].
	if got := binary.LittleEndian.Uint32(body[4:]); got != numBlocks {
		t.Errorf("num_blocks = %d, want %d", got, numBlocks)
	}
	if got := binary.LittleEndian.Uint32(body[8:]); got != 1313 {
		t.Errorf("field2 = %d, want 1313 (bootstrap pre-header + 64)", got)
	}
	if got := binary.LittleEndian.Uint32(body[12:]); got != blockSz {
		t.Errorf("block_size = %d, want %d", got, blockSz)
	}
}

// EncodeFrame is deterministic for identical input except for the per-session
// nonce in the descriptor (and the outer CRC that covers it).
func TestEncodeFrameDeterministicExceptNonce(t *testing.T) {
	img := gradient(64, 64)
	a, b := EncodeFrame(img), EncodeFrame(img)
	if len(a) != len(b) {
		t.Fatalf("lengths differ: %d vs %d", len(a), len(b))
	}
	// Nonce = descriptor[29:33]; descriptor starts at body offset 20, body at
	// frame offset 20 → nonce at frame[69:73]. Outer CRC is frame[16:20].
	for _, f := range [][]byte{a, b} {
		copy(f[16:20], []byte{0, 0, 0, 0})
		copy(f[69:73], []byte{0, 0, 0, 0})
	}
	if !bytes.Equal(a, b) {
		t.Error("frames differ outside the nonce/CRC region")
	}
}
