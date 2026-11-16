package main

import (
	"bytes"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// fixer removes ticket keys from comments where the result is still a correct sentence:
// a parenthetical such as "(PROJ-50)", a leading label such as "PROJ-33: ", or a comment that is only a key.
// Keys inside running prose are left for a person or an agent to reword.
type fixer struct {
	key     *regexp.Regexp
	whole   *regexp.Regexp
	leading *regexp.Regexp // a key label and its separator: "PROJ-1: ", "(PROJ-1) — "
	label   *regexp.Regexp // the key label alone, for a line that continues a sentence
	paren   *regexp.Regexp
	item    *regexp.Regexp
}

func newFixer(prefixes map[string]bool) *fixer {
	alts := sortedKeys(prefixes)
	sort.Slice(alts, func(i, j int) bool { return len(alts[i]) > len(alts[j]) })
	for i, a := range alts {
		alts[i] = regexp.QuoteMeta(a)
	}
	key := `\b(?:` + strings.Join(alts, "|") + `)-[1-9][0-9]{0,5}(?:/[0-9]+)*\b`
	list := key + `(?:\s*(?:,|/|&|\+|;|\band\b|\bor\b)\s*` + key + `)*`
	return &fixer{
		key:     regexp.MustCompile(key),
		whole:   regexp.MustCompile(`^[\[(]?` + list + `[\])]?[.:,;]?$`),
		leading: regexp.MustCompile(`^(?:\(` + list + `\)|\[` + list + `\]|` + list + `)\s*(?::|—|–|-|,)\s+`),
		label:   regexp.MustCompile(`^(?:\(` + list + `\)|\[` + list + `\]|` + list + `)\s*`),
		paren:   regexp.MustCompile(`\s*[(\[]([^()\[\]]*)[)\]]`),
		item:    regexp.MustCompile(`^\s*(?:(?:see|per|cf\.?|via|from|for|in)\s+)?` + list + `\s*$`),
	}
}

// fixText returns the comment text with removable keys gone, and the shapes it applied. An empty
// result means the comment held nothing but keys; no shapes means "leave it for a person or an agent".
// above is the full-line comment on the line before, if any; carry is punctuation that belongs at the
// end of that line once a key label at the start of this line is gone.
func (f *fixer) fixText(text, above string, hasAbove bool) (out string, shapes []string, carry string) {
	orig := text
	cont := hasAbove && !endsSentence(above)       // this line continues a sentence
	glued := hasAbove && endsWithConnective(above) // ... and the key is that sentence's object: "since\nPROJ-1"
	if keyQuoted.MatchString(text) && f.key.MatchString(strings.Join(keyQuoted.FindAllString(text, -1), " ")) {
		return orig, nil, "" // the key is also an example or an argument, as in grep "PROJ-1"
	}
	if f.whole.MatchString(text) {
		if glued {
			return orig, nil, ""
		}
		if cont {
			carry = sentenceEnd(text)
		}
		return "", []string{"whole"}, carry
	}

	if loc := f.leading.FindStringIndex(text); loc != nil && loc[1] < len(text) {
		if glued {
			return orig, nil, ""
		}
		if cont {
			// Mid-sentence: drop only the key. Punctuation right after it ends the line above.
			rest := strings.TrimLeft(text[f.label.FindStringIndex(text)[1]:], " \t")
			if rest != "" && strings.ContainsRune(",;:.", rune(rest[0])) {
				carry, rest = rest[:1], strings.TrimLeft(rest[1:], " \t")
				if carry == "." {
					rest = capitalizeFirstWord(rest)
				}
			} else if d := strings.TrimLeft(rest, "—–"); d != rest {
				// Wrap a dash at the end of the line above, not at the start of this one.
				carry, rest = " "+rest[:len(rest)-len(d)], strings.TrimLeft(d, " \t")
			}
			text = rest
		} else {
			text = capitalizeFirstWord(text[loc[1]:])
		}
		shapes = append(shapes, "prefix")
	}

	var b strings.Builder
	last := 0
	for _, m := range f.paren.FindAllStringSubmatchIndex(text, -1) {
		b.WriteString(text[last:m[0]])
		last = m[1]
		whole := text[m[0]:m[1]]
		lead := whole[:len(whole)-len(strings.TrimLeft(whole, " \t"))]
		body := strings.TrimLeft(whole, " \t")
		open, close, inner := body[:1], body[len(body)-1:], text[m[2]:m[3]]
		switch {
		case !f.key.MatchString(inner) || endsWithConnective(text[:m[0]]):
			b.WriteString(whole) // "drags it to (PROJ-1) so": the key is the object
		case f.item.MatchString(inner):
			shapes = append(shapes, "paren")
		default:
			parts := splitItems.Split(inner, -1)
			var kept []string
			for _, p := range parts {
				if !f.item.MatchString(p) {
					kept = append(kept, p)
				}
			}
			if len(kept) == len(parts) {
				b.WriteString(whole)
				continue
			}
			shapes = append(shapes, "paren-item")
			b.WriteString(lead + open + strings.Join(kept, ", ") + close)
		}
	}
	b.WriteString(text[last:])
	text = strings.TrimSpace(b.String())

	if strings.Trim(text, ".,;:—–- ") == "" {
		if glued {
			return orig, nil, ""
		}
		if cont {
			carry = sentenceEnd(text)
		}
		return "", append(shapes, "whole"), carry
	}
	// A parenthetical removed from the line start can leave the punctuation that followed it, as in
	// "(PROJ-1). Next sentence": that punctuation belongs to the sentence on the line above.
	if p := strings.IndexFunc(text, func(r rune) bool { return !strings.ContainsRune(".,;", r) }); p > 0 && len(shapes) > 0 {
		punct := text[:1]
		if cont {
			carry = punct
		}
		text = strings.TrimSpace(text[p:])
		if punct == "." {
			text = capitalizeFirstWord(text)
		}
	}

	// Leave the line alone when the edit would unbalance its parentheses, when a label came off but
	// another key stays in the sentence ("PROJ-2 — the PROJ-1 analogue"), or when the text still
	// points at the ticket ("the changes for this ticket").
	if parenBalance(text) != parenBalance(orig) || hasShape(shapes, "prefix") && f.key.MatchString(text) ||
		mentionsTicket.MatchString(text) {
		return orig, nil, ""
	}
	return text, shapes, carry
}

var (
	keyQuoted      = regexp.MustCompile("`[^`]*`|\"[^\"]*\"")
	splitItems     = regexp.MustCompile(`\s*[,;]\s*`)
	mentionsTicket = regexp.MustCompile(`(?i)\btickets?\b|\bticket's\b`)
	connective     = setOf("since", "until", "per", "see", "before", "after", "by", "from", "to", "of", "in",
		"and", "or", "the", "a", "an", "with", "via", "for", "than", "like", "into", "as")
)

// endsWithConnective reports whether s ends with a word that needs an object, such as "since" or "to".
var shortWords = setOf("a", "an", "as", "at", "be", "by", "do", "if", "in", "is", "it", "no", "of", "on", "or", "so", "to", "up", "we")

func endsWithConnective(s string) bool {
	f := strings.Fields(s)
	return len(f) > 0 && connective[strings.ToLower(f[len(f)-1])]
}

func parenBalance(s string) int {
	return strings.Count(s, "(") - strings.Count(s, ")") + strings.Count(s, "[") - strings.Count(s, "]")
}

func hasShape(shapes []string, s string) bool {
	for _, x := range shapes {
		if x == s {
			return true
		}
	}
	return false
}

func sentenceEnd(s string) string {
	if strings.HasSuffix(s, ".") {
		return "."
	}
	return ""
}

// capitalizeFirstWord upper-cases a leading plain lowercase word such as "the" or "logged-in", but not
// an identifier such as "useFoo", "fetch()" or "max_rows".
func capitalizeFirstWord(s string) string {
	end := strings.IndexAny(s, " \t")
	if end < 0 {
		end = len(s)
	}
	word := strings.TrimRight(s[:end], ".,;:")
	if word == "" || !unicode.IsLower(rune(word[0])) || len(word) <= 2 && !shortWords[word] {
		return s // an abbreviation such as "hf" stays as written
	}
	for _, r := range word {
		if !unicode.IsLower(r) && r != '-' && r != '\'' && r != '/' {
			return s
		}
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// endsSentence reports whether a comment line ends a sentence, so the line below starts a new one.
// An XML doc tag ("<summary>") or a one-word title line ("-- 0496-AddDateType") also counts.
func endsSentence(text string) bool {
	t := strings.TrimRight(text, " \t*/")
	return t == "" || strings.ContainsAny(t[len(t)-1:], ".:!?;)>") || !strings.ContainsAny(t, " \t")
}

type fixRecord struct {
	Path      string   `json:"path"`
	Line      int      `json:"line"`
	Shapes    []string `json:"shapes"`
	Before    string   `json:"before"`
	After     *string  `json:"after"` // nil: the line is deleted
	Remaining bool     `json:"remaining,omitempty"`
	Prev      string   `json:"prev,omitempty"`
	Next      string   `json:"next,omitempty"`
}

var (
	trailingMarker = regexp.MustCompile(`^(.*\S)\s*(?://+|#)\s*$`)
	directiveRe    = regexp.MustCompile(`eslint-|@ts-|prettier-ignore|biome-ignore|#__PURE__|@vite-ignore|istanbul ignore|c8 ignore|webpackChunkName|toller:ignore`)
)

// planFixes rewrites the comments in src that hold removable keys. It returns the new source and one
// record per changed line. If the edit touched anything outside comments, it returns src unchanged.
func (f *fixer) planFixes(path string, src []byte, sx *syntax, comments []Comment) ([]byte, []fixRecord) {
	lines := strings.SplitAfter(string(src), "\n")
	starts := make([]int, len(lines))
	for i, off := 1, 0; i < len(lines); i++ {
		off += len(lines[i-1])
		starts[i] = off
	}
	newLines := append([]string(nil), lines...)
	deleted := map[int]bool{}
	var recs []fixRecord
	byLine := map[int]Comment{}
	for _, c := range comments {
		byLine[c.Line] = c
	}
	textEnd := map[int]int{} // line index -> end of the comment text in newLines, after edits

	for _, c := range comments {
		if !f.key.MatchString(c.Text) || directiveRe.MatchString(c.Text) {
			continue
		}
		// Only a full-line comment above can hold the start of this line's sentence.
		above, hasAbove := byLine[c.Line-1]
		if hasAbove {
			ai := c.Line - 2
			pre := strings.TrimSpace(lines[ai][:above.Off-starts[ai]])
			hasAbove = !deleted[ai] && strings.Trim(pre, "/*#!<-") == ""
		}
		newText, shapes, carry := f.fixText(c.Text, above.Text, hasAbove)
		if len(shapes) == 0 {
			continue
		}
		li := c.Line - 1
		if carry != "" {
			ai := li - 1
			end, ok := textEnd[ai]
			if !ok {
				end = above.Off - starts[ai] + len(above.Text)
			}
			if l := newLines[ai]; !strings.ContainsAny(l[end-1:end], ".!?:;,") {
				newLines[ai] = l[:end] + carry + l[end:]
				textEnd[ai] = end + len(carry)
			}
		}
		phys := strings.TrimRight(lines[li], "\r\n")
		eol := lines[li][len(phys):]
		pre := phys[:c.Off-starts[li]]
		suf := phys[c.Off-starts[li]+len(c.Text):]

		rec := fixRecord{Path: path, Line: c.Line, Shapes: shapes, Before: phys, Remaining: f.key.MatchString(newText)}
		if li > 0 {
			rec.Prev = strings.TrimRight(lines[li-1], "\r\n")
		}
		if li+1 < len(lines) {
			rec.Next = strings.TrimRight(lines[li+1], "\r\n")
		}

		if newText != "" {
			after := pre + newText + suf
			newLines[li] = after + eol
			textEnd[li] = len(pre) + len(newText)
			rec.After = &after
			recs = append(recs, rec)
			continue
		}
		p, s := strings.TrimSpace(pre), strings.TrimSpace(suf)
		switch {
		case s == "" && (p == "" || p == "*" || strings.Trim(p, "/") == "" || strings.Trim(p, "#") == ""),
			strings.HasPrefix(p, "/*") && s == "*/",
			p == "<!--" && s == "-->":
			deleted[li] = true
		case s == "" && trailingMarker.MatchString(pre):
			after := trailingMarker.FindStringSubmatch(pre)[1]
			newLines[li] = after + eol
			rec.After = &after
		case strings.HasPrefix(p, "/*") || s == "*/" || s == "-->":
			// The line opens or closes a block; keep the marker.
			after := strings.TrimRight(pre, " \t") + strings.TrimLeft(suf, " \t")
			if s != "" && p != "" {
				after = strings.TrimRight(pre, " \t") + " " + strings.TrimLeft(suf, " \t")
			}
			newLines[li] = after + eol
			rec.After = &after
		default:
			continue
		}
		recs = append(recs, rec)
	}
	if len(recs) == 0 {
		return src, nil
	}

	// A doc block whose only text line was deleted would be left as an empty "/**" + " */" pair.
	for i := range newLines {
		if deleted[i] || i+1 >= len(newLines) {
			continue
		}
		j := i + 1
		for j < len(newLines) && deleted[j] {
			j++
		}
		if j > i+1 && j < len(newLines) {
			o, cl := strings.TrimSpace(newLines[i]), strings.TrimSpace(newLines[j])
			if (o == "/**" || o == "/*") && cl == "*/" {
				deleted[i], deleted[j] = true, true
			}
		}
	}

	var out strings.Builder
	for i, l := range newLines {
		if !deleted[i] {
			out.WriteString(l)
		}
	}
	result := []byte(out.String())
	if !bytes.Equal(codeOnly(src, sx), codeOnly(result, sx)) {
		return src, nil
	}
	return result, recs
}

// codeOnly is src without its comments and whitespace; a comment-only edit must leave it unchanged.
func codeOnly(src []byte, sx *syntax) []byte {
	_, spans := lex(src, sx)
	var b bytes.Buffer
	prev := 0
	for _, sp := range spans {
		b.Write(src[prev:sp[0]])
		prev = sp[1]
	}
	b.Write(src[prev:])
	return bytes.Join(bytes.Fields(b.Bytes()), nil)
}
