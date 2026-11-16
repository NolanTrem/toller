package main

import (
	"bytes"
	"path/filepath"
	"strings"
)

// Comment is one line of comment text with the position where the text starts.
type Comment struct {
	Line int
	Col  int
	Off  int // byte offset of Text in the source
	Text string
}

type syntax struct {
	line   []string
	block  [][2]string
	quotes string
	// '#' only opens a comment at line start or after whitespace (shell, YAML).
	hashNeedsSpace bool
	// JS-style /regex/ literals, so "/*" or "//" inside one does not open a comment.
	regexLiterals bool
	// Python and TOML strings in triple quotes, which can span lines.
	tripleQuotes bool
	// A triple-quoted string in statement position is a Python docstring: documentation, checked like a comment.
	docstrings bool
	// C# @"verbatim" strings (where "" is a quote) and """raw""" strings, which can span lines.
	verbatim bool
}

var (
	cLike   = &syntax{line: []string{"//"}, block: [][2]string{{"/*", "*/"}}, quotes: "\"'`", regexLiterals: true}
	csLike  = &syntax{line: []string{"//"}, block: [][2]string{{"/*", "*/"}}, quotes: "\"'", verbatim: true}
	pyLike  = &syntax{line: []string{"#"}, quotes: "\"'", hashNeedsSpace: true, tripleQuotes: true, docstrings: true}
	tomlLik = &syntax{line: []string{"#"}, quotes: "\"'", hashNeedsSpace: true, tripleQuotes: true}
	rustLik = &syntax{line: []string{"//"}, block: [][2]string{{"/*", "*/"}}, quotes: "\""}
	cssLike = &syntax{block: [][2]string{{"/*", "*/"}}, quotes: "\"'"}
	hashLik = &syntax{line: []string{"#"}, quotes: "\"'", hashNeedsSpace: true}
	sqlLike = &syntax{line: []string{"--"}, block: [][2]string{{"/*", "*/"}}, quotes: "'\""}
	htmlLik = &syntax{block: [][2]string{{"<!--", "-->"}}}
	sfcLike = &syntax{line: []string{"//"}, block: [][2]string{{"<!--", "-->"}, {"/*", "*/"}}, quotes: "\"'`", regexLiterals: true}
)

var syntaxByExt = map[string]*syntax{}

func init() {
	reg := func(sx *syntax, exts ...string) {
		for _, e := range exts {
			syntaxByExt[e] = sx
		}
	}
	reg(cLike, ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts", ".go", ".java", ".kt", ".kts",
		".swift", ".c", ".h", ".cc", ".cpp", ".hpp", ".scss", ".less", ".php", ".dart", ".scala", ".proto")
	reg(rustLik, ".rs")
	reg(cssLike, ".css")
	reg(csLike, ".cs")
	reg(pyLike, ".py", ".pyi")
	reg(tomlLik, ".toml")
	reg(hashLik, ".rb", ".sh", ".bash", ".zsh", ".yml", ".yaml", ".r", ".pl", ".tf", ".ex", ".exs")
	reg(sqlLike, ".sql")
	reg(htmlLik, ".html", ".htm", ".xml")
	reg(sfcLike, ".vue", ".svelte", ".astro")
}

func syntaxFor(path string) *syntax {
	base := filepath.Base(path)
	switch {
	case base == "Dockerfile" || strings.HasPrefix(base, "Dockerfile.") || base == "Makefile":
		return hashLik
	case strings.Contains(base, ".min."):
		return nil
	}
	return syntaxByExt[strings.ToLower(filepath.Ext(base))]
}

// extractComments returns every comment line in src. Block comments yield one entry per line.
func extractComments(src []byte, sx *syntax) []Comment {
	comments, _ := lex(src, sx)
	return comments
}

