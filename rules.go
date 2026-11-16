package main

import (
	"regexp"
	"strings"
)

type Rule struct {
	ID  string
	Msg string
	Re  *regexp.Regexp
	// Accept rejects a regex match that is a known false positive. Nil accepts all matches.
	Accept func(text string, loc []int) bool
}

// Uppercase prefixes that look like ticket keys but are standards, encodings, or time zones.
var notTicketPrefixes = setOf(
	"UTF", "UCS", "SHA", "MD", "ISO", "IEC", "RFC", "CVE", "CWE", "GHSA", "ES", "ECMA", "AES", "RSA",
	"HMAC", "PKCS", "TLS", "SSL", "HTTP", "IPV", "IP", "TCP", "UDP", "BASE", "CRC", "WCAG", "PEP",
	"GPL", "LGPL", "AGPL", "BSD", "MIT", "CC", "APACHE", "ASCII", "ANSI", "IEEE", "UTC", "GMT", "EST",
	"PST", "CST", "MST", "EDT", "PDT", "COVID", "WGS", "EPSG", "X", "H", "P", "U", "A", "I", "WIN",
	"ARM", "AMD", "MPEG", "ISBN", "SRGB", "PNG", "VP", "AV", "HSL", "RGB", "OWASP", "DNS", "LICENSE",
)

func setOf(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = true
	}
	return m
}

func ci(pattern string) *regexp.Regexp { return regexp.MustCompile(`(?i)` + pattern) }

func defaultRules(onlyPrefixes, extraAllowed map[string]bool) []*Rule {
	ticket := &Rule{
		ID:  "ticket-ref",
		Msg: "issue-tracker key in a comment; put it in the commit message or PR instead",
		Re:  regexp.MustCompile(`\b([A-Z][A-Z0-9]{0,9})-[0-9]{1,6}\b`),
		Accept: func(text string, loc []int) bool {
			prefix := text[loc[2]:loc[3]]
			// "A-Z0-9" in a character class, or a zero-padded display ID such as "INV-007".
			classRange := loc[0] > 0 && text[loc[0]-1] == '-' && strings.ContainsAny(prefix, "0123456789")
			if classRange || text[loc[3]+1] == '0' {
				return false
			}
			// A quoted key is an example or an argument, as in grep "PROJ-1", not a reference.
			if quoted(text, loc) {
				return false
			}
			if len(onlyPrefixes) > 0 {
				return onlyPrefixes[prefix]
			}
			return len(prefix) >= 2 && !notTicketPrefixes[prefix] && !extraAllowed[prefix]
		},
	}
	return []*Rule{
		ticket,
		{
			ID:  "changelog",
			Msg: "comment narrates the edit history; describe what the code does, or use the commit message",
			Re: ci(`^(?:new|added|changed|updated|fixed|removed|modified|refactored)\s*:` +
				`|^(?:changed|moved|renamed|switched|migrated|converted) (?:this |it )?from\b.*\bto\b` +
				`|\bwas previously\b|\bused to be\b|^previously,` +
				`|^(?:now|this now|it now) (?:uses|handles|supports|returns|calls|accepts|works|correctly)\b` +
				`|^(?:updated|changed) to\b|^(?:updated|fixed|added|removed|changed) (?:so that|per)\b`),
		},
		{
			ID:  "narrator",
			Msg: "comment narrates steps; say why, not what happens next",
			Re: ci(`^(?:step\s*\d+\s*[:.)-]|let's\b|let us\b|here we\b|now,? we\b|first,? we\b` +
				`|next,? we\b|then,? we\b|finally,? we\b|we (?:now|then) )`),
		},
		{
			ID:  "placeholder",
			Msg: "placeholder or stub left by a generator",
			Re: ci(`\.\.\.\s*(?:existing|rest of|other|previous|remaining|same as)\b` +
				`|\bin a real (?:implementation|app|application|world|scenario|project)\b` +
				`|\b(?:simplified|mock|dummy) (?:version|implementation)\b` +
				`|\bfor (?:demo|example|illustration) purposes\b` +
				`|\bplaceholder implementation\b` +
				`|\breplace (?:this )?with (?:your|actual|the actual|real)\b`),
		},
		{
			ID:  "chat-leftover",
			Msg: "text addressed to a chat partner, not to a reader of the code",
			Re: ci(`\bas (?:you )?requested\b|\bper (?:your|the user'?s?) (?:request|instructions?)\b` +
				`|\byou'?re (?:absolutely )?right\b|\bas an ai\b` +
				`|\bi'?ve (?:added|updated|changed|modified|removed|fixed|refactored)\b` +
				`|\bi (?:added|updated|changed|modified|removed|fixed|refactored)\b` +
				`|\bper our (?:discussion|conversation|chat)\b`),
		},
	}
}

