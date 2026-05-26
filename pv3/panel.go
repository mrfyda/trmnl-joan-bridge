// Package pv3 reimplements the device-facing side of the Visionect PV3 wire
// protocol for the Joan 6 e-ink panel: decoding the messages Joan sends
// (ReadMessage) and encoding the frames it renders (EncodeFrame), plus the
// fixed Session ACK. Every byte offset and block-layout fact lives behind this
// package's interface — callers deal in typed Messages and []byte frames, never
// in offsets.
package pv3

// Panel dimensions for the Joan 6 e-ink display. Exported because callers report
// them to TRMNL as the device's Width/Height, but they are a protocol/device
// fact owned here.
const (
	PanelW = 1024
	PanelH = 758
)
