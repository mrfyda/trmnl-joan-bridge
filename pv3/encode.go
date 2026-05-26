package pv3

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"

	lz4 "github.com/pierrec/lz4/v4"
	"golang.org/x/image/draw"
)

// VSS block layout — must match exactly (Joan validates total pixel coverage):
//
//	79 blocks × 4800 bytes = 375600 bytes
//	 1 block  × 4176 bytes =   4176 bytes  (last block, partial)
//	 total in blocks:         383376 bytes  (pixels 0..766751)
//	 pre-header raw:            4720 bytes  (pixels 766752..776191)
//	 grand total:             388096 bytes  = 1024×758/2 ✓
const (
	numBlocks   = 80
	blockSz     = 4800
	lastBlockSz = 4176
	blockTotal  = 79*blockSz + lastBlockSz     // 383376
	preHdrRawSz = PanelW*PanelH/2 - blockTotal // 4720
)

// descriptor is the 68-byte session descriptor for Joan UUID 42002800-0d51-3731-3734-393400000000.
// Bytes 0..28 and 33..67 are fixed device constants (UUID, display params).
// Bytes 29..32 (nonce) are randomised per-session on startup — analysis of 5 MITM captures
// confirmed Joan simply echoes the nonce back in its image ACK and never validates it.
// No VSS dependency remains.
var descriptor = func() [68]byte {
	d := [68]byte{
		0x00, 0x00, 0x00, 0x00, 0xf0, 0x01, 0x00, 0x00,
		0x00, 0x00, 0x42, 0x00, 0x28, 0x00, 0x0d, 0x51,
		0x37, 0x31, 0x37, 0x34, 0x39, 0x34, 0x10, 0x00,
		0xa0, 0x05, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, // nonce — overwritten below
		0x2c, 0xec, 0x0a, 0x00, 0x70, 0x00, 0x00, 0x54,
		0x21, 0xf5, 0x72, 0x01, 0x09, 0x00, 0x43, 0x00,
		0x00, 0x00, 0x18, 0x14, 0x00, 0x04, 0x10, 0x00,
		0x90, 0x00, 0x04, 0xf6, 0x02, 0x02, 0x01, 0x00,
		0x00, 0x04, 0x1e,
	}
	if _, err := rand.Read(d[29:33]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return d
}()

// bootstrapPreHeader is used for the very first push after Joan shows "no connection".
// It represents the compressed tail pixels of Joan's built-in no-connection bitmap,
// which Joan uses as the LZ4 sliding-window dictionary for the first frame's blocks.
// Captured from VSS session s25b (1249 bytes → field[2] = 1249+64 = 1313).
var bootstrapPreHeader = mustDecodeHex(
	"0083ec0500333333334401001377010040666666660b00000800000200000c00" +
		"40555555550c00080200081400002400001c0000140004020000570000140000" +
		"0200041400000c00000200001000000800000200000c000c02000f1800050054" +
		"00000200042000000c000002000040000f08000108e000000c00040200172201" +
		"001311010007130001020003170014113400000200011900302222220c000070" +
		"00000800000200000c00040200048c00041800041000000200040c0000080000" +
		"0200002400005100000800002000000200000c00000200042000000800006001" +
		"042800001000002800041000040200001400000c0000180004020000c400000c" +
		"0000180004020000400000400100020000140000380000020000640040999999" +
		"991800000c00000800000200000c000834000402000814000024000018000014" +
		"00040200005400001400000200041400000c0000020000100000080000020000" +
		"0c000c02000f180005005400000200042000000c000002000040000f08000108" +
		"e000000c00080200001801000200002401000200000c00080200001400000200" +
		"043400081c00001400007000000800000200000c00040200048c000418000410" +
		"00000200040c00000800000200002400005800000800002000000200000c0000" +
		"0200042000000800006001042800001000002800041000040200001400000c00" +
		"00180004020000c000000c000018000402000040000040010002000014000038" +
		"00000200006400000200001800040800000200000c0008340004020008140000" +
		"24000018000014000002000050000002000014000002000014000002000c0c00" +
		"0002000018000c02000f180005005400000200042000000c000002000040000f" +
		"08000108e000000c000402000014010402000424010c02000018000002000434" +
		"00000200042400000c00007000040800080200048c000f1c0009002400000200" +
		"002400005400000800044400000c000002000420000008000060010428000010" +
		"00002c00041000040200001400000c0000180004020000c000000c0000180004" +
		"0200004000004001000200001400002000000c0004fc030f0200090088000002" +
		"00042400004c00000200001400000800008400001c00040c0000080008020000" +
		"1c000002000428000002000820000f02000d00380000020004280004c0000008" +
		"0004020000d000000c00002800040c00080200001400041801000707000c000f" +
		"020001001c00002c00003c00044c000028000414000f020005002c0004020000" +
		"240008ac000402000420000002000c14000c1000004000042800048000040200" +
		"042c0004240000180000240000bc0000f407041000000800082c0000b0010002" +
		"00001400001c000088010f0002ffb61201fb0101020000dc0100100200080000" +
		"ec01000200002c0200fc01000200001000001c000f0002ffba01f90103020000" +
		"dc010f0002fffffffffd001005000200041405002705000c000f020001001c00" +
		"002c00002407008009000c000028000008000014000f020005002c0004020000" +
		"2400007007000200000c000402000420000002000c14000c1000003c00042800" +
		"048000040200042c0004240000180000b800040200001000043400082c0000fc" +
		"070002000014000024000000080010000008000f020009002400000200042400" +
		"00780000020000140000080000a800001c00040c000008000302005055555555" +
		"55")

// EncodeFrame converts an image.Image to a complete Joan 6 PV3 Frame.
//
// Every frame carries the fixed bootstrap pre-header (field[2]=1313). Joan was
// observed to reject frames with a smaller, per-frame "derived" pre-header
// (e.g. field[2]=109 for a mostly-blank image) by closing the connection
// without acknowledging the image, while accepting the bootstrap pre-header
// regardless of the block (pixel) content. VSS itself always sends an
// ~1249-byte pre-header, never a tiny one.
func EncodeFrame(src image.Image) []byte {
	packed := toPackedGray4(rotateImage(src, 180)) // 388096 bytes

	preHdr := bootstrapPreHeader

	// Build 80 blocks: 79 × 4800B + 1 × 4176B = 383376B total.
	blocks := buildBlocks(packed)

	// field[2] = len(pre-header) + 64  (pure size formula, no hash).
	field2 := uint32(len(preHdr) + 64)

	// ProtocolHeader: [0, num_blocks, field2, block_size, 0]
	var ph [20]byte
	binary.LittleEndian.PutUint32(ph[4:], numBlocks)
	binary.LittleEndian.PutUint32(ph[8:], field2)
	binary.LittleEndian.PutUint32(ph[12:], blockSz)

	// Body: ProtocolHeader + descriptor + pre-header + blocks
	body := make([]byte, 0, 20+68+len(preHdr)+len(blocks))
	body = append(body, ph[:]...)
	body = append(body, descriptor[:]...)
	body = append(body, preHdr...)
	body = append(body, blocks...)

	// Outer PV3 header: [3, 0, 1, body_len, CRC32(body)]
	outer := make([]byte, 20)
	binary.LittleEndian.PutUint32(outer[0:], 3)
	binary.LittleEndian.PutUint32(outer[4:], 0)
	binary.LittleEndian.PutUint32(outer[8:], 1)
	binary.LittleEndian.PutUint32(outer[12:], uint32(len(body)))
	binary.LittleEndian.PutUint32(outer[16:], crc32.ChecksumIEEE(body))

	return append(outer, body...)
}

// rotateImage applies a clockwise rotation of 0, 90, 180, or 270 degrees.
// Must match the rotation configured in Joan Configurator to avoid cyclic shift artifacts.
func rotateImage(src image.Image, deg int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	switch ((deg % 360) + 360) % 360 {
	case 90:
		// CW 90°: new image is h×w; new(x,y) = orig(y, h-1-x)
		dst := image.NewNRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < w; y++ {
			for x := 0; x < h; x++ {
				dst.Set(x, y, src.At(b.Min.X+y, b.Min.Y+h-1-x))
			}
		}
		return dst
	case 180:
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.Set(x, y, src.At(b.Min.X+w-1-x, b.Min.Y+h-1-y))
			}
		}
		return dst
	case 270:
		// CW 270° (= CCW 90°): new image is h×w; new(x,y) = orig(w-1-y, x)
		dst := image.NewNRGBA(image.Rect(0, 0, h, w))
		for y := 0; y < w; y++ {
			for x := 0; x < h; x++ {
				dst.Set(x, y, src.At(b.Min.X+w-1-y, b.Min.Y+x))
			}
		}
		return dst
	default:
		return src
	}
}

