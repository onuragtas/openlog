package query

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestEveryWrittenFragmentPassesTheGuard runs every literal query fragment in the repository through the
// same forbidden pattern the builder applies at runtime.
//
// It exists because that pattern refuses fragments by vocabulary, not by meaning, and two endpoints have
// already shipped unable to build their SQL at all — a 500 on every request, from the first one:
//
//	GET /api/v1/vulnerabilities  "uniqExact(host_id) AS hosts"      -> forbidden token "hosts"
//	GET /api/v1/jobs/monitors/{id}/runs  "timestamp >= {from:...}"  -> forbidden token "from"
//
// Neither is an escape attempt. `hosts` is a column alias and `from` a parameter name; both are perfectly
// ordinary SQL, and both collide with the guard's list of table names and keywords. The collision is
// invisible at the call site and silent until a request arrives, because Select records the failure and
// returns it only when the query is finally built.
//
// Being in package query is the point: this checks the real `forbidden`, so it cannot drift from it.
func TestEveryWrittenFragmentPassesTheGuard(t *testing.T) {
	// The builder methods that call check(); a fragment reaching any of them is validated.
	validated := map[string]bool{
		"Columns": true, "Where": true, "Having": true,
		"GroupBy": true, "OrderBy": true, "LimitBy": true,
	}

	fset := token.NewFileSet()
	var files, fragments, dynamic int
	root := filepath.Join("..", "..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "web", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		// Test files are skipped: the guard's own tests pass forbidden fragments on purpose.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil // not this test's business to police what does not parse
		}
		files++
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !validated[sel.Sel.Name] {
				return true
			}
			for _, arg := range call.Args {
				lit, isLit := arg.(*ast.BasicLit)
				if !isLit {
					// A fragment assembled at run time (fmt.Sprintf, a variable, a constant) cannot be read
					// here. Counted, not ignored, so the gap is visible rather than assumed away.
					dynamic++
					continue
				}
				if lit.Kind != token.STRING {
					continue // LimitBy's count, not a fragment
				}
				frag, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				fragments++
				if m := forbidden.FindString(frag); m != "" {
					t.Errorf("%s: fragment %q contains forbidden token %q, so .%s() records an error and the "+
						"query can never be built — rename the alias or parameter (host_count, t_from)",
						fset.Position(lit.Pos()), frag, m, sel.Sel.Name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 || fragments == 0 {
		t.Fatalf("scanned %d files and %d fragments: the walk found nothing to check", files, fragments)
	}
	t.Logf("%d literal fragments checked across %d files; %d built at run time and unreadable here",
		fragments, files, dynamic)
}
