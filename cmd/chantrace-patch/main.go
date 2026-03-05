package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/khzaw/chantrace/rewriteassist"
	"golang.org/x/tools/go/packages"
)

const (
	patchesDirName   = ".chantrace/patches"
	activePatchFile  = "ACTIVE"
	manifestFileName = "manifest.json"
	manualNotesFile  = ".chantrace/manual-notes.md"
)

type manifest struct {
	ID            string                      `json:"id"`
	CreatedAt     string                      `json:"created_at"`
	Root          string                      `json:"root"`
	Patterns      []string                    `json:"patterns"`
	RewriteConfig rewriteassist.RewriteConfig `json:"rewrite_config"`
	Includes      []string                    `json:"includes,omitempty"`
	Excludes      []string                    `json:"excludes,omitempty"`
	OnlyFiles     []string                    `json:"only_files,omitempty"`
	OnlyGlobs     []string                    `json:"only_globs,omitempty"`
	ManualNotes   string                      `json:"manual_notes,omitempty"`
	Files         []manifestFile              `json:"files"`
	Issues        []manifestNote              `json:"issues,omitempty"`
}

type manifestFile struct {
	Path         string `json:"path"`
	BackupPath   string `json:"backup_path"`
	SHA256Before string `json:"sha256_before"`
	SHA256After  string `json:"sha256_after"`
	Rewrites     int    `json:"rewrites"`
}