// colStartOffset is the e-ink panel's hardware column scan start.
// The controller begins reading at pixel 224, so packed byte 0 maps to screen column 224.
// We pre-shift source pixels by this amount so the image appears correctly positioned.
const colStartOffset = 224

// toPackedGray4 resizes src to PanelW×PanelH, converts to 4-bit grayscale,
// and packs 2 pixels per byte (high nibble = left pixel). Returns 388096 bytes.
// Pixels are packed starting at colStartOffset and wrapping, matching the panel's scan order.
func toPackedGray4(src image.Image) []byte {
	dst := image.NewNRGBA(image.Rect(0, 0, PanelW, PanelH))
	draw.BiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)

	packed := make([]byte, PanelW*PanelH/2)
	p := 0
	for y := 0; y < PanelH; y++ {
		for i := 0; i < PanelW/2; i++ {
			x0 := (i*2 + colStartOffset) % PanelW
			x1 := (i*2 + 1 + colStartOffset) % PanelW
			g0 := to4bit(dst.NRGBAAt(x0, y))
			g1 := to4bit(dst.NRGBAAt(x1, y))
			packed[p] = (g0 << 4) | g1
			p++
		}
	}
	return packed
}

func to4bit(c color.NRGBA) uint8 {
	gray := color.GrayModel.Convert(c).(color.Gray)
	return uint8((uint16(gray.Y)*15 + 127) / 255)
}

