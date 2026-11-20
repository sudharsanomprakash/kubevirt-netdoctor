package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// Engine packages observe and reason; they must never change cluster or
// node state, and must never depend on a remediation subsystem.
var enginePackages = []string{
	"kube", "kubevirt", "multus", "inspect", "topology", "host", "hostpath",
	"route", "expect", "graph", "diagnose", "ebpf", "traceplan", "capability", "output",
}

var writeMethods = map[string]bool{
	"Create": true, "Update": true, "UpdateStatus": true, "Patch": true,
	"Apply": true, "ApplyStatus": true, "Delete": true, "DeleteCollection": true,
}

// allowedWrites are calls that use a write verb but persist nothing.
var allowedWrites = map[string]bool{
	"SelfSubjectAccessReviews": true, // evaluated and returned, never stored
}

func TestEngineIsReadOnly(t *testing.T) {
	for _, pkg := range enginePackages {
		dir := filepath.Join("..", pkg)
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			for _, imp := range f.Imports {
				if strings.Contains(imp.Path.Value, "remediation") {
					t.Errorf("%s imports %s: the diagnostic engine must not depend on remediation", path, imp.Path.Value)
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !writeMethods[sel.Sel.Name] {
					return true
				}
				if recv, ok := sel.X.(*ast.CallExpr); ok {
					if rs, ok := recv.Fun.(*ast.SelectorExpr); ok && allowedWrites[rs.Sel.Name] {
						return true
					}
				}
				t.Errorf("%s: call to %s: the diagnostic engine is read-only", path, sel.Sel.Name)
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("%s: %v", pkg, err)
		}
	}
}
