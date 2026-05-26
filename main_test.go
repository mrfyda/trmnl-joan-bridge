package main

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"

	"trmnl-joan-bridge/pv3"
)

func TestFrameStoreNewFrameIsNew(t *testing.T) {
	fs := &frameStore{}
	if _, full, _, isNew := fs.load(); full != nil || isNew {
		t.Fatalf("empty store: full=%v isNew=%v, want nil/false", full, isNew)
	}
	fs.set(nil, []byte("a"))
	_, full, v, isNew := fs.load()
	if string(full) != "a" || !isNew {
		t.Fatalf("after set: full=%q isNew=%v, want \"a\"/true", full, isNew)
	}
	fs.markSent(v)
	if _, _, _, isNew := fs.load(); isNew {
		t.Fatal("after markSent: isNew=true, want false")
	}
}

func TestFrameStoreSetAfterSentIsNewAgain(t *testing.T) {
	fs := &frameStore{}
	fs.set(nil, []byte("a"))
	_, _, v, _ := fs.load()
	fs.markSent(v)
	fs.set(nil, []byte("b"))
	if _, full, _, isNew := fs.load(); string(full) != "b" || !isNew {
		t.Fatalf("new frame after sent: full=%q isNew=%v, want \"b\"/true", full, isNew)
	}
}

// The version token guards the race where a new frame lands between load and
// markSent: marking the peeked version sent must NOT swallow the newer frame.
func TestFrameStoreMarkSentDoesNotSwallowNewerFrame(t *testing.T) {
	fs := &frameStore{}
	fs.set(nil, []byte("a"))
	_, _, vA, _ := fs.load() // peek version A
	fs.set(nil, []byte("b")) // newer frame lands before we mark A sent
	fs.markSent(vA)          // mark only A delivered
	if _, full, _, isNew := fs.load(); string(full) != "b" || !isNew {
		t.Fatalf("after markSent(A): full=%q isNew=%v, want \"b\"/true (B still unsent)", full, isNew)
	}
}

func TestFrameStoreMarkSentNeverRollsBack(t *testing.T) {
	fs := &frameStore{}
	fs.set(nil, []byte("a")) // v1
	fs.set(nil, []byte("b")) // v2
	_, _, v2, _ := fs.load()
	fs.markSent(v2)
	fs.markSent(v2 - 1) // stale, lower — must be a no-op
	if _, _, _, isNew := fs.load(); isNew {
		t.Fatal("stale markSent rolled back sent; isNew=true, want false")
	}
}

// scriptedSession is a test session: it returns queued read results (then io.EOF
// to end serve's loop) and records every write. beforeRead, if set, fires before
// each read with the upcoming read index — letting a test mutate the frameStore
// between messages (e.g. land a new frame before the next hello).
type scriptedSession struct {
	reads      []readResult
	ri         int
	writes     [][]byte
	beforeRead func(i int)
}

type readResult struct {
	msg pv3.Message
	err error
}

func (s *scriptedSession) read(timeout time.Duration) (pv3.Message, error) {
	if s.beforeRead != nil {
		s.beforeRead(s.ri)
	}
	if s.ri >= len(s.reads) {
		return nil, io.EOF
	}
	rr := s.reads[s.ri]
	s.ri++
	return rr.msg, rr.err
}

func (s *scriptedSession) write(p []byte) error {
	s.writes = append(s.writes, append([]byte(nil), p...))
	return nil
}

func TestServePushesNewFrame(t *testing.T) {
	fc := &frameStore{}
	fc.set(nil, []byte("FRAME"))
	s := &scriptedSession{reads: []readResult{{msg: pv3.Hello{}}, {msg: pv3.ImageAck{}}}}
	serve(s, fc, &deviceStatus{}, func() {}, "test")

	if len(s.writes) != 2 {
		t.Fatalf("writes = %d, want 2 (ACK + frame)", len(s.writes))
	}
	if !bytes.Equal(s.writes[0], pv3.SessionACK) {
		t.Error("first write is not the SessionACK")
	}
	if string(s.writes[1]) != "FRAME" {
		t.Errorf("second write = %q, want FRAME (full on first push)", s.writes[1])
	}
	if _, _, _, isNew := fc.load(); isNew {
		t.Error("frame still marked new after push + image ACK")
	}
}

