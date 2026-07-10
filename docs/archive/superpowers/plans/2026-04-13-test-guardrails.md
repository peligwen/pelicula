# Test Guardrails Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add automated enforcement to the pre-commit hook for three test integrity rules: no bare `defer` for global restores, no discarded `json.Unmarshal` errors, and no assertion-free test functions.

**Architecture:** Two grep checks added directly to `.githooks/pre-commit` (fast, zero dependencies), plus a small Go tool at `tools/check-test-assertions/` that uses `go/ast` to find Test functions with no assertion calls. The tool ships with its own `go.mod` (stdlib-only) and is invoked via `go run` in the pre-commit hook.

**Tech Stack:** Bash (hook modifications), Go stdlib `go/ast`, `go/parser`, `go/token` (assertion checker)

---

## Background

The test audit identified four automatable guardrails. One (`-race` by default) is already done. This plan implements the remaining three:

| Rule | Mechanism |
|------|-----------|
| No bare `defer` for global restores — use `t.Cleanup()` | grep in pre-commit |
| No discarded `json.Unmarshal` errors in tests | grep in pre-commit |
| Every Test function must have at least one assertion | `tools/check-test-assertions` Go tool |

**What the assertion checker detects:**  
A Test function is flagged if its body contains no call to `t.Error`, `t.Errorf`, `t.Fatal`, `t.Fatalf`, `t.Fail`, or `t.FailNow` (where `t` is the function's `*testing.T` parameter), AND no non-method function call where `t` is the first argument (the delegation-to-helper pattern). The check recurses into the full function body including nested closures, so table-driven tests using `t.Run` are handled correctly.

**Known limitation:** Tests that delegate all assertions to a helper called as `helper(t, ...)` will pass the check (the delegation is detected). Tests that use *only* `t.Cleanup`/`t.Helper`/`t.Log` (no assertions, no delegation) will be flagged — this is the exact failure mode we want to prevent.

---

## File Structure

**Created:**
- `tools/check-test-assertions/go.mod` — stdlib-only module (no external dependencies)
- `tools/check-test-assertions/main.go` — AST walker; accepts file paths as args, exits 1 if any violations found
- `tools/check-test-assertions/main_test.go` — tests covering: pass (has t.Fatal), pass (delegates to helper), pass (t.Run subtest), fail (no assertions), fail (only t.Cleanup)

**Modified:**
- `.githooks/pre-commit` — add grep checks for rules 2 & 3 and `go run ./tools/check-test-assertions` invocation for rule 1

---

## Task 1: Add grep-based checks to pre-commit hook

**Files:**
- Modify: `.githooks/pre-commit`

These two checks scan only staged `*_test.go` files so they never fire on non-test code.

- [ ] **Step 1: Write the failing smoke test**

Create a temp test file and verify `grep` catches the pattern:

```bash
echo 'defer func() { myGlobal = orig }()' > /tmp/bad_test.go
grep -En 'defer func\(\) \{[^}]*= [A-Za-z_]+[^}]*\}\(\)' /tmp/bad_test.go && echo "grep works"
grep -n '_ = json\.' <<< '_ = json.Unmarshal(b, &v)' && echo "json grep works"
rm /tmp/bad_test.go
```

Expected: both lines print the match and `grep works`.

- [ ] **Step 2: Add the checks to pre-commit**

Find the comment `# ── Shell checks ──` in `.githooks/pre-commit` and insert the following block immediately **before** it (after the closing `fi` of the Go checks block):

```bash
# ── Test-file quality checks ───────────────────────────────────────────────────

# Collect staged *_test.go files
staged_test_files=()
while IFS= read -r f; do
  [[ "$f" == *_test.go ]] && staged_test_files+=("$REPO_ROOT/$f")
done <<< "$staged_files"

if [[ ${#staged_test_files[@]} -gt 0 ]]; then
  # Rule: use t.Cleanup() for global restores, not bare defer
  if grep -En 'defer func\(\) \{[^}]*= [A-Za-z_]+[^}]*\}\(\)' \
       "${staged_test_files[@]}" 2>/dev/null; then
    echo "pre-commit: use t.Cleanup() instead of bare defer for global variable restores in tests" >&2
    exit 1
  fi

  # Rule: don't discard json decode errors in tests
  if grep -n '_ = json\.' "${staged_test_files[@]}" 2>/dev/null; then
    echo "pre-commit: don't discard json decode errors in tests — check the error with t.Fatal" >&2
    exit 1
  fi
fi
```

- [ ] **Step 3: Verify grep checks work end-to-end**

```bash
# Stage a file with each bad pattern and confirm the hook fires

# Test rule 2
cat > /tmp/bad_defer_test.go <<'EOF'
package main
import "testing"
func TestBad(t *testing.T) {
    orig := myGlobal
    myGlobal = "x"
    defer func() { myGlobal = orig }()
}
EOF
cp /tmp/bad_defer_test.go middleware/bad_defer_test.go
git add middleware/bad_defer_test.go
git stash  # stash the hook changes temporarily to test it
# Actually just run the grep directly:
grep -En 'defer func\(\) \{[^}]*= [A-Za-z_]+[^}]*\}\(\)' middleware/bad_defer_test.go && echo "CAUGHT"
git restore --staged middleware/bad_defer_test.go && rm middleware/bad_defer_test.go

# Test rule 3
grep -n '_ = json\.' <<< '    _ = json.Unmarshal(data, &v)' && echo "CAUGHT"
```

Expected: both print `CAUGHT`.

- [ ] **Step 4: Commit**

```bash
git add .githooks/pre-commit
git commit -m "test(hooks): add grep checks for defer-restore and json.Unmarshal patterns"
```

---

## Task 2: Create the check-test-assertions tool

**Files:**
- Create: `tools/check-test-assertions/go.mod`
- Create: `tools/check-test-assertions/main.go`
- Create: `tools/check-test-assertions/main_test.go`

- [ ] **Step 1: Write the failing tests first**

Create `tools/check-test-assertions/main_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "x_test.go")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheck_PassHasFatal(t *testing.T) {
	path := writeFile(t, `package p
import "testing"
func TestFoo(t *testing.T) {
    if 1 != 1 { t.Fatal("bad") }
}`)
	if err := checkFile(path); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestCheck_PassHasErrorf(t *testing.T) {
	path := writeFile(t, `package p
import "testing"
func TestFoo(t *testing.T) {
    if got := compute(); got != 1 {
        t.Errorf("got %d", got)
    }
}`)
	if err := checkFile(path); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestCheck_PassDelegatesHelper(t *testing.T) {
	// Calls assertFoo(t, ...) — t is first arg, counts as delegation
	path := writeFile(t, `package p
import "testing"
func TestFoo(t *testing.T) {
    result := compute()
    assertFoo(t, result)
}`)
	if err := checkFile(path); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestCheck_PassTableDriven(t *testing.T) {
	// t.Run is a method call on t — not Error/Fatal but t is used
	// and the subtest closure has its own t.Fatal
	path := writeFile(t, `package p
import "testing"
func TestFoo(t *testing.T) {
    for _, tc := range []struct{ in, want int }{{1, 1}} {
        t.Run("case", func(t *testing.T) {
            if got := fn(tc.in); got != tc.want {
                t.Fatalf("got %d want %d", got, tc.want)
            }
        })
    }
}`)
	if err := checkFile(path); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestCheck_FailNoAssertions(t *testing.T) {
	path := writeFile(t, `package p
import "testing"
func TestFoo(t *testing.T) {
    doSomething() // no t usage at all
}`)
	if err := checkFile(path); err == nil {
		t.Fatal("expected error for assertion-free test, got nil")
	}
}

func TestCheck_FailOnlyCleanup(t *testing.T) {
	path := writeFile(t, `package p
import "testing"
func TestFoo(t *testing.T) {
    orig := globalVar
    globalVar = "x"
    t.Cleanup(func() { globalVar = orig })
    _ = doSomething() // result discarded, no assertion
}`)
	if err := checkFile(path); err == nil {
		t.Fatal("expected error: t.Cleanup alone is not an assertion")
	}
}

func TestCheck_PassNonTestFunc(t *testing.T) {
	// helper functions named TestXxx but not func(*testing.T) are ignored
	path := writeFile(t, `package p
import "testing"
func TestHelper(input string) string {
    return input
}
func TestReal(t *testing.T) {
    if TestHelper("x") != "x" { t.Fatal("bad") }
}`)
	if err := checkFile(path); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestCheck_PassEmptyFile(t *testing.T) {
	path := writeFile(t, `package p`)
	if err := checkFile(path); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}
```

Run to confirm all fail (function `checkFile` not defined yet):

```bash
cd tools/check-test-assertions && go test ./... 2>&1
```

Expected: `undefined: checkFile`

- [ ] **Step 2: Create go.mod**

```bash
mkdir -p tools/check-test-assertions
cat > tools/check-test-assertions/go.mod <<'EOF'
module check-test-assertions

go 1.22
EOF
```

- [ ] **Step 3: Implement main.go**

Create `tools/check-test-assertions/main.go`:

```go
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
```

- [ ] **Step 4: Run the tests**

```bash
cd /Users/gwen/workspace/pelicula/tools/check-test-assertions && go test -v ./...
```

Expected output (all pass):
```
=== RUN   TestCheck_PassHasFatal
--- PASS: TestCheck_PassHasFatal (0.00s)
=== RUN   TestCheck_PassHasErrorf
--- PASS: TestCheck_PassHasErrorf (0.00s)
=== RUN   TestCheck_PassDelegatesHelper
--- PASS: TestCheck_PassDelegatesHelper (0.00s)
=== RUN   TestCheck_PassTableDriven
--- PASS: TestCheck_PassTableDriven (0.00s)
=== RUN   TestCheck_FailNoAssertions
--- PASS: TestCheck_FailNoAssertions (0.00s)
=== RUN   TestCheck_FailOnlyCleanup
--- PASS: TestCheck_FailOnlyCleanup (0.00s)
=== RUN   TestCheck_PassNonTestFunc
--- PASS: TestCheck_PassNonTestFunc (0.00s)
=== RUN   TestCheck_PassEmptyFile
--- PASS: TestCheck_PassEmptyFile (0.00s)
PASS
ok  	check-test-assertions
```

- [ ] **Step 5: Verify it catches the original false positive**

```bash
cat > /tmp/false_positive_test.go <<'EOF'
package main

import (
    "sync"
    "testing"
    "time"
)

type UpdateInfo struct {
    CurrentVersion string
    CheckedAt      time.Time
}

func TestUpdateCacheThreadSafety(t *testing.T) {
    var wg sync.WaitGroup
    for i := 0; i < 20; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            _ = "getCachedUpdate()"
        }()
    }
    wg.Wait()
}
EOF
cd /Users/gwen/workspace/pelicula/tools/check-test-assertions && go run . /tmp/false_positive_test.go
echo "exit: $?"
```

Expected: prints the file path with a violation and exits 1.

- [ ] **Step 6: Commit**

```bash
cd /Users/gwen/workspace/pelicula
git add tools/check-test-assertions/
git commit -m "feat(tools): add check-test-assertions AST tool for assertion-free Test detection"
```

---

## Task 3: Wire check-test-assertions into pre-commit hook

**Files:**
- Modify: `.githooks/pre-commit`

- [ ] **Step 1: Add the tool invocation to the test-file checks block**

In `.githooks/pre-commit`, find the block added in Task 1:

```bash
if [[ ${#staged_test_files[@]} -gt 0 ]]; then
```

Add the tool invocation at the END of that block, before the closing `fi`:

```bash
  # Rule: every Test function must have at least one assertion
  (cd "$REPO_ROOT" && go run ./tools/check-test-assertions "${staged_test_files[@]}") || {
    echo "pre-commit: fix assertion-free tests above before committing" >&2
    exit 1
  }
```

The full block after Task 1 + Task 3 modifications should look like:

```bash
if [[ ${#staged_test_files[@]} -gt 0 ]]; then
  # Rule: use t.Cleanup() for global restores, not bare defer
  if grep -En 'defer func\(\) \{[^}]*= [A-Za-z_]+[^}]*\}\(\)' \
       "${staged_test_files[@]}" 2>/dev/null; then
    echo "pre-commit: use t.Cleanup() instead of bare defer for global variable restores in tests" >&2
    exit 1
  fi

  # Rule: don't discard json decode errors in tests
  if grep -n '_ = json\.' "${staged_test_files[@]}" 2>/dev/null; then
    echo "pre-commit: don't discard json decode errors in tests — check the error with t.Fatal" >&2
    exit 1
  fi

  # Rule: every Test function must have at least one assertion
  (cd "$REPO_ROOT" && go run ./tools/check-test-assertions "${staged_test_files[@]}") || {
    echo "pre-commit: fix assertion-free tests above before committing" >&2
    exit 1
  }
fi
```

- [ ] **Step 2: Verify the tool fires on a staged bad test**

```bash
cat > /Users/gwen/workspace/pelicula/middleware/assertion_free_test.go <<'EOF'
package main

import "testing"

func TestAssertionFree(t *testing.T) {
    // no assertion at all
    _ = "something"
}
EOF
cd /Users/gwen/workspace/pelicula
git add middleware/assertion_free_test.go
bash .githooks/pre-commit 2>&1
echo "Exit: $?"
git restore --staged middleware/assertion_free_test.go
rm middleware/assertion_free_test.go
```

Expected: pre-commit prints the violation and exits non-zero.

- [ ] **Step 3: Verify a legitimate test passes**

```bash
cat > /Users/gwen/workspace/pelicula/middleware/good_test_test.go <<'EOF'
package main

import "testing"

func TestGoodTest(t *testing.T) {
    if 1 != 1 {
        t.Fatal("math is broken")
    }
}
EOF
cd /Users/gwen/workspace/pelicula
git add middleware/good_test_test.go
bash .githooks/pre-commit 2>&1
echo "Exit: $?"
git restore --staged middleware/good_test_test.go
rm middleware/good_test_test.go
```

Expected: pre-commit prints `pre-commit: ok` and exits 0.

- [ ] **Step 4: Verify existing test suite still passes the hook**

Stage all existing modified test files and run the hook:

```bash
cd /Users/gwen/workspace/pelicula
git add middleware/ procula/
bash .githooks/pre-commit 2>&1
echo "Exit: $?"
git restore --staged .
```

Expected: `pre-commit: ok`, exit 0. (The existing tests we fixed should all pass cleanly.)

- [ ] **Step 5: Commit**

```bash
git add .githooks/pre-commit
git commit -m "test(hooks): wire check-test-assertions tool into pre-commit"
```

---

## Verification

Run the full test suite to confirm nothing was broken by the hook changes:

```bash
cd /Users/gwen/workspace/pelicula

# All unit tests still pass
make test

# Confirm hook installation works fresh
make install-hooks
echo "make install-hooks: $?"

# Confirm check-test-assertions tests pass
cd tools/check-test-assertions && go test -v ./... && cd ../..

# Stage and attempt to commit a known-bad test — should be blocked
cat > middleware/bad_test_canary_test.go <<'EOF'
package main
import "testing"
func TestNoAssertion(t *testing.T) { _ = "hello" }
EOF
git add middleware/bad_test_canary_test.go
bash .githooks/pre-commit 2>&1 | grep "assertion-free"
git restore --staged middleware/bad_test_canary_test.go
rm middleware/bad_test_canary_test.go
```

Expected final line: `pre-commit: fix assertion-free tests above before committing`
