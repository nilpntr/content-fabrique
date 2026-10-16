// Package envlint fails when code reads an environment variable that .env.example does not list.
//
// It scans, from the repository root:
//   - apps/server/**/*.go: a string literal passed to os.Getenv, os.LookupEnv, or any function or method named
//     getenv or Getenv (internal/config takes its lookup function under that name). Test files are skipped.
//   - apps/web/src/**/*.{ts,tsx}: import.meta.env.NAME and process.env.NAME, also with a ["NAME"] subscript.
//
// Names the platform sets, rather than us, are on the allowlist below. Names that are built at run time are not
// seen. Keep the literal in a getenv call so this check can see it.
package envlint

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Allowlist holds variables that code may read without .env.example listing them, because the platform sets them.
// Each entry says who sets it. Add to this list only for platform-set names, never for our own configuration.
var Allowlist = map[string]string{
	// Vite sets these on import.meta.env in every build.
	"MODE":     "Vite built-in",
	"DEV":      "Vite built-in",
	"PROD":     "Vite built-in",
	"SSR":      "Vite built-in",
	"BASE_URL": "Vite built-in",
	// Node and CI runners.
	"NODE_ENV": "set by Node tooling",
	"CI":       "set by GitHub Actions and most CI",
}

// Finding is one read of an unlisted variable.
type Finding struct {
	File string // relative to the root, slash-separated
	Line int
	Name string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: %s is read but not listed in .env.example", f.File, f.Line, f.Name)
}

var (
	// import.meta.env.NAME and process.env.NAME.
	webDotRE = regexp.MustCompile(`(?:import\.meta\.env|process\.env)\.([A-Za-z_][A-Za-z0-9_]*)`)
	// import.meta.env["NAME"] and process.env['NAME'].
	webIndexRE = regexp.MustCompile(`(?:import\.meta\.env|process\.env)\[\s*["']([A-Za-z_][A-Za-z0-9_]*)["']\s*\]`)
)

// Lint scans the code under root and returns every read of a name that is neither in known nor on the Allowlist.
// Findings are sorted by file and line.
func Lint(root string, known map[string]bool) ([]Finding, error) {
	var out []Finding
	report := func(file string, line int, name string) {
		if known[name] {
			return
		}
		if _, ok := Allowlist[name]; ok {
			return
		}
		out = append(out, Finding{File: file, Line: line, Name: name})
	}

	if err := walk(filepath.Join(root, "apps", "server"), root, ".go", func(rel, path string) error {
		return scanGo(rel, path, report)
	}); err != nil {
		return nil, err
	}
	if err := walk(filepath.Join(root, "apps", "web", "src"), root, ".ts", func(rel, path string) error {
		return scanWeb(rel, path, report)
	}); err != nil {
		return nil, err
	}

	slices.SortFunc(out, func(a, b Finding) int {
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		return a.Line - b.Line
	})
	return out, nil
}

// walk calls fn for each source file under dir. ext ".ts" also matches ".tsx". A missing dir is not an error.
func walk(dir, root, ext string, fn func(rel, path string) error) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "testdata", "vendor", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, "_test.go") {
			return nil
		}
		isSource := strings.HasSuffix(name, ext) || (ext == ".ts" && strings.HasSuffix(name, ".tsx"))
		if !isSource {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return fn(filepath.ToSlash(rel), path)
	})
}

func scanGo(rel, path string, report func(file string, line int, name string)) error {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("envlint: parse %s: %w", rel, err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		if !isEnvReader(call.Fun) {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if name, err := strconv.Unquote(lit.Value); err == nil {
			report(rel, fset.Position(lit.Pos()).Line, name)
		}
		return true
	})
	return nil
}

// isEnvReader reports whether fun is os.Getenv, os.LookupEnv, or something named getenv or Getenv.
func isEnvReader(fun ast.Expr) bool {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name == "getenv" || f.Name == "Getenv"
	case *ast.SelectorExpr:
		if x, ok := f.X.(*ast.Ident); ok && x.Name == "os" {
			return f.Sel.Name == "Getenv" || f.Sel.Name == "LookupEnv"
		}
		return f.Sel.Name == "getenv" || f.Sel.Name == "Getenv"
	}
	return false
}

func scanWeb(rel, path string, report func(file string, line int, name string)) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("envlint: read %s: %w", rel, err)
	}
	for i, line := range strings.Split(string(data), "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*") {
			continue
		}
		for _, re := range []*regexp.Regexp{webDotRE, webIndexRE} {
			for _, m := range re.FindAllStringSubmatch(line, -1) {
				report(rel, i+1, m[1])
			}
		}
	}
	return nil
}
