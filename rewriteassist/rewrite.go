package rewriteassist

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"go/types"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ast/astutil"
)

const chantraceImportPath = "github.com/khzaw/chantrace"
const contextImportPath = "context"

// RewriteIssue is a non-fatal note produced while rewriting.
type RewriteIssue struct {
	Position   token.Position
	Kind       string
	Message    string
	Suggestion string
	Scaffold   string
}

// RewriteConfig controls which transformations are applied.
type RewriteConfig struct {
	RewriteSend  bool
	RewriteRecv  bool
	RewriteRange bool
	RewriteGo    bool
	ReportGoStmt bool
	ReportSelect bool
}

// DefaultRewriteConfig returns the default rewrite behavior.
func DefaultRewriteConfig() RewriteConfig {
	return RewriteConfig{
		RewriteSend:  true,
		RewriteRecv:  true,
		RewriteRange: true,
		RewriteGo:    false,
		ReportGoStmt: true,
		ReportSelect: true,
	}
}

// RewriteResult summarizes transformations for one file.
type RewriteResult struct {
	Changed  bool
	Rewrites int
	Issues   []RewriteIssue
}

// RewriteFile rewrites native channel operations into chantrace wrappers.
// The input AST is mutated in place.
func RewriteFile(fset *token.FileSet, file *ast.File, info *types.Info, cfg RewriteConfig) RewriteResult {
	var out RewriteResult
	if file == nil || info == nil || fset == nil {
		return out
	}

	qual, hasImport, unusableImport := importQualifier(file, chantraceImportPath, "chantrace")
	if unusableImport {
		out.Issues = append(out.Issues, RewriteIssue{
			Position: fset.Position(file.Pos()),
			Kind:     "config",
			Message:  "chantrace import exists with unsupported alias (_ or .); skipping rewrites in file",
		})
		return out
	}
	ctxQual, hasContextImport, unusableContextImport := importQualifier(file, contextImportPath, "context")
	needContextImport := false

	inSelectComm := collectSelectCommNodes(file)
	parents := buildParentMap(file)

	astutil.Apply(file, func(c *astutil.Cursor) bool {
		node := c.Node()
		switch n := node.(type) {
		case *ast.SelectStmt:
			if cfg.ReportSelect {
				out.Issues = append(out.Issues, RewriteIssue{
					Position:   fset.Position(n.Select),
					Kind:       "select",
					Message:    "select statement requires manual migration to chantrace.Select",
					Suggestion: "replace native select with chantrace.Select(...) using OnRecv/OnRecvOK/OnSend/OnDefault",
					Scaffold:   buildSelectScaffold(fset, info, n),
				})
			}
		case *ast.GoStmt:
			if cfg.RewriteGo {
				if unusableContextImport {
					out.Issues = append(out.Issues, RewriteIssue{
						Position:   fset.Position(n.Go),
						Kind:       "go",
						Message:    "context import has unsupported alias (_ or .); cannot auto-rewrite go statement",
						Suggestion: `replace context import alias and rerun with --rewrite-go, or rewrite manually using chantrace.Go`,
						Scaffold:   buildGoScaffold(fset, n, "ctx", ctxQual),
					})
					return true
				}
				if rewritten, issue := rewriteGoStmt(c, fset, info, n, parents, qual, ctxQual); rewritten {
					out.Rewrites++
					out.Changed = true
					if !hasContextImport {
						needContextImport = true
					}
					return false
				} else if cfg.ReportGoStmt && issue != nil {
					out.Issues = append(out.Issues, *issue)
				}
			} else if cfg.ReportGoStmt {
				out.Issues = append(out.Issues, RewriteIssue{
					Position:   fset.Position(n.Go),
					Kind:       "go",
					Message:    "go statement requires manual migration to chantrace.Go",
					Suggestion: `enable RewriteGo for safe auto-migration, or replace manually with chantrace.Go(ctx, "label", func(ctx context.Context) { ... })`,
					Scaffold:   buildGoScaffold(fset, n, "ctx", ctxQual),
				})
			}
		case *ast.SendStmt:
			if cfg.RewriteSend && !inSelectComm[node] && isChanType(info.TypeOf(n.Chan)) {
				c.Replace(&ast.ExprStmt{
					X: chantraceCall(qual, "Send", n.Chan, n.Value),
				})
				out.Rewrites++
				out.Changed = true
				return false
			}
		case *ast.AssignStmt:
			if cfg.RewriteRecv && !inSelectComm[node] && len(n.Rhs) == 1 {
				if recv, ok := n.Rhs[0].(*ast.UnaryExpr); ok && recv.Op == token.ARROW && isChanType(info.TypeOf(recv.X)) {
					name := "Recv"
					if len(n.Lhs) == 2 {
						name = "RecvOk"
					}
					n.Rhs[0] = chantraceCall(qual, name, recv.X)
					out.Rewrites++
					out.Changed = true
				}
			}
		case *ast.ValueSpec:
			if cfg.RewriteRecv && !inSelectComm[node] && len(n.Values) == 1 {
				if recv, ok := n.Values[0].(*ast.UnaryExpr); ok && recv.Op == token.ARROW && isChanType(info.TypeOf(recv.X)) {
					n.Values[0] = chantraceCall(qual, "Recv", recv.X)
					out.Rewrites++
					out.Changed = true
				}
			}
		case *ast.RangeStmt:
			if cfg.RewriteRange && isChanType(info.TypeOf(n.X)) {
				n.X = chantraceCall(qual, "Range", n.X)
				out.Rewrites++
				out.Changed = true
			}
		case *ast.UnaryExpr:
			if cfg.RewriteRecv && !inSelectComm[node] && n.Op == token.ARROW && isChanType(info.TypeOf(n.X)) {
				c.Replace(chantraceCall(qual, "Recv", n.X))
				out.Rewrites++
				out.Changed = true
				return false
			}
		}
		return true
	}, nil)

	if out.Changed && !hasImport {
		astutil.AddImport(fset, file, chantraceImportPath)
	}
	if out.Changed && needContextImport && !hasContextImport {
		astutil.AddImport(fset, file, contextImportPath)
	}

	return out
}

