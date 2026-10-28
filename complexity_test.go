package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Structure gate. It runs with the ordinary test suite (go test ./...), so it
// is enforced on every push and PR without extra tooling, and locally before a
// commit. .golangci.yml enforces the same limits through gocyclo and funlen.
//
// The thresholds are about structure, not style: a function that does not fit
// on a screen, or branches too many ways to hold in your head, is where bugs
// hide. Long files are called out separately because they are the symptom the
// gate exists for.
const (
	maxFileLines  = 700
	maxFuncLines  = 100
	maxCyclomatic = 20
)

// grandfatheredFiles predates this gate. Split a file when you next touch it,
// and delete its entry in the same change. Never add one.
var grandfatheredFiles = map[string]bool{
	"internal/manager/sftp.go":       true,
	"internal/config/cli.go":         true,
	"internal/manager/manager.go":    true,
	"internal/config/config.go":      true,
	"internal/manager/commandlog.go": true,
}

// grandfatheredFuncs predates this gate, keyed "path:FuncName". Same rule:
// shrink the list, never grow it.
var grandfatheredFuncs = map[string]bool{
	"internal/config/cli.go:ParseArgs":               true,
	"internal/config/cli.go:normalizeConfig":         true,
	"internal/config/cli.go:buildSingleHostConfig":   true,
	"internal/config/config.go:Normalize":            true,
	"internal/manager/sftp.go:upload":                true,
	"internal/manager/sftp.go:download":              true,
	"internal/manager/sftp.go:TransferFile":          true,
	"internal/manager/session.go:runShellScriptOnce": true,
	"internal/manager/exec.go:runExecCommand":        true,
	"internal/manager/status.go:collectStatus":       true,
}

// skippedDirs hold no hand-written Go.
var skippedDirs = map[string]bool{
	".git": true, "vendor": true, "node_modules": true, "downloads": true, "logs": true,
}

// finding is one violated limit.
type finding struct {
	path   string
	line   int
	what   string
	detail string
}

func TestStructureGate(t *testing.T) {
	findings, err := collectFindings(".")
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(findings) == 0 {
		return
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].path != findings[j].path {
			return findings[i].path < findings[j].path
		}
		return findings[i].line < findings[j].line
	})
	var b strings.Builder
	b.WriteString("structure gate failed:\n")
	for _, f := range findings {
		fmt.Fprintf(&b, "  %s:%d  %s — %s\n", f.path, f.line, f.what, f.detail)
	}
	b.WriteString("\nsplit the function or file. If it predates this gate, add it to\n" +
		"grandfatheredFiles/grandfatheredFuncs in complexity_test.go and delete the\n" +
		"entry in the same change that fixes it.")
	t.Error(b.String())
}

func collectFindings(root string) ([]finding, error) {
	var findings []finding
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, "./"))
		if d.IsDir() {
			if skippedDirs[rel] {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			findings = append(findings, inspectFile(path, rel)...)
		}
		return nil
	})
	return findings, err
}

// inspectFile reports the limits one file violates.
func inspectFile(path, rel string) []finding {
	var out []finding
	if data, err := os.ReadFile(path); err == nil && !grandfatheredFiles[rel] {
		if n := strings.Count(string(data), "\n"); n > maxFileLines {
			out = append(out, finding{rel, 1, "file length",
				fmt.Sprintf("%d lines exceeds %d", n, maxFileLines)})
		}
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return out // a file that does not parse is go build's problem
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || grandfatheredFuncs[rel+":"+fn.Name.Name] {
			continue
		}
		out = append(out, inspectFunc(fset, rel, fn)...)
	}
	return out
}

func inspectFunc(fset *token.FileSet, rel string, fn *ast.FuncDecl) []finding {
	var out []finding
	start := fset.Position(fn.Pos()).Line
	if n := fset.Position(fn.End()).Line - start + 1; n > maxFuncLines {
		out = append(out, finding{rel, start, "function length",
			fmt.Sprintf("%s: %d lines exceeds %d", fn.Name.Name, n, maxFuncLines)})
	}
	if c := cyclomatic(fn); c > maxCyclomatic {
		out = append(out, finding{rel, start, "cyclomatic complexity",
			fmt.Sprintf("%s: complexity %d exceeds %d", fn.Name.Name, c, maxCyclomatic)})
	}
	return out
}

// cyclomatic counts the decision points a reader has to hold in mind.
func cyclomatic(fn *ast.FuncDecl) int {
	c := 1
	ast.Inspect(fn, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt:
			c++
		case *ast.CaseClause:
			if v.List != nil { // `default:` is not a branch
				c++
			}
		case *ast.CommClause:
			if v.Comm != nil {
				c++
			}
		case *ast.BinaryExpr:
			if v.Op == token.LAND || v.Op == token.LOR {
				c++
			}
		}
		return true
	})
	return c
}
