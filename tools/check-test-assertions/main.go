// check-test-assertions inspects Go test files and reports Test functions
// that have no assertion calls. A test function is considered to have
// assertions if its body contains:
//   - a call to t.Error, t.Errorf, t.Fatal, t.Fatalf, t.Fail, or t.FailNow
//     on the *testing.T parameter, OR
//   - a non-method function call where the *testing.T parameter is the
//     first argument (delegation to a helper).
//
// Usage: check-test-assertions file1_test.go file2_test.go ...
// Exit 1 if any violations are found.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		os.Exit(0)
	}
	failed := false
	for _, path := range args {
		if err := checkFile(path); err != nil {
			fmt.Fprintln(os.Stderr, err)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}

// checkFile parses a single Go file and returns a non-nil error describing
// any assertion-free Test functions found.
func checkFile(path string) error {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		// Not a valid Go file — skip silently (go build will catch it)
		return nil
	}

	var violations []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if !strings.HasPrefix(fn.Name.Name, "Test") {
			continue
		}
		tParam := testingTParam(fn)
		if tParam == "" {
			// Not a standard Test function signature — skip
			continue
		}
		if !hasAssertion(fn.Body, tParam) {
			pos := fset.Position(fn.Pos())
			violations = append(violations, fmt.Sprintf(
				"%s:%d: %s has no assertions (add t.Error/t.Fatal or pass t to a helper)",
				pos.Filename, pos.Line, fn.Name.Name,
			))
		}
	}

	if len(violations) > 0 {
		return fmt.Errorf("%s", strings.Join(violations, "\n"))
	}
	return nil
}

// testingTParam returns the name of the *testing.T parameter of fn, or ""
// if fn is not a standard Test function.
func testingTParam(fn *ast.FuncDecl) string {
	if fn.Type.Params == nil {
		return ""
	}
	for _, field := range fn.Type.Params.List {
		star, ok := field.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		sel, ok := star.X.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			continue
		}
		if pkg.Name == "testing" && sel.Sel.Name == "T" && len(field.Names) > 0 {
			return field.Names[0].Name
		}
	}
	return ""
}

// assertionMethods is the set of *testing.T method names that constitute
// an assertion (as opposed to setup/logging methods).
var assertionMethods = map[string]bool{
	"Error": true, "Errorf": true,
	"Fatal": true, "Fatalf": true,
	"Fail": true, "FailNow": true,
}

// hasAssertion reports whether body contains at least one assertion call
// involving the named *testing.T parameter.
func hasAssertion(body *ast.BlockStmt, tParam string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		// Pattern 1: t.Error / t.Fatal / etc.
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				if id.Name == tParam && assertionMethods[sel.Sel.Name] {
					found = true
					return false
				}
			}
		}

		// Pattern 2: helper(t, ...) — non-method call with t as first arg
		if _, isMethod := call.Fun.(*ast.SelectorExpr); !isMethod {
			if len(call.Args) > 0 {
				if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == tParam {
					found = true
					return false
				}
			}
		}

		return true
	})
	return found
}