func collectSelectCommNodes(file *ast.File) map[ast.Node]bool {
	out := make(map[ast.Node]bool)
	ast.Inspect(file, func(node ast.Node) bool {
		sel, ok := node.(*ast.SelectStmt)
		if !ok {
			return true
		}
		for _, stmt := range sel.Body.List {
			cc, ok := stmt.(*ast.CommClause)
			if !ok || cc.Comm == nil {
				continue
			}
			ast.Inspect(cc.Comm, func(n ast.Node) bool {
				if n != nil {
					out[n] = true
				}
				return true
			})
		}
		return true
	})
	return out
}

func importQualifier(file *ast.File, importPath, defaultName string) (qual string, hasImport bool, unusable bool) {
	for _, imp := range file.Imports {
		if imp.Path == nil {
			continue
		}
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		if path != importPath {
			continue
		}
		hasImport = true
		if imp.Name == nil {
			return defaultName, true, false
		}
		switch imp.Name.Name {
		case "_", ".":
			return "", true, true
		default:
			return imp.Name.Name, true, false
		}
	}
	return defaultName, false, false
}

func chantraceCall(qual, fn string, args ...ast.Expr) ast.Expr {
	return &ast.CallExpr{
		Fun: &ast.SelectorExpr{
			X:   ast.NewIdent(qual),
			Sel: ast.NewIdent(fn),
		},
		Args: args,
	}
}

func buildSelectScaffold(fset *token.FileSet, info *types.Info, sel *ast.SelectStmt) string {
	lines := []string{"chantrace.Select("}
	for _, stmt := range sel.Body.List {
		cc, ok := stmt.(*ast.CommClause)
		if !ok {
			continue
		}
		caseLines := buildSelectCaseScaffold(fset, info, cc)
		for _, line := range caseLines {
			lines = append(lines, "\t"+line)
		}
	}
	lines = append(lines, ")")
	return strings.Join(lines, "\n")
}

