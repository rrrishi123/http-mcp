// Package run is the neutral contract between an adapter's `up` (which seats a
// target: a device via appium, a browser via CDP/geckodriver), the host that
// asked for it (pilot, which stores the Result in its registry), and the
// witness (8, which reads the Result to know a session exists and then
// observes it on the wire by session id + hub).
//
// One Request and one Result for every kind. The JSON names are exactly the
// ones adapters/byod and adapters/browser already emit, so an existing
// consumer keeps parsing; Kind says which extension fields are meaningful.
package run

import "github.com/rrrishi123/http-mcp/contract"

// Kinds of target an adapter can seat.
const (
	KindByod    = "byod"    // a physical device through appium (adapters/byod)
	KindBrowser = "browser" // a browser through CDP or geckodriver (adapters/browser)
)

// Request is what `up` takes. Target is the one required field: a device udid
// for byod, an engine name for browser. Everything else is the kind's own
// knobs, mirrored from the adapters' flags so a Request round-trips to a
// command line without loss.
type Request struct {
	Contract string `json:"contract,omitempty"` // contract.Version; Stamp sets it
	Kind     string `json:"kind"`
	Target   string `json:"target"` // byod: device udid · browser: engine (chrome|chromium|edge|brave|firefox)

	// byod
	HubURL string `json:"hub_url,omitempty"` // appium base url to seat on
	MJPEG  int    `json:"mjpeg,omitempty"`   // host mjpeg port
	Team   string `json:"team,omitempty"`    // ios signing team (xcodeOrgId)

	// browser
	Port    int    `json:"port,omitempty"`    // CDP remote-debugging port or geckodriver port
	URL     string `json:"url,omitempty"`     // initial url
	Bin     string `json:"bin,omitempty"`     // browser/driver binary override
	Broker  int    `json:"broker,omitempty"`  // suggested channel broker port for the hint
	Profile string `json:"profile,omitempty"` // firefox profile dir
}

// Result is what `up` returns: the seat, and how to reach and watch it.
// Transport names the wire mode the session speaks ("call" for W3C/Appium
// over HTTP, "channel" for CDP over websocket); Stream is the afferent media
// plane 8 consumes. PID-shaped fields hand teardown to the lifecycle owner so
// an orphaned helper is reclaimable instead of leaked invisibly.
type Result struct {
	Contract  string `json:"contract,omitempty"` // contract.Version; Stamp sets it
	Kind      string `json:"kind,omitempty"`
	SessionID string `json:"session_id"`
	HubURL    string `json:"hub_url"`
	Stream    string `json:"stream,omitempty"`
	Transport string `json:"transport"` // contract.ModeCall | contract.ModeChannel
	PID       int    `json:"pid,omitempty"`

	// byod
	Platform  string `json:"platform,omitempty"`   // android | ios
	Device    string `json:"device,omitempty"`     // udid
	IproxyPID int    `json:"iproxy_pid,omitempty"` // ios mjpeg forwarder that outlives the CLI

	// browser
	Engine     string `json:"engine,omitempty"`      // chrome | chromium | edge | brave | firefox
	BrokerHint string `json:"broker_hint,omitempty"` // the channel invocation the host should run
}

// Stamp marks the request with the contract version it was built under.
func (r *Request) Stamp() *Request { r.Contract = contract.Version; return r }

// Stamp marks the result with the contract version it was built under.
func (r *Result) Stamp() *Result { r.Contract = contract.Version; return r }

// Compatible reports whether a Result stamped elsewhere can be consumed here.
func (r Result) Compatible() bool { return contract.Compatible(r.Contract) }
