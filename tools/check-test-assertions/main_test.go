package main

import (
	"os"
	"path/filepath"
	"strings"
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
    if got := 1; got != 1 {
        t.Errorf("got %d", got)
    }
}`)
	if err := checkFile(path); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestCheck_PassDelegatesHelper(t *testing.T) {
	path := writeFile(t, `package p
import "testing"
func TestFoo(t *testing.T) {
    result := 42
    assertFoo(t, result)
}`)
	if err := checkFile(path); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestCheck_PassTableDriven(t *testing.T) {
	path := writeFile(t, `package p
import "testing"
func TestFoo(t *testing.T) {
    for _, tc := range []struct{ in, want int }{{1, 1}} {
        t.Run("case", func(t *testing.T) {
            if got := tc.in; got != tc.want {
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
    doSomething()
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
    _ = doSomething()
}`)
	if err := checkFile(path); err == nil {
		t.Fatal("expected error: t.Cleanup alone is not an assertion")
	}
}

func TestCheck_PassNonTestFunc(t *testing.T) {
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

func TestCheck_PassMethodHelper(t *testing.T) {
	// suite.assertOK(t, value) — t is first arg to a method call
	path := writeFile(t, `package p
import "testing"
type suite struct{}
func TestFoo(t *testing.T) {
    s := suite{}
    s.assertOK(t, 42)
}`)
	if err := checkFile(path); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestCheck_ErrorMessageFormat(t *testing.T) {
	path := writeFile(t, `package p
import "testing"
func TestNoAssert(t *testing.T) {
    _ = 42
}`)
	err := checkFile(path)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "TestNoAssert") {
		t.Errorf("error message missing function name: %q", msg)
	}
	// Should contain a line number
	if !strings.Contains(msg, ":3:") && !strings.Contains(msg, ":4:") {
		t.Errorf("error message missing line number: %q", msg)
	}
}