type manifestNote struct {
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Column     int    `json:"column"`
	Kind       string `json:"kind,omitempty"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
	Scaffold   string `json:"scaffold,omitempty"`
}

type plannedFile struct {
	absPath  string
	relPath  string
	before   []byte
	after    []byte
	rewrites int
	fileMode os.FileMode
}

type plan struct {
	files  []plannedFile
	issues []manifestNote
}

type stringList []string

func (s *stringList) String() string {
	return strings.Join(*s, ",")
}

func (s *stringList) Set(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	*s = append(*s, filepath.ToSlash(v))
	return nil
}

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}

	var code int
	switch os.Args[1] {
	case "apply":
		code = runApply(os.Args[2:])
	case "revert":
		code = runRevert(os.Args[2:])
	case "status":
		code = runStatus(os.Args[2:])
	case "-h", "--help", "help":
		usage(os.Stdout)
		code = 0
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", os.Args[1])
		usage(os.Stderr)
		code = 2
	}
	os.Exit(code)
}

func usage(w *os.File) {
	fmt.Fprintln(w, "chantrace-patch: reversible chantrace codemod workflow")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  chantrace-patch apply [--dry-run] [--include glob] [--exclude glob] [--only-file path] [--only-glob glob] [--rewrite-go] [--no-send] [--no-recv] [--no-range] [--include-generated] [packages...]")
	fmt.Fprintln(w, "  chantrace-patch status")
	fmt.Fprintln(w, "  chantrace-patch revert [--force]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  go run ./cmd/chantrace-patch apply ./...")
	fmt.Fprintln(w, "  go run ./cmd/chantrace-patch status")
	fmt.Fprintln(w, "  go run ./cmd/chantrace-patch revert")
}

func runApply(args []string) int {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var dryRun bool
	var noSend bool
	var noRecv bool
	var noRange bool
	var rewriteGo bool
	var noGoNotes bool
	var noSelectNotes bool
	var includeGenerated bool
	var includes stringList
	var excludes stringList
	var onlyFiles stringList
	var onlyGlobs stringList
	fs.BoolVar(&dryRun, "dry-run", false, "print planned changes without writing files")
	fs.BoolVar(&noSend, "no-send", false, "do not rewrite channel sends")
	fs.BoolVar(&noRecv, "no-recv", false, "do not rewrite channel receives")
	fs.BoolVar(&noRange, "no-range", false, "do not rewrite range-over-channel")
	fs.BoolVar(&rewriteGo, "rewrite-go", false, "rewrite go statements when a context.Context variable is unambiguous in scope")
	fs.BoolVar(&noGoNotes, "no-go-notes", false, "do not emit manual migration notes for go statements")
	fs.BoolVar(&noSelectNotes, "no-select-notes", false, "do not emit manual migration notes for select statements")
	fs.BoolVar(&includeGenerated, "include-generated", false, "allow rewriting generated Go files")
	fs.Var(&includes, "include", "path glob to include (repeatable, matches repo-relative slash paths)")
	fs.Var(&excludes, "exclude", "path glob to exclude (repeatable, matches repo-relative slash paths)")
	fs.Var(&onlyFiles, "only-file", "rewrite only this repo-relative file (repeatable)")
	fs.Var(&onlyGlobs, "only-glob", "rewrite only files matching this glob (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	patterns := fs.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}

	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pwd: %v\n", err)
		return 1
	}
	root, err = filepath.Abs(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "abs root: %v\n", err)
		return 1
	}

	if !dryRun {
		active, err := readActivePatchID(root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read active patch: %v\n", err)
			return 1
		}
		if active != "" {
			fmt.Fprintf(os.Stderr, "active patch %q already exists; revert it before applying a new patch\n", active)
			return 1
		}
	}

	pkgs, err := loadPackages(patterns)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load packages: %v\n", err)
		return 1
	}

	rewriteCfg := rewriteassist.DefaultRewriteConfig()
	rewriteCfg.RewriteSend = !noSend
	rewriteCfg.RewriteRecv = !noRecv
	rewriteCfg.RewriteRange = !noRange
	rewriteCfg.RewriteGo = rewriteGo
	rewriteCfg.ReportGoStmt = !noGoNotes
	rewriteCfg.ReportSelect = !noSelectNotes

	includePatterns := []string(includes)
	excludePatterns := []string(excludes)
	onlyGlobPatterns := []string(onlyGlobs)
	onlyFilePaths := normalizeOnlyFiles(root, []string(onlyFiles))
	if err := validateGlobPatterns(includePatterns); err != nil {
		fmt.Fprintf(os.Stderr, "invalid --include pattern: %v\n", err)
		return 2
	}
	if err := validateGlobPatterns(excludePatterns); err != nil {
		fmt.Fprintf(os.Stderr, "invalid --exclude pattern: %v\n", err)
		return 2
	}
	if err := validateGlobPatterns(onlyGlobPatterns); err != nil {
		fmt.Fprintf(os.Stderr, "invalid --only-glob pattern: %v\n", err)
		return 2
	}

	plan, err := buildPlan(root, pkgs, rewriteCfg, includePatterns, excludePatterns, onlyFilePaths, onlyGlobPatterns, includeGenerated)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build patch plan: %v\n", err)
		return 1
	}

	sort.Slice(plan.files, func(i, j int) bool {
		return plan.files[i].relPath < plan.files[j].relPath
	})
	sort.Slice(plan.issues, func(i, j int) bool {
		if plan.issues[i].Path != plan.issues[j].Path {
			return plan.issues[i].Path < plan.issues[j].Path
		}
		if plan.issues[i].Line != plan.issues[j].Line {
			return plan.issues[i].Line < plan.issues[j].Line
		}
		return plan.issues[i].Column < plan.issues[j].Column
	})

	if len(plan.files) == 0 {
		fmt.Println("no rewrite changes needed")
		if len(plan.issues) > 0 {
			fmt.Printf("manual notes: %d\n", len(plan.issues))
			printIssues(plan.issues)
			if !dryRun {
				m := manifest{
					ID:            time.Now().UTC().Format("20060102T150405Z"),
					CreatedAt:     time.Now().UTC().Format(time.RFC3339),
					Root:          root,
					Patterns:      append([]string(nil), patterns...),
					RewriteConfig: rewriteCfg,
					Includes:      append([]string(nil), includePatterns...),
					Excludes:      append([]string(nil), excludePatterns...),
					OnlyFiles:     append([]string(nil), onlyFilePaths...),
					OnlyGlobs:     append([]string(nil), onlyGlobPatterns...),
					ManualNotes:   filepath.ToSlash(manualNotesFile),
					Issues:        plan.issues,
				}
				reportPath := filepath.Join(root, manualNotesFile)
				if err := writeManualNotesReport(reportPath, m); err != nil {
					fmt.Fprintf(os.Stderr, "write manual notes report: %v\n", err)
					return 1
				}
				fmt.Printf("manual notes report: %s\n", m.ManualNotes)
			}
		}
		return 0
	}

	fmt.Printf("planned file rewrites: %d\n", len(plan.files))
	if len(plan.issues) > 0 {
		fmt.Printf("manual notes: %d\n", len(plan.issues))
	}
	for _, f := range plan.files {
		fmt.Printf("  %s (%d rewrites)\n", f.relPath, f.rewrites)
	}
	if len(plan.issues) > 0 {
		printIssues(plan.issues)
	}

	if dryRun {
		return 0
	}

	patchID := time.Now().UTC().Format("20060102T150405Z")
	patchDir := filepath.Join(root, patchesDirName, patchID)
	origDir := filepath.Join(patchDir, "original")
	if err := os.MkdirAll(origDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir patch dir: %v\n", err)
		return 1
	}

	m := manifest{
		ID:            patchID,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		Root:          root,
		Patterns:      append([]string(nil), patterns...),
		RewriteConfig: rewriteCfg,
		Includes:      append([]string(nil), includePatterns...),
		Excludes:      append([]string(nil), excludePatterns...),
		OnlyFiles:     append([]string(nil), onlyFilePaths...),
		OnlyGlobs:     append([]string(nil), onlyGlobPatterns...),
		Issues:        plan.issues,
	}

	for _, f := range plan.files {
		backupAbs := filepath.Join(origDir, filepath.FromSlash(f.relPath))
		if err := os.MkdirAll(filepath.Dir(backupAbs), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "mkdir backup dir for %s: %v\n", f.relPath, err)
			return 1
		}
		if err := os.WriteFile(backupAbs, f.before, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write backup for %s: %v\n", f.relPath, err)
			return 1
		}
		if err := os.WriteFile(f.absPath, f.after, f.fileMode); err != nil {
			fmt.Fprintf(os.Stderr, "write rewritten file %s: %v\n", f.relPath, err)
			return 1
		}

		m.Files = append(m.Files, manifestFile{
			Path:         f.relPath,
			BackupPath:   filepath.ToSlash(strings.TrimPrefix(backupAbs, patchDir+string(os.PathSeparator))),
			SHA256Before: hashBytes(f.before),
			SHA256After:  hashBytes(f.after),
			Rewrites:     f.rewrites,
		})
	}

	m.ManualNotes = filepath.ToSlash(manualNotesFile)
	reportPath := filepath.Join(root, manualNotesFile)
	if err := writeManualNotesReport(reportPath, m); err != nil {
		fmt.Fprintf(os.Stderr, "write manual notes report: %v\n", err)
		return 1
	}
	if err := writeManifest(patchDir, m); err != nil {
		fmt.Fprintf(os.Stderr, "write manifest: %v\n", err)
		return 1
	}
	if err := writeActivePatchID(root, patchID); err != nil {
		fmt.Fprintf(os.Stderr, "write active patch: %v\n", err)
		return 1
	}

	fmt.Printf("applied patch %s (%d files)\n", patchID, len(m.Files))
	if len(m.Issues) > 0 {
		fmt.Printf("manual notes report: %s\n", m.ManualNotes)
	}
	return 0
}

func runRevert(args []string) int {
	fs := flag.NewFlagSet("revert", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var force bool
	fs.BoolVar(&force, "force", false, "revert even if files changed since patch apply")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "revert does not accept package patterns")
		return 2
	}

	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pwd: %v\n", err)
		return 1
	}
	root, err = filepath.Abs(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "abs root: %v\n", err)
		return 1
	}

	patchID, err := readActivePatchID(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read active patch: %v\n", err)
		return 1
	}
	if patchID == "" {
		fmt.Println("no active patch")
		if _, err := os.Stat(filepath.Join(root, manualNotesFile)); err == nil {
			fmt.Printf("manual report: %s\n", filepath.ToSlash(manualNotesFile))
		}
		return 0
	}

	patchDir := filepath.Join(root, patchesDirName, patchID)
	m, err := readManifest(patchDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read manifest for patch %s: %v\n", patchID, err)
		return 1
	}

	drifted, err := checkDrift(root, m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "check drift: %v\n", err)
		return 1
	}
	if len(drifted) > 0 && !force {
		fmt.Fprintf(os.Stderr, "refusing revert: %d file(s) changed after patch apply:\n", len(drifted))
		for _, p := range drifted {
			fmt.Fprintf(os.Stderr, "  %s\n", p)
		}
		fmt.Fprintln(os.Stderr, "use --force to restore backups anyway")
		return 1
	}

	for _, f := range m.Files {
		backupAbs := filepath.Join(patchDir, filepath.FromSlash(f.BackupPath))
		content, err := os.ReadFile(backupAbs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read backup %s: %v\n", f.Path, err)
			return 1
		}
		target := filepath.Join(root, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "mkdir target dir for %s: %v\n", f.Path, err)
			return 1
		}
		info, statErr := os.Stat(target)
		mode := os.FileMode(0o644)
		if statErr == nil {
			mode = info.Mode().Perm()
		}
		if err := os.WriteFile(target, content, mode); err != nil {
			fmt.Fprintf(os.Stderr, "restore %s: %v\n", f.Path, err)
			return 1
		}
	}

	if err := clearActivePatchID(root); err != nil {
		fmt.Fprintf(os.Stderr, "clear active patch: %v\n", err)
		return 1
	}
	_ = os.Remove(filepath.Join(root, manualNotesFile))

	fmt.Printf("reverted patch %s (%d files)\n", patchID, len(m.Files))
	return 0
}

func runStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "status does not accept package patterns")
		return 2
	}

	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pwd: %v\n", err)
		return 1
	}
	root, err = filepath.Abs(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "abs root: %v\n", err)
		return 1
	}

	patchID, err := readActivePatchID(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read active patch: %v\n", err)
		return 1
	}
	if patchID == "" {
		fmt.Println("no active patch")
		return 0
	}

	patchDir := filepath.Join(root, patchesDirName, patchID)
	m, err := readManifest(patchDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read manifest for patch %s: %v\n", patchID, err)
		return 1
	}
	drifted, err := checkDrift(root, m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "check drift: %v\n", err)
		return 1
	}

	fmt.Printf("active patch: %s\n", patchID)
	fmt.Printf("created at: %s\n", m.CreatedAt)
	fmt.Printf("files: %d\n", len(m.Files))
	fmt.Printf("manual notes: %d\n", len(m.Issues))
	fmt.Printf("rewrite config: send=%t recv=%t range=%t rewrite-go=%t go-notes=%t select-notes=%t\n",
		m.RewriteConfig.RewriteSend,
		m.RewriteConfig.RewriteRecv,
		m.RewriteConfig.RewriteRange,
		m.RewriteConfig.RewriteGo,
		m.RewriteConfig.ReportGoStmt,
		m.RewriteConfig.ReportSelect,
	)
	if len(m.Includes) > 0 {
		fmt.Printf("includes: %s\n", strings.Join(m.Includes, ", "))
	}
	if len(m.Excludes) > 0 {
		fmt.Printf("excludes: %s\n", strings.Join(m.Excludes, ", "))
	}
	if len(m.OnlyFiles) > 0 {
		fmt.Printf("only-files: %s\n", strings.Join(m.OnlyFiles, ", "))
	}
	if len(m.OnlyGlobs) > 0 {
		fmt.Printf("only-globs: %s\n", strings.Join(m.OnlyGlobs, ", "))
	}
	if m.ManualNotes != "" {
		fmt.Printf("manual report: %s\n", m.ManualNotes)
	}
	if len(m.Issues) > 0 {
		printIssues(m.Issues)
	}
	if len(drifted) == 0 {
		fmt.Println("drift: clean")
		return 0
	}
	fmt.Printf("drift: %d file(s)\n", len(drifted))
	for _, p := range drifted {
		fmt.Printf("  %s\n", p)
	}
	return 0
}

func loadPackages(patterns []string) ([]*packages.Package, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedCompiledGoFiles |
			packages.NeedSyntax |
			packages.NeedTypes |
			packages.NeedTypesInfo,
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, err
	}
	if n := packages.PrintErrors(pkgs); n > 0 {
		return nil, fmt.Errorf("%d package load errors", n)
	}
	return pkgs, nil
}

func buildPlan(root string, pkgs []*packages.Package, cfg rewriteassist.RewriteConfig, includes, excludes, onlyFiles, onlyGlobs []string, includeGenerated bool) (plan, error) {
	var out plan

	seen := make(map[string]struct{})
	for _, pkg := range pkgs {
		for _, file := range pkg.Syntax {
			filename := pkg.Fset.Position(file.Pos()).Filename
			if filename == "" {
				continue
			}
			abs, err := filepath.Abs(filename)
			if err != nil {
				return out, fmt.Errorf("abs path for %s: %w", filename, err)
			}
			if _, ok := seen[abs]; ok {
				continue
			}
			seen[abs] = struct{}{}

			rel := relPath(root, abs)
			ok, err := matchFilePath(rel, includes, excludes, onlyFiles, onlyGlobs)
			if err != nil {
				return out, err
			}
			if !ok {
				continue
			}
			if !includeGenerated && ast.IsGenerated(file) {
				out.issues = append(out.issues, manifestNote{
					Path:       rel,
					Line:       1,
					Column:     1,
					Kind:       "generated",
					Message:    "generated file skipped (use --include-generated to rewrite)",
					Suggestion: "rerun apply with --include-generated if rewriting this file is intentional",
				})
				continue
			}

			res := rewriteassist.RewriteFile(pkg.Fset, file, pkg.TypesInfo, cfg)
			for _, issue := range res.Issues {
				issuePath := issue.Position.Filename
				if issuePath == "" {
					issuePath = abs
				}
				rel := relPath(root, issuePath)
				out.issues = append(out.issues, manifestNote{
					Path:       rel,
					Line:       issue.Position.Line,
					Column:     issue.Position.Column,
					Kind:       issue.Kind,
					Message:    issue.Message,
					Suggestion: issue.Suggestion,
					Scaffold:   issue.Scaffold,
				})
			}
			if !res.Changed {
				continue
			}

			before, err := os.ReadFile(abs)
			if err != nil {
				return out, fmt.Errorf("read source %s: %w", abs, err)
			}

			var b bytes.Buffer
			if err := format.Node(&b, pkg.Fset, file); err != nil {
				return out, fmt.Errorf("format rewritten %s: %w", abs, err)
			}
			after := b.Bytes()
			if !bytes.HasSuffix(after, []byte("\n")) {
				after = append(after, '\n')
			}
			if bytes.Equal(before, after) {
				continue
			}

			info, err := os.Stat(abs)
			if err != nil {
				return out, fmt.Errorf("stat source %s: %w", abs, err)
			}
			out.files = append(out.files, plannedFile{
				absPath:  abs,
				relPath:  rel,
				before:   before,
				after:    after,
				rewrites: res.Rewrites,
				fileMode: info.Mode().Perm(),
			})
		}
	}

	return out, nil
}

func printIssues(issues []manifestNote) {
	fmt.Println("manual notes:")
	for _, n := range issues {
		prefix := ""
		if n.Kind != "" {
			prefix = "[" + n.Kind + "] "
		}
		if n.Line > 0 {
			fmt.Printf("  %s:%d:%d: %s%s\n", n.Path, n.Line, n.Column, prefix, n.Message)
		} else {
			fmt.Printf("  %s: %s%s\n", n.Path, prefix, n.Message)
		}
		if n.Suggestion != "" {
			fmt.Printf("    suggestion: %s\n", n.Suggestion)
		}
	}
}

func readActivePatchID(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, patchesDirName, activePatchFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func writeActivePatchID(root, id string) error {
	base := filepath.Join(root, patchesDirName)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(base, activePatchFile), []byte(id+"\n"), 0o644)
}

func clearActivePatchID(root string) error {
	err := os.Remove(filepath.Join(root, patchesDirName, activePatchFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func writeManifest(patchDir string, m manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(patchDir, manifestFileName), data, 0o644)
}

func readManifest(patchDir string) (manifest, error) {
	var m manifest
	data, err := os.ReadFile(filepath.Join(patchDir, manifestFileName))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	return m, nil
}

func checkDrift(root string, m manifest) ([]string, error) {
	drifted := make([]string, 0)
	for _, f := range m.Files {
		p := filepath.Join(root, filepath.FromSlash(f.Path))
		content, err := os.ReadFile(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				drifted = append(drifted, f.Path+" (missing)")
				continue
			}
			return nil, fmt.Errorf("read %s: %w", f.Path, err)
		}
		if hashBytes(content) != f.SHA256After {
			drifted = append(drifted, f.Path)
		}
	}
	sort.Strings(drifted)
	return drifted, nil
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func relPath(root, target string) string {
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return filepath.ToSlash(target)
	}
	rel, err := filepath.Rel(root, absTarget)
	if err != nil || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." {
		return filepath.ToSlash(absTarget)
	}
	return filepath.ToSlash(rel)
}

func normalizeOnlyFiles(root string, files []string) []string {
	out := make([]string, 0, len(files))
	for _, p := range files {
		if p == "" {
			continue
		}
		if filepath.IsAbs(p) {
			out = append(out, relPath(root, p))
			continue
		}
		out = append(out, filepath.ToSlash(filepath.Clean(p)))
	}
	sort.Strings(out)
	return out
}

func validateGlobPatterns(patterns []string) error {
	for _, p := range patterns {
		if _, err := filepath.Match(p, "sample/path.go"); err != nil {
			return fmt.Errorf("%q: %w", p, err)
		}
	}
	return nil
}

func matchFilePath(rel string, includes, excludes, onlyFiles, onlyGlobs []string) (bool, error) {
	rel = filepath.ToSlash(rel)
	if len(onlyFiles) > 0 || len(onlyGlobs) > 0 {
		allowed := false
		for _, p := range onlyFiles {
			if rel == filepath.ToSlash(p) {
				allowed = true
				break
			}
		}
		if !allowed {
			for _, p := range onlyGlobs {
				m, err := filepath.Match(p, rel)
				if err != nil {
					return false, fmt.Errorf("only-glob pattern %q: %w", p, err)
				}
				if m {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			return false, nil
		}
	}

	included := len(includes) == 0
	for _, p := range includes {
		m, err := filepath.Match(p, rel)
		if err != nil {
			return false, fmt.Errorf("include pattern %q: %w", p, err)
		}
		if m {
			included = true
			break
		}
	}
	if !included {
		return false, nil
	}
	for _, p := range excludes {
		m, err := filepath.Match(p, rel)
		if err != nil {
			return false, fmt.Errorf("exclude pattern %q: %w", p, err)
		}
		if m {
			return false, nil
		}
	}
	return true, nil
}

func writeManualNotesReport(path string, m manifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# chantrace manual migration notes\n\n")
	b.WriteString(fmt.Sprintf("- Patch ID: `%s`\n", m.ID))
	b.WriteString(fmt.Sprintf("- Generated: %s\n", m.CreatedAt))
	b.WriteString(fmt.Sprintf("- Rewritten files: %d\n", len(m.Files)))
	b.WriteString(fmt.Sprintf("- Manual notes: %d\n\n", len(m.Issues)))
	if len(m.Issues) == 0 {
		b.WriteString("No manual migration notes for this patch.\n")
		return os.WriteFile(path, []byte(b.String()), 0o644)
	}
	for i, n := range m.Issues {
		loc := n.Path
		if n.Line > 0 {
			loc = fmt.Sprintf("%s:%d:%d", n.Path, n.Line, n.Column)
		}
		kind := n.Kind
		if kind == "" {
			kind = "note"
		}
		b.WriteString(fmt.Sprintf("## %d. `%s` (%s)\n\n", i+1, loc, kind))
		b.WriteString(n.Message + "\n\n")
		if n.Suggestion != "" {
			b.WriteString("Suggestion:\n")
			b.WriteString(fmt.Sprintf("- %s\n\n", n.Suggestion))
		}
		if n.Scaffold != "" {
			b.WriteString("Scaffold:\n\n```go\n")
			b.WriteString(n.Scaffold)
			if !strings.HasSuffix(n.Scaffold, "\n") {
				b.WriteString("\n")
			}
			b.WriteString("```\n\n")
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
