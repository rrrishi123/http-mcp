// Append route-table snapshots to specs/ from version-addressable upstreams.
//
// Append-only: a snapshot file is named <name>@<version>.json and is never
// rewritten. New upstream version -> new file. Run with no args; it fetches
// the latest published versions and writes only what is missing.
package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var wdioProtocols = []string{"webdriver", "chromium", "gecko", "mjsonwp", "selenium"}

type route struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

type snapshot struct {
	Source  string  `json:"source"`
	Version string  `json:"version"`
	Fetched string  `json:"fetched"`
	Count   int     `json:"count"`
	Routes  []route `json:"routes"`
}

func get(url string) []byte {
	resp, err := http.Get(url)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		panic(fmt.Sprintf("GET %s -> %d", url, resp.StatusCode))
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	return b
}

func npmLatest(pkg string) string {
	var meta struct {
		DistTags map[string]string `json:"dist-tags"`
	}
	if err := json.Unmarshal(get("https://registry.npmjs.org/"+strings.ReplaceAll(pkg, "/", "%2F")), &meta); err != nil {
		panic(err)
	}
	return meta.DistTags["latest"]
}

var (
	pathKey   = regexp.MustCompile(`'(/[^']*)':\s*\{`)
	methodTok = regexp.MustCompile(`[{}]|\b(GET|POST|DELETE|PUT|PATCH)\s*:`)
)

// extractRoutes pulls (method, path) pairs out of a JS/TS route-map object.
func extractRoutes(text string) []route {
	seen := map[route]bool{}
	for _, m := range pathKey.FindAllStringSubmatchIndex(text, -1) {
		path := text[m[2]:m[3]]
		i, depth := m[1], 1
		for i < len(text) && depth > 0 {
			switch text[i] {
			case '{':
				depth++
			case '}':
				depth--
			}
			i++
		}
		block, d := text[m[1]:i], 0
		for _, t := range methodTok.FindAllStringSubmatch(block, -1) {
			switch t[0] {
			case "{":
				d++
			case "}":
				d--
			default:
				if d == 0 {
					seen[route{Method: t[1], Path: path}] = true
				}
			}
		}
	}
	routes := make([]route, 0, len(seen))
	for r := range seen {
		routes = append(routes, r)
	}
	sort.Slice(routes, func(a, b int) bool {
		if routes[a].Method != routes[b].Method {
			return routes[a].Method < routes[b].Method
		}
		return routes[a].Path < routes[b].Path
	})
	return routes
}

func specsDir() string {
	wd, _ := os.Getwd()
	return filepath.Join(wd, "specs")
}

func writeSnapshot(rel, source, version string, routes []route) {
	out := filepath.Join(specsDir(), rel)
	if _, err := os.Stat(out); err == nil {
		fmt.Println("current ", rel)
		return
	}
	os.MkdirAll(filepath.Dir(out), 0o755)
	b, _ := json.MarshalIndent(snapshot{
		Source: source, Version: version,
		Fetched: time.Now().Format("2006-01-02"),
		Count:   len(routes), Routes: routes,
	}, "", " ")
	os.WriteFile(out, b, 0o644)
	fmt.Printf("appended %s (%d routes)\n", rel, len(routes))
}

func harvestAppium() {
	v := npmLatest("@appium/base-driver")
	rel := "appium/base-driver@" + v + ".json"
	if _, err := os.Stat(filepath.Join(specsDir(), rel)); err == nil {
		fmt.Println("current ", rel)
		return
	}
	gz, err := gzip.NewReader(strings.NewReader(string(get(
		"https://registry.npmjs.org/@appium/base-driver/-/base-driver-" + v + ".tgz"))))
	if err != nil {
		panic(err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			panic("routes.js not in tarball: " + err.Error())
		}
		if h.Name == "package/build/lib/protocol/routes.js" {
			text, _ := io.ReadAll(tr)
			writeSnapshot(rel, "npm:@appium/base-driver", v, extractRoutes(string(text)))
			return
		}
	}
}

func harvestWdio() {
	v := npmLatest("@wdio/protocols")
	base := "https://raw.githubusercontent.com/webdriverio/webdriverio/refs/tags/v" + v +
		"/packages/wdio-protocols/src/protocols"
	for _, name := range wdioProtocols {
		label := name
		if name == "selenium" {
			label = "selenium-grid"
		}
		rel := "webdriver/" + label + "@wdio-protocols-" + v + ".json"
		if _, err := os.Stat(filepath.Join(specsDir(), rel)); err == nil {
			fmt.Println("current ", rel)
			continue
		}
		writeSnapshot(rel, "npm:@wdio/protocols/"+name, v,
			extractRoutes(string(get(base+"/"+name+".ts"))))
	}
}

