package main

import (
	"bytes"
	"crypto/sha1"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type FuncInfo struct {
	Package   string
	File      string
	Name      string
	Receiver  string
	Signature string
	Exported  bool
	StartLine int
	EndLine   int
	LineCount int
}

type FuncKey struct {
	Package  string
	Receiver string
	Name     string
}

type FuncSet map[FuncKey]*FuncInfo

type PackageStats struct {
	New     int
	Removed int
	Changed int
}

func main() {
	dirFlag := flag.String("dir", "", "Path to the git repository (optional). If empty, use current working directory.")
	fromRef := flag.String("from", "development", "Git ref to compare from (e.g. branch, tag, commit)")
	toRef := flag.String("to", "master", "Git ref to compare to (e.g. branch, tag, commit)")
	onlyExported := flag.Bool("only-exported", false, "Include only exported (public) functions and methods")
	summaryOnly := flag.Bool("summary-only", false, "Show only summary and package-level stats (no detailed function lists)")
	pkgFilter := flag.String("package", "", "Optional substring filter for package path (e.g. 'internal/' or 'pkg/foo')")
	outDir := flag.String("out-dir", "", "If set, write each changed function report as its own Markdown file in this directory")
	flag.Parse()

	// If --dir is provided, change working directory first
	if *dirFlag != "" {
		if err := os.Chdir(*dirFlag); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to change directory to %s: %v\n", *dirFlag, err)
			os.Exit(1)
		}
	}

	repoRoot, err := gitRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fromFuncs, err := collectFuncs(*fromRef, repoRoot, *onlyExported, *pkgFilter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error collecting functions from %s: %v\n", *fromRef, err)
		os.Exit(1)
	}

	toFuncs, err := collectFuncs(*toRef, repoRoot, *onlyExported, *pkgFilter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error collecting functions from %s: %v\n", *toRef, err)
		os.Exit(1)
	}

	report := buildMarkdownReport(*fromRef, *toRef, fromFuncs, toFuncs, *summaryOnly, *outDir)
	fmt.Println(report)
}

// gitRoot returns the root directory of the git repo.
func gitRoot() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository or git not available: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// gitListGoFiles lists all .go files for a given ref.
func gitListGoFiles(ref string) ([]string, error) {
	cmd := exec.Command("git", "ls-tree", "-r", "--name-only", ref)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-tree failed for ref %s: %w", ref, err)
	}

	lines := strings.Split(string(out), "\n")
	var files []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if strings.HasSuffix(l, ".go") && !strings.HasSuffix(l, "_test.go") {
			files = append(files, l)
		}
	}
	return files, nil
}

// gitShowFile returns the contents of file at ref:path.
func gitShowFile(ref, path string) ([]byte, error) {
	spec := fmt.Sprintf("%s:%s", ref, path)
	cmd := exec.Command("git", "show", spec)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git show failed for %s: %w", spec, err)
	}
	return out, nil
}

