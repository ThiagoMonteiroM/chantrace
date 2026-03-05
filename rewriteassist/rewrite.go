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
	ReportGoStmt bool
	ReportSelect bool
}

// DefaultRewriteConfig returns the default rewrite behavior.
func DefaultRewriteConfig() RewriteConfig {
	return RewriteConfig{
		RewriteSend:  true,
		RewriteRecv:  true,
		RewriteRange: true,
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

	qual, hasImport, unusableImport := chantraceQualifier(file)
	if unusableImport {
		out.Issues = append(out.Issues, RewriteIssue{
			Position: fset.Position(file.Pos()),
			Message:  "chantrace import exists with unsupported alias (_ or .); skipping rewrites in file",
		})
		return out
	}

	inSelectComm := collectSelectCommNodes(file)

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
			if cfg.ReportGoStmt {
				out.Issues = append(out.Issues, RewriteIssue{
					Position:   fset.Position(n.Go),
					Kind:       "go",
					Message:    "go statement requires manual migration to chantrace.Go",
					Suggestion: `replace with chantrace.Go(ctx, "label", func(ctx context.Context) { ... })`,
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

func chantraceQualifier(file *ast.File) (qual string, hasImport bool, unusable bool) {
	for _, imp := range file.Imports {
		if imp.Path == nil {
			continue
		}
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		if path != chantraceImportPath {
			continue
		}
		hasImport = true
		if imp.Name == nil {
			return "chantrace", true, false
		}
		switch imp.Name.Name {
		case "_", ".":
			return "", true, true
		default:
			return imp.Name.Name, true, false
		}
	}
	return "chantrace", false, false
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
