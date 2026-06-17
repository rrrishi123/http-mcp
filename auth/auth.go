// Package auth keeps the credential below the trust boundary.
//
// The agent above the boundary passes only a profile *name*; the secret bytes
// are resolved here, once, from the environment (curl-style) or a gitignored
// profile file — never from committed code, never from the agent. This is the
// frustum: the tip of the cone (the key) stays in the vault; everything above
// holds a signed request, not the secret.
//
// Resolution order for a profile name:
//  1. env LT_USERNAME + LT_ACCESS_KEY  (how any prod injects a secret; the
//     client exports these from their own LambdaTest dashboard → Settings → Keys)
//  2. auth/<profile>.json              (gitignored; {"username":..,"accessKey":..})
//
// Scope is deliberately one account — prod:adminltqa — not an env/user matrix.
// http-mcp stays curl-shaped: it knows how to *carry* a credential, not where
// any particular org keeps theirs.
package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rrrishi123/http-mcp/internal/httpx"
)

type profile struct {
	Username  string `json:"username"`
	AccessKey string `json:"accessKey"`
}

// Resolve turns a profile name into a Basic credential. The returned Auth is
// meant to be Apply'd to a single Request and discarded; nothing above this
// package retains the key.
func Resolve(name string) (httpx.Auth, error) {
	if u, k := os.Getenv("LT_USERNAME"), os.Getenv("LT_ACCESS_KEY"); u != "" && k != "" {
		return httpx.Auth{Type: "basic", User: u, Key: k}, nil
	}

	rel := "auth/" + strings.ReplaceAll(name, ":", "-") + ".json"
	// Look next to the working directory first, then next to the executable.
	// The binary lives at repo-root/http-mcp, so repo-root/auth/<profile>.json
	// resolves no matter where the MCP host launched it from.
	candidates := []string{rel}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), rel))
	}
	var raw []byte
	var err error
	for _, c := range candidates {
		if raw, err = os.ReadFile(c); err == nil {
			break
		}
	}
	if err != nil {
		return httpx.Auth{}, fmt.Errorf(
			"auth profile %q: set LT_USERNAME/LT_ACCESS_KEY in the environment or provide %s: %w",
			name, rel, err)
	}

	var p profile
	if err := json.Unmarshal(raw, &p); err != nil {
		return httpx.Auth{}, fmt.Errorf("auth profile %s: %w", rel, err)
	}
	if p.Username == "" || p.AccessKey == "" {
		return httpx.Auth{}, fmt.Errorf("auth profile %s: username/accessKey must both be set", rel)
	}
	return httpx.Auth{Type: "basic", User: p.Username, Key: p.AccessKey}, nil
}