// collectFuncs parses Go files from a ref and builds a FuncSet.
func collectFuncs(ref, repoRoot string, onlyExported bool, pkgFilter string) (FuncSet, error) {
	files, err := gitListGoFiles(ref)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	funcs := make(FuncSet)

	for _, path := range files {
		src, err := gitShowFile(ref, path)
		if err != nil {
			// If a single file fails (e.g. deleted or binary), log and continue.
			fmt.Fprintf(os.Stderr, "Warning: skipping %s@%s: %v\n", path, ref, err)
			continue
		}

		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: parsing failed for %s@%s: %v\n", path, ref, err)
			continue
		}

		pkgName := file.Name.Name
		// Derive a pseudo package path from directory + package name.
		dir := filepath.Dir(path)
		// Make it relative style: ./dir/pkg
		var pkgPath string
		if dir == "." {
			pkgPath = pkgName
		} else {
			pkgPath = filepath.ToSlash(filepath.Join(dir, pkgName))
		}

		if pkgFilter != "" && !strings.Contains(pkgPath, pkgFilter) {
			continue
		}

		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok {
				return true
			}

			name := fn.Name.Name
			if onlyExported && !fn.Name.IsExported() {
				return true
			}

			receiver := formatReceiver(fn.Recv)
			exported := fn.Name.IsExported()
			signature := formatSignature(fn.Type)

			pos := fset.Position(fn.Pos())
			end := fset.Position(fn.End())
			startLine := pos.Line
			endLine := end.Line
			lineCount := endLine - startLine + 1
			if lineCount < 0 {
				lineCount = 0
			}

			info := &FuncInfo{
				Package:   pkgPath,
				File:      path,
				Name:      name,
				Receiver:  receiver,
				Signature: signature,
				Exported:  exported,
				StartLine: startLine,
				EndLine:   endLine,
				LineCount: lineCount,
			}

			key := FuncKey{
				Package:  pkgPath,
				Receiver: receiver,
				Name:     name,
			}
			funcs[key] = info

			return true
		})
	}

	return funcs, nil
}

func formatReceiver(fl *ast.FieldList) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}
	// methods have at most one receiver field.
	field := fl.List[0]
	var buf bytes.Buffer
	switch t := field.Type.(type) {
	case *ast.Ident:
		buf.WriteString(t.Name)
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			buf.WriteString("*" + id.Name)
		}
	default:
		// fallback to source slice (less pretty but OK)
		buf.WriteString(exprToString(field.Type))
	}
	return buf.String()
}

func formatSignature(ft *ast.FuncType) string {
	params := fieldListToString(ft.Params)
	results := fieldListToString(ft.Results)

	if results == "" {
		return fmt.Sprintf("(%s)", params)
	}
	return fmt.Sprintf("(%s) (%s)", params, results)
}

func fieldListToString(fl *ast.FieldList) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}
	var parts []string
	for _, f := range fl.List {
		typeStr := exprToString(f.Type)
		if len(f.Names) == 0 {
			parts = append(parts, typeStr)
		} else {
			for _, name := range f.Names {
				parts = append(parts, fmt.Sprintf("%s %s", name.Name, typeStr))
			}
		}
	}
	return strings.Join(parts, ", ")
}

// exprToString is a simple printer for AST expressions.
func exprToString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name

	case *ast.StarExpr:
		return "*" + exprToString(x.X)

	case *ast.SelectorExpr:
		return exprToString(x.X) + "." + exprToString(x.Sel)

	case *ast.ArrayType:
		return "[]" + exprToString(x.Elt)

	case *ast.MapType:
		return "map[" + exprToString(x.Key) + "]" + exprToString(x.Value)

	case *ast.FuncType:
		// Print as: func(params) (results)
		return "func" + formatSignature(x)

	case *ast.InterfaceType:
		// For now, just "interface{}"
		return "interface{}"

	case *ast.ChanType:
		// Very simple: "chan <T>"
		return "chan " + exprToString(x.Value)

	default:
		// Fallback: we don't know how to pretty-print this AST node;
		// return a generic placeholder so code still compiles and runs.
		return "<?>"
	}
}

type DiffResult struct {
	NewFuncs     []*FuncInfo
	RemovedFuncs []*FuncInfo
	ChangedFuncs [][2]*FuncInfo // [from, to]
	FromTotal    int
	ToTotal      int
	PkgStats     map[string]*PackageStats
}

