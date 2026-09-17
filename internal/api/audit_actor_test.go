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

// TestAuditActorsCarryTheAPIKey keeps audit attribution honest: a change made with an API key (D-133) has no
// user, so the audit row is anonymous unless the handler passes the key's id and name into the package's Actor.
// Six call sites dropped them when keys learned to write — the SLO write was attributed to nobody — and the
// omission is invisible at compile time, because the missing fields simply stay empty. The scan is syntactic:
// it fails on the shape of the omission, not on its intent.
func TestAuditActorsCarryTheAPIKey(t *testing.T) {
	// Actor types whose writes are never made by an API key, with the reason.
	exempt := map[string]string{
		"quota.Actor": "usage and quota changes come from billing webhooks and operators, not from keys",
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
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Actor" {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			typeName := pkg.Name + ".Actor"
			if why, skip := exempt[typeName]; skip {
				t.Logf("skipping %s: %s", typeName, why)
				return true
			}
			checked++
			var hasID, hasName bool
			for _, e := range lit.Elts {
				kv, ok := e.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				switch key, _ := kv.Key.(*ast.Ident); {
				case key == nil:
				case key.Name == "APIKeyID":
					hasID = true
				case key.Name == "APIKeyName":
					hasName = true
				}
			}
			if !hasID || !hasName {
				t.Errorf("%s: %s must carry APIKeyID and APIKeyName, or the audit row of a key-authenticated change names nobody",
					fset.Position(lit.Pos()), typeName)
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no audit actors scanned")
	}
}
