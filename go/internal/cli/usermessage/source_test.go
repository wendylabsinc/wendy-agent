package usermessage

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var errorIdentifier = regexp.MustCompile(`^(err|[A-Za-z_]*Err|[A-Za-z_]*Error|lerr|aErr|oErr|tErr|zerr|berr|derr|perr|cerr)$`)

// TestDisplaySources covers every platform file, including files excluded by
// this host's build tags. It intentionally leaves error construction, status
// predicates, application logs and protocol payloads alone.
func TestDisplaySources(t *testing.T) {
	for _, root := range []string{"..", "../../../cmd/wendy"} {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// These serve machine protocols, not terminal message presentation.
				if d.Name() == "usermessage" || d.Name() == "agentservice" || d.Name() == "mcp" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, position := range displayViolations(path, string(src)) {
				t.Errorf("%s: raw failure text bypasses usermessage", position)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func displayViolations(path, src string) []token.Position {
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, path, src, parser.ParseComments)
	if err != nil {
		panic(err)
	}
	var out []token.Position
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name.Name == "Error" {
			continue
		}
		// Raw diagnostics stay behind explicit, existing debug switches.
		var debugRanges [][2]token.Pos
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if x, ok := n.(*ast.IfStmt); ok {
				s := src[int(x.Cond.Pos())-1 : int(x.Cond.End())-1]
				if strings.Contains(s, "WENDY_TLS_DEBUG") || strings.Contains(s, "WENDY_TUNNEL_DEBUG") || s == "tlsDebug" || s == "w.tlsDebug" || s == "tunnelDebug" {
					debugRanges = append(debugRanges, [2]token.Pos{x.Body.Pos(), x.Body.End()})
				}
			}
			return true
		})
		tainted := map[string]bool{}
		declaredErrors := map[string]bool{}
		ast.Inspect(fn, func(n ast.Node) bool {
			if field, ok := n.(*ast.Field); ok {
				if typ, ok := field.Type.(*ast.Ident); ok && typ.Name == "error" {
					for _, id := range field.Names {
						declaredErrors[id.Name] = true
					}
				}
			}
			if spec, ok := n.(*ast.ValueSpec); ok {
				if typ, ok := spec.Type.(*ast.Ident); ok && typ.Name == "error" {
					for _, id := range spec.Names {
						declaredErrors[id.Name] = true
					}
				}
			}
			return true
		})
		var raw func(ast.Expr, bool) bool
		raw = func(e ast.Expr, typedErrors bool) bool {
			switch x := e.(type) {
			case *ast.Ident:
				return tainted[x.Name] || typedErrors && (errorIdentifier.MatchString(x.Name) || declaredErrors[x.Name])
			case *ast.SelectorExpr:
				return typedErrors && x.Sel.Name != "err" && x.Sel.Name != "Error" && errorIdentifier.MatchString(x.Sel.Name)
			case *ast.CallExpr:
				if s, ok := x.Fun.(*ast.SelectorExpr); ok {
					if id, ok := s.X.(*ast.Ident); ok && id.Name == "usermessage" {
						return false
					}
					if s.Sel.Name == "Text" {
						if call, ok := s.X.(*ast.CallExpr); ok {
							if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
								if id, ok := selector.X.(*ast.Ident); ok && id.Name == "usermessage" && selector.Sel.Name == "Explain" {
									return false
								}
							}
						}
					}
					if s.Sel.Name == "Error" || s.Sel.Name == "GetErrorMessage" || s.Sel.Name == "GetMessage" || s.Sel.Name == "Message" {
						return true
					}
				}
				if id, ok := x.Fun.(*ast.Ident); ok && (id.Name == "userFacingError" || id.Name == "userFacingGRPCError" || id.Name == "renderError") {
					return false
				}
				for _, arg := range x.Args {
					if raw(arg, false) {
						return true
					}
				}
			case *ast.BinaryExpr:
				return raw(x.X, typedErrors) || raw(x.Y, typedErrors)
			case *ast.ParenExpr:
				return raw(x.X, typedErrors)
			}
			return false
		}
		// Track string aliases, including status.Convert(err).Message(). Error
		// construction retains its own raw cause and is checked at the display sink.
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if a, ok := n.(*ast.AssignStmt); ok && len(a.Lhs) == len(a.Rhs) {
				for i, r := range a.Rhs {
					if id, ok := a.Lhs[i].(*ast.Ident); ok && raw(r, false) {
						tainted[id.Name] = true
					}
				}
			}
			return true
		})
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if ret, ok := n.(*ast.ReturnStmt); ok && (fn.Name.Name == "userFacingError" || fn.Name.Name == "userFacingGRPCError") {
				for _, expr := range ret.Results {
					if raw(expr, false) {
						out = append(out, fs.Position(ret.Pos()))
					}
				}
			}
			if a, ok := n.(*ast.AssignStmt); ok && len(a.Rhs) == len(a.Lhs) {
				for i, lhs := range a.Lhs {
					if field, ok := lhs.(*ast.SelectorExpr); ok && (field.Sel.Name == "message" || field.Sel.Name == "flashMessage" || field.Sel.Name == "flash") && raw(a.Rhs[i], false) {
						out = append(out, fs.Position(a.Pos()))
					}
				}
			}
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			for _, r := range debugRanges {
				if c.Pos() >= r[0] && c.End() <= r[1] {
					return false
				}
			}
			name, pkg := "", ""
			switch s := c.Fun.(type) {
			case *ast.SelectorExpr:
				name = s.Sel.Name
				if id, ok := s.X.(*ast.Ident); ok {
					pkg = id.Name
				}
			case *ast.Ident:
				name = s.Name
			}
			sink := pkg == "fmt" && (strings.HasPrefix(name, "Print") || strings.HasPrefix(name, "Fprint") || name == "Sprintf") || name == "cliNotice" || name == "cliLogln" || name == "cliSuccess" || name == "Render" || name == "WriteString" || name == "WarningMessage" || name == "ErrorMessage" || name == "Note" || name == "appendEntry"
			mapper := pkg == "usermessage" && (name == "Sprintf" || name == "Printf" || name == "Fprintf")
			if !sink && !mapper {
				return true
			}
			// Protocol and application log streams are intentionally transparent. Each
			// such output documents that fact at the call site rather than exempting a file.
			line := fs.Position(c.Pos()).Line
			for _, group := range f.Comments {
				if fs.Position(group.End()).Line == line-1 && strings.Contains(group.Text(), "usermessage: payload") {
					return false
				}
			}
			for _, arg := range c.Args {
				if raw(arg, !mapper) {
					out = append(out, fs.Position(c.Pos()))
					break
				}
			}
			return true
		})
	}
	return out
}

