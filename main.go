package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
)

// version is set at link time by release builds: -ldflags "-X main.version=1.2.3".
var version = ""

// toolVersion is the release version, or the module version for a "go install ...@vX.Y.Z" build.
func toolVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return strings.TrimPrefix(info.Main.Version, "v")
	}
	return "dev"
}

const (
	maxFileSize = 2 << 20
	configFile  = ".toller"
)

func main() {
	os.Exit(run())
}

type fileComments struct {
	path     string
	comments []Comment
	refsOK   bool // dangling-ref applies to this file's language
}

func run() int {
	staged := flag.Bool("staged", false, "check only lines added in the git index (for pre-commit)")
	diffBase := flag.String("diff", "", "check only lines added since this git ref, e.g. origin/main")
	prefixes := flag.String("prefixes", "", "comma-separated ticket prefixes to flag (e.g. PROJ,WEB); default: detected from git history")
	noAuto := flag.Bool("no-auto-prefixes", false, "do not detect prefixes from git; flag any KEY-123 except known standards")
	allow := flag.String("allow-prefix", "", "comma-separated extra prefixes that are not tickets")
	disable := flag.String("disable", "", "comma-separated rule IDs to turn off")
	refIndex := flag.String("ref-index", "", "comma-separated extra directories whose symbols count as known (another repo, node_modules)")
	fix := flag.Bool("fix", false, "remove ticket keys that can go without rewording; report the rest")
	dryRun := flag.Bool("dry-run", false, "with --fix, print each proposed change as JSON instead of writing files")
	format := flag.String("format", "text", "finding output: text, or jsonl (one finding per line, with the full comment block and the code after it)")
	quiet := flag.Bool("q", false, "print findings only, no summary")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: toller [flags] [paths...]\n\nRules: ticket-ref, tracker-url, changelog, narrator, placeholder, chat-leftover, dangling-ref\nSuppress one comment with %q.\n\n", ignoreMarker)
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nA %s file in the current directory holds default flags, one per line; flags on the command line win.\n", configFile)
	}
	args, err := configArgs(configFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "toller:", err)
		return 2
	}
	flag.CommandLine.Parse(append(args, os.Args[1:]...))
	if *showVersion {
		fmt.Println("toller", toolVersion())
		return 0
	}

	began := time.Now()
	paths := flag.Args()
	if len(paths) == 0 {
		paths = []string{"."}
	}

	only := csvSet(*prefixes, true)
	prefixSource := "--prefixes"
	if len(only) == 0 && !*noAuto {
		only = detectPrefixes()
		prefixSource = "git history"
	}

	allowed := csvSet(*allow, true)
	off := csvSet(*disable, false)
	var rules []*Rule
	for _, r := range append(defaultRules(only, allowed), trackerRule(originRepo())) {
		if !off[r.ID] {
			rules = append(rules, r)
		}
	}
	checkRefs := !off[danglingRule.ID]

	var diff *diffInfo
	if *staged || *diffBase != "" {
		var err error
		if diff, err = readDiff(*staged, *diffBase); err != nil {
			fmt.Fprintln(os.Stderr, "toller: git diff failed:", err)
			return 2
		}
	}

	scanFiles, err := listFiles(paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, "toller:", err)
		return 2
	}
	// The symbol index must cover the whole tree, even when only some paths are checked.
	allFiles := scanFiles
	if checkRefs && !(len(paths) == 1 && paths[0] == ".") {
		if allFiles, err = listFiles([]string{"."}); err != nil {
			fmt.Fprintln(os.Stderr, "toller:", err)
			return 2
		}
	}
	inScan := make(map[string]bool, len(scanFiles))
	for _, f := range scanFiles {
		inScan[f] = true
	}

	index := map[string]struct{}{}
	for _, f := range allFiles {
		// Vue and Nuxt refer to components by file name.
		stem, _, _ := strings.Cut(filepath.Base(f), ".")
		index[stem] = struct{}{}
	}

	var (
		mu      sync.Mutex
		results []fileComments
		wg      sync.WaitGroup
	)
	jobs := make(chan string)
	for w := 0; w < runtime.NumCPU(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				sx := syntaxFor(path)
				src := readSource(path)
				if src == nil {
					continue
				}
				comments, spans := lex(src, sx)
				var idents map[string]struct{}
				if checkRefs {
					idents = map[string]struct{}{}
					codeIdents(src, spans, idents)
				}
				mu.Lock()
				for id := range idents {
					index[id] = struct{}{}
				}
				if inScan[path] {
					results = append(results, fileComments{path, comments, sx == cLike || sx == sfcLike || sx == csLike})
				}
				mu.Unlock()
			}
		}()
	}
	if checkRefs {
		for _, dir := range strings.Split(*refIndex, ",") {
			if dir = strings.TrimSpace(dir); dir != "" {
				indexDir(dir, index)
			}
		}
	}
	for _, f := range allFiles {
		if syntaxFor(f) == nil {
			continue
		}
		// Without the index, files outside the diff need no work.
		if !checkRefs && (!inScan[f] || diff != nil && diff.added[f] == nil) {
			continue
		}
		jobs <- f
	}
	close(jobs)
	wg.Wait()

	// Without prefixes from git or flags, detection flags any `KEY-123`. --fix is stricter: it trusts only a
	// prefix that the comments use with minDistinctKeys different numbers.
	fixPrefixes := only
	var inComments []prefixCount
	if len(only) == 0 && !*noAuto {
		inComments = commentPrefixes(results, allowed)
		fixPrefixes = map[string]bool{}
		for _, pc := range inComments {
			if pc.numbers >= minDistinctKeys {
				fixPrefixes[pc.prefix] = true
			}
		}
	}

	if *fix {
		if len(fixPrefixes) == 0 {
			fmt.Fprintf(os.Stderr, "toller: --fix needs ticket prefixes, and none are in git history.%s\n", prefixHint(inComments))
			return 2
		}
		if len(only) == 0 && !*quiet {
			fmt.Fprintf(os.Stderr, "toller: --fix uses prefixes from comments: %s\n", strings.Join(sortedKeys(fixPrefixes), ", "))
		}
		fixed, err := applyFixes(results, newFixer(fixPrefixes), *dryRun)
		if err != nil {
			fmt.Fprintln(os.Stderr, "toller:", err)
			return 2
		}
		if *dryRun {
			if !*quiet {
				fmt.Fprintf(os.Stderr, "toller: %d line(s) would change\n", fixed)
			}
			return 0
		}
		if !*quiet {
			fmt.Fprintf(os.Stderr, "toller: fixed %d line(s)\n", fixed)
		}
	}

	// In diff mode, a symbol the diff removed and that no longer exists anywhere makes every comment
	// that names it stale, even a comment on a line the diff did not touch.
	deleted := map[string]bool{}
	if diff != nil {
		for id := range diff.removed {
			if _, ok := index[id]; !ok {
				deleted[id] = true
			}
		}
	}

	var findings []Finding
	for _, fc := range results {
		var added map[int]bool
		if diff != nil {
			added = diff.added[fc.path]
		}
		for _, f := range checkComments(fc.path, fc.comments, rules) {
			if diff == nil || added[f.Line] {
				findings = append(findings, f)
			}
		}
		if checkRefs && fc.refsOK {
			for _, f := range danglingRefs(fc.path, fc.comments, index) {
				if diff == nil || added[f.Line] || deleted[f.Ref] {
					findings = append(findings, f)
				}
			}
		}
	}

	sort.Slice(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Line < b.Line
	})
	fileSet := map[string]bool{}
	for _, f := range findings {
		fileSet[f.Path] = true
	}
	if *format == "jsonl" {
		writeJSONL(os.Stdout, findings, results)
	} else {
		for _, f := range findings {
			msg := f.Rule.Msg
			if f.Ref != "" {
				msg += ": " + f.Ref
			}
			fmt.Printf("%s:%d:%d: %s: %s\n    %s\n", f.Path, f.Line, f.Col, f.Rule.ID, msg, truncate(f.Text, 120))
		}
	}
	if !*quiet {
		if len(only) > 0 {
			fmt.Fprintf(os.Stderr, "\ntoller: ticket prefixes from %s: %s", prefixSource, strings.Join(sortedKeys(only), ", "))
		} else if !*noAuto {
			fmt.Fprintf(os.Stderr, "\ntoller: no ticket prefixes in git history, so any KEY-123 is flagged.%s", prefixHint(inComments))
		}
		fmt.Fprintf(os.Stderr, "\ntoller: %d finding(s) in %d file(s); scanned %d files in %s\n",
			len(findings), len(fileSet), len(results), time.Since(began).Round(time.Millisecond))
	}
	if len(findings) > 0 {
		return 1
	}
	return 0
}