// With a confirmed baseline, the next image is pushed as a partial (the
// EncodePartial diff), not a full frame.
func TestServePushesPartialAgainstBaseline(t *testing.T) {
	const fbSize = pv3.PanelW * pv3.PanelH / 2
	prev := make([]byte, fbSize)
	next := make([]byte, fbSize)
	copy(next, prev)
	for i := 100000; i < 130000; i++ { // a sizable contiguous change → a valid partial
		next[i] = 0x5A
	}
	fullPrev := pv3.EncodeFramePacked(prev)
	wantPartial, ok := pv3.EncodePartial(prev, next)
	if !ok {
		t.Fatal("test setup: EncodePartial returned ok=false")
	}

	fc := &frameStore{}
	fc.set(prev, fullPrev)
	s := &scriptedSession{reads: []readResult{
		{msg: pv3.Hello{}}, {msg: pv3.ImageAck{}}, // push full(prev) → baseline
		{msg: pv3.Hello{}}, {msg: pv3.ImageAck{}}, // push partial(next)
	}}
	s.beforeRead = func(i int) {
		if i == 2 { // a new frame lands before the second hello
			fc.set(next, pv3.EncodeFramePacked(next))
		}
	}
	serve(s, fc, &deviceStatus{}, func() {}, "test")

	if len(s.writes) != 4 {
		t.Fatalf("writes = %d, want 4 (ACK+full, ACK+partial)", len(s.writes))
	}
	if !bytes.Equal(s.writes[1], fullPrev) {
		t.Error("first push is not the full frame")
	}
	if !bytes.Equal(s.writes[3], wantPartial) {
		t.Errorf("second push = %d bytes, want the partial (%d bytes)", len(s.writes[3]), len(wantPartial))
	}
	if len(s.writes[3]) >= len(fullPrev) {
		t.Errorf("partial (%d) not smaller than full (%d)", len(s.writes[3]), len(fullPrev))
	}
}

func TestServeAckOnlyWhenUnchanged(t *testing.T) {
	fc := &frameStore{}
	fc.set(nil, []byte("FRAME"))
	_, _, v, _ := fc.load()
	fc.markSent(v) // already delivered
	s := &scriptedSession{reads: []readResult{{msg: pv3.Hello{}}}}
	serve(s, fc, &deviceStatus{}, func() {}, "test")

	if len(s.writes) != 1 {
		t.Fatalf("writes = %d, want 1 (ACK only, frame unchanged)", len(s.writes))
	}
}

func TestServeTouchAdvances(t *testing.T) {
	fc := &frameStore{}
	fc.set(nil, []byte("FRAME"))
	touched := 0
	s := &scriptedSession{reads: []readResult{{msg: pv3.Touch{X: 1, Y: 2}}, {msg: pv3.ImageAck{}}}}
	serve(s, fc, &deviceStatus{}, func() { touched++ }, "test")

	if touched != 1 {
		t.Fatalf("onTouch called %d times, want 1", touched)
	}
}

func TestServeRecordsTelemetry(t *testing.T) {
	fc := &frameStore{}
	fc.set(nil, []byte("FRAME"))
	st := &deviceStatus{}
	s := &scriptedSession{reads: []readResult{
		{msg: pv3.Hello{Telemetry: &pv3.Telemetry{VoltageMv: 3676, RSSIMag: 54, BatteryPct: 20}}},
		{msg: pv3.ImageAck{}},
	}}
	serve(s, fc, st, func() {}, "test")

	h := http.Header{}
	st.setHeaders(h)
	if got := h.Get("Battery-Voltage"); got != "3.676" {
		t.Errorf("Battery-Voltage = %q, want 3.676", got)
	}
}

func TestDeviceStatusHeaders(t *testing.T) {
	st := &deviceStatus{}
	h := http.Header{}
	st.setHeaders(h) // no hello parsed yet → nothing forwarded
	if len(h) != 0 {
		t.Fatalf("headers before any hello = %v, want none", h)
	}

	st.update(pv3.Telemetry{VoltageMv: 3676, RSSIMag: 54, BatteryPct: 20})
	st.setHeaders(h)
	for k, want := range map[string]string{
		"Battery-Voltage": "3.676",
		"Rssi":            "-54", // http.Header canonicalises RSSI → Rssi
		"Percent-Charged": "20.0",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestDeviceStatusDropsImplausible(t *testing.T) {
	st := &deviceStatus{}
	st.update(pv3.Telemetry{VoltageMv: 1000, RSSIMag: 54, BatteryPct: 20}) // 1000 mV implausible
	h := http.Header{}
	st.setHeaders(h)
	if len(h) != 0 {
		t.Errorf("implausible telemetry forwarded: %v", h)
	}
}