func buildSelectCaseScaffold(fset *token.FileSet, info *types.Info, cc *ast.CommClause) []string {
	if cc.Comm == nil {
		return []string{
			"chantrace.OnDefault(func() {",
			"\t// TODO: move default branch body here",
			"}),",
		}
	}

	switch comm := cc.Comm.(type) {
	case *ast.SendStmt:
		ch := exprString(fset, comm.Chan)
		val := exprString(fset, comm.Value)
		return []string{
			fmt.Sprintf("chantrace.OnSend(%s, %s, func() {", ch, val),
			"\t// TODO: move this case body here",
			"}),",
		}
	case *ast.ExprStmt:
		recv, ok := comm.X.(*ast.UnaryExpr)
		if !ok || recv.Op != token.ARROW {
			break
		}
		ch := exprString(fset, recv.X)
		elemType := recvElemType(info, recv.X)
		return []string{
			fmt.Sprintf("chantrace.OnRecv(%s, func(v %s) {", ch, elemType),
			"\t_ = v",
			"\t// TODO: move this case body here",
			"}),",
		}
	case *ast.AssignStmt:
		if len(comm.Rhs) != 1 {
			break
		}
		recv, ok := comm.Rhs[0].(*ast.UnaryExpr)
		if !ok || recv.Op != token.ARROW {
			break
		}
		ch := exprString(fset, recv.X)
		elemType := recvElemType(info, recv.X)
		if len(comm.Lhs) == 2 {
			vName := lhsName(comm.Lhs[0], "v")
			okName := lhsName(comm.Lhs[1], "ok")
			return []string{
				fmt.Sprintf("chantrace.OnRecvOK(%s, func(%s %s, %s bool) {", ch, vName, elemType, okName),
				"\t// TODO: move this case body here",
				"}),",
			}
		}
		vName := "v"
		if len(comm.Lhs) >= 1 {
			vName = lhsName(comm.Lhs[0], "v")
		}
		return []string{
			fmt.Sprintf("chantrace.OnRecv(%s, func(%s %s) {", ch, vName, elemType),
			"\t// TODO: move this case body here",
			"}),",
		}
	}

	return []string{
		"// TODO: unsupported select case shape; migrate manually",
	}
}

func exprString(fset *token.FileSet, expr ast.Expr) string {
	if expr == nil {
		return ""
	}
	var b bytes.Buffer
	if err := format.Node(&b, fset, expr); err != nil {
		return ""
	}
	return b.String()
}

func recvElemType(info *types.Info, ch ast.Expr) string {
	if info == nil {
		return "any"
	}
	t := info.TypeOf(ch)
	if t == nil {
		return "any"
	}
	chanT, ok := t.Underlying().(*types.Chan)
	if !ok || chanT.Elem() == nil {
		return "any"
	}
	return chanT.Elem().String()
}

func lhsName(expr ast.Expr, fallback string) string {
	id, ok := expr.(*ast.Ident)
	if !ok || id.Name == "" {
		return fallback
	}
	return id.Name
}

func rewriteGoStmt(c *astutil.Cursor, fset *token.FileSet, info *types.Info, n *ast.GoStmt, parents map[ast.Node]ast.Node, chantraceQual, ctxQual string) (bool, *RewriteIssue) {
	pos := n.Pos()
	if n.Call != nil {
		pos = n.Call.Pos()
	}
	ctxVar, reason := chooseContextVar(info, pos, n, parents)
	if ctxVar == "" {
		msg := "go statement requires manual migration to chantrace.Go"
		if reason != "" {
			msg = reason
		}
		return false, &RewriteIssue{
			Position:   fset.Position(n.Go),
			Kind:       "go",
			Message:    msg,
			Suggestion: `ensure a context.Context variable is in scope (prefer "ctx"), then rewrite with chantrace.Go`,
			Scaffold:   buildGoScaffold(fset, n, "ctx", ctxQual),
		}
	}

	label := goCallLabel(n.Call)
	ctxParamType := &ast.SelectorExpr{
		X:   ast.NewIdent(ctxQual),
		Sel: ast.NewIdent("Context"),
	}

	call := chantraceCall(
		chantraceQual,
		"Go",
		ast.NewIdent(ctxVar),
		&ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(label)},
		&ast.FuncLit{
			Type: &ast.FuncType{
				Params: &ast.FieldList{
					List: []*ast.Field{
						{
							Names: []*ast.Ident{ast.NewIdent("_")},
							Type:  ctxParamType,
						},
					},
				},
			},
			Body: &ast.BlockStmt{
				List: []ast.Stmt{
					&ast.ExprStmt{X: n.Call},
				},
			},
		},
	)
	c.Replace(&ast.ExprStmt{X: call})
	return true, nil
}