// getLT fetches a LambdaTest public catalog endpoint. It is CORS-gated, so it
// wants the generator's own origin/referer — the same headers the
// capabilities-generator UI sends. No auth: this is the public device catalog.
func getLT(url string) []byte {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("accept", "application/json")
	req.Header.Set("origin", "https://www.lambdatest.com")
	req.Header.Set("referer", "https://www.lambdatest.com/capabilities-generator/")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		panic(fmt.Sprintf("GET %s -> %d", url, resp.StatusCode))
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	return b
}

// ltBrands flattens a {brands: {<brand>: [{name, osVersion[]}]}} node into
// {deviceName: [osVersion...]}. Both catalog shapes bottom out here.
func ltBrands(node map[string]any) map[string][]string {
	out := map[string][]string{}
	brands, _ := node["brands"].(map[string]any)
	for _, list := range brands {
		arr, _ := list.([]any)
		for _, d := range arr {
			dm, _ := d.(map[string]any)
			name, _ := dm["name"].(string)
			var vers []string
			if ov, ok := dm["osVersion"].([]any); ok {
				for _, v := range ov {
					if s, ok := v.(string); ok {
						vers = append(vers, s)
					}
				}
			}
			sort.Strings(vers)
			if name != "" {
				out[name] = vers
			}
		}
	}
	return out
}

// ltAppiumLatest flattens appiumVersions.<plat>[] -> {os_version: latest_version}.
func ltAppiumLatest(section map[string]any, plat string) map[string]string {
	out := map[string]string{}
	av, _ := section["appiumVersions"].(map[string]any)
	arr, _ := av[plat].([]any)
	for _, e := range arr {
		em, _ := e.(map[string]any)
		os, _ := em["os_version"].(string)
		latest, _ := em["latest_version"].(string)
		if os != "" {
			out[os] = latest
		}
	}
	return out
}

// harvestLambdatestCatalog pulls the LIVE device + appium + browser catalog the
// capabilities-generator UI calls, for both pools (virtual + real device), and
// writes a normalized snapshot. Unlike the route tables this is REWRITTEN each
// run with no timestamp inside it — so the file changes only when LambdaTest's
// catalog actually changes (a new device/OS/appium version), and harvest-drift
// surfaces that. This is the api-doc/catalog -> specs pull the matrix needs.
func harvestLambdatestCatalog() {
	defer func() {
		// A third-party live endpoint can blip; do not fail the whole harvest.
		if r := recover(); r != nil {
			fmt.Printf("lambdatest catalog: skipped (%v)\n", r)
		}
	}()
	base := "https://mobile-api.lambdatest.com/mobile-automation/api/v1/capability/generator?isVirtualDevice="
	pools := map[string]any{}
	for _, p := range []struct{ key, flag string }{{"vd", "true"}, {"rd", "false"}} {
		var raw map[string]any
		if err := json.Unmarshal(getLT(base+p.flag), &raw); err != nil {
			panic(err)
		}
		entry := map[string]any{}
		if _, hasApp := raw["app"]; hasApp {
			// VD shape: app/web sections, each {appiumVersions, devices:{<plat>:{brands}}}.
			for _, sect := range []string{"app", "web"} {
				s, _ := raw[sect].(map[string]any)
				if s == nil {
					continue
				}
				devs, _ := s["devices"].(map[string]any)
				se := map[string]any{
					"appiumLatestAndroid": ltAppiumLatest(s, "android"),
					"appiumLatestIos":     ltAppiumLatest(s, "ios"),
				}
				for plat, node := range devs {
					if nm, ok := node.(map[string]any); ok {
						se[plat] = ltBrands(nm)
					}
				}
				if vm, ok := s["vdOsBrowserMapping"]; ok {
					se["osBrowserMapping"] = vm
				}
				entry[sect] = se
			}
		} else {
			// RD shape: top-level <plat> -> {brands} (android/ios/roku/tvos).
			for plat, node := range raw {
				if nm, ok := node.(map[string]any); ok {
					if _, hasBrands := nm["brands"]; hasBrands {
						entry[plat] = ltBrands(nm)
					}
				}
			}
		}
		pools[p.key] = entry
	}
	out := map[string]any{
		"source":   "lambdatest capabilities-generator (live device/appium/browser catalog)",
		"endpoint": base + "{true|false}",
		"_note":    "rewritten each harvest, no timestamp inside — a diff means LambdaTest's catalog changed. pools: vd=virtual, rd=real device; each has app+web sections.",
		"pools":    pools,
	}
	b, _ := json.MarshalIndent(out, "", " ")
	path := filepath.Join(specsDir(), "providers", "lambdatest-catalog.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, append(b, '\n'), 0o644)
	fmt.Println("wrote specs/providers/lambdatest-catalog.json")
}

func main() {
	harvestAppium()
	harvestWdio()
	harvestLambdatestCatalog()
}
