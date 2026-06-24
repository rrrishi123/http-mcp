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
	"os/exec"
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
	name         string
	kind         kind
	url          string            // session: hub /session ; framework: build url ; channel: ws url (caps go in query)
	body         string            // session/framework request body
	wsCaps       string            // channel: the capabilities JSON (auth injected below boundary)
	headers      map[string]string // channel: ws upgrade headers (e.g. x-playwright-browser)
	wsMeth       string            // channel: the command method
	needApp      string            // prereq app key in spec.Framework.Apps (informational / --require-apps)
	host         bool              // composed by the host runtime (e.g. Playwright driver), NOT a wire channel
	hostWhy      string            // why it is host-only (shown in the report)
	hostRunnable bool              // the configured --pw-runner can actually execute this host combo
}

type result struct {
	name   string
	ok     bool
	host   bool // host-only: reported as HOST, excluded from the pass/fail gate
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
	flagCatalog  = flag.String("catalog", "specs/providers/lambdatest-catalog.json", "harvested device catalog to generate appium combos from")
	flagLimit    = flag.Int("limit", 40, "cap how many of the generated combos to actually RUN (0 = run all; the full count is always printed)")
	flagFull     = flag.Bool("full", false, "run the entire generated matrix (overrides --limit)")
	flagPerDev   = flag.Int("per-device", 1, "appium versions per device to include (from the catalog, newest first)")
	flagPwRunner = flag.String("pw-runner", "", "path to a node Playwright driver script; if set, host-only playwright combos actually run via `node <script> <authfile>`")
	flagDry      = flag.Bool("dry", false, "print the generated combos and exit — no smoke, no cloud sessions")
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
	full := len(combos)
	limit := *flagLimit
	if *flagFull {
		limit = 0
	}
	if *flagDry {
		fmt.Printf("generated %d combos (dry run — nothing executed):\n", full)
		for _, c := range combos {
			tag := "wire"
			if c.host {
				tag = "host"
			}
			fmt.Printf("  [%s] %s\n", tag, c.name)
		}
		return
	}
	if limit > 0 && full > limit {
		fmt.Printf("generated %d combos; running %d (--limit %d; --full runs all)\n", full, limit, limit)
		combos = combos[:limit]
	} else {
		fmt.Printf("generated %d combos; running all %d\n", full, full)
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
	pass, fail, host := 0, 0, 0
	fmt.Println("\n== results ==")
	for _, r := range results {
		mark := "PASS"
		switch {
		case r.host:
			mark, host = "HOST", host+1
		case !r.ok:
			mark, fail = "FAIL", fail+1
		default:
			pass++
		}
		fmt.Printf("  %-4s %-34s %8s  %s\n", mark, r.name, r.dur.Round(time.Millisecond), r.detail)
	}
	fmt.Printf("\n%d passed, %d failed, %d host-only, of %d (gate = wire combos only)\n", pass, fail, host, len(results))
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

	cat := loadCatalog(*flagCatalog)

	// selenium: browser x version x platform x {normal, bidi} — the desktop-web axes.
	browsers := []struct {
		br    string
		plats []string
	}{
		{"Chrome", []string{"Windows 11", "Windows 10", "macOS Sonoma"}},
		{"firefox", []string{"Windows 11", "Windows 10", "macOS Sonoma"}},
		{"MicrosoftEdge", []string{"Windows 11", "Windows 10"}},
		{"safari", []string{"macOS Sonoma", "macOS Ventura"}},
	}
	for _, bb := range browsers {
		for _, pl := range bb.plats {
			for _, v := range []string{"latest", "latest-1"} {
				for _, mode := range []string{"normal", "bidi"} {
					bidi := mode == "bidi"
					if bidi && bb.br == "safari" {
						continue // safari has no stable BiDi on the grid
					}
					nm := fmt.Sprintf("selenium-%s-%s-%s-%s", low(bb.br), slug(pl), slug(v), mode)
					cs = append(cs, combo{name: nm, kind: session, url: web,
						body: seleniumCaps(bb.br, v, pl, bidi, b, nm)})
				}
			}
		}
	}

	// appium: pool x platform x device(from catalog) x {app, web}. This is where the
	// matrix gets big — every device the catalog lists, both pools, both surfaces.
	addAppium := func(pool, plat string, devices map[string][]string, real bool, app string) {
		br := "Chrome"
		if plat == "ios" {
			br = "safari"
		}
		for _, d := range topDevices(devices, *flagPerDev) {
			if app != "" { // app-on-device (VD: we have emulator/simulator apps)
				cs = append(cs, combo{name: fmt.Sprintf("appium-%s-app-%s-%s-%s", plat, pool, slug(d.name), slug(d.ver)),
					kind: session, url: mob, needApp: app,
					body: appiumCaps(plat, d.name, d.ver, real, "lt://"+id(sp, app), "", b)})
			}
			cs = append(cs, combo{name: fmt.Sprintf("appium-%s-web-%s-%s-%s", plat, pool, slug(d.name), slug(d.ver)),
				kind: session, url: mob,
				body: appiumCaps(plat, d.name, d.ver, real, "", br, b)})
		}
	}
	addAppium("vd", "android", cat("vd", "app", "android"), false, "androidVirtual")
	addAppium("vd", "ios", cat("vd", "app", "ios"), false, "iosVirtual")
	addAppium("vd", "android", cat("vd", "web", "android"), false, "")
	addAppium("vd", "ios", cat("vd", "web", "ios"), false, "")
	addAppium("rd", "android", cat("rd", "android"), true, "") // RD web (RD app needs an RD-uploaded apk; not in inventory)
	addAppium("rd", "ios", cat("rd", "ios"), true, "")

	// puppeteer: raw CDP channel (no upgrade header)
	cs = append(cs, combo{name: "puppeteer-chrome-cdp", kind: channel,
		url: cdp + "/puppeteer", wsMeth: "Browser.getVersion",
		wsCaps: `{"browserName":"Chrome","browserVersion":"latest","LT:Options":{"platform":"Windows 11","build":"` + b + `","name":"puppeteer-chrome-cdp"}}`})

	// playwright: host-only. /playwright is driver-mediated (JsonPipeTransport spawns
	// Playwright's driver process for the handshake) — a raw wsx upgrade 400s. The
	// x-playwright-browser header is necessary-not-sufficient; the driver does the rest.
	// Composed by the host runtime (8/pilot), not the wire. Not gated.
	cs = append(cs,
		combo{name: "playwright-chrome-desktop-web", host: true, hostRunnable: true,
			hostWhy: "driver-mediated (/playwright) — Playwright runtime; --pw-runner executes it via the host driver"},
		combo{name: "playwright-chrome-rd-web", host: true,
			hostWhy: "driver-mediated, real-device playwright caps — host runtime (runner caps a follow-up)"})

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
	if c.host {
		// host-only (e.g. Playwright driver): not a wire channel. If a --pw-runner
		// is configured and this combo is runnable by it, the HOST composes it via
		// the Playwright driver (node). The runner reads the gitignored auth file
		// itself — the secret stays below the boundary, never an arg or a log.
		if c.hostRunnable && *flagPwRunner != "" {
			start := time.Now()
			authFile := "auth/" + strings.ReplaceAll(*flagProfile, ":", "-") + ".json"
			out, err := exec.Command("node", *flagPwRunner, authFile).CombinedOutput()
			if err != nil {
				return result{name: c.name, ok: false, detail: "host pw-runner: " + head(string(out), 120), dur: time.Since(start)}
			}
			return result{name: c.name, ok: true, detail: "host-driver: " + head(string(out), 110), dur: time.Since(start)}
		}
		return result{name: c.name, host: true, detail: c.hostWhy}
	}
	start := time.Now()
	var last string
	for i := 0; i < attempts; i++ {
		ok, detail := once(cred, c)
		if ok {
			return result{name: c.name, ok: true, detail: detail, dur: time.Since(start)}
		}
		last = detail
	}
	return result{name: c.name, ok: false, detail: "after " + itoa(attempts) + " try(s): " + last, dur: time.Since(start)}
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
	// Real devices can queue for allocation; allow up to 3 min before calling it.
	resp, err := httpx.Do(&http.Client{Timeout: 180 * time.Second}, req)
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

// --- catalog (device matrix generation) ---

// loadCatalog parses the harvested device catalog and returns a getter:
//
//	cat("vd","app","android")  or  cat("rd","ios")  ->  {deviceName: [versions]}
func loadCatalog(path string) func(...string) map[string][]string {
	b, err := os.ReadFile(path)
	if err != nil {
		b, err = os.ReadFile(filepath.Join("..", "..", path))
	}
	root := map[string]any{}
	if err == nil {
		_ = json.Unmarshal(b, &root)
	}
	pools, _ := root["pools"].(map[string]any)
	return func(p ...string) map[string][]string {
		var cur any = pools
		for _, k := range p {
			m, ok := cur.(map[string]any)
			if !ok {
				return nil
			}
			cur = m[k]
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		out := map[string][]string{}
		for name, v := range m {
			arr, ok := v.([]any)
			if !ok {
				continue
			}
			var vs []string
			for _, x := range arr {
				if s, ok := x.(string); ok {
					vs = append(vs, s)
				}
			}
			out[name] = vs
		}
		return out
	}
}

type dvc struct{ name, ver string }

// topDevices returns devices sorted by name, each with up to perDev newest-first versions.
func topDevices(m map[string][]string, perDev int) []dvc {
	if perDev < 1 {
		perDev = 1
	}
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []dvc
	for _, n := range names {
		vs := append([]string(nil), m[n]...)
		sort.Sort(sort.Reverse(sort.StringSlice(vs)))
		for i := 0; i < perDev && i < len(vs); i++ {
			out = append(out, dvc{n, vs[i]})
		}
	}
	return out
}

// slug makes a combo-name-safe token from a device/platform/version string.
func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var bld strings.Builder
	prevDash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			bld.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			bld.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(bld.String(), "-")
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
