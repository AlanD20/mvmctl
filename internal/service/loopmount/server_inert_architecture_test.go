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

const (
	systemRootfsResourceLocksFactory      = "newSystemRootfsResourceLocks"
	systemRootfsPartitionDiscoveryFactory = "systemRootfsPartitionDiscoveryDeps"
	rootfsPartitionDiscoveryEntry         = "discoverRootfsPartitionDevices"
	systemRootfsImageInspectFactory       = "newSystemRootfsImageInspectAdapter"
	rootfsImageInspectAdapterType         = "rootfsImageInspectAdapter"
	systemRootfsVMProvisionFactory        = "newSystemRootfsVMProvisionAdapter"
	rootfsVMProvisionAdapterType          = "rootfsVMProvisionAdapter"
	provisionInspectServerFactory         = "NewProvisionInspectServer"
	provisionInspectEffectsType           = "provisionInspectRootfsEffects"
)

// Rationale: Production activation is closed to the one reviewed provision/inspect constructor. OptimizeImage must
// remain without a production effect assignment even though its protocol types and inactive Server method exist.
func TestLoopMountProductionActivationRemainsClosedToProvisionAndInspect(t *testing.T) {
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
		assertInertRootfsPartitionDiscovery(t, files, parsed)
		assertInertRootfsImageInspectAdapter(t, files, parsed)
		assertInertRootfsVMProvisionAdapter(t, files, parsed)
		assertInertImageOptimizeEffects(t, files, parsed)
	}
}

// assertInertRootfsPartitionDiscovery rejects production construction or invocation outside reviewed declarations.
func assertInertRootfsPartitionDiscovery(t *testing.T, files *token.FileSet, parsed *ast.File) {
	t.Helper()

	allowed := map[string]token.Pos{
		systemRootfsPartitionDiscoveryFactory: functionDeclarationPosition(
			parsed,
			systemRootfsPartitionDiscoveryFactory,
		),
		rootfsPartitionDiscoveryEntry: functionDeclarationPosition(parsed, rootfsPartitionDiscoveryEntry),
	}
	allowedFactoryBody := identifiersWithinFunctions(
		parsed,
		systemRootfsImageInspectFactory,
		systemRootfsVMProvisionFactory,
	)
	ast.Inspect(parsed, func(node ast.Node) bool {
		identifier, isIdentifier := node.(*ast.Ident)
		if !isIdentifier {
			return true
		}
		declaration, guarded := allowed[identifier.Name]
		if guarded && identifier.Pos() != declaration {
			if _, insideFactory := allowedFactoryBody[identifier.Pos()]; insideFactory {
				return true
			}
			t.Errorf("production partition discovery activation found at %s", files.Position(identifier.Pos()))
		}
		return true
	})
}

// assertInertRootfsVMProvisionAdapter permits only its declaration, method receivers, and unused system factory.
// Construction elsewhere would activate a permanently unshared OS thread without handler-exit semantics.
func assertInertRootfsVMProvisionAdapter(t *testing.T, files *token.FileSet, parsed *ast.File) {
	t.Helper()

	assertInertRootfsEffectAdapter(
		t,
		files,
		parsed,
		systemRootfsVMProvisionFactory,
		rootfsVMProvisionAdapterType,
		"VM provisioning",
	)
}

func assertInertRootfsEffectAdapter(
	t *testing.T,
	files *token.FileSet,
	parsed *ast.File,
	factoryName string,
	adapterType string,
	operation string,
) {
	t.Helper()

	factoryDeclaration := functionDeclarationPosition(parsed, factoryName)
	allowedAdapter := identifiersWithinFunction(parsed, factoryName)
	for position := range identifiersWithinFunction(parsed, provisionInspectServerFactory) {
		allowedAdapter[position] = struct{}{}
	}
	for position := range identifiersWithinType(parsed, provisionInspectEffectsType) {
		allowedAdapter[position] = struct{}{}
	}
	allowedFactoryReferences := identifiersWithinFunction(parsed, provisionInspectServerFactory)
	for _, declaration := range parsed.Decls {
		switch value := declaration.(type) {
		case *ast.GenDecl:
			for _, spec := range value.Specs {
				typeSpec, isTypeSpec := spec.(*ast.TypeSpec)
				if isTypeSpec && typeSpec.Name.Name == adapterType {
					allowedAdapter[typeSpec.Name.Pos()] = struct{}{}
				}
			}
		case *ast.FuncDecl:
			if value.Recv == nil || len(value.Recv.List) != 1 {
				continue
			}
			ast.Inspect(value.Recv.List[0].Type, func(node ast.Node) bool {
				identifier, ok := node.(*ast.Ident)
				if ok && identifier.Name == adapterType {
					allowedAdapter[identifier.Pos()] = struct{}{}
				}
				return true
			})
		}
	}
	nonTypeNames := nonTypeNamePositions(parsed)
	ast.Inspect(parsed, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if !ok {
			return true
		}
		if identifier.Name == factoryName && identifier.Pos() != factoryDeclaration {
			if _, allowed := allowedFactoryReferences[identifier.Pos()]; allowed {
				return true
			}
			t.Errorf("production %s adapter factory reference found at %s", operation, files.Position(identifier.Pos()))
			return true
		}
		if identifier.Name != adapterType {
			return true
		}
		if _, excluded := nonTypeNames[identifier.Pos()]; excluded {
			return true
		}
		if _, allowed := allowedAdapter[identifier.Pos()]; !allowed {
			t.Errorf("production %s adapter construction found at %s", operation, files.Position(identifier.Pos()))
		}
		return true
	})
}

