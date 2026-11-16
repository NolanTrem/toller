package main

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestExtractComments(t *testing.T) {
	src := "const url = \"https://x.io\" // PROJ-12 fix\n/**\n * Doc line\n */\nlet s = `a\n// not a comment\n`\n"
	got := extractComments([]byte(src), cLike)
	want := []Comment{
		{Line: 1, Col: 31, Off: 30, Text: "PROJ-12 fix"},
		{Line: 3, Col: 4, Off: 49, Text: "Doc line"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestHashNeedsSpace(t *testing.T) {
	got := extractComments([]byte("color: \"#fff\"\nlen=${#arr}\nx: 1 # real\n"), hashLik)
	if len(got) != 1 || got[0].Text != "real" {
		t.Fatalf("got %+v", got)
	}
}

func TestVueTemplateAndScript(t *testing.T) {
	src := "<template>\n  <!-- WEB-9 -->\n  <a href=\"https://x\">x</a>\n</template>\n<script>\n// Step 1: go\n</script>\n"
	got := extractComments([]byte(src), sfcLike)
	if len(got) != 2 || got[0].Text != "WEB-9" || got[1].Text != "Step 1: go" {
		t.Fatalf("got %+v", got)
	}
}

func TestRegexLiteral(t *testing.T) {
	src := "const re = /^a\\/*b[/]$/.test(x) // PROJ-5\nconst half = a / 2 // ok\n"
	got := extractComments([]byte(src), cLike)
	if len(got) != 2 || got[0].Text != "PROJ-5" || got[1].Text != "ok" {
		t.Fatalf("got %+v", got)
	}
}

func TestRules(t *testing.T) {
	rules := defaultRules(nil, nil)
	cases := []struct {
		text string
		want string // rule ID, or "" for no finding
	}{
		{"Fixes PROJ-2122 regression", "ticket-ref"},
		{"Run grep -rn \"PROJ-151\" to find the call sites", ""},
		{"See WEB-633 for details", "ticket-ref"},
		{"Encode as UTF-8 before hashing", ""},
		{"Use SHA-256 and ISO-8601", ""},
		{"Offset is UTC-5 in winter", ""},
		{"A DNS-1123 label accepts 63 characters under the LICENSE-2.0 terms", ""},
		{"Matches [A-Z0-9] only", ""},
		{"Matches the pre-PROJ-33 behavior", "ticket-ref"},
		{"The invoice being deleted, e.g. INV-019", ""},
		{"Fixed for the page's lifetime", ""},
		{"Added to the existing selection", ""},
		{"The width the user asked for by dragging", ""},
		{"Owned by the placeholder logic", ""},
		{"Updated to use the new client", "changelog"},
		{"Added: retry on 429", "changelog"},
		{"Changed from forEach to map for speed", "changelog"},
		{"Now uses the cached client", "changelog"},
		{"Removed items stay in the trash for 30 days", ""},
		{"Step 2: normalize input", "narrator"},
		{"Let's fetch the user first", "narrator"},
		{"... existing code ...", "placeholder"},
		{"In a real implementation this would call the API", "placeholder"},
		{"As requested, the button is now blue", "chat-leftover"},
		{"I've updated the handler to retry", "chat-leftover"},
		{"Retry because the CDN drops the first request", ""},
		{"PROJ-1 toller:ignore", ""},
	}
	for _, c := range cases {
		got := checkComments("f", []Comment{{Line: 1, Col: 1, Text: c.text}}, rules)
		id := ""
		if len(got) > 0 {
			id = got[0].Rule.ID
		}
		if id != c.want {
			t.Errorf("%q: got %q, want %q", c.text, id, c.want)
		}
	}
}

func TestOnlyPrefixes(t *testing.T) {
	rules := defaultRules(setOf("PROJ"), nil)
	if n := len(checkComments("f", []Comment{{Text: "ABC-12 and PROJ-3"}}, rules)); n != 1 {
		t.Fatalf("got %d findings, want 1", n)
	}
}

func TestCodeIdentsSkipsComments(t *testing.T) {
	src := []byte("const liveName = 1 // mirrors ghostName\n")
	_, spans := lex(src, cLike)
	idx := map[string]struct{}{}
	codeIdents(src, spans, idx)
	if _, ok := idx["liveName"]; !ok {
		t.Fatal("liveName missing from index")
	}
	if _, ok := idx["ghostName"]; ok {
		t.Fatal("comment text leaked into the index")
	}
}

func TestDanglingRefs(t *testing.T) {
	known := map[string]struct{}{"liveName": {}}
	comments := []Comment{
		{Line: 1, Text: "Mirrors SidebarToolbar and calls `liveName()`"},
		{Line: 2, Text: "see `iconClass` for the faded icon"},
		{Line: 3, Text: "the `migrations` key and like before"}, // not camelCase
		{Line: 4, Text: "see ghostName toller:ignore"},
	}
	var got []string
	for _, f := range danglingRefs("f", comments, known) {
		got = append(got, f.Ref)
	}
	if !reflect.DeepEqual(got, []string{"SidebarToolbar", "iconClass"}) {
		t.Fatalf("got %v", got)
	}
}

func TestFixText(t *testing.T) {
	f := newFixer(setOf("PROJ", "WEB"))
	cases := []struct{ in, want string }{
		{"Page breaks (PROJ-50) write other pages the same way.", "Page breaks write other pages the same way."},
		{"Nobody owns the org (PROJ-896).", "Nobody owns the org."},
		{"PROJ-330: the column was renamed", "The column was renamed"},
		{"PROJ-528 — useFoo reads the host", "useFoo reads the host"},
		{"(PROJ-47)", ""},
		{"PROJ-47.", ""},
		{"Opens the page (PROJ-34, review finding 8).", "Opens the page (review finding 8)."},
		{"Lock rows (see PROJ-1 and WEB-2) first", "Lock rows first"},
		{"PROJ-51 moved the budget off the row setting", "PROJ-51 moved the budget off the row setting"},
		{"Uses UTF-8 (PROJ-1)", "Uses UTF-8"},
		{"(PROJ-45, no viewId) both use it", "(no viewId) both use it"},
		{"PROJ-263: sent-attachment preview.", "Sent-attachment preview."},
		{"(PROJ-480). Only one has a logo.", "Only one has a logo."},
		{"PROJ-236 — the PROJ-227 analogue", "PROJ-236 — the PROJ-227 analogue"},
		{"tagged PROJ-151 — grep -rn \"PROJ-151\" is the index", "tagged PROJ-151 — grep -rn \"PROJ-151\" is the index"},
		{"drags it to (PROJ-239) so the override", "drags it to (PROJ-239) so the override"},
		{"(PROJ-38 — availability) Pure.", "(PROJ-38 — availability) Pure."},
		{"PROJ-1 — header/footer state", "Header/footer state"},
		{"PROJ-637/PROJ-809: hf + page-number elements", "hf + page-number elements"},
	}
	for _, c := range cases {
		if got, _, _ := f.fixText(c.in, "", false); got != c.want {
			t.Errorf("%q: got %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPlanFixes(t *testing.T) {
	f := newFixer(setOf("PROJ"))
	src := "/**\n * (PROJ-47)\n */\nconst a = \"(PROJ-1)\" // PROJ-2\n// PROJ-3: keep the value\nlet b = 1 /* (PROJ-4) */ + 2\n"
	got, recs := f.planFixes("f.ts", []byte(src), cLike, extractComments([]byte(src), cLike))
	want := "const a = \"(PROJ-1)\"\n// Keep the value\nlet b = 1 /* (PROJ-4) */ + 2\n" // an inline block between code is skipped
	if string(got) != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d records, want 3", len(recs))
	}
}

func TestFixDocAndTitleLines(t *testing.T) {
	f := newFixer(setOf("PROJ"))
	src := "\t/// <summary>\r\n\t/// PROJ-330: loads Date values\r\n\t/// </summary>\r\n" +
		"-- 0496-AddDateType\r\n-- PROJ-330: adds the Date type.\r\n// PROJ-42 — all changes for this ticket live here\r\n"
	got, _ := f.planFixes("f.cs", []byte(src), cLike, extractComments([]byte(src), cLike))
	want := "\t/// <summary>\r\n\t/// Loads Date values\r\n\t/// </summary>\r\n" +
		"-- 0496-AddDateType\r\n-- PROJ-330: adds the Date type.\r\n// PROJ-42 — all changes for this ticket live here\r\n"
	if string(got) != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	sql := "-- 0496-AddDateType\r\n-- PROJ-330: adds the Date type.\r\n"
	got, _ = f.planFixes("f.sql", []byte(sql), sqlLike, extractComments([]byte(sql), sqlLike))
	if string(got) != "-- 0496-AddDateType\r\n-- Adds the Date type.\r\n" {
		t.Fatalf("sql: got %q", got)
	}
}

func TestFixContinuation(t *testing.T) {
	f := newFixer(setOf("PROJ"))
	src := "// checked against the current rows\n// (PROJ-34) — the core logic lives here\n" +
		"// the page owns the drawer\n// (PROJ-145).\n// keeps the old value\n// (PROJ-9). Then it reloads.\n" +
		"// what made it a state\n// (PROJ-151), which is why\n// fixed since\n// PROJ-586, but a rep\n"
	got, _ := f.planFixes("f.ts", []byte(src), cLike, extractComments([]byte(src), cLike))
	want := "// checked against the current rows —\n// the core logic lives here\n" +
		"// the page owns the drawer.\n// keeps the old value.\n// Then it reloads.\n" +
		"// what made it a state,\n// which is why\n// fixed since\n// PROJ-586, but a rep\n"
	if string(got) != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestJSONLBlockKeepsCRLF(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/a.cs"
	src := "class A {\r\n  // PROJ-1 is why\r\n  // we wait\r\n  int x;\r\n}\r\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	comments := extractComments([]byte(src), cLike)
	rules := defaultRules(setOf("PROJ"), nil)
	var buf bytes.Buffer
	writeJSONL(&buf, checkComments(path, comments, rules), []fileComments{{path: path, comments: comments}})
	var task jsonTask
	if err := json.Unmarshal(buf.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(src, task.Block) || !strings.Contains(task.Block, "\r\n") {
		t.Fatalf("block %q is not an exact substring of the file", task.Block)
	}
}

func TestConfigArgs(t *testing.T) {
	path := t.TempDir() + "/.toller"
	os.WriteFile(path, []byte("# defaults\n--prefixes SEC,PROJ  # trackers\n\n--disable narrator\n"), 0o644)
	got, err := configArgs(path)
	if err != nil || !reflect.DeepEqual(got, []string{"--prefixes", "SEC,PROJ", "--disable", "narrator"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := configArgs(path + ".missing"); got != nil || err != nil {
		t.Fatalf("missing file: got %v, %v", got, err)
	}
}

func TestCommentPrefixes(t *testing.T) {
	var cs []Comment
	for _, n := range []string{"1", "2", "3", "4", "5", "5"} {
		cs = append(cs, Comment{Text: "fixed in SEC-" + n + " and UTF-8 \"QA-1\""})
	}
	cs = append(cs, Comment{Text: "see WEB-12"})
	got := commentPrefixes([]fileComments{{comments: cs}}, nil)
	want := []prefixCount{{"SEC", 5}, {"WEB", 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTrackerRule(t *testing.T) {
	r := trackerRule("acme/app")
	cases := map[string]bool{
		"@see https://linear.app/acme/issue/PROJ-1247":                       true,
		"see linear.app/acme/project/q3-launch-abc123":                       true,
		"https://acme.atlassian.net/browse/WEB-12":                           true,
		"https://github.com/acme/app/pull/10#issuecomment-4004761607":        true,
		"https://github.com/ACME/App/issues/7":                               true,
		"works around https://github.com/vuejs/core/issues/123":              false,
		"docs at https://linear.app/docs/api":                                false,
		`Accepts a Linear URL ("https://linear.app/acme/issue/PROJ-7/slug")`: false,
	}
	for text, want := range cases {
		got := len(checkComments("f", []Comment{{Text: text}}, []*Rule{r})) > 0
		if got != want {
			t.Errorf("%q: got %v, want %v", text, got, want)
		}
	}
}

func commentTexts(src string, sx *syntax) []string {
	var out []string
	for _, c := range extractComments([]byte(src), sx) {
		out = append(out, c.Text)
	}
	return out
}

func TestPythonDocstrings(t *testing.T) {
	src := `"""Module doc."""
def load():
    """PROJ-12: load the rows.

    Retries once # not a comment, inside the docstring
    """
    query = """SELECT 1 -- PROJ-9 is in the SQL"""
    run(
        """SELECT 2"""
    )
    return 1  # real comment
`
	want := []string{"Module doc.", "PROJ-12: load the rows.", "Retries once # not a comment, inside the docstring", "real comment"}
	if got := commentTexts(src, pyLike); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestTOMLMultilineStrings(t *testing.T) {
	src := "text = \"\"\"\n# not a comment\n\"\"\"\nx = 1 # real\n"
	if got := commentTexts(src, tomlLik); !reflect.DeepEqual(got, []string{"real"}) {
		t.Fatalf("got %q", got)
	}
}

func TestCSharpVerbatimAndRawStrings(t *testing.T) {
	src := "class A {\r\n" +
		"  string sql = @\"SELECT 1\r\n    /* PROJ-21 in the query text */\r\n    FROM t\";\r\n" +
		"  string path = @\"C:\\temp\\\"; // real one\r\n" +
		"  string q = $@\"say \"\"hi\"\" // not a comment {x}\";\r\n" +
		"  string raw = \"\"\"\r\n    // not a comment either\r\n    \"\"\";\r\n" +
		"  // real two\r\n}\r\n"
	if got := commentTexts(src, csLike); !reflect.DeepEqual(got, []string{"real one", "real two"}) {
		t.Fatalf("got %q", got)
	}
	// Line numbers stay right after strings that span lines.
	cs := extractComments([]byte(src), csLike)
	if cs[0].Line != 5 || cs[1].Line != 10 {
		t.Fatalf("lines: got %d and %d, want 5 and 10", cs[0].Line, cs[1].Line)
	}
}

func TestFixInsideDocstringKeepsCode(t *testing.T) {
	f := newFixer(setOf("PROJ"))
	src := "def load():\n    \"\"\"Load the rows (PROJ-12).\"\"\"\n    q = \"\"\"x (PROJ-3)\"\"\"\n"
	got, recs := f.planFixes("a.py", []byte(src), pyLike, extractComments([]byte(src), pyLike))
	want := "def load():\n    \"\"\"Load the rows.\"\"\"\n    q = \"\"\"x (PROJ-3)\"\"\"\n"
	if string(got) != want || len(recs) != 1 {
		t.Fatalf("got %q (%d records)", got, len(recs))
	}
}
