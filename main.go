// trmnl-joan-bridge: standalone PV3 server for Joan 6 e-ink display.
//
// Polls a TRMNL server for the current image, encodes it into a
// Visionect PV3 frame, and serves it to Joan over TCP:11112.
// The heartbeat loop keeps the connection alive between Joan's 3-minute hello cycles.
//
// The PV3 wire protocol — framing, message decode, frame encode, the session
// ACK — lives in package pv3. This file is the application: TRMNL polling, the
// frame cache, and the per-connection heartbeat loop.
//
// Configuration (env vars; flags override):
//
//	TRMNL_SERVER      TRMNL base URL (e.g. http://192.168.1.210:2300)
//	DEVICE_ID         Joan MAC address uppercase (e.g. 42:00:28:00:0D:51)
//	ACCESS_TOKEN      TRMNL device access token
//	REFRESH_INTERVAL  Fallback re-fetch interval if TRMNL omits refresh_rate (default: 60s)
//	LISTEN_ADDR       TCP address to bind (default: :11112)
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"trmnl-joan-bridge/pv3"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

func main() {
	trmnlServer := flag.String("trmnl-server", env("TRMNL_SERVER", ""), "TRMNL base URL (e.g. http://192.168.1.210:2300)")
	deviceID := flag.String("device-id", env("DEVICE_ID", ""), "Joan MAC address uppercase (e.g. 42:00:28:00:0D:51)")
	accessToken := flag.String("access-token", env("ACCESS_TOKEN", ""), "TRMNL device access token")
	refresh := flag.Duration("refresh", envDuration("REFRESH_INTERVAL", 60*time.Second), "Fallback refresh interval when TRMNL omits refresh_rate")
	addr := flag.String("addr", env("LISTEN_ADDR", ":11112"), "TCP listen address")
	flag.Parse()

	if *trmnlServer == "" || *deviceID == "" || *accessToken == "" {
		log.Fatal("required: -trmnl-server, -device-id, -access-token")
	}

	fc := &frameStore{}
	st := &deviceStatus{}
	tc := &trmnlClient{server: *trmnlServer, deviceID: *deviceID, token: *accessToken, fallback: *refresh, status: st}
	log.Printf("polling TRMNL at %s (device %s)", *trmnlServer, *deviceID)
	for {
		if err := tc.refresh(fc); err != nil {
			log.Printf("initial TRMNL fetch failed: %v — retrying in 5s", err)
			time.Sleep(5 * time.Second)
			continue
		}
		_, full, _, _ := fc.load()
		if len(full) == 0 {
			log.Printf("TRMNL has no image for this device yet — retrying in 10s")
			time.Sleep(10 * time.Second)
			continue
		}
		log.Printf("initial frame ready (%d bytes)", len(full))
		break
	}
	go tc.loop(fc)

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen %s: %v", *addr, err)
	}
	log.Printf("listening on %s", *addr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go handleConn(conn, fc, st, tc)
	}
}

// handleConn serves a Joan connection for its full lifetime: it wraps the socket
// as a session and runs the heartbeat exchange policy (serve). The touch handler
// advances the TRMNL playlist by re-polling.
func handleConn(conn net.Conn, fc *frameStore, st *deviceStatus, tc *trmnlClient) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()
	log.Printf("[%s] connected", remote)

	s := &netSession{conn: conn, r: bufio.NewReader(conn)}
	serve(s, fc, st, func() {
		if err := tc.refresh(fc); err != nil {
			log.Printf("[%s] touch refresh failed: %v", remote, err)
		}
	}, remote)
}

// session is the per-connection transport the heartbeat policy needs: read the
// next PV3 message (waiting at most timeout) and write raw bytes. Two adapters
// satisfy it — netSession over a real socket, and a scripted fake in tests — so
// serve's push/ack/await logic is testable without a network.
type session interface {
	read(timeout time.Duration) (pv3.Message, error)
	write(p []byte) error
}

// netSession adapts a net.Conn to session: it applies the read deadline serve
// asks for, then decodes one framed PV3 message.
type netSession struct {
	conn net.Conn
	r    *bufio.Reader
}

func (s *netSession) read(timeout time.Duration) (pv3.Message, error) {
	s.conn.SetReadDeadline(time.Now().Add(timeout))
	return pv3.ReadMessage(s.r)
}

func (s *netSession) write(p []byte) error {
	_, err := s.conn.Write(p)
	return err
}

