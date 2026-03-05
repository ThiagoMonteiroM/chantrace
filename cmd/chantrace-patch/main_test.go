package main

import (
	"os"
	"strings"
	"testing"
)

func TestMatchFilePath(t *testing.T) {
	tests := []struct {
		name      string
		rel       string
		includes  []string
		excludes  []string
		onlyFiles []string
		onlyGlobs []string
		want      bool
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
		{
			name:      "only-file match",
			rel:       "examples/web_demo/main.go",
			onlyFiles: []string{"examples/web_demo/main.go"},
			want:      true,
		},
		{
			name:      "only-file miss",
			rel:       "examples/web_demo/main.go",
			onlyFiles: []string{"examples/basic/main.go"},
			want:      false,
		},
		{
			name:      "only-glob match",
			rel:       "examples/web_demo/main.go",
			onlyGlobs: []string{"examples/*/*.go"},
			want:      true,
		},
		{
			name:      "only-glob miss",
			rel:       "examples/web_demo/main.go",
			onlyGlobs: []string{"rewriteassist/*.go"},
			want:      false,
		},
		{
			name:      "only-filter before include",
			rel:       "examples/web_demo/main.go",
			includes:  []string{"examples/*/*.go"},
			onlyFiles: []string{"rewriteassist/hints.go"},
			want:      false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := matchFilePath(tc.rel, tc.includes, tc.excludes, tc.onlyFiles, tc.onlyGlobs)
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

func TestNormalizeOnlyFiles(t *testing.T) {
	root := "/repo"
	got := normalizeOnlyFiles(root, []string{
		"examples/../examples/web_demo/main.go",
		"/repo/rewriteassist/hints.go",
		"",
	})
	want := []string{"examples/web_demo/main.go", "rewriteassist/hints.go"}
	if len(got) != len(want) {
		t.Fatalf("len(normalized) = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalized[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWriteManualNotesReport(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/manual.md"
	m := manifest{
		ID:        "20260101T000000Z",
		CreatedAt: "2026-01-01T00:00:00Z",
		Files:     []manifestFile{{Path: "a.go"}},
		Issues: []manifestNote{
			{
				Path:       "x.go",
				Line:       12,
				Column:     3,
				Kind:       "select",
				Message:    "manual select migration required",
				Suggestion: "replace with chantrace.Select",
				Scaffold:   "chantrace.Select(\n\tchantrace.OnDefault(func() {}),\n)",
			},
		},
	}
	if err := writeManualNotesReport(path, m); err != nil {
		t.Fatalf("writeManualNotesReport: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, "chantrace manual migration notes") {
		t.Fatalf("missing report header:\n%s", out)
	}
	if !strings.Contains(out, "manual select migration required") {
		t.Fatalf("missing issue message:\n%s", out)
	}
	if !strings.Contains(out, "```go") || !strings.Contains(out, "chantrace.Select(") {
		t.Fatalf("missing scaffold block:\n%s", out)
	}
}
