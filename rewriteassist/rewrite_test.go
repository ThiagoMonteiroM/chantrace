package rewriteassist

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

type stubImporter struct {
	base types.Importer
}

func (s stubImporter) Import(path string) (*types.Package, error) {
	if path == chantraceImportPath {
		return types.NewPackage(path, "chantrace"), nil
	}
	return s.base.Import(path)
}

func mustParseAndTypecheck(t *testing.T, src string) (*token.FileSet, *ast.File, *types.Info) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "sample.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	info := &types.Info{
		Types:  make(map[ast.Expr]types.TypeAndValue),
		Defs:   make(map[*ast.Ident]types.Object),
		Uses:   make(map[*ast.Ident]types.Object),
		Scopes: make(map[ast.Node]*types.Scope),
	}
	conf := &types.Config{
		Importer: stubImporter{base: importer.Default()},
	}
	if _, err := conf.Check("sample", fset, []*ast.File{file}, info); err != nil {
		t.Fatalf("type-check: %v", err)
	}
	return fset, file, info
}

func mustFormat(t *testing.T, fset *token.FileSet, file *ast.File) string {
	t.Helper()
	var b bytes.Buffer
	if err := format.Node(&b, fset, file); err != nil {
		t.Fatalf("format.Node: %v", err)
	}
	return b.String()
}

func TestRewriteFileSendRecvRange(t *testing.T) {
	const src = `package p
func f(ch chan int, ro <-chan int) {
	ch <- 1
	x := <-ro
	_ = <-ro
	for v := range ro {
		_ = v
	}
	_ = x
}
`

	fset, file, info := mustParseAndTypecheck(t, src)
	got := RewriteFile(fset, file, info, DefaultRewriteConfig())
	if !got.Changed {
		t.Fatal("Changed = false, want true")
	}
	if got.Rewrites < 4 {
		t.Fatalf("Rewrites = %d, want >= 4", got.Rewrites)
	}
	out := mustFormat(t, fset, file)
	wantSubstrings := []string{
		`import "github.com/khzaw/chantrace"`,
		`chantrace.Send(ch, 1)`,
		`x := chantrace.Recv(ro)`,
		`_ = chantrace.Recv(ro)`,
		`for v := range chantrace.Range(ro) {`,
	}
	for _, sub := range wantSubstrings {
		if !strings.Contains(out, sub) {
			t.Fatalf("output missing %q:\n%s", sub, out)
		}
	}
}

func TestRewriteFileRecvOk(t *testing.T) {
	const src = `package p
func f(ro <-chan int) {
	v, ok := <-ro
	_, _ = v, ok
}
`

	fset, file, info := mustParseAndTypecheck(t, src)
	got := RewriteFile(fset, file, info, DefaultRewriteConfig())
	if !got.Changed {
		t.Fatal("Changed = false, want true")
	}
	out := mustFormat(t, fset, file)
	if !strings.Contains(out, `v, ok := chantrace.RecvOk(ro)`) {
		t.Fatalf("missing RecvOk rewrite:\n%s", out)
	}
}

func TestRewriteFileGoStmtIssue(t *testing.T) {
	const src = `package p
func f() {
	go func() {}()
}
`

	fset, file, info := mustParseAndTypecheck(t, src)
	got := RewriteFile(fset, file, info, DefaultRewriteConfig())
	if got.Changed {
		t.Fatal("Changed = true, want false")
	}
	if len(got.Issues) != 1 {
		t.Fatalf("issue count = %d, want 1", len(got.Issues))
	}
	if got.Issues[0].Kind != "go" {
		t.Fatalf("issue kind = %q, want %q", got.Issues[0].Kind, "go")
	}
	if !strings.Contains(got.Issues[0].Message, "manual migration") {
		t.Fatalf("unexpected issue message: %q", got.Issues[0].Message)
	}
	if !strings.Contains(got.Issues[0].Suggestion, "chantrace.Go") {
		t.Fatalf("unexpected go suggestion: %q", got.Issues[0].Suggestion)
	}
}

func TestRewriteFileSelectCommNotRewritten(t *testing.T) {
	const src = `package p
func f(ch chan int, ro <-chan int) {
	select {
	case ch <- 1:
	case v := <-ro:
		_ = v
	default:
	}
}
`

	fset, file, info := mustParseAndTypecheck(t, src)
	got := RewriteFile(fset, file, info, DefaultRewriteConfig())
	if got.Changed {
		t.Fatalf("Changed = true, want false (select comms must stay native)")
	}
	if len(got.Issues) == 0 {
		t.Fatal("expected manual migration issue for select")
	}
	if got.Issues[0].Kind != "select" {
		t.Fatalf("issue kind = %q, want %q", got.Issues[0].Kind, "select")
	}
	if !strings.Contains(got.Issues[0].Scaffold, "chantrace.Select(") {
		t.Fatalf("missing select scaffold header:\n%s", got.Issues[0].Scaffold)
	}
	if !strings.Contains(got.Issues[0].Scaffold, "chantrace.OnSend(") {
		t.Fatalf("missing OnSend scaffold:\n%s", got.Issues[0].Scaffold)
	}
	if !strings.Contains(got.Issues[0].Scaffold, "chantrace.OnRecv(") {
		t.Fatalf("missing OnRecv scaffold:\n%s", got.Issues[0].Scaffold)
	}
	if !strings.Contains(got.Issues[0].Scaffold, "chantrace.OnDefault(") {
		t.Fatalf("missing OnDefault scaffold:\n%s", got.Issues[0].Scaffold)
	}
	out := mustFormat(t, fset, file)
	if strings.Contains(out, "chantrace.Send(") || strings.Contains(out, "chantrace.Recv(") {
		t.Fatalf("select comm unexpectedly rewritten:\n%s", out)
	}
}

