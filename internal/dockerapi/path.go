// Package dockerapi identifies Docker Engine routes without interpreting their
// request bodies or changing Docker's API version negotiation.
package dockerapi

import (
	"regexp"
	"strings"
)

var version = regexp.MustCompile(`^/v[0-9]+\.[0-9]+(?:/|$)`)

// EnginePath returns the unversioned route solely for validation. The original
// versioned and escaped path must be forwarded unchanged to the selected engine.
func EnginePath(path string) (string, bool) {
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\\x00\r\n") || strings.Contains(path, "//") {
		return "", false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return "", false
		}
	}
	if match := version.FindString(path); match != "" {
		path = "/" + strings.TrimPrefix(path[len(match):], "/")
	}
	root, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	switch root {
	case "_ping", "version", "info", "containers", "images", "networks", "volumes", "exec", "events", "build", "session", "auth", "system", "distribution", "plugins", "swarm", "nodes", "services", "tasks", "secrets", "configs", "commit":
		return path, true
	}
	return "", false
}

func IsGatewayRequest(path string) bool {
	if strings.HasPrefix(path, "/docker/") || strings.HasPrefix(path, "/v1.") {
		return true
	}
	root, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	_, ok := EnginePath("/" + root)
	return ok
}
