// joan-shim: standalone PV3 server for Joan 6 e-ink display.
//
// Polls a TRMNL Terminus server for the current image, encodes it into a
// Visionect PV3 frame, and serves it to Joan over TCP:11112.
// The heartbeat loop keeps the connection alive between Joan's 3-minute hello cycles.
//
// The PV3 wire protocol — framing, message decode, frame encode, the session
// ACK — lives in package pv3. This file is the application: Terminus polling, the
// frame cache, and the per-connection heartbeat loop.
//
// Configuration (env vars; flags override):
//
//	TRMNL_SERVER      Terminus base URL (e.g. http://192.168.1.210:2300)
//	DEVICE_ID         Joan MAC address uppercase (e.g. 42:00:28:00:0D:51)
//	ACCESS_TOKEN      Terminus device access token
//	REFRESH_INTERVAL  Fallback re-fetch interval if Terminus omits refresh_rate (default: 60s)
//	LISTEN_ADDR       TCP address to bind (default: :11112)
package main

import (
	"bufio"
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

	"joan-shim/pv3"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

func main() {
	trmnlServer := flag.String("trmnl-server", env("TRMNL_SERVER", ""), "Terminus base URL (e.g. http://192.168.1.210:2300)")
	deviceID := flag.String("device-id", env("DEVICE_ID", ""), "Joan MAC address uppercase (e.g. 42:00:28:00:0D:51)")
	accessToken := flag.String("access-token", env("ACCESS_TOKEN", ""), "Terminus device access token")
	refresh := flag.Duration("refresh", envDuration("REFRESH_INTERVAL", 60*time.Second), "Fallback refresh interval when Terminus omits refresh_rate")
	addr := flag.String("addr", env("LISTEN_ADDR", ":11112"), "TCP listen address")
	flag.Parse()

	if *trmnlServer == "" || *deviceID == "" || *accessToken == "" {
		log.Fatal("required: -trmnl-server, -device-id, -access-token")
	}

	fc := &frameCache{}
	st := &deviceStatus{}
	tc := &trmnlClient{server: *trmnlServer, deviceID: *deviceID, token: *accessToken, fallback: *refresh, status: st}
	log.Printf("polling Terminus at %s (device %s)", *trmnlServer, *deviceID)
	for {
		if err := tc.refresh(fc); err != nil {
			log.Printf("initial Terminus fetch failed: %v — retrying in 5s", err)
			time.Sleep(5 * time.Second)
			continue
		}
		if fc.frameLen() == 0 {
			log.Printf("Terminus has no image for this device yet — retrying in 10s")
			time.Sleep(10 * time.Second)
			continue
		}
		log.Printf("initial frame ready (%d bytes)", fc.frameLen())
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

// handleConn serves a Joan connection for its full lifetime.
//
// Joan sends a Status hello every heartbeat (~3 min) and a touch packet when the
// screen is tapped. We respond with ACK + frame on first hello or when the frame
// has changed; ACK-only otherwise. pv3 decodes the bytes into typed messages;
// the read deadlines (how long to wait) are this loop's concern.
func handleConn(conn net.Conn, fc *frameCache, st *deviceStatus, tc *trmnlClient) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()
	log.Printf("[%s] connected", remote)

	r := bufio.NewReader(conn)

	for {
		conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
		msg, err := pv3.ReadMessage(r)
		if err != nil {
			log.Printf("[%s] disconnected: %v", remote, err)
			return
		}
		switch m := msg.(type) {
		case pv3.Touch:
			// A tap advances the Terminus playlist: re-poll (its rotator rotates
			// to the next item and we re-encode), then fall through to push the
			// resulting frame on this connection.
			log.Printf("[%s] touch (%d,%d) → advancing playlist", remote, m.X, m.Y)
			if err := tc.refresh(fc); err != nil {
				log.Printf("[%s] touch refresh failed: %v", remote, err)
			}
		case pv3.Hello:
			// The Status hello carries battery voltage, RSSI and charge %.
			if m.Telemetry != nil {
				st.update(*m.Telemetry)
			}
		}

		frame, version, isNew := fc.load()
		if len(frame) == 0 {
			log.Printf("[%s] no frame ready", remote)
			return
		}

		if _, err := conn.Write(pv3.SessionACK); err != nil {
			log.Printf("[%s] write ack: %v", remote, err)
			return
		}
		if !isNew {
			log.Printf("[%s] heartbeat ACK", remote)
			continue
		}

		if _, err := conn.Write(frame); err != nil {
			log.Printf("[%s] write frame: %v", remote, err)
			return
		}
		fc.markSent(version)
		log.Printf("[%s] pushed frame (%d bytes)", remote, len(pv3.SessionACK)+len(frame))

		// Joan replies with an image ACK ~1.6s after rendering the frame.
		conn.SetReadDeadline(time.Now().Add(15 * time.Second))
		reply, err := pv3.ReadMessage(r)
		if err != nil {
			log.Printf("[%s] no image ACK after push: %v", remote, err)
			return
		}
		if _, ok := reply.(pv3.ImageAck); ok {
			log.Printf("[%s] image ACK received", remote)
		} else {
			log.Printf("[%s] post-push: expected image ACK, got %T", remote, reply)
		}
	}
}

// deviceStatus holds the latest battery/signal readings parsed from Joan's
// Status hello, for forwarding to Terminus on the next /api/display poll.
type deviceStatus struct {
	mu         sync.Mutex
	valid      bool
	voltageMv  int
	rssiMag    int // magnitude; actual RSSI is negative (dBm)
	batteryPct int
}

// update stores the latest telemetry, dropping implausible readings so we never
// forward garbage to Terminus — a header that fails validation makes the
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

func (d *deviceStatus) snapshot() (ok bool, voltageMv, rssiMag, batteryPct int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.valid, d.voltageMv, d.rssiMag, d.batteryPct
}

// trmnlClient polls a Terminus /api/display endpoint for the current image URL
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

func (tc *trmnlClient) refresh(fc *frameCache) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	req, err := http.NewRequest("GET", tc.server+"/api/display", nil)
	if err != nil {
		return err
	}
	req.Header.Set("ID", tc.deviceID)
	req.Header.Set("Access-Token", tc.token)
	// Panel resolution is a fixed device fact Terminus persists and uses for
	// render sizing. Constant integers, so always valid.
	req.Header.Set("Width", strconv.Itoa(pv3.PanelW))
	req.Header.Set("Height", strconv.Itoa(pv3.PanelH))
	// Report our poll cadence (the refresh_rate Terminus last gave us).
	req.Header.Set("Refresh-Rate", strconv.Itoa(int(tc.fallback.Seconds())))
	// Forward the latest readings parsed from Joan's hello, in the units Terminus
	// expects: Battery-Voltage in volts (float), RSSI in dBm (negative int),
	// Percent-Charged 0..100 (float). Omitted until a hello has been parsed.
	if tc.status != nil {
		if ok, mv, rssiMag, pct := tc.status.snapshot(); ok {
			req.Header.Set("Battery-Voltage", strconv.FormatFloat(float64(mv)/1000, 'f', 3, 64))
			req.Header.Set("RSSI", strconv.Itoa(-rssiMag))
			req.Header.Set("Percent-Charged", strconv.FormatFloat(float64(pct), 'f', 1, 64))
		}
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Terminus returned %s", resp.Status)
	}

	var dr displayResponse
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		return err
	}
	if dr.ImageURL == "" {
		return nil // no new image
	}

	// Rewrite image_url host to match configured server (Terminus may return its
	// local hostname which won't resolve inside a container).
	imageURL := tc.rewriteHost(dr.ImageURL)
	log.Printf("Terminus image_url=%s refresh_rate=%ds", imageURL, dr.RefreshRate)
	if err := fc.fetchAndEncode(imageURL); err != nil {
		return err
	}
	if dr.RefreshRate > 0 {
		tc.fallback = time.Duration(dr.RefreshRate) * time.Second
	}
	return nil
}

