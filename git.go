package main

import (
	"bufio"
	"bytes"
	"io/fs"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// listFiles returns the files under paths, honoring .gitignore when inside a git repo.
func listFiles(paths []string) ([]string, error) {
	args := append([]string{"ls-files", "-z", "--cached", "--others", "--exclude-standard", "--"}, paths...)
	if out, err := exec.Command("git", args...).Output(); err == nil {
		var files []string
		for _, f := range bytes.Split(out, []byte{0}) {
			if len(f) > 0 {
				files = append(files, string(f))
			}
		}
		return files, nil
	}

	var files []string
	for _, root := range paths {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch d.Name() {
				case ".git", "node_modules", "vendor", "dist", "build", "target":
					return filepath.SkipDir
				}
				return nil
			}
			files = append(files, p)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

type diffInfo struct {
	added   map[string]map[int]bool // file -> line numbers that the diff adds
	removed map[string]struct{}     // identifiers on lines that the diff removes
}

// readDiff reads the index (staged) or the work tree against base.
func readDiff(staged bool, base string) (*diffInfo, error) {
	args := []string{"diff", "--unified=0", "--no-color", "--no-ext-diff", "--relative"}
	if staged {
		args = append(args, "--cached")
	} else if base != "" {
		args = append(args, base)
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil, err
	}

	d := &diffInfo{added: map[string]map[int]bool{}, removed: map[string]struct{}{}}
	var cur map[int]bool
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		l := sc.Bytes()
		switch {
		case bytes.HasPrefix(l, []byte("+++ ")):
			cur = nil
			if name := l[4:]; bytes.HasPrefix(name, []byte("b/")) {
				cur = map[int]bool{}
				d.added[string(name[2:])] = cur
			}
		case bytes.HasPrefix(l, []byte("--- ")):
		case bytes.HasPrefix(l, []byte("-")):
			codeIdents(l[1:], nil, d.removed)
		case bytes.HasPrefix(l, []byte("@@ ")) && cur != nil:
			// @@ -a,b +c,d @@
			fields := strings.Fields(string(l))
			if len(fields) < 3 {
				continue
			}
			start, count := parseRange(strings.TrimPrefix(fields[2], "+"))
			for k := 0; k < count; k++ {
				cur[start+k] = true
			}
		}
	}
	return d, sc.Err()
}

var (
	commitKeyRe = regexp.MustCompile(`\b([A-Z][A-Z0-9]{1,9})-([0-9]{1,6})\b`)
	branchKeyRe = regexp.MustCompile(`(?i)\b([a-z][a-z0-9]{1,9})-([0-9]{1,6})\b`)
)

// minDistinctKeys is how many different ticket numbers a prefix needs in the history to count as a tracker key.
const minDistinctKeys = 5

// detectPrefixes finds the tracker prefixes the team really uses, from commit messages (uppercase keys)
// and branch names (any case). A prefix needs minDistinctKeys different numbers, so a word that
// appears with only one or two numbers, such as a review round or an encoding name, does not qualify.
func detectPrefixes() map[string]bool {
	logOut, err := exec.Command("git", "log", "-n", "3000", "--format=%s%n%b").Output()
	if err != nil {
		return nil
	}
	branches, _ := exec.Command("git", "for-each-ref", "--format=%(refname:short)", "refs/heads", "refs/remotes").Output()

	keys := map[string]map[string]bool{}
	add := func(re *regexp.Regexp, text []byte) {
		for _, m := range re.FindAllSubmatch(text, -1) {
			p := strings.ToUpper(string(m[1]))
			if keys[p] == nil {
				keys[p] = map[string]bool{}
			}
			keys[p][string(m[2])] = true
		}
	}
	add(commitKeyRe, logOut)
	add(branchKeyRe, branches)

	found := map[string]bool{}
	for p, nums := range keys {
		if len(nums) >= minDistinctKeys && !notTicketPrefixes[p] {
			found[p] = true
		}
	}
	return found
}

func parseRange(s string) (start, count int) {
	count = 1
	if a, b, ok := strings.Cut(s, ","); ok {
		s = a
		count, _ = strconv.Atoi(b)
	}
	start, _ = strconv.Atoi(s)
	return start, count
}

// originRepo returns "owner/name" of the GitHub origin remote, or "".
func originRepo() string {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	m := regexp.MustCompile(`github\.com[:/]([\w.-]+/[\w.-]+?)(?:\.git)?\s*$`).FindSubmatch(out)
	if m == nil {
		return ""
	}
	return string(m[1])
}
