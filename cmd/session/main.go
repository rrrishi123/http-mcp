// Run one composed WebDriver session as pure HTTP calls.
//
// Route probes verify components in isolation; this verifies the whole:
// the session id returned by one call must thread through every later
// call, and any broken link fails the chain.
//
//	status -> new session -> control -> perceive -> delete
//
// With -inspector (or HTTP_MCP_INSPECTOR set), the created session is
// opened in Appium Inspector via its --attach-state CLI flag and left
// alive for interactive use. Without it, nothing else is needed.
//
// Usage:
//
//	session -hub http://localhost:4723 -udid <android-udid> [-shot out.png]
//	        [-inspector "/path/to/Appium Inspector.app"] [-keep]
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

type chain struct {
	hub    string
	client *http.Client
	step   int
}

// call makes one HTTP call, prints the link, and decodes value into out.
func (c *chain) call(method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.hub+path, rd)
	req.Header.Set("Content-Type", "application/json")
	t0 := time.Now()
	resp, err := c.client.Do(req)
	c.step++
	if err != nil {
		fmt.Printf("%d. %-6s %-40s !! %v\n", c.step, method, path, err)
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	fmt.Printf("%d. %-6s %-40s -> %d  %5dB  %6.1fs\n",
		c.step, method, path, resp.StatusCode, len(raw), time.Since(t0).Seconds())
	var envelope struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("non-JSON response: %s", raw[:min(len(raw), 120)])
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, envelope.Value[:min(len(envelope.Value), 200)])
	}
	if out != nil {
		return json.Unmarshal(envelope.Value, out)
	}
	return nil
}

// mobile dispatches an Appium `mobile:` execute method.
func (c *chain) mobile(sid, script string, args any, out any) error {
	payload := map[string]any{"script": "mobile: " + script, "args": []any{}}
	if args != nil {
		payload["args"] = []any{args}
	}
	return c.call("POST", "/session/"+sid+"/execute/sync", payload, out)
}

// launchInspector opens Appium Inspector attached to the session via the
// --attach-state CLI flag (requires the inspector build that supports it).
func launchInspector(appPath, hub, sid, platform string) error {
	u, err := url.Parse(hub)
	if err != nil {
		return err
	}
	port := u.Port()
	// No manual-entry mode here: the inspector's normal attach path fetches
	// GET /session/:id itself, so it learns the FULL caps — including
	// mjpegServerPort, which it needs to render the live stream. The manual
	// path skips that fetch and hands it {platformName} only (no stream).
	_ = platform
	state := map[string]any{
		"serverType": "remote",
		"server": map[string]any{"remote": map[string]any{
			"hostname": u.Hostname(), "port": port, "path": "/",
		}},
		"attachSessId": sid,
	}
	b, _ := json.Marshal(state)
	arg := "--attach-state=" + string(b)
	var cmd *exec.Cmd
	if strings.HasSuffix(appPath, ".app") {
		cmd = exec.Command("open", "-n", appPath, "--args", arg)
	} else {
		cmd = exec.Command(appPath, arg)
	}
	return cmd.Start()
}