// rewriteHost replaces the host in rawURL with the host from tc.server so that
// URLs containing Terminus's local hostname (e.g. umbrel.local) resolve correctly
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

func (tc *trmnlClient) loop(fc *frameCache) {
	for {
		interval := tc.fallback
		if interval < time.Second {
			interval = time.Second
		}
		time.Sleep(interval)
		if err := tc.refresh(fc); err != nil {
			log.Printf("Terminus refresh failed: %v", err)
			continue
		}
		log.Printf("frame updated (%d bytes)", fc.frameLen())
	}
}

// frameCache holds the current encoded frame and tracks state for heartbeats.
type frameCache struct {
	mu      sync.Mutex
	frame   []byte
	flen    int
	version uint64
	sent    uint64
}

func (fc *frameCache) fetchAndEncode(url string) error {
	resp, err := httpClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("image fetch returned %s", resp.Status)
	}

	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return err
	}

	frame := pv3.EncodeFrame(img)

	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.frame = frame
	fc.flen = len(frame)
	fc.version++
	return nil
}

// load returns the current frame, its version, and whether it's unsent.
// Does NOT mark it sent — call markSent(version) after a successful write.
func (fc *frameCache) load() (frame []byte, version uint64, isNew bool) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.frame, fc.version, fc.version > fc.sent
}

// markSent records version as delivered. Only advances fc.sent, never rolls back.
func (fc *frameCache) markSent(version uint64) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if version > fc.sent {
		fc.sent = version
	}
}

func (fc *frameCache) frameLen() int {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.flen
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