// serve runs the heartbeat exchange policy over a session. Joan sends a Status
// hello every heartbeat (~3 min) and a touch packet when tapped. For each
// message: a touch calls onTouch (advance the playlist); a hello records
// telemetry. Then we ACK. When the stored image is new we push it — as a partial
// update (only the changed rectangle, flicker-free) when we have a confirmed
// baseline of what the device currently shows and the change is small enough,
// otherwise as a full frame — and wait for Joan's image ACK.
//
// lastDisplayed is the packed framebuffer the device has actually rendered; it is
// updated only on an image ACK and is local to the connection (nil at the start).
// So the first push of any connection is full, a reconnect re-syncs with a full,
// and a partial is only ever diffed against a frame we know the device shows.
// Returns when a session read fails (disconnect).
func serve(s session, fc *frameStore, st *deviceStatus, onTouch func(), remote string) {
	var lastDisplayed []byte
	for {
		msg, err := s.read(5 * time.Minute)
		if err != nil {
			log.Printf("[%s] disconnected: %v", remote, err)
			return
		}
		switch m := msg.(type) {
		case pv3.Touch:
			log.Printf("[%s] touch (%d,%d) → advancing playlist", remote, m.X, m.Y)
			onTouch()
		case pv3.Hello:
			if m.Telemetry != nil {
				st.update(*m.Telemetry)
			}
		}

		packed, full, version, isNew := fc.load()
		if len(full) == 0 {
			log.Printf("[%s] no frame ready", remote)
			return
		}

		if err := s.write(pv3.SessionACK); err != nil {
			log.Printf("[%s] write ack: %v", remote, err)
			return
		}
		if !isNew {
			log.Printf("[%s] heartbeat ACK", remote)
			continue
		}

		// A re-poll of identical content (TRMNL bumps the frame every interval):
		// the device already shows it, so don't repaint.
		if lastDisplayed != nil && bytes.Equal(packed, lastDisplayed) {
			fc.markSent(version)
			log.Printf("[%s] image unchanged — heartbeat ACK", remote)
			continue
		}

		// Partial when we have a confirmed baseline and the change is small enough;
		// EncodePartial returns ok=false (→ full) for a whole-screen or tiny change.
		frame, mode := full, "full"
		if lastDisplayed != nil {
			if p, ok := pv3.EncodePartial(lastDisplayed, packed); ok {
				frame, mode = p, "partial"
			}
		}

		if err := s.write(frame); err != nil {
			log.Printf("[%s] write frame: %v", remote, err)
			return
		}
		fc.markSent(version)
		log.Printf("[%s] pushed %s frame (%d bytes)", remote, mode, len(pv3.SessionACK)+len(frame))

		// Joan replies with an image ACK ~1.6s after rendering the frame. The ACK
		// confirms the device now shows this framebuffer — the baseline for the
		// next partial. A missed ACK returns (disconnect), so the next connection
		// re-syncs with a full frame.
		reply, err := s.read(15 * time.Second)
		if err != nil {
			log.Printf("[%s] no image ACK after push: %v", remote, err)
			return
		}
		if _, ok := reply.(pv3.ImageAck); ok {
			lastDisplayed = packed
			log.Printf("[%s] image ACK received", remote)
		} else {
			log.Printf("[%s] post-push: expected image ACK, got %T", remote, reply)
		}
	}
}

// deviceStatus holds the latest battery/signal readings parsed from Joan's
// Status hello, for forwarding to TRMNL on the next /api/display poll.
type deviceStatus struct {
	mu         sync.Mutex
	valid      bool
	voltageMv  int
	rssiMag    int // magnitude; actual RSSI is negative (dBm)
	batteryPct int
}

// update stores the latest telemetry, dropping implausible readings so we never
// forward garbage to TRMNL — a header that fails validation makes the
// /api/display action return 404 and Joan would get no image. The ranges were
// confirmed against the device's own readout (2000–5000 mV, RSSI ≤ 120).
func (d *deviceStatus) update(t pv3.Telemetry) {
	if t.VoltageMv < 2000 || t.VoltageMv > 5000 || t.RSSIMag > 120 || t.BatteryPct > 100 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.valid, d.voltageMv, d.rssiMag, d.batteryPct = true, t.VoltageMv, t.RSSIMag, t.BatteryPct
}

// setHeaders adds the telemetry headers TRMNL expects, in its units:
// Battery-Voltage in volts, RSSI in dBm (negative), Percent-Charged 0..100.
// No-op until a plausible hello has been parsed. The unit conversions live here,
// next to the readings they describe, rather than leaking into the HTTP client.
func (d *deviceStatus) setHeaders(h http.Header) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.valid {
		return
	}
	h.Set("Battery-Voltage", strconv.FormatFloat(float64(d.voltageMv)/1000, 'f', 3, 64))
	h.Set("RSSI", strconv.Itoa(-d.rssiMag))
	h.Set("Percent-Charged", strconv.FormatFloat(float64(d.batteryPct), 'f', 1, 64))
}

