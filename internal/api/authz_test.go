package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAuthorizationGoesThroughOneGate keeps the promise of authz.go: an API key's
// role (D-133) is enforced by auth.Allow alone. A handler that compared a
// principal's Kind or Role itself would be a second mechanism, and the two would
// drift — which is exactly how "API keys are read-only" ended up scattered over a
// dozen files before. The scan is deliberately syntactic: it fails on the shape of
// the bypass, not on its intent.
func TestAuthorizationGoesThroughOneGate(t *testing.T) {
	// Files that may name a principal's kind without authorizing with it.
	exempt := map[string]string{
		"authz.go":   "defines the gate helpers themselves",
		"account.go": "renders the principal in GET /auth/me and builds one after sign-in",
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		if why, ok := exempt[name]; ok {
			t.Logf("skipping %s: %s", name, why)
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.BinaryExpr:
				if x.Op != token.EQL && x.Op != token.NEQ {
					return true
				}
				for _, side := range []ast.Expr{x.X, x.Y} {
					if isAuthKind(side) || isPrincipalRole(side) {
						t.Errorf("%s: authorization must go through auth.Allow/auth.Authorize, not a comparison of %s",
							fset.Position(x.Pos()), render(side))
					}
				}
			case *ast.CallExpr:
				sel, ok := x.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "Can" && sel.Sel.Name != "AtLeast") {
					return true
				}
				if isPrincipalRole(sel.X) {
					t.Errorf("%s: authorization must go through auth.Allow/auth.Authorize, not %s.%s(...)",
						fset.Position(x.Pos()), render(sel.X), sel.Sel.Name)
				}
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no source files scanned")
	}
}

// isAuthKind reports whether e is auth.KindSession or auth.KindAPIKey.
func isAuthKind(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "auth" && (sel.Sel.Name == "KindSession" || sel.Sel.Name == "KindAPIKey")
}

// isPrincipalRole reports whether e reads the Role or Kind field of a principal.
func isPrincipalRole(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel.Name != "Role" && sel.Sel.Name != "Kind" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "p" // the principal is called p throughout this package
}

func render(e ast.Expr) string {
	if sel, ok := e.(*ast.SelectorExpr); ok {
		if id, ok := sel.X.(*ast.Ident); ok {
			return id.Name + "." + sel.Sel.Name
		}
	}
	return "the expression"
}
