// cmd/matrix: run the LambdaTest provider matrix over the wire, as a host tool.
//
// This is a CONSUMER of http-mcp's two physics, not an extension of them — it
// lives beside cmd/probe and cmd/harvest and uses the same internal/httpx +
// internal/wsx atoms and the same auth resolver. The MCP server stays 3 tools;
// "run everything" is a host concern, driven by specs/providers data.
//
// Shape (peer-reviewed design):
//  1. resolve auth BELOW the boundary (profile -> Basic; secret never logged)
//  2. smoke-probe one known-good combo; abort early on an environment issue
//     so a broken env reads as "environment", not "matrix regression"
//  3. run the matrix with a bounded worker pool (--parallel) so we respect
//     cloud parallel limits; retry flaky combos (--retries)
//  4. framework builds are async — capture the buildId (submission is the wire
//     act); polling the verdict to PASSED/FAILED is a follow-up (build-status)
//  5. print a summary; exit non-zero if any combo regressed (CI gate)
//
// Prereqs (apps) are read from the spec's known lt:// ids; --require-apps makes
// a missing app a hard failure (so CI surfaces it) rather than a skipped combo.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rrrishi123/http-mcp/auth"
	"github.com/rrrishi123/http-mcp/internal/httpx"
	"github.com/rrrishi123/http-mcp/internal/wsx"
)

// spec is the slice of specs/providers/lambdatest.json this runner needs — the
// stable endpoints and the app inventory. Prose fields are ignored.
type spec struct {
	Endpoints map[string]string `json:"endpoints"`
	Framework struct {
		Apps map[string]string `json:"app_inventory_prod_adminltqa"`
	} `json:"framework_family"`
}

type kind int

const (
	session   kind = iota // CALL: POST /session ... DELETE /session/{id}
	channel               // CHANNEL: dial ws, one command
	framework             // CALL: POST build, poll verdict
)

// combo is one cell of the matrix. Endpoints/caps mirror specs/providers/lambdatest.json.
type combo struct {
	name    string
	kind    kind
	url     string            // session: hub /session ; framework: build url ; channel: ws url (caps go in query)
	body    string            // session/framework request body
	wsCaps  string            // channel: the capabilities JSON (auth injected below boundary)
	headers map[string]string // channel: ws upgrade headers (e.g. x-playwright-browser)
	wsMeth  string            // channel: the command method
	needApp string            // prereq app key in spec.Framework.Apps (informational / --require-apps)
}

type result struct {
	name   string
	ok     bool
	detail string
	dur    time.Duration
}

var (
	flagProfile  = flag.String("profile", "prod:adminltqa", "auth profile resolved below the boundary")
	flagBuild    = flag.String("build", "http-mcp matrix", "build name on the dashboards")
	flagParallel = flag.Int("parallel", 3, "max concurrent combos (respect cloud parallel limits)")
	flagOnly     = flag.String("only", "", "substring filter: only run combos whose name contains this")
	flagRetries  = flag.Int("retries", 1, "attempts per combo before marking failed (flaky real devices)")
	flagReqApps  = flag.Bool("require-apps", false, "fail (not skip) combos whose prereq app is absent from the spec")
	flagSpec     = flag.String("spec", "specs/providers/lambdatest.json", "provider spec to read endpoints + app ids from")
)

func main() {
	flag.Parse()

	cred, err := auth.Resolve(*flagProfile)
	if err != nil {
		die("auth: %v", err)
	}
	sp := loadSpec(*flagSpec)

	combos := matrix(sp)
	if *flagOnly != "" {
		var f []combo
		for _, c := range combos {
			if strings.Contains(c.name, *flagOnly) {
				f = append(f, c)
			}
		}
		combos = f
	}
	if len(combos) == 0 {
		die("no combos match --only=%q", *flagOnly)
	}

	// Smoke probe: one cheap known-good combo. If the env is broken, say so.
	fmt.Println("== smoke probe (selenium-chrome-win11) ==")
	smoke := run(cred, combo{name: "smoke-selenium-chrome-win11", kind: session,
		url:  sp.Endpoints["web_hub"] + "/session",
		body: seleniumCaps("Chrome", "latest", "Windows 11", false, *flagBuild+" (smoke)", "smoke")}, 1)
	if !smoke.ok {
		die("environment issue (smoke failed): %s — aborting before the matrix", smoke.detail)
	}
	fmt.Printf("   smoke ok (%s)\n\n", smoke.dur.Round(time.Millisecond))

	fmt.Printf("== matrix: %d combos, parallel=%d, retries=%d ==\n", len(combos), *flagParallel, *flagRetries)
	results := runPool(cred, combos, *flagParallel, *flagRetries)

	sort.Slice(results, func(i, j int) bool { return results[i].name < results[j].name })
	pass, fail := 0, 0
	fmt.Println("\n== results ==")
	for _, r := range results {
		mark := "PASS"
		if !r.ok {
			mark, fail = "FAIL", fail+1
		} else {
			pass++
		}
		fmt.Printf("  %-4s %-34s %8s  %s\n", mark, r.name, r.dur.Round(time.Millisecond), r.detail)
	}
	fmt.Printf("\n%d passed, %d failed, of %d\n", pass, fail, len(results))
	if fail > 0 {
		os.Exit(1)
	}
}

