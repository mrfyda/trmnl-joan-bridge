package pv3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// frame wraps a body in a 20-byte PV3 header with the body length at [12:16].
func frame(body []byte) []byte {
	h := make([]byte, headerLen)
	binary.LittleEndian.PutUint32(h[12:16], uint32(len(body)))
	return append(h, body...)
}

// helloBody builds a hello-sized body carrying the three telemetry keys.
func helloBody(v, r, p uint32) []byte {
	body := make([]byte, 436)
	off := helloKVStart
	put := func(k, val uint32) {
		binary.LittleEndian.PutUint32(body[off:], k)
		binary.LittleEndian.PutUint32(body[off+4:], val)
		off += 8
	}
	put(helloKeyBatteryPct, p)
	put(helloKeyRSSI, r)
	put(helloKeyVoltage, v)
	binary.LittleEndian.PutUint32(body[off:], 0xffffffff)
	return body
}

func touchBody(x, y uint16) []byte {
	body := make([]byte, 56)
	binary.LittleEndian.PutUint16(body[touchMarkerOff:], touchMarkerVal)
	binary.LittleEndian.PutUint16(body[touchXOff:], x)
	binary.LittleEndian.PutUint16(body[touchYOff:], y)
	return body
}

func TestReadMessageHello(t *testing.T) {
	m, err := ReadMessage(bytes.NewReader(frame(helloBody(3676, 54, 20))))
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	h, ok := m.(Hello)
	if !ok {
		t.Fatalf("got %T, want Hello", m)
	}
	if h.Telemetry == nil {
		t.Fatal("Telemetry nil, want parsed")
	}
	if got := *h.Telemetry; got.VoltageMv != 3676 || got.RSSIMag != 54 || got.BatteryPct != 20 {
		t.Errorf("telemetry = %+v, want {3676 54 20}", got)
	}
}

func TestReadMessageHelloNoTelemetry(t *testing.T) {
	body := make([]byte, 436) // hello-sized, but sentinel before any key
	binary.LittleEndian.PutUint32(body[helloKVStart:], 0xffffffff)
	m, err := ReadMessage(bytes.NewReader(frame(body)))
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	h, ok := m.(Hello)
	if !ok {
		t.Fatalf("got %T, want Hello", m)
	}
	if h.Telemetry != nil {
		t.Errorf("Telemetry = %+v, want nil", *h.Telemetry)
	}
}

func TestReadMessageTouch(t *testing.T) {
	m, err := ReadMessage(bytes.NewReader(frame(touchBody(970, 770))))
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	tm, ok := m.(Touch)
	if !ok {
		t.Fatalf("got %T, want Touch", m)
	}
	if tm.X != 970 || tm.Y != 770 {
		t.Errorf("touch = %+v, want {970 770}", tm)
	}
}

func TestReadMessageImageAck(t *testing.T) {
	m, err := ReadMessage(bytes.NewReader(frame(make([]byte, 44))))
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if _, ok := m.(ImageAck); !ok {
		t.Fatalf("got %T, want ImageAck", m)
	}
}

func TestReadMessageUnknown(t *testing.T) {
	m, err := ReadMessage(bytes.NewReader(frame(make([]byte, 100))))
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	u, ok := m.(Unknown)
	if !ok {
		t.Fatalf("got %T, want Unknown", m)
	}
	if u.Len != headerLen+100 {
		t.Errorf("Unknown.Len = %d, want %d", u.Len, headerLen+100)
	}
}

func TestReadMessageDesync(t *testing.T) {
	h := make([]byte, headerLen)
	binary.LittleEndian.PutUint32(h[12:16], maxMsgBody+1)
	if _, err := ReadMessage(bytes.NewReader(h)); err == nil {
		t.Fatal("want error on implausible body length")
	}
}

func TestReadMessageTruncatedBody(t *testing.T) {
	h := make([]byte, headerLen)
	binary.LittleEndian.PutUint32(h[12:16], 436) // claims 436, supplies 10
	_, err := ReadMessage(bytes.NewReader(append(h, make([]byte, 10)...)))
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("got %v, want ErrUnexpectedEOF", err)
	}
}

func TestReadMessageShortHeader(t *testing.T) {
	if _, err := ReadMessage(bytes.NewReader(make([]byte, 5))); err == nil {
		t.Fatal("want error on short header")
	}
}