func TestRewriteFileSelectRecvOkScaffold(t *testing.T) {
	const src = `package p
func f(ro <-chan int) {
	select {
	case v, ok := <-ro:
		_, _ = v, ok
	}
}
`

	fset, file, info := mustParseAndTypecheck(t, src)
	got := RewriteFile(fset, file, info, DefaultRewriteConfig())
	if len(got.Issues) == 0 {
		t.Fatal("expected select issue")
	}
	scaffold := got.Issues[0].Scaffold
	if !strings.Contains(scaffold, "chantrace.OnRecvOK(") {
		t.Fatalf("missing OnRecvOK in scaffold:\n%s", scaffold)
	}
	if !strings.Contains(scaffold, "func(v int, ok bool)") {
		t.Fatalf("missing typed recvok callback in scaffold:\n%s", scaffold)
	}
}

func TestRewriteFileCanDisableSelectIssue(t *testing.T) {
	const src = `package p
func f(ch chan int) {
	select {
	case ch <- 1:
	default:
	}
}
`

	fset, file, info := mustParseAndTypecheck(t, src)
	cfg := DefaultRewriteConfig()
	cfg.ReportSelect = false
	got := RewriteFile(fset, file, info, cfg)
	if len(got.Issues) != 0 {
		t.Fatalf("issue count = %d, want 0", len(got.Issues))
	}
}

func TestRewriteFileGoStmtSafeAutoRewrite(t *testing.T) {
	const src = `package p
import "context"
func worker() {}
func f(ctx context.Context) {
	go worker()
}
`

	fset, file, info := mustParseAndTypecheck(t, src)
	cfg := DefaultRewriteConfig()
	cfg.RewriteGo = true
	got := RewriteFile(fset, file, info, cfg)
	if !got.Changed {
		t.Fatalf("Changed = false, want true; issues=%+v", got.Issues)
	}
	out := mustFormat(t, fset, file)
	if !strings.Contains(out, `chantrace.Go(ctx, "worker", func(_ context.Context) {`) {
		t.Fatalf("missing chantrace.Go rewrite:\n%s", out)
	}
	if !strings.Contains(out, "worker()") {
		t.Fatalf("missing wrapped go call:\n%s", out)
	}
}

func TestRewriteFileGoStmtNoContextFallsBackToIssue(t *testing.T) {
	const src = `package p
func worker() {}
func f() {
	go worker()
}
`

	fset, file, info := mustParseAndTypecheck(t, src)
	cfg := DefaultRewriteConfig()
	cfg.RewriteGo = true
	got := RewriteFile(fset, file, info, cfg)
	if got.Changed {
		t.Fatal("Changed = true, want false")
	}
	if len(got.Issues) != 1 {
		t.Fatalf("issue count = %d, want 1", len(got.Issues))
	}
	if got.Issues[0].Kind != "go" {
		t.Fatalf("issue kind = %q, want go", got.Issues[0].Kind)
	}
	if !strings.Contains(got.Issues[0].Message, "context.Context") {
		t.Fatalf("unexpected issue message: %q", got.Issues[0].Message)
	}
	if !strings.Contains(got.Issues[0].Scaffold, "chantrace.Go(ctx") {
		t.Fatalf("missing go scaffold:\n%s", got.Issues[0].Scaffold)
	}
}

func TestRewriteFileRespectsImportAlias(t *testing.T) {
	const src = `package p
import ct "github.com/khzaw/chantrace"
func f(ch chan int) {
	ch <- 1
}
`

	fset, file, info := mustParseAndTypecheck(t, src)
	got := RewriteFile(fset, file, info, DefaultRewriteConfig())
	if !got.Changed {
		t.Fatal("Changed = false, want true")
	}
	out := mustFormat(t, fset, file)
	if !strings.Contains(out, `ct.Send(ch, 1)`) {
		t.Fatalf("missing aliased Send rewrite:\n%s", out)
	}
}

func TestRewriteFileSkipsUnsupportedImportAlias(t *testing.T) {
	const src = `package p
import _ "github.com/khzaw/chantrace"
func f(ch chan int) {
	ch <- 1
}
`

	fset, file, info := mustParseAndTypecheck(t, src)
	got := RewriteFile(fset, file, info, DefaultRewriteConfig())
	if got.Changed {
		t.Fatal("Changed = true, want false")
	}
	if len(got.Issues) == 0 {
		t.Fatal("expected issue for unsupported import alias")
	}
}
