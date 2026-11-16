package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// applyFixes rewrites (or, in dry-run, prints) the fixable comments in results, and re-lexes changed
// files so later rules see the new text. Files with unstaged changes are skipped, so that git diff
// shows exactly what toller changed.
func applyFixes(results []fileComments, f *fixer, dryRun bool) (int, error) {
	dirty := map[string]bool{}
	if !dryRun {
		if out, err := exec.Command("git", "diff", "--name-only", "--relative").Output(); err == nil {
			for _, p := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				dirty[p] = true
			}
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	total := 0
	for i := range results {
		fc := &results[i]
		hasKey := false
		for _, c := range fc.comments {
			if f.key.MatchString(c.Text) {
				hasKey = true
				break
			}
		}
		if !hasKey {
			continue
		}
		src := readSource(fc.path)
		if src == nil {
			continue
		}
		sx := syntaxFor(fc.path)
		out, recs := f.fixAll(fc.path, src, sx, fc.comments)
		if len(recs) == 0 {
			continue
		}
		if dryRun {
			for _, r := range recs {
				if err := enc.Encode(r); err != nil {
					return total, err
				}
			}
			total += len(recs)
			continue
		}
		if dirty[fc.path] {
			fmt.Fprintf(os.Stderr, "toller: skipped %s (unstaged changes)\n", fc.path)
			continue
		}
		info, err := os.Stat(fc.path)
		if err != nil {
			return total, err
		}
		if err := os.WriteFile(fc.path, out, info.Mode().Perm()); err != nil {
			return total, err
		}
		fc.comments, _ = lex(out, sx)
		total += len(recs)
	}
	return total, nil
}

type jsonFinding struct {
	Line    int    `json:"line"`
	Rule    string `json:"rule"`
	Message string `json:"message"`
	Ref     string `json:"ref,omitempty"`
}

// jsonTask is one comment block with all its findings: the unit an agent should rewrite at once.
type jsonTask struct {
	Path       string        `json:"path"`
	BlockStart int           `json:"block_start"`
	BlockEnd   int           `json:"block_end"`
	Findings   []jsonFinding `json:"findings"`
	Block      string        `json:"block"`
	CodeAfter  string        `json:"code_after"`
}

const codeAfterLines = 4

// writeJSONL prints one task per comment block. Block is the exact file text of the whole block, so an
// agent can rewrite it with one exact string replacement; CodeAfter shows what the comment describes.
func writeJSONL(w io.Writer, findings []Finding, results []fileComments) {
	byPath := map[string][]Comment{}
	for _, fc := range results {
		byPath[fc.path] = fc.comments
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	var lines []string
	cur := ""
	var task *jsonTask
	flush := func() {
		if task != nil {
			enc.Encode(task)
			task = nil
		}
	}
	for _, f := range findings {
		if f.Path != cur {
			flush()
			cur = f.Path
			lines = strings.Split(string(readSource(f.Path)), "\n") // a CRLF line keeps its "\r", so Block matches the file byte for byte
		}
		jf := jsonFinding{Line: f.Line, Rule: f.Rule.ID, Message: f.Rule.Msg, Ref: f.Ref}
		if task != nil && f.Line <= task.BlockEnd {
			task.Findings = append(task.Findings, jf)
			continue
		}
		flush()
		commentLines := map[int]bool{}
		for _, c := range byPath[f.Path] {
			commentLines[c.Line] = true
		}
		start, end := f.Line, f.Line
		for commentLines[start-1] {
			start--
		}
		for commentLines[end+1] {
			end++
		}
		// Include a block's bare opener and closer lines ("/**", " */"), which hold no comment text.
		if start > 1 && isBareMarker(lines[start-2]) {
			start--
		}
		if end < len(lines) && isBareMarker(lines[end]) {
			end++
		}
		var code []string
		for l := end + 1; l <= len(lines) && len(code) < codeAfterLines; l++ {
			if strings.TrimSpace(lines[l-1]) != "" {
				code = append(code, strings.TrimRight(lines[l-1], "\r"))
			}
		}
		task = &jsonTask{Path: f.Path, BlockStart: start, BlockEnd: end, Findings: []jsonFinding{jf},
			Block: strings.Join(lines[start-1:min(end, len(lines))], "\n"), CodeAfter: strings.Join(code, "\n")}
	}
	flush()
}
func isBareMarker(l string) bool {
	switch strings.TrimSpace(l) {
	case "/*", "/**", "*/", "<!--", "-->":
		return true
	}
	return false
}