// indexDir adds the code symbols under dir to index. Inside node_modules it reads only type declarations.
func indexDir(dir string, index map[string]struct{}) {
	var files []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git":
				return filepath.SkipDir
			case "bin", "obj", "dist", ".nuxt", ".output":
				// Build output, except in node_modules, where packages ship their types in dist.
				if !strings.Contains(p, "node_modules") {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if syntaxFor(p) == nil || strings.Contains(p, "node_modules") && !isDecl(p) {
			return nil
		}
		files = append(files, p)
		return nil
	})

	var mu sync.Mutex
	var wg sync.WaitGroup
	jobs := make(chan string)
	for w := 0; w < runtime.NumCPU(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := map[string]struct{}{}
			for p := range jobs {
				if src := readSource(p); src != nil {
					_, spans := lex(src, syntaxFor(p))
					codeIdents(src, spans, local)
				}
			}
			mu.Lock()
			for id := range local {
				index[id] = struct{}{}
			}
			mu.Unlock()
		}()
	}
	for _, f := range files {
		jobs <- f
	}
	close(jobs)
	wg.Wait()
}

func readSource(path string) []byte {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxFileSize {
		return nil
	}
	src, err := os.ReadFile(path)
	if err != nil || bytes.IndexByte(src[:min(len(src), 8000)], 0) >= 0 {
		return nil
	}
	return src
}

