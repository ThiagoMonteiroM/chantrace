package main

import "testing"

func TestMatchFilePath(t *testing.T) {
	tests := []struct {
		name     string
		rel      string
		includes []string
		excludes []string
		want     bool
	}{
		{
			name: "no filters includes all",
			rel:  "a/b.go",
			want: true,
		},
		{
			name:     "include match",
			rel:      "examples/web_demo/main.go",
			includes: []string{"examples/*/*.go"},
			want:     true,
		},
		{
			name:     "include miss",
			rel:      "rewriteassist/hints.go",
			includes: []string{"examples/*/*.go"},
			want:     false,
		},
		{
			name:     "excluded takes precedence",
			rel:      "examples/web_demo/main.go",
			includes: []string{"examples/*/*.go"},
			excludes: []string{"examples/web_demo/*"},
			want:     false,
		},
		{
			name:     "exclude miss keeps include",
			rel:      "examples/web_demo/main.go",
			includes: []string{"examples/*/*.go"},
			excludes: []string{"examples/basic/*"},
			want:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := matchFilePath(tc.rel, tc.includes, tc.excludes)
			if err != nil {
				t.Fatalf("matchFilePath error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("matchFilePath(%q) = %v, want %v", tc.rel, got, tc.want)
			}
		})
	}
}

func TestValidateGlobPatterns(t *testing.T) {
	if err := validateGlobPatterns([]string{"*.go", "a/*/b.go"}); err != nil {
		t.Fatalf("validate valid patterns: %v", err)
	}
	if err := validateGlobPatterns([]string{"["}); err == nil {
		t.Fatal("expected invalid glob error")
	}
}
