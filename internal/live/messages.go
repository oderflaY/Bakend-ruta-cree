package live

import "encoding/json"

// Wire format of /api/socket. It mirrors Traccar's socket (devices / positions / events) because
// the app already consumes that through TraccarSocketFeed; unknown keys are ignored by the app.

const knotsPerKmh = 1 / 1.852

type Device struct {
	ID         uint64         `json:"id"`
	Name       string         `json:"name"`
	Attributes map[string]any `json:"attributes"`
}

type Position struct {
	DeviceID   uint64         `json:"deviceId"`
	Latitude   float64        `json:"latitude"`
	Longitude  float64        `json:"longitude"`
	Speed      float64        `json:"speed"` // knots, like Traccar
	FixTime    string         `json:"fixTime"`
	Attributes map[string]any `json:"attributes"`
}

type Event struct {
	ID         uint64         `json:"id"`
	Type       string         `json:"type"`
	DeviceID   uint64         `json:"deviceId"`
	Attributes map[string]any `json:"attributes"`
}

// RampRequest goes only to drivers (driver socket), never to the public feed.
type RampRequest struct {
	ID        uint64 `json:"id"`
	RouteID   string `json:"routeId"`
	StopID    string `json:"stopId"`
	StopName  string `json:"stopName"`
	CreatedAt string `json:"createdAt"`
}

type message struct {
	Devices      []Device      `json:"devices,omitempty"`
	Positions    []Position    `json:"positions,omitempty"`
	Events       []Event       `json:"events,omitempty"`
	RampRequests []RampRequest `json:"rampRequests,omitempty"`
	// Ids of ramp requests another driver already acknowledged: clear them from the screen.
	RampAcks []uint64 `json:"rampAcks,omitempty"`
}

func jsonMessage(m message) ([]byte, error) { return json.Marshal(m) }
