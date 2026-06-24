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
	pwCaps       string            // host playwright caps JSON passed to the driver runner
	skip         string            // non-empty => a prereq is missing; reported SKIP, not gated
}

type result struct {
	name   string
	ok     bool
	host   bool // host-only: reported as HOST, excluded from the pass/fail gate
	skip   bool // prereq missing: reported SKIP, excluded from the gate
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
	flagLimit    = flag.Int("limit", 0, "cap how many combos to actually RUN (0 = run all; the curated set is small)")
	flagFull     = flag.Bool("full", false, "run the entire matrix (overrides --limit)")
	flagPwRunner = flag.String("pw-runner", "", "path to a node Playwright driver script; if set, host playwright combos run via `node <script> <authfile> <capsJSON>`")
	flagDry      = flag.Bool("dry", false, "print the generated combos and exit — no smoke, no cloud sessions")
	flagDuration = flag.Duration("duration", 0, "drive each session this long with real activity (navigate+screenshot) so artifacts are non-empty; e.g. 3m for a genuine run (0 = create+verify+teardown only)")
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
	pass, fail, host, skip := 0, 0, 0, 0
	fmt.Println("\n== results ==")
	for _, r := range results {
		mark := "PASS"
		switch {
		case r.skip:
			mark, skip = "SKIP", skip+1
		case r.host:
			mark, host = "HOST", host+1
		case !r.ok:
			mark, fail = "FAIL", fail+1
		default:
			pass++
		}
		fmt.Printf("  %-4s %-30s %8s  %s\n", mark, r.name, r.dur.Round(time.Millisecond), r.detail)
	}
	fmt.Printf("\n%d passed, %d failed, %d host-only, %d skipped, of %d (gate = wire combos only)\n", pass, fail, host, skip, len(results))
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

	// Curated, representative matrix — one default capability per type (NOT the
	// per-device catalog explosion). Desktop = win+mac x {chrome,firefox}; mobile
	// = one device per type. All combos carry ALL artifact capabilities (video,
	// network, console, visual, deviceLog) so a genuine ~3-min run produces evidence.
	desktop := []struct{ tag, plat, br string }{
		{"win-chrome", "Windows 11", "Chrome"}, {"win-firefox", "Windows 11", "firefox"},
		{"mac-chrome", "macOS Sonoma", "Chrome"}, {"mac-firefox", "macOS Sonoma", "firefox"},
	}
	const rdAndroid, rdAndroidV = "Galaxy S23", "14"
	const rdIOS, rdIOSV = "iPhone 14", "18"

	// 5 — selenium desktop (wire CALL)
	for _, d := range desktop {
		cs = append(cs, combo{name: "selenium-desktop-" + d.tag, kind: session, url: web,
			body: seleniumCaps(d.br, "latest", d.plat, false, b, "selenium-"+d.tag)})
	}

	// 2 — puppeteer desktop (wire CHANNEL). CDP is chromium-only => firefox can't.
	for _, d := range desktop {
		if low(d.br) == "firefox" {
			cs = append(cs, combo{name: "puppeteer-desktop-" + d.tag, skip: "puppeteer is CDP/chromium-only — firefox not supported over /puppeteer"})
			continue
		}
		cs = append(cs, combo{name: "puppeteer-desktop-" + d.tag, kind: channel, url: cdp + "/puppeteer",
			wsMeth: "Browser.getVersion", wsCaps: browserChanCaps(d.br, d.plat, b, "puppeteer-"+d.tag)})
	}

	// 1 — playwright desktop (host driver, chrome+firefox) + RD web android/ios
	for _, d := range desktop {
		cs = append(cs, combo{name: "playwright-desktop-" + d.tag, host: true, hostRunnable: true,
			hostWhy: "Playwright driver runtime (--pw-runner)", pwCaps: browserChanCaps(d.br, d.plat, b, "playwright-"+d.tag)})
	}
	cs = append(cs,
		combo{name: "playwright-android-rd-web", host: true, hostRunnable: true,
			hostWhy: "Playwright driver, real android", pwCaps: pwMobileCaps("android", rdAndroid, rdAndroidV, b)},
		combo{name: "playwright-ios-rd-web", host: true, hostRunnable: true,
			hostWhy: "Playwright driver, real ios", pwCaps: pwMobileCaps("ios", rdIOS, rdIOSV, b)})

	// 4 — appium real device, app + web, android + ios. RD app needs an RD-uploaded
	// app (appium app upload, not the framework upload) — skip with a clear prereq.
	cs = append(cs,
		combo{name: "appium-rd-app-android", kind: session, url: mob,
			skip: "needs an RD-uploaded android apk (app/upload/realDevice) — not in inventory"},
		combo{name: "appium-rd-web-android", kind: session, url: mob,
			body: appiumCaps("android", rdAndroid, rdAndroidV, true, "", "Chrome", b)},
		combo{name: "appium-rd-app-ios", kind: session, url: mob,
			skip: "needs an RD-uploaded ios ipa (app/upload/realDevice) — not in inventory"},
		combo{name: "appium-rd-web-ios", kind: session, url: mob,
			body: appiumCaps("ios", rdIOS, rdIOSV, true, "", "safari", b)})

	// 3 — cypress desktop (CALL job): needs a project zip + cli submit. WIP runner.
	for _, d := range desktop {
		cs = append(cs, combo{name: "cypress-desktop-" + d.tag,
			skip: "needs a cypress project zip + cli/build POST — cypress runner WIP"})
	}

	// 6 — xcui + espresso framework builds. NO testName/name — the build name carries
	// the type (espresso/xcui), per the dashboard convention.
	cs = append(cs,
		fwCombo(sp, "xcui-rd-ios", fw+"/v1/xcui/build", "ProverbialApp", "ProverbialTestApp", rdIOS+"-"+rdIOSV, false, b,
			"RD ios xcui needs an RD-uploaded ipa+testSuite (only VD proverbial present)"),
		fwCombo(sp, "xcui-vd-ios", fw+"/v1/xcui/build", "ProverbialApp", "ProverbialTestApp", "iPhone 15-18.0", true, b, ""),
		fwCombo(sp, "espresso-rd-android", fw+"/v1/espresso/build", "espressoMyApp", "espressoMyTestApp", rdAndroid+"-"+rdAndroidV, false, b, ""),
		fwCombo(sp, "espresso-vd-android", fw+"/v1/espresso/build", "androidVirtual", "", "Galaxy S23-14", true, b,
			"VD espresso needs an emulator app+testSuite (androidVirtual app present, no test suite)"))

	return cs
}