// assertInertImageOptimizeEffects permits only the existing field declaration and inactive Server method paths.
func assertInertImageOptimizeEffects(t *testing.T, files *token.FileSet, parsed *ast.File) {
	t.Helper()

	allowed := identifiersWithinFunctions(parsed, "OptimizeImage", "validateImageOptimizeDependencies")
	for _, declaration := range parsed.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range general.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != "Server" {
				continue
			}
			structure, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range structure.Fields.List {
				for _, name := range field.Names {
					if name.Name == "optimizeEffects" {
						allowed[name.Pos()] = struct{}{}
					}
				}
			}
		}
	}
	ast.Inspect(parsed, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if !ok || identifier.Name != "optimizeEffects" {
			return true
		}
		if _, ok := allowed[identifier.Pos()]; !ok {
			t.Errorf("production image optimization effect activation found at %s", files.Position(identifier.Pos()))
		}
		return true
	})
}

// assertInertRootfsImageInspectAdapter permits only its declaration, method receivers, and its unused system factory.
// Constructing the adapter elsewhere would activate a permanently unshared OS thread without handler-exit semantics.
func assertInertRootfsImageInspectAdapter(t *testing.T, files *token.FileSet, parsed *ast.File) {
	t.Helper()

	assertInertRootfsEffectAdapter(
		t,
		files,
		parsed,
		systemRootfsImageInspectFactory,
		rootfsImageInspectAdapterType,
		"image inspection",
	)
}

func identifiersWithinFunction(parsed *ast.File, name string) map[token.Pos]struct{} {
	positions := make(map[token.Pos]struct{})
	for _, declaration := range parsed.Decls {
		function, isFunction := declaration.(*ast.FuncDecl)
		if !isFunction || function.Name.Name != name {
			continue
		}
		ast.Inspect(function, func(node ast.Node) bool {
			if identifier, ok := node.(*ast.Ident); ok {
				positions[identifier.Pos()] = struct{}{}
			}
			return true
		})
	}
	return positions
}

func identifiersWithinFunctions(parsed *ast.File, names ...string) map[token.Pos]struct{} {
	positions := make(map[token.Pos]struct{})
	for _, name := range names {
		for position := range identifiersWithinFunction(parsed, name) {
			positions[position] = struct{}{}
		}
	}
	return positions
}

func identifiersWithinType(parsed *ast.File, name string) map[token.Pos]struct{} {
	positions := make(map[token.Pos]struct{})
	for _, declaration := range parsed.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range general.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != name {
				continue
			}
			ast.Inspect(typeSpec, func(node ast.Node) bool {
				if identifier, ok := node.(*ast.Ident); ok {
					positions[identifier.Pos()] = struct{}{}
				}
				return true
			})
		}
	}
	return positions
}

func functionDeclarationPosition(parsed *ast.File, name string) token.Pos {
	for _, declaration := range parsed.Decls {
		function, isFunction := declaration.(*ast.FuncDecl)
		if isFunction && function.Name.Name == name {
			return function.Name.Pos()
		}
	}
	return token.NoPos
}

// assertInertLoopMountFile rejects every production reference outside the reviewed type and method declarations.
func assertInertLoopMountFile(t *testing.T, files *token.FileSet, name string, parsed *ast.File) {
	t.Helper()

	allowedServer := allowedServerDeclarationPositions(name, parsed)
	allowedAdapters := allowedRootfsLockAdapterPositions(name, parsed)
	factoryDeclaration := factoryDeclarationPosition(parsed)
	allowedFactoryReferences := identifiersWithinFunction(parsed, provisionInspectServerFactory)
	nonTypeNames := nonTypeNamePositions(parsed)
	ast.Inspect(parsed, func(node ast.Node) bool {
		identifier, isIdentifier := node.(*ast.Ident)
		if !isIdentifier {
			return true
		}
		if identifier.Name == systemRootfsResourceLocksFactory {
			if identifier.Pos() != factoryDeclaration {
				if _, allowed := allowedFactoryReferences[identifier.Pos()]; allowed {
					return true
				}
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
	for position := range identifiersWithinFunction(parsed, provisionInspectServerFactory) {
		allowed[position] = struct{}{}
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
