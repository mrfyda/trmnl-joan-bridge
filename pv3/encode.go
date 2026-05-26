package pv3

import (
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

// VSS block layout for a full frame — the 80 dataBlocks cover the first blockTotal
// bytes, the pre-header tail carries the remaining preHdrRawSz:
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

// rect is a panel-space rectangle. Its region payload is w*h/2 packed bytes (the
// panel is 4-bit grayscale, 2 px/byte).
type rect struct{ x, y, w, h int }

func (r rect) payload() int { return r.w * r.h / 2 }

// fullScreen is the rectangle a full frame paints — the whole panel.
var fullScreen = rect{x: 0, y: 0, w: PanelW, h: PanelH}

// preamble is the 60-byte pre-header preamble captured from a real VSS frame for
// this device: leading zeros, the device UUID, the PacketImage type (5), and two
// nonce fields Joan does NOT validate (verified on device). encodeRegion rewrites
// the two payload-size fields at bytes 32 and 52 per region; the 24-byte
// RectangleHeader and the raw tail pixels follow it. See docs/pv3-frame-format.md.
var preamble = mustDecodeHex(
	"0000000000000000420028000d513731373439340000000005000000" +
		"b3f1a71a2cec050000000000b0e099a0010000000000000018ec0500" +
		"00000000")

// EncodeFrame converts an image.Image to a complete Joan 6 PV3 Frame painting the
// whole screen. EncodeFrame is encodeRegion over the full-screen rectangle, after
// packing. See docs/pv3-frame-format.md.
func EncodeFrame(src image.Image) []byte {
	return EncodeFramePacked(Pack(src))
}

// EncodeFramePacked builds a full PV3 frame from an already-packed framebuffer
// (PanelW*PanelH/2 bytes, as returned by Pack). The split from EncodeFrame lets a
// caller pack once and reuse the framebuffer for a subsequent EncodePartial diff.
func EncodeFramePacked(packed []byte) []byte {
	return encodeRegion(packed, fullScreen)
}

// encodeRegion assembles a complete PV3 frame painting data — the region's w*h/2
// packed pixels — at panel rectangle r. The last preHdrRawSz bytes of data ride in
// the pre-header tail; the rest are LZ4-chunked into blocks. A full frame is
// encodeRegion(packed, fullScreen); a partial is encodeRegion(readRegion(packed,
// r), r). See docs/pv3-frame-format.md.
func encodeRegion(data []byte, r rect) []byte {
	tail := data[len(data)-preHdrRawSz:]
	blocks, count := buildBlocks(data[:len(data)-preHdrRawSz])
	preHdr := buildPreHeader(r, tail)

	// ImageHeader: [Checksum, NrPrimitives, Options=len-4, PayloadLength, Reserved=1]
	var ih [20]byte
	binary.LittleEndian.PutUint32(ih[4:], uint32(count))
	binary.LittleEndian.PutUint32(ih[8:], uint32(len(preHdr)-4))
	binary.LittleEndian.PutUint32(ih[12:], blockSz)
	binary.LittleEndian.PutUint32(ih[16:], 1)

	// Body: ImageHeader + pre-header + blocks
	body := make([]byte, 0, 20+len(preHdr)+len(blocks))
	body = append(body, ih[:]...)
	body = append(body, preHdr...)
	body = append(body, blocks...)

	// Outer PV3 ProtocolHeader: [3, 0, 1, body_len, CRC32(body)]
	outer := make([]byte, 20)
	binary.LittleEndian.PutUint32(outer[0:], 3)
	binary.LittleEndian.PutUint32(outer[8:], 1)
	binary.LittleEndian.PutUint32(outer[12:], uint32(len(body)))
	binary.LittleEndian.PutUint32(outer[16:], crc32.ChecksumIEEE(body))
	return append(outer, body...)
}

// buildPreHeader assembles the pre-header for rectangle r: the 60-byte preamble
// (with its two payload-size fields rewritten — the device validates them), the
// 24-byte RectangleHeader, then the raw tail pixels. buildPreHeader(fullScreen, …)
// reproduces the device's full-frame header exactly.
func buildPreHeader(r rect, tail []byte) []byte {
	p := append([]byte(nil), preamble...)
	binary.LittleEndian.PutUint32(p[32:], uint32(r.payload()+44))
	binary.LittleEndian.PutUint32(p[52:], uint32(r.payload()+24))

	pre := make([]byte, 0, len(p)+24+len(tail))
	pre = append(pre, p...)
	pre = append(pre, rectHeader(r)...)
	pre = append(pre, tail...)
	return pre
}

// rectHeader is the 24-byte RectangleHeader for region r: ImageType=1 (Gray),
// Encoding=4 (4-bit), RectangleUpdateOptions=0x0102 (4-bit), PayloadLength=w*h/2.
// All little-endian.
func rectHeader(r rect) []byte {
	var rh [24]byte
	binary.LittleEndian.PutUint16(rh[0:], 1) // ImageType = Gray
	binary.LittleEndian.PutUint16(rh[4:], uint16(r.x))
	binary.LittleEndian.PutUint16(rh[6:], uint16(r.y))
	binary.LittleEndian.PutUint16(rh[8:], uint16(r.w))
	binary.LittleEndian.PutUint16(rh[10:], uint16(r.h))
	binary.LittleEndian.PutUint16(rh[12:], 0x0102) // RectangleUpdateOptions (4-bit)
	binary.LittleEndian.PutUint16(rh[16:], 4)      // Encoding = 4-bit
	binary.LittleEndian.PutUint32(rh[20:], uint32(r.payload()))
	return rh[:]
}

// buildBlocks LZ4-chunks data into blockSz-byte blocks (the last block smaller),
// returning the wire bytes and the block count for ImageHeader.NrPrimitives. Each
// block is a 24-byte header [BlockID:u32][BlockLast:u32][compSize:u32][rawSize:u32]
// [pad:8] then the LZ4 data, stored raw when incompressible. A full frame's
// blockTotal bytes chunk to numBlocks (79×blockSz + lastBlockSz).
func buildBlocks(data []byte) (out []byte, count int) {
	count = (len(data) + blockSz - 1) / blockSz
	var comp lz4.Compressor
	tmp := make([]byte, lz4.CompressBlockBound(blockSz))
	out = make([]byte, 0, len(data)/2+count*24)

	for i := 0; i < count; i++ {
		end := (i + 1) * blockSz
		if end > len(data) {
			end = len(data)
		}
		raw := data[i*blockSz : end]

		n, err := comp.CompressBlock(raw, tmp)
		compressed := raw // incompressible — store raw
		if err == nil && n != 0 {
			compressed = append([]byte(nil), tmp[:n]...)
		}

		var hdr [24]byte
		binary.LittleEndian.PutUint32(hdr[0:], uint32(i+1))
		binary.LittleEndian.PutUint32(hdr[4:], uint32(count))
		binary.LittleEndian.PutUint32(hdr[8:], uint32(len(compressed)))
		binary.LittleEndian.PutUint32(hdr[12:], uint32(len(raw)))
		out = append(out, hdr[:]...)
		out = append(out, compressed...)
	}
	return out, count
}

// Pack converts an image to the device's packed 4-bit framebuffer (PanelW*PanelH/2
// bytes) exactly as EncodeFrame does internally: fixed 180° rotation, resize, and
// 2-px-per-byte gray4 packing with the colStartOffset scan shift. Callers that want
// partial updates keep the packed framebuffer of the last displayed image and pass
// successive frames to EncodePartial.
func Pack(src image.Image) []byte {
	return toPackedGray4(rotateImage(src, 180))
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

func mustDecodeHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic("invalid pv3 hex constant: " + err.Error())
	}
	return b
}
