package cli

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"handloom/internal/api"
	"handloom/internal/knowledge"
)

// search looks for what the team already knows: the client's notes on this
// machine (the knowledge repository an agent has at .handloom/knowledge, or
// any folder of notes given with --knowledge) and what earlier jobs recorded
// on the hub (briefs, results, handoff notes, answered questions). The notes are
// read here and never sent to the hub.
//
//	handloom search invoice prefix
//	handloom search --notes --knowledge ~/clients/acme-knowledge "billing contact"
func (e *env) search(args []string) error {
	fs := e.flags("search")
	onlyNotes := fs.Bool("notes", false, "only the client's notes on this machine")
	onlyJobs := fs.Bool("jobs", false, "only what earlier jobs recorded on the hub")
	root := fs.String("knowledge", "", "a folder of markdown notes (default: .handloom/knowledge, found from here upwards)")
	limit := fs.Int("limit", 8, "most results per kind")
	pos, err := fs.parse(args)
	if err != nil {
		return err
	}
	query := strings.TrimSpace(strings.Join(pos, " "))
	if query == "" || len(knowledge.Terms(query)) == 0 {
		return usageErr("usage: handloom search <words> [--notes | --jobs] [--knowledge FOLDER] [--limit N]   (say what to look for: a few words)")
	}
	wantNotes, wantJobs := !*onlyJobs, !*onlyNotes

	type result struct {
		Notes     []knowledge.Hit `json:"notes"`
		NotesFrom string          `json:"notes_from,omitempty"`
		Jobs      []api.SearchHit `json:"jobs"`
	}
	res := result{Notes: []knowledge.Hit{}, Jobs: []api.SearchHit{}}
	var warns []string

	if wantNotes {
		dir := *root
		if dir == "" {
			dir = findKnowledge()
		}
		switch {
		case dir == "" && *onlyNotes:
			return fmt.Errorf("no notes here: there is no .handloom/knowledge folder above this one. Give a folder of notes with --knowledge FOLDER")
		case dir == "":
			warns = append(warns, "No knowledge repository here (no .handloom/knowledge above this folder); give one with --knowledge FOLDER to search notes.")
		default:
			if st, err := os.Stat(dir); err != nil || !st.IsDir() {
				return fmt.Errorf("%s is not a folder of notes", dir)
			}
			hits, err := knowledge.Search(dir, query, *limit)
			if err != nil {
				return err
			}
			res.Notes, res.NotesFrom = hits, dir
			if hits == nil {
				res.Notes = []knowledge.Hit{}
			}
		}
	}
	if wantJobs {
		c, err := conn()
		if err != nil {
			if *onlyJobs {
				return err
			}
			warns = append(warns, "Earlier jobs were not searched: "+err.Error())
		} else if err := c.Get("/v1/search?q="+url.QueryEscape(query)+"&limit="+fmt.Sprint(*limit), &res.Jobs); err != nil {
			if *onlyJobs {
				return err
			}
			warns = append(warns, "Earlier jobs were not searched: "+err.Error())
		}
	}
	e.print(res, func() {
		if wantNotes && res.NotesFrom != "" {
			fmt.Fprintf(e.out, "Notes in %s\n", res.NotesFrom)
			if len(res.Notes) == 0 {
				fmt.Fprintln(e.out, "  nothing matches")
			}
			for _, h := range res.Notes {
				mark := ""
				if !h.Direct {
					mark = "  (linked from a match)"
				}
				fmt.Fprintf(e.out, "  %s  %s%s\n", h.Path, h.Title, mark)
				for _, l := range h.Lines {
					fmt.Fprintf(e.out, "      %s\n", l)
				}
			}
		}
		if wantJobs && (len(res.Jobs) > 0 || *onlyJobs || len(warns) == 0) {
			if wantNotes && res.NotesFrom != "" {
				fmt.Fprintln(e.out)
			}
			fmt.Fprintln(e.out, "Earlier jobs (on the hub)")
			if len(res.Jobs) == 0 {
				fmt.Fprintln(e.out, "  nothing matches")
			}
			for _, h := range res.Jobs {
				fmt.Fprintf(e.out, "  [%s] %s  (%s)\n      %s\n", h.Kind, h.Title, ago(h.At), h.Snippet)
			}
		}
		for _, w := range warns {
			fmt.Fprintln(e.out, w)
		}
		if len(res.Notes)+len(res.Jobs) > 0 {
			fmt.Fprintln(e.out, "\nIf something here helps, say so in your evidence: --evidence \"used:<note path or job #id>\".")
		}
	})
	return nil
}

// findKnowledge looks for .handloom/knowledge from the current folder upwards.
func findKnowledge() string {
	dir, _ := os.Getwd()
	for dir != "" {
		p := filepath.Join(dir, ".handloom", "knowledge")
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}