func diffFuncs(from, to FuncSet) DiffResult {
	result := DiffResult{
		PkgStats: make(map[string]*PackageStats),
	}

	result.FromTotal = len(from)
	result.ToTotal = len(to)

	// Helper to get or create stats for a package.
	getStats := func(pkg string) *PackageStats {
		if s, ok := result.PkgStats[pkg]; ok {
			return s
		}
		s := &PackageStats{}
		result.PkgStats[pkg] = s
		return s
	}

	// Identify new and changed
	for key, fromInfo := range from {
		toInfo, exists := to[key]
		if !exists {
			result.NewFuncs = append(result.NewFuncs, fromInfo)
			getStats(fromInfo.Package).New++
			continue
		}

		// Check if signature or file/lines differ:
		if fromInfo.Signature != toInfo.Signature ||
			fromInfo.File != toInfo.File ||
			fromInfo.StartLine != toInfo.StartLine ||
			fromInfo.EndLine != toInfo.EndLine {
			result.ChangedFuncs = append(result.ChangedFuncs, [2]*FuncInfo{fromInfo, toInfo})
			getStats(fromInfo.Package).Changed++
		}
	}

	// Identify removed
	for key, toInfo := range to {
		if _, exists := from[key]; !exists {
			result.RemovedFuncs = append(result.RemovedFuncs, toInfo)
			getStats(toInfo.Package).Removed++
		}
	}

	return result
}

func buildMarkdownReport(fromRef, toRef string, fromFuncs, toFuncs FuncSet, summaryOnly bool, outDir string) string {
	diff := diffFuncs(fromFuncs, toFuncs)

	var b strings.Builder

	// Header
	fmt.Fprintf(&b, "### Function Diff: `%s` → `%s`\n\n", fromRef, toRef)

	// Summary
	fmt.Fprintf(&b, "#### Summary\n")
	fmt.Fprintf(&b, "- Total functions in `%s`: %d\n", fromRef, diff.FromTotal)
	fmt.Fprintf(&b, "- Total functions in `%s`: %d\n", toRef, diff.ToTotal)
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "- New functions in `%s` only: %d\n", fromRef, len(diff.NewFuncs))
	fmt.Fprintf(&b, "- Removed functions (only in `%s`): %d\n", toRef, len(diff.RemovedFuncs))
	fmt.Fprintf(&b, "- Changed functions: %d\n\n", len(diff.ChangedFuncs))

	// High-level changes by package
	fmt.Fprintf(&b, "#### High-Level Changes by Package\n\n")
	fmt.Fprintf(&b, "| Package | New | Removed | Changed |\n")
	fmt.Fprintf(&b, "|---------|-----|---------|---------|\n")

	pkgs := make([]string, 0, len(diff.PkgStats))
	for pkg := range diff.PkgStats {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)

	for _, pkg := range pkgs {
		stats := diff.PkgStats[pkg]
		fmt.Fprintf(&b, "| `%s` | %d | %d | %d |\n", pkg, stats.New, stats.Removed, stats.Changed)
	}
	fmt.Fprintf(&b, "\n")

	if summaryOnly {
		if outDir != "" {
			files := writeAllChangedFuncFiles(outDir, fromRef, toRef, diff.ChangedFuncs)
			addChangedFilesIndex(&b, outDir, files)
		}
		return b.String()
	}

	// New functions section
	fmt.Fprintf(&b, "#### New Functions in `%s` (not in `%s`)\n\n", fromRef, toRef)
	if len(diff.NewFuncs) == 0 {
		fmt.Fprintf(&b, "_None_\n\n")
	} else {
		printFuncListByPackage(&b, diff.NewFuncs)
	}

	// Removed functions section
	fmt.Fprintf(&b, "#### Removed Functions (only in `%s`)\n\n", toRef)
	if len(diff.RemovedFuncs) == 0 {
		fmt.Fprintf(&b, "_None_\n\n")
	} else {
		printFuncListByPackage(&b, diff.RemovedFuncs)
	}

	// Changed functions – only an index in the main report; details go to files
	fmt.Fprintf(&b, "#### Changed Functions\n\n")
	if len(diff.ChangedFuncs) == 0 {
		fmt.Fprintf(&b, "_None_\n\n")
	} else {
		if outDir != "" {
			files := writeAllChangedFuncFiles(outDir, fromRef, toRef, diff.ChangedFuncs)
			addChangedFilesIndex(&b, outDir, files)
		} else {
			// If no outDir, we can at least list the names
			for _, pair := range diff.ChangedFuncs {
				fi := pair[0]
				name := fi.Name
				if fi.Receiver != "" {
					name = fmt.Sprintf("(%s).%s", fi.Receiver, fi.Name)
				}
				fmt.Fprintf(&b, "- `%s`: `%s`\n", fi.File, name)
			}
			fmt.Fprintf(&b, "\n")
		}
	}

	return b.String()
}