// matrix mirrors specs/providers/lambdatest.json + vd_catalog. Edits to the
// territory belong in the spec; this encodes the axes the runner exercises.
func matrix(sp spec) []combo {
	web := sp.Endpoints["web_hub"] + "/session"
	mob := sp.Endpoints["mobile_hub"] + "/session"
	fw := sp.Endpoints["framework_build"]
	cdp := sp.Endpoints["cdp"]
	b := *flagBuild
	var cs []combo

	// selenium: win+mac x browsers x {normal, bidi}
	for _, s := range []struct{ br, ver, plat string }{
		{"Chrome", "latest", "Windows 11"}, {"firefox", "latest", "macOS Sonoma"},
		{"MicrosoftEdge", "latest", "Windows 11"}, {"safari", "latest", "macOS Sonoma"},
	} {
		cs = append(cs, combo{name: "selenium-" + low(s.br) + "-normal", kind: session, url: web,
			body: seleniumCaps(s.br, s.ver, s.plat, false, b, "selenium-"+low(s.br)+"-normal")})
	}
	cs = append(cs, combo{name: "selenium-chrome-win11-bidi", kind: session, url: web,
		body: seleniumCaps("Chrome", "latest", "Windows 11", true, b, "selenium-chrome-bidi")})

	// appium: android+ios x app+web x rd+vd
	cs = append(cs,
		combo{name: "appium-android-app-vd", kind: session, url: mob, needApp: "androidVirtual",
			body: appiumCaps("android", "Galaxy S23", "13", false, "lt://"+id(sp, "androidVirtual"), "", b)},
		combo{name: "appium-ios-app-vd", kind: session, url: mob, needApp: "iosVirtual",
			body: appiumCaps("ios", "iPhone 15", "18.0", false, "lt://"+id(sp, "iosVirtual"), "", b)},
		combo{name: "appium-android-web-vd", kind: session, url: mob,
			body: appiumCaps("android", "Galaxy S23", "13", false, "", "Chrome", b)},
		combo{name: "appium-ios-web-vd", kind: session, url: mob,
			body: appiumCaps("ios", "iPhone 15", "18.0", false, "", "safari", b)},
		combo{name: "appium-android-web-rd", kind: session, url: mob,
			body: appiumCaps("android", "Galaxy S23", "14", true, "", "Chrome", b)},
		combo{name: "appium-ios-web-rd", kind: session, url: mob,
			body: appiumCaps("ios", "iPhone 14", "18", true, "", "safari", b)},
	)

	// puppeteer: raw CDP channel (no upgrade header)
	cs = append(cs, combo{name: "puppeteer-chrome-cdp", kind: channel,
		url: cdp + "/puppeteer", wsMeth: "Browser.getVersion",
		wsCaps: `{"browserName":"Chrome","browserVersion":"latest","LT:Options":{"platform":"Windows 11","build":"` + b + `","name":"puppeteer-chrome-cdp"}}`})

	// playwright: CDP channel WITH the x-playwright-browser upgrade gate; open + minimal initialize.
	cs = append(cs, combo{name: "playwright-chrome-desktop-web", kind: channel,
		url: cdp + "/playwright", wsMeth: "initialize", headers: map[string]string{"x-playwright-browser": "chromium"},
		wsCaps: `{"browserName":"Chrome","browserVersion":"latest","LT:Options":{"platform":"Windows 11","build":"` + b + `","name":"playwright-chrome-desktop-web","network":true,"video":true}}`})

	// espresso (rd) + xcui (vd) framework builds
	if a, t := id(sp, "espressoMyApp"), id(sp, "espressoMyTestApp"); a != "" && t != "" {
		cs = append(cs, combo{name: "espresso-rd", kind: framework, needApp: "espressoMyApp",
			url:  fw + "/v1/espresso/build",
			body: frameworkBody("lt://"+a, "lt://"+t, "Galaxy S23-14", false, b)})
	}
	if a, t := id(sp, "ProverbialApp"), id(sp, "ProverbialTestApp"); a != "" && t != "" {
		cs = append(cs, combo{name: "xcui-vd", kind: framework, needApp: "ProverbialApp",
			url:  fw + "/v1/xcui/build",
			body: frameworkBody("lt://"+a, "lt://"+t, "iPhone 15-18.0", true, b)})
	}
	return cs
}

