package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, text string) {
	t.Helper()
	p := filepath.Join(root, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSearchFindsNotesRanksThemAndFollowsLinks(t *testing.T) {
	root := t.TempDir()
	write(t, root, "clients/acme.md", "# Acme Ltd\n\n- Invoice prefix: ACM (their accounting system rejects anything else).\n- Billing contact: Dana. See [[contract-terms]].\n")
	write(t, root, "clients/contract-terms.md", "# Contract terms\n\nPayment net 30. Late fee 2 percent.\n")
	write(t, root, "decisions/2026-invoice-ids.md", "# Invoice ids\n\nWe number invoices per year.\n")
	write(t, root, "misc/lunch.md", "# Lunch\n\nNothing about billing.\n")
	write(t, root, ".git/objects/x.md", "invoice prefix in git internals must not be searched\n")
	write(t, root, "notes.txt", "invoice prefix in a text file is not a note\n")

	hits, err := Search(root, "invoice prefix", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 2 || hits[0].Path != "clients/acme.md" {
		t.Fatalf("the note with both words should come first: %+v", hits)
	}
	if hits[0].Title != "Acme Ltd" || len(hits[0].Lines) == 0 || !strings.Contains(hits[0].Lines[0], "ACM") || !strings.HasPrefix(hits[0].Lines[0], "3:") {
		t.Fatalf("title and matching line: %+v", hits[0])
	}
	var seen = map[string]Hit{}
	for _, h := range hits {
		seen[h.Path] = h
		if strings.Contains(h.Path, ".git") || strings.HasSuffix(h.Path, ".txt") {
			t.Errorf("searched something that is not a note: %s", h.Path)
		}
	}
	if _, ok := seen["decisions/2026-invoice-ids.md"]; !ok {
		t.Errorf("a note matching one word was left out: %+v", hits)
	}
	if _, ok := seen["misc/lunch.md"]; ok {
		t.Error("an unrelated note was returned")
	}
	// the note it links to is offered, marked as indirect
	linked, ok := seen["clients/contract-terms.md"]
	if !ok || linked.Direct {
		t.Fatalf("the linked note should be there, marked indirect: %+v", linked)
	}
	if len(hits[0].Links) != 1 || hits[0].Links[0] != "clients/contract-terms.md" {
		t.Fatalf("links: %+v", hits[0].Links)
	}
	// a title match counts even when the body never says the word
	write(t, root, "people/dana.md", "# Dana\n\nWorks in accounts.\n")
	if h, _ := Search(root, "dana", 5); len(h) == 0 || h[0].Path != "people/dana.md" {
		t.Fatalf("title match: %+v", h)
	}
	// nothing to look for, or nothing found, is an empty answer
	if h, _ := Search(root, "the and", 5); len(h) != 0 {
		t.Fatal("a query of stop words matched")
	}
	if h, _ := Search(root, "zebra", 5); len(h) != 0 {
		t.Fatal("a miss matched")
	}
	if _, err := Search(filepath.Join(root, "missing"), "x1", 5); err != nil {
		t.Fatalf("a missing folder is an empty search, not an error: %v", err)
	}
	// the limit applies to direct hits
	if h, _ := Search(root, "invoice prefix", 1); len(h) < 1 || !h[0].Direct {
		t.Fatalf("limit: %+v", h)
	}
}
