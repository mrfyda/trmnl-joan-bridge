package pv3

// SessionACK is the fixed 84-byte reply to a Joan Hello. It (re)establishes the
// session before any Frame is pushed and carries the device UUID and display
// parameters; the CRC is pre-computed. Decoded once at init (panics on a bad
// constant, like the other embedded blobs).
var SessionACK = mustDecodeHex(
	"03000000000000000100000040000000ee13318c" +
		"0000000000000000280000002c0000000000000000000000f00100000000" +
		"420028000d5137313734393410001301040010080d00b000000001000000" +
		"00000000")