func printFuncListByPackage(b *strings.Builder, funcs []*FuncInfo) {
	// group by package
	pkgMap := make(map[string][]*FuncInfo)
	for _, f := range funcs {
		pkgMap[f.Package] = append(pkgMap[f.Package], f)
	}

	// sort packages
	pkgs := make([]string, 0, len(pkgMap))
	for pkg := range pkgMap {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)

	for _, pkg := range pkgs {
		fmt.Fprintf(b, "- `%s`\n", pkg)
		list := pkgMap[pkg]

		// sort by receiver + name
		sort.Slice(list, func(i, j int) bool {
			if list[i].Receiver == list[j].Receiver {
				return list[i].Name < list[j].Name
			}
			return list[i].Receiver < list[j].Receiver
		})

		for _, f := range list {
			fullName := f.Name
			if f.Receiver != "" {
				fullName = fmt.Sprintf("(%s).%s", f.Receiver, f.Name)
			}
			fmt.Fprintf(b, "  - `%s`\n", fullName)
			fmt.Fprintf(b, "    - signature: `%s`\n", f.Signature)
			fmt.Fprintf(b, "    - file: `%s` (lines %d–%d, %d LOC)\n",
				f.File, f.StartLine, f.EndLine, f.LineCount)
		}
		fmt.Fprintf(b, "\n")
	}
}

func formatFuncHeader(info *FuncInfo) string {
	recvPart := ""
	if info.Receiver != "" {
		recvPart = fmt.Sprintf("(%s) ", info.Receiver)
	}
	// Signature already holds "(params)" or "(params) (results)"
	return fmt.Sprintf("func %s%s%s", recvPart, info.Name, info.Signature)
}

// extractLines returns the text of lines [startLine, endLine] (1-based, inclusive).
func extractLines(src []byte, startLine, endLine int) string {
	lines := strings.Split(string(src), "\n")
	if len(lines) == 0 {
		return ""
	}

	if startLine < 1 {
		startLine = 1
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	if startLine > endLine {
		return ""
	}

	// slice is 0-based, startLine/endLine are 1-based
	return strings.Join(lines[startLine-1:endLine], "\n")
}

// sanitizeFilenamePart ensures we don't accidentally create weird filenames.
// For now we just replace spaces with "_" and remove backticks.
func sanitizeFilenamePart(s string) string {
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "`", "")
	return s
}