// lex also returns the byte span of each whole comment, markers included, in ascending order.
func lex(src []byte, sx *syntax) ([]Comment, [][2]int) {
	var out []Comment
	var spans [][2]int
	n := len(src)
	line, lineStart, i := 1, 0, 0
	var lastSig byte // last non-space code byte, to tell a regex literal from division

	emit := func(start, end, ln, ls int) {
		raw := string(src[start:end])
		t := cleanComment(raw)
		if t == "" {
			return
		}
		off := start + strings.Index(raw, t)
		out = append(out, Comment{Line: ln, Col: off - ls + 1, Off: off, Text: t})
	}

outer:
	for i < n {
		c := src[i]
		if c == '\n' {
			line++
			i++
			lineStart = i
			continue
		}

		if sx.regexLiterals && c == '/' && i+1 < n && src[i+1] != '/' && src[i+1] != '*' &&
			(lastSig == 0 || strings.IndexByte("(,=:[!&|?{};+-*%<>~^", lastSig) >= 0) {
			i = skipRegex(src, i)
			lastSig = '/'
			continue
		}

		if sx.tripleQuotes && (c == '"' || c == '\'') && bytes.HasPrefix(src[i:], []byte{c, c, c}) {
			// A docstring starts its own line, in statement position: not after "(", ",", "=" and similar.
			doc := sx.docstrings && strings.Trim(string(src[lineStart:i]), " \trRuUbBfF") == "" &&
				!strings.ContainsRune("(,=[{+-*/%<>|&\\", rune(lastSig))
			close := []byte{c, c, c}
			open := i
			i += 3
			seg := i
			for i < n {
				switch {
				case bytes.HasPrefix(src[i:], close):
					if doc {
						emit(seg, i, line, lineStart)
						spans = append(spans, [2]int{open, i + 3})
					}
					i += 3
					lastSig = c
					continue outer
				case src[i] == '\\' && i+1 < n && src[i+1] != '\n':
					i += 2
				case src[i] == '\n':
					if doc {
						emit(seg, i, line, lineStart)
					}
					line++
					i++
					lineStart, seg = i, i
				default:
					i++
				}
			}
			if doc {
				emit(seg, n, line, lineStart)
				spans = append(spans, [2]int{open, n})
			}
			continue
		}

		if sx.verbatim && c == '"' && bytes.HasPrefix(src[i:], []byte(`"""`)) {
			// C# raw string: it closes with as many quotes as it opened with.
			k := 0
			for i+k < n && src[i+k] == '"' {
				k++
			}
			close := bytes.Repeat([]byte{'"'}, k)
			for i += k; i < n && !bytes.HasPrefix(src[i:], close); i++ {
				if src[i] == '\n' {
					line++
					lineStart = i + 1
				}
			}
			i += k
			lastSig = '"'
			continue
		}

		if sx.verbatim && (c == '@' || c == '$') {
			// C# verbatim string: @"...", $@"..." or @$"...". A backslash is plain text; "" is a quote.
			start := 0
			switch {
			case c == '@' && i+1 < n && src[i+1] == '"':
				start = i + 2
			case i+2 < n && (c == '@' && src[i+1] == '$' || c == '$' && src[i+1] == '@') && src[i+2] == '"':
				start = i + 3
			}
			if start > 0 {
				for i = start; i < n; i++ {
					if src[i] == '"' {
						if i+1 < n && src[i+1] == '"' {
							i++
							continue
						}
						break
					}
					if src[i] == '\n' {
						line++
						lineStart = i + 1
					}
				}
				i++
				lastSig = '"'
				continue
			}
		}

		if strings.IndexByte(sx.quotes, c) >= 0 {
			lastSig = c
			q := c
			i++
			for i < n {
				switch src[i] {
				case '\\':
					if i+1 < n && src[i+1] == '\n' {
						line++
						i += 2
						lineStart = i
						continue
					}
					i += 2
					continue
				case '\n':
					if q != '`' {
						continue outer // unterminated single-line string: give up at end of line
					}
					line++
					i++
					lineStart = i
					continue
				case q:
					i++
					continue outer
				}
				i++
			}
			continue
		}

		for _, b := range sx.block {
			if !bytes.HasPrefix(src[i:], []byte(b[0])) {
				continue
			}
			open := i
			i += len(b[0])
			seg := i
			for i < n {
				if bytes.HasPrefix(src[i:], []byte(b[1])) {
					emit(seg, i, line, lineStart)
					i += len(b[1])
					spans = append(spans, [2]int{open, i})
					continue outer
				}
				if src[i] == '\n' {
					emit(seg, i, line, lineStart)
					line++
					i++
					lineStart = i
					seg = i
					continue
				}
				i++
			}
			emit(seg, n, line, lineStart)
			spans = append(spans, [2]int{open, n})
			continue outer
		}

		for _, m := range sx.line {
			if !bytes.HasPrefix(src[i:], []byte(m)) {
				continue
			}
			if sx.hashNeedsSpace && m == "#" && i != lineStart && src[i-1] != ' ' && src[i-1] != '\t' {
				continue
			}
			start := i + len(m)
			j := start
			for j < n && src[j] != '\n' {
				j++
			}
			emit(start, j, line, lineStart)
			spans = append(spans, [2]int{i, j})
			i = j
			continue outer
		}
		if c != ' ' && c != '\t' && c != '\r' {
			lastSig = c
		}
		i++
	}
	return out, spans
}

// codeIdents adds every identifier of 3+ bytes outside the comment spans to into.
func codeIdents(src []byte, spans [][2]int, into map[string]struct{}) {
	k := 0
	for i := 0; i < len(src); {
		for k < len(spans) && spans[k][1] <= i {
			k++
		}
		if k < len(spans) && i >= spans[k][0] {
			i = spans[k][1]
			continue
		}
		if !isIdentStart(src[i]) {
			i++
			continue
		}
		j := i + 1
		for j < len(src) && (isIdentStart(src[j]) || src[j] >= '0' && src[j] <= '9') {
			j++
		}
		if j-i >= 3 {
			into[string(src[i:j])] = struct{}{}
		}
		i = j
	}
}

func isIdentStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '$'
}

// skipRegex returns the index after a /regex/ literal that starts at i, or the end of the line.
func skipRegex(src []byte, i int) int {
	inClass := false
	for i++; i < len(src) && src[i] != '\n'; i++ {
		switch src[i] {
		case '\\':
			i++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if !inClass {
				return i + 1
			}
		}
	}
	return i
}

// cleanComment drops decoration such as "*", "/", "!" and "#" that leads a comment line.
func cleanComment(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "*/!#-")
	s = strings.TrimSuffix(s, "*/")
	return strings.TrimSpace(s)
}
