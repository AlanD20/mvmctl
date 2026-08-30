package privileged

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/service/agent"
)

// Rationale: The release build's generated guest agent is the only executable byte source authorized for VM
// provisioning. Each effect request must receive an independent copy rather than the agent package's shared slice.
func TestSystemLoopMountAgentBinaryReturnsFreshGeneratedAgent(t *testing.T) {
	embedded := agent.AgentBinary()
	require.NotEmpty(t, embedded)

	first, err := systemLoopMountAgentBinary(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, first)
	second, err := systemLoopMountAgentBinary(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, second)
	if diff := cmp.Diff(embedded, first); diff != "" {
		t.Errorf("first agent copy mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(embedded, second); diff != "" {
		t.Errorf("second agent copy mismatch (-want +got):\n%s", diff)
	}

	wantFirstByte := embedded[0]
	first[0] ^= 0xff
	assert.Equal(t, wantFirstByte, embedded[0])
	assert.Equal(t, wantFirstByte, second[0])
	second[0] ^= 0xff
	assert.Equal(t, wantFirstByte, embedded[0])
	assert.NotEqual(t, first[0], embedded[0])
	assert.NotEqual(t, second[0], embedded[0])
}

// Rationale: A correct accessor is ineffective unless the production composition root injects it into the only
// activated LoopMount constructor. Keep this binding direct and statically reviewable.
func TestRunBindsSystemLoopMountAgentBinaryDirectly(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	entryPath := filepath.Join(filepath.Dir(currentFile), "entry.go")
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, entryPath, nil, 0)
	require.NoError(t, err)

	var runDeclaration *ast.FuncDecl
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == "Run" {
			runDeclaration = function
			break
		}
	}
	require.NotNil(t, runDeclaration)

	constructorCalls := 0
	ast.Inspect(runDeclaration.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "NewProvisionInspectServer" {
			return true
		}
		packageName, ok := selector.X.(*ast.Ident)
		if !ok || packageName.Name != "loopmount" {
			return true
		}
		constructorCalls++
		require.Len(t, call.Args, 1)
		accessor, ok := call.Args[0].(*ast.Ident)
		require.True(t, ok)
		assert.Equal(t, "systemLoopMountAgentBinary", accessor.Name)
		return true
	})
	assert.Equal(t, 1, constructorCalls)
}
