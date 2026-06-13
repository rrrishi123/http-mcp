// Verify harvested route tables against a live binary.
//
// Probes every route in every specs/ snapshot with a bogus session id.
// The server's own answer discriminates:
//
//	W3C JSON error body  -> route matched a handler -> EXISTS
//	                        (any error except 'unknown command')
//	non-JSON body        -> the router never matched -> ABSENT
//
// No real session is created or touched. Zero side effects.
//
// Usage: probe <base_url> <label>
//
//	probe http://localhost:4444 geckodriver@0.37.0
package main

import (
	"bytes"
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

const bogus = "deadbeef-dead-4dead-dead-deaddeadbeef"

var absentErrors = map[string]bool{"unknown command": true, "unknown method": true}

type routeKey struct{ Method, Path string }

type result struct {
	Method    string   `json:"method"`
	Path      string   `json:"path"`
	Verdict   string   `json:"verdict"`
	HTTP      int      `json:"http"`
	Error     string   `json:"error"`
	ClaimedBy []string `json:"claimed_by"`
}

type report struct {
	Binary    string         `json:"binary"`
	BaseURL   string         `json:"base_url"`
	Probed    string         `json:"probed"`
	UnionSize int            `json:"union_size"`
	Tally     map[string]int `json:"tally"`
	Results   []result       `json:"results"`
}

func fill(path string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		isParam := strings.HasPrefix(s, ":") ||
			(strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}"))
		if !isParam {
			continue
		}
		if strings.Contains(strings.ToLower(s), "session") {
			segs[i] = bogus
		} else {
			segs[i] = "bogus"
		}
	}
	return strings.Join(segs, "/")
}

var errField = regexp.MustCompile(`"error"\s*:\s*"([^"]+)"`)

func probe(client *http.Client, base, method, path string) (string, int, string) {
	var body io.Reader
	if method == "POST" || method == "PUT" || method == "PATCH" {
		body = bytes.NewReader([]byte("{}"))
	}
	req, _ := http.NewRequest(method, base+fill(path), body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "unreachable", 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
	if resp.StatusCode < 300 {
		return "exists", resp.StatusCode, ""
	}
	if m := errField.FindSubmatch(b); m != nil {
		errVal := string(m[1])
		if absentErrors[errVal] {
			return "absent", resp.StatusCode, errVal
		}
		return "exists", resp.StatusCode, errVal
	}
	short := strings.TrimSpace(string(b))
	if len(short) > 40 {
		short = short[:40]
	}
	return "absent", resp.StatusCode, short
}

func unionRoutes(specs string) map[routeKey][]string {
	routes := map[routeKey][]string{}
	filepath.Walk(specs, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.Contains(info.Name(), "@") ||
			!strings.HasSuffix(p, ".json") || filepath.Base(filepath.Dir(p)) == "probes" {
			return nil
		}
		var snap struct {
			Routes []struct{ Method, Path string } `json:"routes"`
		}
		b, _ := os.ReadFile(p)
		if json.Unmarshal(b, &snap) != nil {
			return nil
		}
		stem := strings.TrimSuffix(info.Name(), ".json")
		for _, r := range snap.Routes {
			k := routeKey{r.Method, r.Path}
			routes[k] = append(routes[k], stem)
		}
		return nil
	})
	return routes
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: probe <base_url> <label>")
		os.Exit(1)
	}
	base, label := strings.TrimRight(os.Args[1], "/"), os.Args[2]
	wd, _ := os.Getwd()
	specs := filepath.Join(wd, "specs")

	routes := unionRoutes(specs)
	keys := make([]routeKey, 0, len(routes))
	for k := range routes {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool {
		if keys[a].Method != keys[b].Method {
			return keys[a].Method < keys[b].Method
		}
		return keys[a].Path < keys[b].Path
	})

	client := &http.Client{Timeout: 5 * time.Second}
	tally := map[string]int{}
	results := make([]result, 0, len(keys))
	for _, k := range keys {
		verdict, code, errStr := probe(client, base, k.Method, k.Path)
		tally[verdict]++
		claimed := routes[k]
		sort.Strings(claimed)
		results = append(results, result{k.Method, k.Path, verdict, code, errStr, claimed})
	}

	out := filepath.Join(specs, "probes", label+".json")
	os.MkdirAll(filepath.Dir(out), 0o755)
	b, _ := json.MarshalIndent(report{
		Binary: label, BaseURL: base,
		Probed:    time.Now().Format("2006-01-02"),
		UnionSize: len(routes), Tally: tally, Results: results,
	}, "", " ")
	os.WriteFile(out, b, 0o644)
	fmt.Printf("%s: %v -> %s\n", label, tally, out)
}