func chooseContextVar(info *types.Info, pos token.Pos, n ast.Node, parents map[ast.Node]ast.Node) (string, string) {
	if info == nil {
		return "", "type info unavailable; cannot auto-rewrite go statement"
	}
	var candidates []string
	seen := make(map[string]struct{})

	// Prefer lexical variables in scope, including locals introduced near the go statement.
	scope := innermostScope(info, pos)
	for sc := scope; sc != nil; sc = sc.Parent() {
		for _, name := range sc.Names() {
			if name == "_" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			obj := sc.Lookup(name)
			if obj == nil {
				continue
			}
			if isContextType(obj.Type()) {
				candidates = append(candidates, name)
			}
		}
	}

	// Some go/types scope maps omit function parameter bindings; add them from AST parents.
	for _, name := range contextParamsFromParents(info, n, parents) {
		if name == "_" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		candidates = append(candidates, name)
	}

	for _, name := range candidates {
		if name == "ctx" {
			return "ctx", ""
		}
	}
	if len(candidates) == 1 {
		return candidates[0], ""
	}
	if len(candidates) == 0 {
		return "", `no context.Context variable in scope for go statement`
	}
	return "", fmt.Sprintf("multiple context.Context variables in scope (%s); choose one explicitly", strings.Join(candidates, ", "))
}

func contextParamsFromParents(info *types.Info, n ast.Node, parents map[ast.Node]ast.Node) []string {
	if info == nil || n == nil {
		return nil
	}
	for cur := n; cur != nil; cur = parents[cur] {
		switch fn := cur.(type) {
		case *ast.FuncDecl:
			return contextParamNames(info, fn.Type)
		case *ast.FuncLit:
			return contextParamNames(info, fn.Type)
		}
	}
	return nil
}

func contextParamNames(info *types.Info, typ *ast.FuncType) []string {
	if info == nil || typ == nil || typ.Params == nil {
		return nil
	}
	out := make([]string, 0)
	for _, f := range typ.Params.List {
		if !isContextType(info.TypeOf(f.Type)) {
			continue
		}
		for _, name := range f.Names {
			if name == nil || name.Name == "" {
				continue
			}
			out = append(out, name.Name)
		}
	}
	return out
}

func innermostScope(info *types.Info, pos token.Pos) *types.Scope {
	var best ast.Node
	var bestScope *types.Scope
	for node, sc := range info.Scopes {
		if sc == nil || node == nil {
			continue
		}
		if pos < node.Pos() || pos >= node.End() {
			continue
		}
		if best == nil || (node.End()-node.Pos()) < (best.End()-best.Pos()) {
			best = node
			bestScope = sc
		}
	}
	return bestScope
}

func isContextType(t types.Type) bool {
	if t == nil {
		return false
	}
	t = types.Unalias(t)
	n, ok := t.(*types.Named)
	if !ok || n.Obj() == nil || n.Obj().Pkg() == nil {
		return false
	}
	return n.Obj().Pkg().Path() == contextImportPath && n.Obj().Name() == "Context"
}

func goCallLabel(call *ast.CallExpr) string {
	if call == nil || call.Fun == nil {
		return "go"
	}
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		if fn.Name != "" {
			return fn.Name
		}
	case *ast.SelectorExpr:
		if fn.Sel != nil && fn.Sel.Name != "" {
			return fn.Sel.Name
		}
	case *ast.FuncLit:
		return "anon"
	}
	return "go"
}

func buildGoScaffold(fset *token.FileSet, n *ast.GoStmt, ctxVar, ctxQual string) string {
	if ctxVar == "" {
		ctxVar = "ctx"
	}
	if ctxQual == "" {
		ctxQual = "context"
	}
	label := "label"
	callExpr := "<call>()"
	if n != nil && n.Call != nil {
		label = goCallLabel(n.Call)
		callExpr = exprString(fset, n.Call)
	}
	lines := []string{
		fmt.Sprintf(`chantrace.Go(%s, %q, func(_ %s.Context) {`, ctxVar, label, ctxQual),
		fmt.Sprintf("\t%s", callExpr),
		"})",
	}
	return strings.Join(lines, "\n")
}

func buildParentMap(file *ast.File) map[ast.Node]ast.Node {
	out := make(map[ast.Node]ast.Node)
	if file == nil {
		return out
	}
	var stack []ast.Node
	ast.Inspect(file, func(node ast.Node) bool {
		if node == nil {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			return false
		}
		if len(stack) > 0 {
			out[node] = stack[len(stack)-1]
		}
		stack = append(stack, node)
		return true
	})
	return out
}
