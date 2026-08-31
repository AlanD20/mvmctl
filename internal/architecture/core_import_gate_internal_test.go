package architecture

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type coreImportViolation struct {
	File            string
	Line            int
	ImportingDomain string
	ImportedDomain  string
	ImportPath      string
}

func findCoreDomainImportViolations(repositoryRoot, modulePath string) ([]coreImportViolation, error) {
	coreRoot := filepath.Join(repositoryRoot, "internal", "core")
	domains, err := os.ReadDir(coreRoot)
	if err != nil {
		return nil, fmt.Errorf("read internal/core: %w", err)
	}

	var violations []coreImportViolation
	for _, domain := range domains {
		if !domain.IsDir() {
			continue
		}

		importingDomain := domain.Name()
		domainRoot := filepath.Join(coreRoot, importingDomain)
		err = filepath.WalkDir(domainRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
				strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}

			relativePath, relErr := filepath.Rel(repositoryRoot, path)
			if relErr != nil {
				return fmt.Errorf("make %q repository-relative: %w", path, relErr)
			}
			relativePath = filepath.ToSlash(relativePath)
			files := token.NewFileSet()
			parsed, parseErr := parser.ParseFile(files, path, nil, parser.ImportsOnly)
			if parseErr != nil {
				return fmt.Errorf("parse %s: %w", relativePath, parseErr)
			}

			for _, imported := range parsed.Imports {
				importPath, unquoteErr := strconv.Unquote(imported.Path.Value)
				if unquoteErr != nil {
					return fmt.Errorf("parse import in %s: %w", relativePath, unquoteErr)
				}
				importedDomain, isCoreDomain := coreDomainFromImport(importPath, modulePath)
				if !isCoreDomain || importedDomain == importingDomain {
					continue
				}
				violations = append(violations, coreImportViolation{
					File:            relativePath,
					Line:            files.Position(imported.Pos()).Line,
					ImportingDomain: importingDomain,
					ImportedDomain:  importedDomain,
					ImportPath:      importPath,
				})
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scan internal/core/%s: %w", importingDomain, err)
		}
	}

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].File != violations[j].File {
			return violations[i].File < violations[j].File
		}
		if violations[i].Line != violations[j].Line {
			return violations[i].Line < violations[j].Line
		}
		return violations[i].ImportPath < violations[j].ImportPath
	})
	return violations, nil
}

func coreDomainFromImport(importPath, modulePath string) (string, bool) {
	prefix := modulePath + "/internal/core/"
	if !strings.HasPrefix(importPath, prefix) {
		return "", false
	}
	domain, _, _ := strings.Cut(strings.TrimPrefix(importPath, prefix), "/")
	return domain, domain != ""
}