func csvSet(s string, upper bool) map[string]bool {
	m := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			if upper {
				p = strings.ToUpper(p)
			}
			m[p] = true
		}
	}
	return m
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func isDecl(p string) bool {
	return strings.HasSuffix(p, ".d.ts") || strings.HasSuffix(p, ".d.mts") || strings.HasSuffix(p, ".d.cts")
}

// configArgs reads default flags from path: one or more flags per line, "#" starts a comment.
// A missing file is not an error.
func configArgs(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var args []string
	for _, line := range strings.Split(string(data), "\n") {
		line, _, _ = strings.Cut(line, "#")
		args = append(args, strings.Fields(line)...)
	}
	return args, nil
}

type prefixCount struct {
	prefix  string
	numbers int
}

// commentPrefixes counts, for each KEY prefix in the comments, how many different numbers it has.
func commentPrefixes(results []fileComments, allowed map[string]bool) []prefixCount {
	ticket := defaultRules(nil, allowed)[0]
	nums := map[string]map[string]bool{}
	for _, fc := range results {
		for _, c := range fc.comments {
			for _, loc := range ticket.Re.FindAllStringSubmatchIndex(c.Text, -1) {
				if !ticket.Accept(c.Text, loc) {
					continue
				}
				p := c.Text[loc[2]:loc[3]]
				if nums[p] == nil {
					nums[p] = map[string]bool{}
				}
				nums[p][c.Text[loc[3]+1:loc[1]]] = true
			}
		}
	}
	var out []prefixCount
	for p, n := range nums {
		out = append(out, prefixCount{p, len(n)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].numbers != out[j].numbers {
			return out[i].numbers > out[j].numbers
		}
		return out[i].prefix < out[j].prefix
	})
	return out
}

// prefixHint tells the user which prefixes the comments use and how to choose them.
func prefixHint(counts []prefixCount) string {
	if len(counts) == 0 {
		return ""
	}
	var parts, names []string
	for i, pc := range counts {
		if i == 8 {
			parts = append(parts, "…")
			break
		}
		parts = append(parts, fmt.Sprintf("%s (%d)", pc.prefix, pc.numbers))
		names = append(names, pc.prefix)
	}
	return fmt.Sprintf("\n  Comments use: %s (different numbers per prefix).\n  To choose, pass --prefixes %s or put that line in %s.",
		strings.Join(parts, ", "), strings.Join(names, ","), configFile)
}
