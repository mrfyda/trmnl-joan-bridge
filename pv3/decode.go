package pv3

import (
	"encoding/binary"
	"fmt"
	"io"
)

// maxMsgBody caps a PV3 message body length to guard against a desynced read
// allocating absurd amounts of memory. Joan's hello body is 436/512 bytes and
// its image ACK body is 44 bytes, so 4096 is a comfortable ceiling.
const maxMsgBody = 4096

// Wire facts for framing and classification. The 20-byte outer header carries
// the little-endian body length at bytes [12:16]; the body follows.
const (
	headerLen = 20

	// Touch: a 76-byte packet whose body marks event type 6, with coordinates
	// at body+48 / body+52 (u16 LE), flipped 180° from the displayed image.
	touchLen       = 76
	touchMarkerOff = 20
	touchMarkerVal = 6
	touchXOff      = 48
	touchYOff      = 52

	// Hello telemetry: a list of [key:u32][value:u32] LE pairs starting at body
	// offset 0x34, terminated by 0xffffffff. Parse by key, not fixed offset.
	helloKVStart       = 0x34
	helloKeyBatteryPct = 10 // value = battery charge percent (0..100)
	helloKeyRSSI       = 13 // value = |dBm| (signal is negative)
	helloKeyVoltage    = 34 // value = battery millivolts

	// Body-length bands used to classify a non-touch message.
	imageAckMaxBody = 64  // image ACK body is ~44 bytes
	helloMinBody    = 400 // hello bodies are 436/512 bytes
)

// Message is one thing Joan sends on its connection: exactly one of Hello,
// Touch, ImageAck, or Unknown. A framing or I/O failure is an error, not a
// Message. The interface is sealed (isMessage is unexported) so callers can rely
// on a type switch covering every variant.
type Message interface{ isMessage() }

// Telemetry is the device-health reading carried in a Hello. RSSIMag is the
// magnitude; the real RSSI is negative (dBm). Plausibility (sane ranges) is the
// caller's concern — pv3 reports whatever it parsed.
type Telemetry struct {
	VoltageMv  int
	RSSIMag    int
	BatteryPct int
}

// Hello is Joan's status heartbeat (~every 3 min). Telemetry is nil if the
// hello carried no recognised telemetry keys.
type Hello struct{ Telemetry *Telemetry }

// Touch is a single screen-tap, carrying raw panel coordinates.
type Touch struct{ X, Y int }

// ImageAck is Joan's acknowledgement that it rendered a pushed Frame.
type ImageAck struct{}

// Unknown is a well-formed but unrecognised message; Len is the total length,
// for logging.
type Unknown struct{ Len int }

func (Hello) isMessage()    {}
func (Touch) isMessage()    {}
func (ImageAck) isMessage() {}
func (Unknown) isMessage()  {}

// ReadMessage reads one complete length-prefixed PV3 message from r and
// classifies it. Reading whole messages (rather than "whatever is buffered")
// keeps us in sync with Joan. Deadlines are the caller's concern — set them on
// the underlying connection before calling.
func ReadMessage(r io.Reader) (Message, error) {
	h := make([]byte, headerLen)
	if _, err := io.ReadFull(r, h); err != nil {
		return nil, err
	}
	blen := binary.LittleEndian.Uint32(h[12:16])
	if blen > maxMsgBody {
		return nil, fmt.Errorf("implausible body length %d (out of sync)", blen)
	}
	body := make([]byte, blen)
	if blen > 0 {
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, err
		}
	}
	return classify(body), nil
}

// classify maps a message body to its Message type. Touch is recognised
// structurally (length + marker); everything else falls into a body-length band.
func classify(body []byte) Message {
	total := headerLen + len(body)
	switch {
	case total == touchLen && isTouch(body):
		return Touch{
			X: int(binary.LittleEndian.Uint16(body[touchXOff:])),
			Y: int(binary.LittleEndian.Uint16(body[touchYOff:])),
		}
	case len(body) <= imageAckMaxBody:
		return ImageAck{}
	case len(body) >= helloMinBody:
		return Hello{Telemetry: parseTelemetry(body)}
	default:
		return Unknown{Len: total}
	}
}

func isTouch(body []byte) bool {
	return len(body) >= touchYOff+2 &&
		binary.LittleEndian.Uint16(body[touchMarkerOff:]) == touchMarkerVal
}

// parseTelemetry walks the hello key-value list and returns the battery/RSSI/
// charge readings, or nil if any of the three keys is absent. No range check —
// plausibility belongs with the caller that forwards to Terminus.
func parseTelemetry(body []byte) *Telemetry {
	var v, r, p uint32
	var haveV, haveR, haveP bool
	for off := helloKVStart; off+8 <= len(body); off += 8 {
		key := binary.LittleEndian.Uint32(body[off:])
		if key == 0xffffffff || key > 0x200 {
			break // end-of-list sentinel, or left the key-value region
		}
		val := binary.LittleEndian.Uint32(body[off+4:])
		switch key {
		case helloKeyVoltage:
			v, haveV = val, true
		case helloKeyRSSI:
			r, haveR = val, true
		case helloKeyBatteryPct:
			p, haveP = val, true
		}
	}
	if !haveV || !haveR || !haveP {
		return nil
	}
	return &Telemetry{VoltageMv: int(v), RSSIMag: int(r), BatteryPct: int(p)}
}
