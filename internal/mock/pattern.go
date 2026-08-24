package mock

import (
	"fmt"
	"strings"
)

// Pattern is a compiled request-path matcher.
//
// Path patterns are deliberately simpler than net/http's ServeMux patterns:
// rules are matched in declaration order rather than by specificity, so a
// pattern only needs to answer "does this path match, and what did it
// capture" — never "am I more specific than that other pattern".
type Pattern struct {
	raw      string
	segments []segment
	// wildcard is true when the pattern ends in "*", which matches the rest
	// of the path (including slashes) and captures it as "*".
	wildcard bool
}

type segment struct {
	literal string
	// param is non-empty for a {name} segment.
	param string
}

// Compile builds a Pattern from a path like "/users/{id}/posts/*".
func Compile(pattern string) (*Pattern, error) {
	if !strings.HasPrefix(pattern, "/") {
		return nil, fmt.Errorf("path pattern %q must start with /", pattern)
	}
	p := &Pattern{raw: pattern}
	parts := splitPath(pattern)
	seen := map[string]bool{}
	for i, part := range parts {
		switch {
		case part == "*":
			if i != len(parts)-1 {
				return nil, fmt.Errorf("path pattern %q: * is only allowed as the last segment", pattern)
			}
			p.wildcard = true
		case strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}"):
			name := part[1 : len(part)-1]
			if name == "" {
				return nil, fmt.Errorf("path pattern %q: empty {} capture", pattern)
			}
			if seen[name] {
				return nil, fmt.Errorf("path pattern %q: duplicate capture {%s}", pattern, name)
			}
			seen[name] = true
			p.segments = append(p.segments, segment{param: name})
		default:
			if strings.ContainsAny(part, "{}") {
				return nil, fmt.Errorf("path pattern %q: malformed capture in segment %q", pattern, part)
			}
			p.segments = append(p.segments, segment{literal: part})
		}
	}
	return p, nil
}

// String returns the pattern as written.
func (p *Pattern) String() string { return p.raw }

// Match reports whether path matches and returns the captured parameters.
// A trailing "*" capture is keyed "*".
func (p *Pattern) Match(path string) (map[string]string, bool) {
	parts := splitPath(path)
	if p.wildcard {
		if len(parts) < len(p.segments) {
			return nil, false
		}
	} else if len(parts) != len(p.segments) {
		return nil, false
	}

	var params map[string]string
	for i, seg := range p.segments {
		if seg.param == "" {
			if parts[i] != seg.literal {
				return nil, false
			}
			continue
		}
		if parts[i] == "" {
			return nil, false
		}
		if params == nil {
			params = make(map[string]string, len(p.segments))
		}
		params[seg.param] = parts[i]
	}
	if p.wildcard {
		if params == nil {
			params = make(map[string]string, 1)
		}
		params["*"] = strings.Join(parts[len(p.segments):], "/")
	}
	if params == nil {
		params = map[string]string{}
	}
	return params, true
}

// splitPath splits on "/" ignoring the leading slash, so "/a/b" yields
// ["a","b"] and "/" yields []. A trailing slash yields a final empty segment,
// which makes "/a/" and "/a" match different patterns.
func splitPath(path string) []string {
	path = strings.TrimPrefix(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}