// trmnlClient polls a TRMNL /api/display endpoint for the current image URL
// and refresh rate, then fetches the image and encodes it for Joan.
type trmnlClient struct {
	mu       sync.Mutex // serializes refresh (poll loop + touch handler both call it)
	server   string
	deviceID string
	token    string
	fallback time.Duration
	status   *deviceStatus
}

type displayResponse struct {
	ImageURL    string `json:"image_url"`
	RefreshRate int    `json:"refresh_rate"` // seconds; 0 means use fallback
}

func (tc *trmnlClient) refresh(fc *frameStore) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	req, err := http.NewRequest("GET", tc.server+"/api/display", nil)
	if err != nil {
		return err
	}
	req.Header.Set("ID", tc.deviceID)
	req.Header.Set("Access-Token", tc.token)
	// Panel resolution is a fixed device fact TRMNL persists and uses for
	// render sizing. Constant integers, so always valid.
	req.Header.Set("Width", strconv.Itoa(pv3.PanelW))
	req.Header.Set("Height", strconv.Itoa(pv3.PanelH))
	// Report our poll cadence (the refresh_rate TRMNL last gave us).
	req.Header.Set("Refresh-Rate", strconv.Itoa(int(tc.fallback.Seconds())))
	// Forward the latest device telemetry (deviceStatus owns the units and the
	// TRMNL header contract); a no-op until a hello has been parsed.
	if tc.status != nil {
		tc.status.setHeaders(req.Header)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("TRMNL returned %s", resp.Status)
	}

	var dr displayResponse
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		return err
	}
	if dr.ImageURL == "" {
		return nil // no new image
	}

	// Rewrite image_url host to match configured server (TRMNL may return its
	// local hostname which won't resolve inside a container).
	imageURL := tc.rewriteHost(dr.ImageURL)
	log.Printf("TRMNL image_url=%s refresh_rate=%ds", imageURL, dr.RefreshRate)
	packed, full, err := tc.fetchFrame(imageURL)
	if err != nil {
		return err
	}
	fc.set(packed, full)
	if dr.RefreshRate > 0 {
		tc.fallback = time.Duration(dr.RefreshRate) * time.Second
	}
	return nil
}

// rewriteHost replaces the host in rawURL with the host from tc.server so that
// URLs containing TRMNL's local hostname (e.g. umbrel.local) resolve correctly
// inside containers that lack mDNS.
func (tc *trmnlClient) rewriteHost(rawURL string) string {
	base, err := url.Parse(tc.server)
	if err != nil {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.Scheme = base.Scheme
	u.Host = base.Host
	return u.String()
}

// fetchFrame downloads the image at url and returns its packed framebuffer plus
// its full-frame encoding. It uses no client state, but lives here because turning
// a TRMNL poll into frames is the client's job; frameStore just holds the result.
func (tc *trmnlClient) fetchFrame(url string) (packed, full []byte, err error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("image fetch returned %s", resp.Status)
	}
	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	packed = pv3.Pack(img)
	return packed, pv3.EncodeFramePacked(packed), nil
}

func (tc *trmnlClient) loop(fc *frameStore) {
	for {
		interval := tc.fallback
		if interval < time.Second {
			interval = time.Second
		}
		time.Sleep(interval)
		if err := tc.refresh(fc); err != nil {
			log.Printf("TRMNL refresh failed: %v", err)
			continue
		}
		_, full, _, _ := fc.load()
		log.Printf("frame updated (%d bytes)", len(full))
	}
}

// frameStore holds the current image as both its packed framebuffer (so serve can
// diff against what the device last displayed to build a partial update) and its
// pre-encoded full PV3 frame (the fallback). Tracks delivery state for heartbeats.
// Pure storage — no HTTP, no image decoding.
type frameStore struct {
	mu      sync.Mutex
	packed  []byte
	full    []byte
	version uint64
	sent    uint64
}

// set stores the packed framebuffer and its full-frame encoding as current, and
// bumps the version so the next heartbeat treats it as new (until markSent records
// that version delivered).
func (fc *frameStore) set(packed, full []byte) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.packed, fc.full = packed, full
	fc.version++
}

// load returns the current packed framebuffer, its full-frame encoding, the
// version, and whether it's unsent. Does NOT mark it sent — call markSent(version)
// after a successful write.
func (fc *frameStore) load() (packed, full []byte, version uint64, isNew bool) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.packed, fc.full, fc.version, fc.version > fc.sent
}

// markSent records version as delivered. Only advances fc.sent, never rolls back.
func (fc *frameStore) markSent(version uint64) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if version > fc.sent {
		fc.sent = version
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
