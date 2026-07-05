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

func main() {
	harvestAppium()
	harvestWdio()
}