// writeChangedFuncReport writes a separate markdown file describing a single changed function.
func writeChangedFuncReport(
	fromRef, toRef string,
	fromInfo, toInfo *FuncInfo,
) (string, error) {
	// File name: <relative-path>__<func-name>.md
	relPath := sanitizeFilenamePart(fromInfo.File)
	funcName := sanitizeFilenamePart(fromInfo.Name)
	fileName := fmt.Sprintf("%s__%s.md", relPath, funcName)

	// Make sure directory exists (for nested paths)
	dir := filepath.Dir(fileName)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	var b strings.Builder


	fmt.Fprintf(&b, "### Changed Function: %s — %s\n\n", fromInfo.File, fromInfo.Name)
	fmt.Fprintf(&b, "- Package: `%s`\n", fromInfo.Package)
	if fromInfo.Receiver != "" {
		fmt.Fprintf(&b, "- Receiver: `%s`\n", fromInfo.Receiver)
	}
	fmt.Fprintf(&b, "- Function: `%s`\n", fromInfo.Name)
	fmt.Fprintf(&b, "- Changed between: `%s` → `%s`\n\n", fromRef, toRef)

	// FROM side (e.g. development)
	fmt.Fprintf(&b, "#### %s (`%s`)\n\n", fromRef, fromInfo.File)

	fmt.Fprintf(&b, "```go\n")
	fmt.Fprintf(&b, "%s\n", formatFuncHeader(fromInfo))
	fmt.Fprintf(&b, "```\n\n")
	fmt.Fprintf(&b, "- lines: %d–%d (%d LOC)\n\n", fromInfo.StartLine, fromInfo.EndLine, fromInfo.LineCount)

	if src, err := gitShowFile(fromRef, fromInfo.File); err == nil {
		body := extractLines(src, fromInfo.StartLine, fromInfo.EndLine)
		if strings.TrimSpace(body) != "" {
			fmt.Fprintf(&b, "```go\n")
			fmt.Fprintf(&b, "%s\n", body)
			fmt.Fprintf(&b, "```\n\n")
		} else {
			fmt.Fprintf(&b, "_No body content found for this function in `%s`_\n\n", fromRef)
		}
	} else {
		fmt.Fprintf(&b, "_Could not load function body from `%s` @ `%s`: %v_\n\n", fromInfo.File, fromRef, err)
	}

	// TO side (e.g. master)
	fmt.Fprintf(&b, "#### %s (`%s`)\n\n", toRef, toInfo.File)

	fmt.Fprintf(&b, "```go\n")
	fmt.Fprintf(&b, "%s\n", formatFuncHeader(toInfo))
	fmt.Fprintf(&b, "```\n\n")
	fmt.Fprintf(&b, "- lines: %d–%d (%d LOC)\n\n", toInfo.StartLine, toInfo.EndLine, toInfo.LineCount)

	if src, err := gitShowFile(toRef, toInfo.File); err == nil {
		body := extractLines(src, toInfo.StartLine, toInfo.EndLine)
		if strings.TrimSpace(body) != "" {
			fmt.Fprintf(&b, "```go\n")
			fmt.Fprintf(&b, "%s\n", body)
			fmt.Fprintf(&b, "```\n\n")
		} else {
			fmt.Fprintf(&b, "_No body content found for this function in `%s`_\n\n", toRef)
		}
	} else {
		fmt.Fprintf(&b, "_Could not load function body from `%s` @ `%s`: %v_\n\n", toInfo.File, toRef, err)
	}

	// Write to disk
	if err := os.WriteFile(fileName, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("failed to write file %s: %w", fileName, err)
	}

	return fileName, nil
}