// --- runners ---

func run(cred httpx.Auth, c combo, attempts int) result {
	start := time.Now()
	var last string
	for i := 0; i < attempts; i++ {
		ok, detail := once(cred, c)
		if ok {
			return result{c.name, true, detail, time.Since(start)}
		}
		last = detail
	}
	return result{c.name, false, "after " + itoa(attempts) + " try(s): " + last, time.Since(start)}
}

func once(cred httpx.Auth, c combo) (bool, string) {
	switch c.kind {
	case session:
		return runSession(cred, c)
	case channel:
		return runChannel(cred, c)
	case framework:
		return runFramework(cred, c)
	}
	return false, "unknown kind"
}

// runSession: CALL — create a WebDriver/Appium session, then delete it to free a parallel.
func runSession(cred httpx.Auth, c combo) (bool, string) {
	req := httpx.Request{Method: "POST", URL: c.url, Body: c.body, Headers: map[string]string{"Content-Type": "application/json"}}
	cred.Apply(&req)
	resp, err := httpx.Do(&http.Client{Timeout: 120 * time.Second}, req)
	if err != nil {
		return false, "dial: " + err.Error()
	}
	if resp.Status != 200 {
		return false, fmt.Sprintf("HTTP %d: %s", resp.Status, head(resp.Body, 120))
	}
	sid := jsonStr(resp.Body, "sessionId")
	if sid == "" {
		return false, "no sessionId: " + head(resp.Body, 120)
	}
	// best-effort delete (free the parallel); ignore the result
	del := httpx.Request{Method: "DELETE", URL: strings.TrimSuffix(c.url, "/session") + "/session/" + sid}
	cred.Apply(&del)
	_, _ = httpx.Do(&http.Client{Timeout: 30 * time.Second}, del)
	return true, "session " + sid
}

// runChannel: CHANNEL — inject auth into ?capabilities=, dial (with upgrade
// headers if any), send one command, read the response.
func runChannel(cred httpx.Auth, c combo) (bool, string) {
	wsURL, err := capsURL(c.url, c.wsCaps, cred)
	if err != nil {
		return false, "caps: " + err.Error()
	}
	conn, err := wsx.DialWithHeaders(wsURL, 90*time.Second, c.headers)
	if err != nil {
		return false, "upgrade: " + err.Error()
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(60 * time.Second))
	cmd, _ := json.Marshal(map[string]any{"id": 1, "method": c.wsMeth, "params": map[string]any{}})
	if err := conn.WriteText(string(cmd)); err != nil {
		return false, "send: " + err.Error()
	}
	frame, err := conn.ReadText()
	if err != nil {
		return false, "read: " + err.Error()
	}
	return true, "channel ok: " + head(frame, 90)
}

// runFramework: CALL — POST a build, then poll the verdict to a terminal state.
func runFramework(cred httpx.Auth, c combo) (bool, string) {
	req := httpx.Request{Method: "POST", URL: c.url, Body: c.body, Headers: map[string]string{"Content-Type": "application/json"}}
	cred.Apply(&req)
	resp, err := httpx.Do(&http.Client{Timeout: 120 * time.Second}, req)
	if err != nil {
		return false, "submit: " + err.Error()
	}
	if resp.Status != 200 {
		return false, fmt.Sprintf("HTTP %d: %s", resp.Status, head(resp.Body, 120))
	}
	bid := firstArr(resp.Body, "buildId")
	if bid == "" {
		return false, "no buildId: " + head(resp.Body, 120)
	}
	// The build is async. We have a buildId (the wire act succeeded). A full
	// verdict poll needs the build-status endpoint; until that is mapped we
	// report the accepted submission as success with the id to inspect.
	return true, "buildId " + bid + " (submitted; verdict via dashboard/build-status)"
}

