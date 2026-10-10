// Package knowledge searches a client's knowledge repository: a folder of
// markdown notes that link to each other with [[links]]. It runs on the machine
// that holds the notes, so note text never goes to the hub.
package knowledge

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Hit is a note that matches.
type Hit struct {
	Path   string   `json:"path"`            // relative to the repository
	Title  string   `json:"title"`           // its first heading, else its file name
	Lines  []string `json:"lines"`           // the matching lines, with their numbers
	Links  []string `json:"links,omitempty"` // notes it links to with [[...]] (which exist)
	Score  int      `json:"score"`
	Direct bool     `json:"direct"` // false: shown only because a matching note links to it
}

var linkRE = regexp.MustCompile(`\[\[([^\]|#]+)(?:[|#][^\]]*)?\]\]`)

const maxNoteBytes = 1 << 20

// Terms splits a query into lower-case words worth matching.
func Terms(q string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r >= 0x80)
	}) {
		if len([]rune(w)) >= 2 && !seen[w] && !stop[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

var stop = map[string]bool{"the": true, "and": true, "for": true, "with": true, "that": true, "this": true, "from": true, "are": true, "was": true, "how": true, "what": true, "does": true, "do": true, "we": true, "is": true, "to": true, "of": true, "in": true, "on": true, "a": true, "an": true}

type note struct {
	path, title string
	text        string
	lines       []string
	links       []string // link targets as written
}

// Search finds the notes under root that mention the query's words, best first,
// and adds the notes the best ones link to (one step), marked as indirect.
func Search(root, query string, limit int) ([]Hit, error) {
	terms := Terms(query)
	if len(terms) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	var notes []*note
	byStem := map[string]*note{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable corner must not stop the search
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > maxNoteBytes {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		n := &note{path: filepath.ToSlash(mustRel(root, p))}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<16), maxNoteBytes)
		for sc.Scan() {
			line := sc.Text()
			n.lines = append(n.lines, line)
			if n.title == "" && strings.HasPrefix(line, "# ") {
				n.title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			}
			for _, m := range linkRE.FindAllStringSubmatch(line, -1) {
				n.links = append(n.links, strings.TrimSpace(m[1]))
			}
		}
		n.text = strings.Join(n.lines, "\n")
		if n.title == "" {
			n.title = strings.TrimSuffix(name, filepath.Ext(name))
		}
		notes = append(notes, n)
		byStem[strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))] = n
		return nil
	})
	if err != nil {
		return nil, err
	}
	var hits []Hit
	direct := map[string]bool{}
	for _, n := range notes {
		distinct, total := 0, 0
		low := strings.ToLower(n.text)
		titleLow := strings.ToLower(n.title + " " + n.path)
		bonus := 0
		for _, t := range terms {
			c := strings.Count(low, t)
			if c > 0 {
				distinct++
				total += c
			}
			if strings.Contains(titleLow, t) {
				bonus += 15
				if c == 0 {
					distinct++
				}
			}
		}
		if distinct == 0 {
			continue
		}
		h := Hit{Path: n.path, Title: n.title, Score: distinct*100 + total + bonus, Direct: true}
		for i, line := range n.lines {
			ll := strings.ToLower(line)
			for _, t := range terms {
				if strings.Contains(ll, t) {
					h.Lines = append(h.Lines, lineText(i+1, line))
					break
				}
			}
			if len(h.Lines) == 3 {
				break
			}
		}
		for _, l := range n.links {
			if t, ok := byStem[strings.ToLower(l)]; ok && t != n {
				h.Links = appendUnique(h.Links, t.path)
			}
		}
		hits = append(hits, h)
		direct[n.path] = true
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Path < hits[j].Path
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	// one step along the links of the best three, for the notes that explain them
	for i := 0; i < len(hits) && i < 3; i++ {
		for _, p := range hits[i].Links {
			if direct[p] {
				continue
			}
			direct[p] = true
			for _, n := range notes {
				if n.path == p {
					hits = append(hits, Hit{Path: p, Title: n.title, Lines: head(n.lines, 2), Direct: false})
				}
			}
		}
	}
	return hits, nil
}

func lineText(n int, line string) string {
	line = strings.TrimSpace(line)
	if len(line) > 200 {
		line = line[:200] + "..."
	}
	return itoa(n) + ": " + line
}

func head(lines []string, n int) []string {
	var out []string
	for i, l := range lines {
		if strings.TrimSpace(l) == "" || strings.HasPrefix(l, "#") {
			continue
		}
		out = append(out, lineText(i+1, l))
		if len(out) == n {
			break
		}
	}
	return out
}

func appendUnique(xs []string, x string) []string {
	for _, y := range xs {
		if y == x {
			return xs
		}
	}
	return append(xs, x)
}

func mustRel(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return r
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