func writeChangedFuncFile(outDir, fromRef, toRef string, fromInfo, toInfo *FuncInfo) (string, error) {
	if outDir == "" {
		return "", nil
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("create out dir: %w", err)
	}

	// Load full file contents to extract bodies
	var fromBody, toBody string

	if src, err := gitShowFile(fromRef, fromInfo.File); err == nil {
		fromBody = extractLines(src, fromInfo.StartLine, fromInfo.EndLine)
	}
	if src, err := gitShowFile(toRef, toInfo.File); err == nil {
		toBody = extractLines(src, toInfo.StartLine, toInfo.EndLine)
	}

	nf := normalizeBody(fromBody)
	nt := normalizeBody(toBody)
	isIdenticalBody := nf != "" && nf == nt

	// Build base filename (no prefix yet)
	baseName := changedFuncFilenameWithRecv(fromInfo)

	// If bodies are identical, prefix the filename
	if isIdenticalBody {
		baseName = "identical_" + baseName
	}

	// Header and content
	var b strings.Builder
	fullName := fromInfo.Name
	if fromInfo.Receiver != "" {
		fullName = fmt.Sprintf("(%s).%s", fromInfo.Receiver, fromInfo.Name)
	}
	fmt.Fprintf(&b, "### %s — `%s`\n\n", fullName, fromInfo.File)

	// From side
	fmt.Fprintf(&b, "#### %s\n\n", fromRef)
	fmt.Fprintf(&b, "```go\n%s\n```\n", formatFuncHeader(fromInfo))
	fmt.Fprintf(&b, "- file: `%s`\n", fromInfo.File)
	fmt.Fprintf(&b, "- lines: %d–%d (%d LOC)\n\n", fromInfo.StartLine, fromInfo.EndLine, fromInfo.LineCount)
	if strings.TrimSpace(fromBody) != "" {
		fmt.Fprintf(&b, "```go\n%s\n```\n\n", fromBody)
	} else {
		fmt.Fprintf(&b, "_function body unavailable_\n\n")
	}

	// To side
	fmt.Fprintf(&b, "#### %s\n\n", toRef)
	fmt.Fprintf(&b, "```go\n%s\n```\n", formatFuncHeader(toInfo))
	fmt.Fprintf(&b, "- file: `%s`\n", toInfo.File)
	fmt.Fprintf(&b, "- lines: %d–%d (%d LOC)\n\n", toInfo.StartLine, toInfo.EndLine, toInfo.LineCount)
	if strings.TrimSpace(toBody) != "" {
		fmt.Fprintf(&b, "```go\n%s\n```\n\n", toBody)
	} else {
		fmt.Fprintf(&b, "_function body unavailable_\n\n")
	}

	// Signature change note
	if fromInfo.Signature != toInfo.Signature {
		fmt.Fprintf(&b, "#### Signature Change\n\n")
		fmt.Fprintf(&b, "- %s: `%s`\n", fromRef, fromInfo.Signature)
		fmt.Fprintf(&b, "- %s: `%s`\n\n", toRef, toInfo.Signature)
	}

	// Body identical note
	if isIdenticalBody {
		fmt.Fprintf(&b, "> Note: function bodies are identical between `%s` and `%s`.\n\n", fromRef, toRef)
	}

	// Optional hash
	h := sha1.Sum([]byte(b.String()))
	fmt.Fprintf(&b, "_report hash: %x_\n", h[:6])

	// Final path
	path := filepath.Join(outDir, baseName)
	if err := ioutil.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return baseName, nil
}

func changedFuncFilenameWithRecv(info *FuncInfo) string {
	safePath := strings.ReplaceAll(strings.ReplaceAll(info.File, "/", "_"), "\\", "_")
	recv := info.Receiver
	if recv != "" {
		recv = strings.ReplaceAll(recv, "*", "ptr")
		return fmt.Sprintf("%s__%s__%s.md", safePath, recv, info.Name)
	}
	return fmt.Sprintf("%s__%s.md", safePath, info.Name)
}

func writeAllChangedFuncFiles(outDir, fromRef, toRef string, changed [][2]*FuncInfo) []string {
	if outDir == "" {
		return nil
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to create out dir %s: %v\n", outDir, err)
		return nil
	}

	var files []string
	for _, pair := range changed {
		fromInfo := pair[0]
		toInfo := pair[1]
		name, err := writeChangedFuncFile(outDir, fromRef, toRef, fromInfo, toInfo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to write changed function file: %v\n", err)
			continue
		}
		if name != "" {
			files = append(files, name)
		}
	}
	return files
}

func addChangedFilesIndex(b *strings.Builder, outDir string, files []string) {
	if outDir == "" || len(files) == 0 {
		return
	}
	fmt.Fprintf(b, "Per-function reports (Markdown files) written to `%s`:\n\n", outDir)
	sort.Strings(files)
	for _, f := range files {
		fmt.Fprintf(b, "- `%s/%s`\n", outDir, f)
	}
	fmt.Fprintf(b, "\n")
}

func normalizeBody(s string) string {
	// Normalize line endings to LF
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")

	// Split into lines
	lines := strings.Split(s, "\n")

	// Trim trailing spaces/tabs on each line
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}

	// Drop leading/trailing completely empty lines
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}

	return strings.Join(lines, "\n")
}