type Finding struct {
	Path string
	Line int
	Col  int
	Rule *Rule
	Text string
	Ref  string // the missing symbol, for dangling-ref
}

const ignoreMarker = "toller:ignore"

func checkComments(path string, comments []Comment, rules []*Rule) []Finding {
	var out []Finding
	for _, c := range comments {
		if strings.Contains(c.Text, ignoreMarker) {
			continue
		}
		for _, r := range rules {
			for _, loc := range r.Re.FindAllStringSubmatchIndex(c.Text, -1) {
				if r.Accept != nil && !r.Accept(c.Text, loc) {
					continue
				}
				f := Finding{Path: path, Line: c.Line, Col: c.Col + loc[0], Rule: r, Text: c.Text}
				if r.ID == "ticket-ref" {
					f.Ref = c.Text[loc[0]:loc[1]]
				}
				out = append(out, f)
				break
			}
		}
	}
	return out
}

var danglingRule = &Rule{ID: "dangling-ref", Msg: "comment names a symbol that does not exist in this repo"}

// A ref is a `backticked` identifier, or a name that follows "mirrors", "see", "same as" and similar.
var refRe = regexp.MustCompile("`([A-Za-z$][A-Za-z0-9$]*)(?:\\(\\))?`" +
	`|\b(?i:mirrors?|mirroring|same (?:\w+ )?as|see|like|matches|via|calls?)\s+` + "`?" + `([A-Za-z$][A-Za-z0-9$]*)`)

// isCamel limits refs to camelCase or PascalCase names, so prose words and config keys are skipped.
func isCamel(s string) bool {
	if len(s) < 4 {
		return false
	}
	for i := 0; i+1 < len(s); i++ {
		if s[i] >= 'a' && s[i] <= 'z' && s[i+1] >= 'A' && s[i+1] <= 'Z' {
			return true
		}
	}
	return false
}

func danglingRefs(path string, comments []Comment, known map[string]struct{}) []Finding {
	var out []Finding
	for _, c := range comments {
		if strings.Contains(c.Text, ignoreMarker) {
			continue
		}
		for _, m := range refRe.FindAllStringSubmatchIndex(c.Text, -1) {
			start, end := m[2], m[3]
			if start < 0 {
				start, end = m[4], m[5]
			}
			name := c.Text[start:end]
			if _, ok := known[name]; ok || !isCamel(name) {
				continue
			}
			out = append(out, Finding{Path: path, Line: c.Line, Col: c.Col + start, Rule: danglingRule, Text: c.Text, Ref: name})
		}
	}
	return out
}

// trackerRule flags links to an issue tracker. A GitHub issue or PR link counts only when it points at
// this repo (ownRepo, "owner/name"): a link to an upstream bug usually explains a workaround.
func trackerRule(ownRepo string) *Rule {
	return &Rule{
		ID:  "tracker-url",
		Msg: "issue-tracker link in a comment; readers outside the team cannot open it, so say what it explains",
		// A URL ends at whitespace, a quote, or a closing bracket.
		Re: regexp.MustCompile(`(?i)\b(?:https?://)?(?:linear\.app/[\w-]+/(?:issue|project|document|view)/[^\s"'` + "`" + `)\]>]+` +
			`|[\w-]+\.atlassian\.net/browse/[^\s"'` + "`" + `)\]>]+|app\.shortcut\.com/[^\s"'` + "`" + `)\]>]+/story/[^\s"'` + "`" + `)\]>]+` +
			`|github\.com/([\w.-]+/[\w.-]+)/(?:issues|pull)/\d+)`),
		Accept: func(text string, loc []int) bool {
			if quoted(text, loc) {
				return false // an example of an input format, not a pointer to a ticket
			}
			return loc[2] < 0 || ownRepo != "" && strings.EqualFold(text[loc[2]:loc[3]], ownRepo)
		},
	}
}

// quoted reports whether the match at loc is inside "double quotes" or `backticks`.
func quoted(text string, loc []int) bool {
	for _, q := range keyQuoted.FindAllStringIndex(text, -1) {
		if loc[0] > q[0] && loc[1] < q[1] {
			return true
		}
	}
	return false
}