// fwCombo builds a framework-build combo if both app ids resolve, else a SKIP with why.
func fwCombo(sp spec, name, url, appKey, suiteKey, device string, virtual bool, build, missingWhy string) combo {
	a, t := id(sp, appKey), id(sp, suiteKey)
	if a == "" || t == "" {
		why := missingWhy
		if why == "" {
			why = "missing app/testSuite in inventory"
		}
		return combo{name: name, skip: why}
	}
	return combo{name: name, kind: framework, url: url, needApp: appKey,
		body: frameworkBody("lt://"+a, "lt://"+t, device, virtual, build)}
}

// --- runners ---

func run(cred httpx.Auth, c combo, attempts int) result {
	if c.skip != "" {
		return result{name: c.name, skip: true, detail: "SKIP prereq: " + c.skip}
	}
	if c.host {
		// host-only (e.g. Playwright driver): not a wire channel. If a --pw-runner
		// is configured and this combo is runnable by it, the HOST composes it via
		// the Playwright driver (node), passing the per-combo caps. The runner reads
		// the gitignored auth file itself — the secret stays below the boundary.
		if c.hostRunnable && *flagPwRunner != "" {
			start := time.Now()
			authFile := "auth/" + strings.ReplaceAll(*flagProfile, ":", "-") + ".json"
			out, err := exec.Command("node", *flagPwRunner, authFile, c.pwCaps).CombinedOutput()
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
	base := strings.TrimSuffix(c.url, "/session")
	drove := ""
	if *flagDuration > 0 {
		n := driveSession(cred, base, sid, strings.Contains(c.body, `"browserName"`), *flagDuration)
		drove = fmt.Sprintf(" (drove %s, %d acts)", flagDuration.Round(time.Second), n)
	}
	// best-effort delete (free the parallel); ignore the result
	del := httpx.Request{Method: "DELETE", URL: base + "/session/" + sid}
	cred.Apply(&del)
	_, _ = httpx.Do(&http.Client{Timeout: 30 * time.Second}, del)
	return true, "session " + sid + drove
}

// driveSession keeps a live session busy for dur with real activity so its video,
// network HAR, and screenshots are non-empty — a genuine run, not a connect-and-quit.
// Best-effort: drive errors don't fail the combo (the session creation was the wire act).
func driveSession(cred httpx.Auth, base, sid string, web bool, dur time.Duration) int {
	urls := []string{"https://www.lambdatest.com", "https://www.lambdatest.com/support/docs/", "https://www.lambdatest.com/blog/"}
	deadline := time.Now().Add(dur)
	acts := 0
	for i := 0; time.Now().Before(deadline); i++ {
		if web {
			body, _ := json.Marshal(map[string]string{"url": urls[i%len(urls)]})
			nav := httpx.Request{Method: "POST", URL: base + "/session/" + sid + "/url", Body: string(body), Headers: map[string]string{"Content-Type": "application/json"}}
			cred.Apply(&nav)
			_, _ = httpx.Do(&http.Client{Timeout: 60 * time.Second}, nav)
			acts++
		}
		// a screenshot is activity AND a captured frame; works on web and native app
		ss := httpx.Request{Method: "GET", URL: base + "/session/" + sid + "/screenshot"}
		cred.Apply(&ss)
		_, _ = httpx.Do(&http.Client{Timeout: 60 * time.Second}, ss)
		acts++
		time.Sleep(8 * time.Second)
	}
	return acts
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

// allArtifacts: every capture LT offers for a web session — so a genuine run
// leaves video, network HAR, console, visual, and terminal logs to fetch.
func allArtifactsWeb(plat, build, name string) map[string]any {
	return map[string]any{"platformName": plat, "w3c": true, "build": build, "name": name,
		"video": true, "network": true, "console": true, "visual": true, "terminal": true}
}

func seleniumCaps(br, ver, plat string, bidi bool, build, name string) string {
	always := map[string]any{"browserName": br, "browserVersion": ver, "LT:Options": allArtifactsWeb(plat, build, name)}
	if bidi {
		always["webSocketUrl"] = true
	}
	b, _ := json.Marshal(map[string]any{"capabilities": map[string]any{"alwaysMatch": always}})
	return string(b)
}

// browserChanCaps: ?capabilities= caps for a CDP/playwright channel, all artifacts on.
func browserChanCaps(br, plat, build, name string) string {
	b, _ := json.Marshal(map[string]any{"browserName": br, "browserVersion": "latest",
		"LT:Options": map[string]any{"platform": plat, "build": build, "name": name,
			"video": true, "network": true, "console": true, "visual": true}})
	return string(b)
}

// pwMobileCaps: playwright-on-real-mobile-web caps (driver consumes it). iOS web = webkit.
func pwMobileCaps(plat, device, ver, build string) string {
	br := "Chrome"
	if plat == "ios" {
		br = "pw-webkit"
	}
	b, _ := json.Marshal(map[string]any{"browserName": br, "browserVersion": "latest",
		"LT:Options": map[string]any{"platformName": plat, "deviceName": device, "platformVersion": ver,
			"isRealMobile": true, "build": build, "name": "playwright-" + plat + "-rd-web",
			"video": true, "network": true, "console": true}})
	return string(b)
}

func appiumCaps(plat, device, ver string, real bool, app, browser, build string) string {
	am := map[string]any{
		"platformName": plat, "appium:deviceName": device, "appium:platformVersion": ver,
		"appium:isRealMobile": real, "LT:Options": map[string]any{"w3c": true, "build": build, "name": "appium-" + plat,
			"video": true, "network": true, "deviceLog": true, "visual": true},
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