func main() {
	hub := flag.String("hub", "http://localhost:4723", "WebDriver/Appium server URL")
	udid := flag.String("udid", "", "Android device udid (adb devices)")
	shot := flag.String("shot", "", "save a screenshot to this path")
	inspector := flag.String("inspector", os.Getenv("HTTP_MCP_INSPECTOR"),
		"path to an Appium Inspector with --attach-state support; empty disables")
	keep := flag.Bool("keep", false, "leave the session alive on exit")
	mjpeg := flag.Int("mjpeg", 0,
		"MJPEG stream port (sets appium:mjpegServerPort) — the inspector "+
			"renders this as a live view instead of on-demand snapshots; 0 disables")
	flag.Parse()

	c := &chain{hub: *hub, client: &http.Client{Timeout: 120 * time.Second}}

	var status struct {
		Ready bool `json:"ready"`
	}
	if err := c.call("GET", "/status", nil, &status); err != nil || !status.Ready {
		fmt.Println("server not ready:", err)
		os.Exit(1)
	}

	var sess struct {
		SessionID string `json:"sessionId"`
	}
	alwaysMatch := map[string]any{
		"platformName":          "Android",
		"appium:automationName": "UiAutomator2",
		"appium:udid":           *udid,
		"appium:noReset":        true,
	}
	if *keep || *inspector != "" {
		// the session outlives this run; don't let it idle out
		alwaysMatch["appium:newCommandTimeout"] = 0
	}
	if *mjpeg > 0 {
		alwaysMatch["appium:mjpegServerPort"] = *mjpeg
	}
	caps := map[string]any{"capabilities": map[string]any{
		"alwaysMatch": alwaysMatch,
		"firstMatch":  []any{map[string]any{}},
	}}
	if err := c.call("POST", "/session", caps, &sess); err != nil {
		fmt.Println("session create failed:", err)
		os.Exit(1)
	}
	sid := sess.SessionID
	fmt.Printf("   handle: %s  (threads through every call below)\n", sid)

	if *mjpeg > 0 {
		// The mjpegServerPort cap makes UiAutomator2 serve the stream on the
		// DEVICE at that port; the inspector (on the host) reaches it only via
		// an adb forward. Set it up so the inspector's http://localhost:<port>
		// stream URL is actually live — otherwise it points at a dead port.
		fwd := exec.Command("adb", "-s", *udid, "forward",
			fmt.Sprintf("tcp:%d", *mjpeg), fmt.Sprintf("tcp:%d", *mjpeg))
		if out, err := fwd.CombinedOutput(); err != nil {
			fmt.Printf("   mjpeg: adb forward failed: %v %s\n", err, out)
		} else {
			fmt.Printf("   mjpeg: adb forward tcp:%d tcp:%d\n", *mjpeg, *mjpeg)
		}
		if conn, err := net.DialTimeout("tcp",
			fmt.Sprintf("localhost:%d", *mjpeg), 2*time.Second); err == nil {
			conn.Close()
			fmt.Printf("   mjpeg: live stream up at http://localhost:%d\n", *mjpeg)
		} else {
			fmt.Printf("   mjpeg: forwarded but not reachable on host:%d (%v)\n", *mjpeg, err)
		}
	}

	var info struct {
		Model, Manufacturer, PlatformVersion string
		RealDisplaySize                      string `json:"realDisplaySize"`
	}
	if err := c.mobile(sid, "deviceInfo", nil, &info); err == nil {
		fmt.Printf("   device: %s %s, Android %s, %s\n",
			info.Manufacturer, info.Model, info.PlatformVersion, info.RealDisplaySize)
	}

	// control, not just perception: HOME, then swipe up into the app drawer
	if err := c.mobile(sid, "pressKey", map[string]any{"keycode": 3}, nil); err == nil {
		fmt.Println("   control: pressed HOME")
	}
	if err := c.mobile(sid, "swipeGesture", map[string]any{
		"left": 100, "top": 1500, "width": 880, "height": 700,
		"direction": "up", "percent": 0.8,
	}, nil); err == nil {
		fmt.Println("   control: swiped up (app drawer)")
	}

	if *shot != "" {
		var b64 string
		if err := c.call("GET", "/session/"+sid+"/screenshot", nil, &b64); err == nil {
			png, _ := base64.StdEncoding.DecodeString(b64)
			os.WriteFile(*shot, png, 0o644)
			fmt.Printf("   screenshot: %s (%d bytes)\n", *shot, len(png))
		}
	}

	if *inspector != "" {
		if err := launchInspector(*inspector, *hub, sid, "Android"); err != nil {
			fmt.Println("   inspector launch failed:", err)
		} else {
			fmt.Println("   inspector: launched, attaching to", sid)
			*keep = true
		}
	}

	if *keep {
		fmt.Printf("   session left alive; delete with:\n   curl -X DELETE %s/session/%s\n", *hub, sid)
		return
	}
	if err := c.call("DELETE", "/session/"+sid, nil, nil); err != nil {
		fmt.Println("delete failed:", err)
	}
}