func TestDisplaySourceCheckCatchesBypasses(t *testing.T) {
	for _, body := range []string{
		`var failure error; fmt.Printf("%v",failure)`,
		`view.message = failure.Error()`,
		`fmt.Printf("Failed: %v", err)`,
		`fmt.Fprintln(os.Stderr, err.Error())`,
		`fmt.Printf("%s", status.Convert(err).Message())`,
		`message := response.GetErrorMessage(); fmt.Println(message)`,
		`fmt.Println(response.GetMessage())`,
		`cliNotice("Failed: %v", networkErr)`,
		`style.Render("Failed: " + err.Error())`,
		`usermessage.Printf("%s", err.Error())`,
	} {
		if len(displayViolations("bad.go", "package p; func f(){"+body+"}")) == 0 {
			t.Errorf("missed %s", body)
		}
	}
	for _, body := range []string{
		`fmt.Println(usermessage.Text(err))`,
		`usermessage.Printf("Could not connect: %v\n", err)`,
		`return fmt.Errorf("connecting: %w", err)`,
		`if status.Convert(err).Message() == "known reason" { return err }`,
	} {
		if got := displayViolations("good.go", "package p; func f(){"+body+"}"); len(got) > 0 {
			t.Errorf("flagged %s: %v", body, got)
		}
	}
}

func TestSourceCheckCatchesRegressedMapperWrapper(t *testing.T) {
	if len(displayViolations("bad.go", `package p; func userFacingError(err error)string{return err.Error()}`)) == 0 {
		t.Fatal("raw fallback in a wrapper was missed")
	}
}