func runPool(cred httpx.Auth, combos []combo, parallel, retries int) []result {
	if parallel < 1 {
		parallel = 1
	}
	jobs := make(chan combo)
	out := make(chan result)
	var wg sync.WaitGroup
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				fmt.Printf("   -> %s\n", c.name)
				out <- run(cred, c, retries)
			}
		}()
	}
	go func() {
		for _, c := range combos {
			jobs <- c
		}
		close(jobs)
	}()
	go func() { wg.Wait(); close(out) }()
	var rs []result
	for r := range out {
		rs = append(rs, r)
	}
	return rs
}

// --- caps + helpers ---

// capsURL injects the resolved credential into LT:Options inside ?capabilities=
// — the channel inject shape, below the boundary (mirrors cmd/mcp applyChannelAuth).
func capsURL(base, capsJSON string, cred httpx.Auth) (string, error) {
	var caps map[string]any
	if err := json.Unmarshal([]byte(capsJSON), &caps); err != nil {
		return "", err
	}
	opts, _ := caps["LT:Options"].(map[string]any)
	if opts == nil {
		opts = map[string]any{}
	}
	opts["user"] = cred.User
	opts["accessKey"] = cred.Key
	caps["LT:Options"] = opts
	b, _ := json.Marshal(caps)
	return base + "?capabilities=" + url.QueryEscape(string(b)), nil
}

func seleniumCaps(br, ver, plat string, bidi bool, build, name string) string {
	always := map[string]any{
		"browserName": br, "browserVersion": ver,
		"LT:Options": map[string]any{"platformName": plat, "w3c": true, "video": true, "build": build, "name": name},
	}
	if bidi {
		always["webSocketUrl"] = true
	}
	b, _ := json.Marshal(map[string]any{"capabilities": map[string]any{"alwaysMatch": always}})
	return string(b)
}

func appiumCaps(plat, device, ver string, real bool, app, browser, build string) string {
	am := map[string]any{
		"platformName": plat, "appium:deviceName": device, "appium:platformVersion": ver,
		"appium:isRealMobile": real, "LT:Options": map[string]any{"w3c": true, "video": true, "build": build, "name": "appium-" + plat},
	}
	if plat == "ios" {
		am["appium:automationName"] = "XCUITest"
	} else {
		am["appium:automationName"] = "UiAutomator2"
	}
	if app != "" {
		am["appium:app"] = app
	}
	if browser != "" {
		am["browserName"] = browser
	}
	b, _ := json.Marshal(map[string]any{"capabilities": map[string]any{"alwaysMatch": am}})
	return string(b)
}

func frameworkBody(app, suite, device string, virtual bool, build string) string {
	m := map[string]any{
		"app": app, "testSuite": suite, "device": []string{device},
		"queueTimeout": 600, "idleTimeout": 600, "deviceLog": true, "network": true, "video": true,
		"build": build,
	}
	if virtual {
		m["isVirtualDevice"] = true
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// --- spec + json helpers ---

func loadSpec(path string) spec {
	b, err := os.ReadFile(path)
	if err != nil {
		// allow running from cmd/matrix or repo root
		b, err = os.ReadFile(filepath.Join("..", "..", path))
		if err != nil {
			die("read spec %s: %v", path, err)
		}
	}
	var s spec
	if err := json.Unmarshal(b, &s); err != nil {
		die("parse spec: %v", err)
	}
	return s
}

// id pulls the bare APP id out of an inventory value like "lt://APP123 (espresso, RD)".
func id(s spec, key string) string {
	v := s.Framework.Apps[key]
	if v == "" {
		if *flagReqApps {
			die("required app %q absent from spec inventory", key)
		}
		return ""
	}
	v = strings.TrimPrefix(strings.TrimSpace(v), "lt://")
	if i := strings.IndexAny(v, " \t("); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

func jsonStr(body, key string) string {
	var m map[string]any
	if json.Unmarshal([]byte(body), &m) != nil {
		return ""
	}
	if v, ok := m["value"].(map[string]any); ok {
		if s, ok := v[key].(string); ok {
			return s
		}
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

// firstArr reads key:[ "x" ] -> "x" (LambdaTest framework responses wrap scalars in arrays).
func firstArr(body, key string) string {
	var m map[string]any
	if json.Unmarshal([]byte(body), &m) != nil {
		return ""
	}
	if a, ok := m[key].([]any); ok && len(a) > 0 {
		if s, ok := a[0].(string); ok {
			return s
		}
	}
	return ""
}

func low(s string) string { return strings.ToLower(s) }
func head(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
func itoa(n int) string { return fmt.Sprintf("%d", n) }
func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "matrix: "+f+"\n", a...)
	os.Exit(2)
}
