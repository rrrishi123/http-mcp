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

// errNotFound marks an upstream 404 so a caller can treat a retired file as
// news rather than a crash (wdio 10 dropped mjsonwp.ts).
type errNotFound struct{ url string }

func (e errNotFound) Error() string { return "GET " + e.url + " -> 404" }

func get(url string) []byte {
	// Transient failures (TLS handshake timeouts, 429, 5xx) must not red the
	// advisory drift harvest — this is a daily "did upstream cut a version"
	// check, not a correctness gate. Retry with linear backoff; a real 4xx or a
	// final exhausted retry still panics, so a genuine upstream change surfaces.
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
		resp, err := http.Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("GET %s -> %d", url, resp.StatusCode)
			continue
		}
		if resp.StatusCode == 404 {
			resp.Body.Close()
			panic(errNotFound{url})
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			panic(fmt.Sprintf("GET %s -> %d", url, resp.StatusCode))
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		return b
	}
	panic(fmt.Sprintf("GET %s failed after retries: %v", url, lastErr))
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

// appiumRoutesPrefix is where @appium/base-driver keeps its route table.
// Up to 10.7.x it was ONE file (routes.js); 10.8.0 split it into a directory
// (routes/w3c.js, jsonwp.js, mjsonwp.js, appium.js, appium-device.js,
// extensions/*.js) assembled by routes/index.js. Harvest both shapes: every
// .js under the prefix is route source; the snapshot is their union.
const appiumRoutesPrefix = "package/build/lib/protocol/routes"

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
	var text strings.Builder
	var files []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			panic("reading @appium/base-driver tarball: " + err.Error())
		}
		if h.Name != appiumRoutesPrefix+".js" &&
			!(strings.HasPrefix(h.Name, appiumRoutesPrefix+"/") && strings.HasSuffix(h.Name, ".js")) {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			panic("reading " + h.Name + ": " + err.Error())
		}
		text.Write(b)
		text.WriteString("\n")
		files = append(files, h.Name)
	}
	if len(files) == 0 {
		// the layout moved again: fail LOUDLY with what we looked for, not a bare EOF
		panic("no route source under " + appiumRoutesPrefix + "{.js,/**/*.js} in @appium/base-driver@" + v +
			" — upstream moved the route table; update appiumRoutesPrefix")
	}
	routes := extractRoutes(text.String())
	if len(routes) == 0 {
		panic(fmt.Sprintf("route source found (%d files) but 0 routes extracted from @appium/base-driver@%s — the route-map shape changed", len(files), v))
	}
	writeSnapshot(rel, "npm:@appium/base-driver", v, routes)
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
		src, ok := getProtocol(base + "/" + name + ".ts")
		if !ok {
			// a protocol upstream no longer ships at this version: say so loudly in
			// CI, keep the older snapshots, and harvest the rest
			fmt.Printf("::warning::retired  %s — %s.ts is gone at @wdio/protocols@%s; older snapshots kept\n", rel, name, v)
			continue
		}
		writeSnapshot(rel, "npm:@wdio/protocols/"+name, v, extractRoutes(string(src)))
	}
}

// getProtocol is get, except a 404 reports ok=false instead of panicking.
func getProtocol(url string) (b []byte, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, nf := r.(errNotFound); !nf {
				panic(r)
			}
			b, ok = nil, false
		}
	}()
	return get(url), true
}

func main() {
	harvestAppium()
	harvestWdio()
}
