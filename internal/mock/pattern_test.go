package mock

import (
	"reflect"
	"testing"
)

func TestPatternMatch(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    map[string]string
		wantOK  bool
	}{
		{"/users", "/users", map[string]string{}, true},
		{"/users", "/users/", nil, false},
		{"/users", "/Users", nil, false},
		{"/users/{id}", "/users/42", map[string]string{"id": "42"}, true},
		{"/users/{id}", "/users", nil, false},
		{"/users/{id}", "/users/42/posts", nil, false},
		{"/users/{id}/posts/{postID}", "/users/1/posts/9", map[string]string{"id": "1", "postID": "9"}, true},
		{"/api/*", "/api/v1/things", map[string]string{"*": "v1/things"}, true},
		{"/api/*", "/api", map[string]string{"*": ""}, true},
		{"/api/*", "/other", nil, false},
		{"/", "/", map[string]string{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+" vs "+tt.path, func(t *testing.T) {
			p, err := Compile(tt.pattern)
			if err != nil {
				t.Fatalf("Compile(%q) error = %v", tt.pattern, err)
			}
			got, ok := p.Match(tt.path)
			if ok != tt.wantOK {
				t.Fatalf("Match(%q) ok = %v, want %v", tt.path, ok, tt.wantOK)
			}
			if ok && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Match(%q) params = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestCompileErrors(t *testing.T) {
	for _, pattern := range []string{
		"users",
		"/a/*/b",
		"/a/{}",
		"/a/{id}/b/{id}",
		"/a/{id",
	} {
		t.Run(pattern, func(t *testing.T) {
			if _, err := Compile(pattern); err == nil {
				t.Errorf("Compile(%q) error = nil, want an error", pattern)
			}
		})
	}
}