// buildBlocks encodes packed pixels into 80 LZ4-compressed blocks.
// Blocks 1..79: 4800 bytes each. Block 80: 4176 bytes (lastBlockSz).
func buildBlocks(packed []byte) []byte {
	var comp lz4.Compressor
	tmp := make([]byte, lz4.CompressBlockBound(blockSz))
	out := make([]byte, 0, 120000)

	for i := 0; i < numBlocks; i++ {
		var raw []byte
		if i < numBlocks-1 {
			raw = packed[i*blockSz : (i+1)*blockSz]
		} else {
			raw = packed[i*blockSz : i*blockSz+lastBlockSz]
		}
		rawLen := len(raw)

		n, err := comp.CompressBlock(raw, tmp)
		var compressed []byte
		if err != nil || n == 0 {
			compressed = raw // incompressible — store raw
		} else {
			compressed = make([]byte, n)
			copy(compressed, tmp[:n])
		}

		// 24-byte block header: [idx:u32][numBlocks:u32][compSize:u32][rawSize:u32][pad:8]
		var hdr [24]byte
		binary.LittleEndian.PutUint32(hdr[0:], uint32(i+1))
		binary.LittleEndian.PutUint32(hdr[4:], numBlocks)
		binary.LittleEndian.PutUint32(hdr[8:], uint32(len(compressed)))
		binary.LittleEndian.PutUint32(hdr[12:], uint32(rawLen))

		out = append(out, hdr[:]...)
		out = append(out, compressed...)
	}
	return out
}

func mustDecodeHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic("invalid pv3 hex constant: " + err.Error())
	}
	return b
}
