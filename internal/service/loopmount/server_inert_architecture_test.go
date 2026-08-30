package loopmount

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const systemRootfsResourceLocksFactory = "newSystemRootfsResourceLocks"

// Rationale: The current Server and system lock adapter are reviewed foundations, not an authorized privileged action.
// Any production reference must land with the explicit activation review that removes this gate.
func TestLoopMountServerRemainsInertUntilExplicitActivation(t *testing.T) {
	t.Parallel()

	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	packageDir := filepath.Dir(currentFile)
	entries, err := os.ReadDir(packageDir)
	require.NoError(t, err)

	files := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, parseErr := parser.ParseFile(files, filepath.Join(packageDir, name), nil, 0)
		require.NoError(t, parseErr)
		assertInertLoopMountFile(t, files, name, parsed)
	}
}

// assertInertLoopMountFile rejects every production reference outside the reviewed type and method declarations.
func assertInertLoopMountFile(t *testing.T, files *token.FileSet, name string, parsed *ast.File) {
	t.Helper()

	allowedServer := allowedServerDeclarationPositions(name, parsed)
	allowedAdapters := allowedRootfsLockAdapterPositions(name, parsed)
	factoryDeclaration := factoryDeclarationPosition(parsed)
	nonTypeNames := nonTypeNamePositions(parsed)
	ast.Inspect(parsed, func(node ast.Node) bool {
		identifier, isIdentifier := node.(*ast.Ident)
		if !isIdentifier {
			return true
		}
		if identifier.Name == systemRootfsResourceLocksFactory {
			if identifier.Pos() != factoryDeclaration {
				t.Errorf("production lock adapter reference found at %s", files.Position(identifier.Pos()))
			}
			return true
		}
		if identifier.Name != "Server" {
			if isInertRootfsLockAdapterType(identifier.Name) {
				if _, isNonTypeName := nonTypeNames[identifier.Pos()]; isNonTypeName {
					return true
				}
				if identifier.Obj != nil && identifier.Obj.Kind != ast.Typ {
					return true
				}
				if _, allowed := allowedAdapters[identifier.Pos()]; !allowed {
					t.Errorf(
						"production rootfs lock adapter construction found at %s",
						files.Position(identifier.Pos()),
					)
				}
			}
			return true
		}
		if _, isNonTypeName := nonTypeNames[identifier.Pos()]; isNonTypeName {
			return true
		}
		if identifier.Obj != nil && identifier.Obj.Kind != ast.Typ {
			return true
		}
		if _, allowed := allowedServer[identifier.Pos()]; !allowed {
			t.Errorf("production LoopMount Server reference found at %s", files.Position(identifier.Pos()))
		}
		return true
	})
}

// allowedRootfsLockAdapterPositions permits only declarations, method receivers, and the unused system factory body.
func allowedRootfsLockAdapterPositions(name string, parsed *ast.File) map[token.Pos]struct{} {
	allowed := make(map[token.Pos]struct{})
	if name != "rootfs_lock.go" {
		return allowed
	}
	for _, declaration := range parsed.Decls {
		switch value := declaration.(type) {
		case *ast.GenDecl:
			for _, spec := range value.Specs {
				typeSpec, isTypeSpec := spec.(*ast.TypeSpec)
				if !isTypeSpec {
					continue
				}
				if isInertRootfsLockAdapterType(typeSpec.Name.Name) {
					allowed[typeSpec.Name.Pos()] = struct{}{}
				}
			}
		case *ast.FuncDecl:
			if value.Name.Name == systemRootfsResourceLocksFactory {
				ast.Inspect(value.Body, func(node ast.Node) bool {
					identifier, isIdentifier := node.(*ast.Ident)
					if isIdentifier && isInertRootfsLockAdapterType(identifier.Name) {
						allowed[identifier.Pos()] = struct{}{}
					}
					return true
				})
			}
			if value.Recv == nil || len(value.Recv.List) != 1 {
				continue
			}
			if receiver := guardedAdapterTypeIdentifier(value.Recv.List[0].Type); receiver != nil {
				allowed[receiver.Pos()] = struct{}{}
			}
		}
	}
	return allowed
}

// guardedAdapterTypeIdentifier unwraps a receiver and returns one of the two reserved concrete adapter types.
func guardedAdapterTypeIdentifier(expression ast.Expr) *ast.Ident {
	switch value := expression.(type) {
	case *ast.Ident:
		if isInertRootfsLockAdapterType(value.Name) {
			return value
		}
	case *ast.StarExpr:
		return guardedAdapterTypeIdentifier(value.X)
	case *ast.ParenExpr:
		return guardedAdapterTypeIdentifier(value.X)
	}
	return nil
}

// isInertRootfsLockAdapterType identifies the concrete adapter types reserved to the unreferenced system factory.
func isInertRootfsLockAdapterType(name string) bool {
	return name == "authorityFSRootfsResourceLocks" || name == "systemLoopMountAuthorityTree"
}

// allowedServerDeclarationPositions returns only the existing type name and method receiver references.
func allowedServerDeclarationPositions(name string, parsed *ast.File) map[token.Pos]struct{} {
	allowed := make(map[token.Pos]struct{})
	if name != "server.go" {
		return allowed
	}
	for _, declaration := range parsed.Decls {
		switch value := declaration.(type) {
		case *ast.GenDecl:
			for _, spec := range value.Specs {
				typeSpec, isTypeSpec := spec.(*ast.TypeSpec)
				if isTypeSpec && typeSpec.Name.Name == "Server" {
					allowed[typeSpec.Name.Pos()] = struct{}{}
				}
			}
		case *ast.FuncDecl:
			if value.Recv == nil || len(value.Recv.List) != 1 {
				continue
			}
			if receiver := serverTypeIdentifier(value.Recv.List[0].Type); receiver != nil {
				allowed[receiver.Pos()] = struct{}{}
			}
		}
	}
	return allowed
}

// serverTypeIdentifier unwraps the syntax permitted for a method receiver and returns a Server identifier.
func serverTypeIdentifier(expression ast.Expr) *ast.Ident {
	switch value := expression.(type) {
	case *ast.Ident:
		if value.Name == "Server" {
			return value
		}
	case *ast.StarExpr:
		return serverTypeIdentifier(value.X)
	case *ast.ParenExpr:
		return serverTypeIdentifier(value.X)
	}
	return nil
}

// factoryDeclarationPosition distinguishes the reviewed factory declaration from any effectful reference to it.
func factoryDeclarationPosition(parsed *ast.File) token.Pos {
	for _, declaration := range parsed.Decls {
		function, isFunction := declaration.(*ast.FuncDecl)
		if isFunction && function.Name.Name == systemRootfsResourceLocksFactory {
			return function.Name.Pos()
		}
	}
	return token.NoPos
}

// nonTypeNamePositions excludes selector and composite-field names that cannot resolve to this package's Server type.
func nonTypeNamePositions(parsed *ast.File) map[token.Pos]struct{} {
	positions := make(map[token.Pos]struct{})
	ast.Inspect(parsed, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.SelectorExpr:
			positions[value.Sel.Pos()] = struct{}{}
		case *ast.KeyValueExpr:
			if identifier, isIdentifier := value.Key.(*ast.Ident); isIdentifier {
				positions[identifier.Pos()] = struct{}{}
			}
		}
		return true
	})
	return positions
}
