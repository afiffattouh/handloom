package link

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"handloom/internal/addon"
	"handloom/internal/api"
	"handloom/internal/client"
	"handloom/internal/profile"
)

func writeFile(t *testing.T, p, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func TestPlainResultFilesAreKept(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "c1")
	writeFile(t, filepath.Join(dir, "page.md"), "hello")
	writeFile(t, filepath.Join(dir, "sub", "more.txt"), "x")
	files, problem := checkResults(dir, 100, []string{"page.md", "sub/more.txt"})
	if problem != "" || strings.Join(files, ",") != "page.md,sub/more.txt" {
		t.Fatalf("files %v, problem %q", files, problem)
	}
	// A call that saved nothing is fine, with or without a folder.
	if files, problem := checkResults(filepath.Join(t.TempDir(), "none"), 100, nil); problem != "" || len(files) != 0 {
		t.Fatalf("no folder: %v %q", files, problem)
	}
}

func TestASymlinkInTheResultsIsRemovedAndNotFollowed(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.txt")
	writeFile(t, outside, "secret")
	outDir := t.TempDir()
	writeFile(t, filepath.Join(outDir, "inside.txt"), "kept by the link")
	dir := filepath.Join(t.TempDir(), "c1")
	writeFile(t, filepath.Join(dir, "ok.txt"), "fine")
	if err := os.Symlink(outside, filepath.Join(dir, "link.txt")); err != nil {
		t.Skip("symlinks are not available here")
	}
	os.Symlink(outDir, filepath.Join(dir, "linkdir"))
	_, problem := checkResults(dir, 100, []string{"link.txt"})
	if !strings.Contains(problem, "link.txt") || !strings.Contains(problem, "linkdir") || !strings.Contains(problem, "not allowed") {
		t.Fatalf("problem: %q", problem)
	}
	if exists(filepath.Join(dir, "link.txt")) || exists(filepath.Join(dir, "linkdir")) {
		t.Fatal("a link was left in the results folder")
	}
	if b, _ := os.ReadFile(outside); string(b) != "secret" || !exists(filepath.Join(outDir, "inside.txt")) {
		t.Fatal("what the link pointed to was touched")
	}
	if !exists(filepath.Join(dir, "ok.txt")) {
		t.Fatal("the plain file was removed with it")
	}
}

func TestResultsOverTheLimitsAreRemoved(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "c1")
	writeFile(t, filepath.Join(dir, "a.txt"), strings.Repeat("a", 60))
	writeFile(t, filepath.Join(dir, "b.txt"), strings.Repeat("b", 60))
	files, problem := checkResults(dir, 100, nil)
	if !strings.Contains(problem, "b.txt") || strings.Contains(problem, "a.txt") || len(files) != 0 || !strings.Contains(problem, "over the output limit") {
		t.Fatalf("files %v, problem %q", files, problem)
	}
	if !exists(filepath.Join(dir, "a.txt")) || exists(filepath.Join(dir, "b.txt")) {
		t.Fatal("the file that went over the limit was not the one removed")
	}

	many := filepath.Join(t.TempDir(), "c2")
	for i := 0; i < maxResultFiles+2; i++ {
		writeFile(t, filepath.Join(many, fmt.Sprintf("f%02d.txt", i)), "x")
	}
	_, problem = checkResults(many, 1000, nil)
	if !strings.Contains(problem, "more than 20 files") || !strings.Contains(problem, "f20.txt") || !strings.Contains(problem, "f21.txt") {
		t.Fatalf("problem: %q", problem)
	}
	if left, _ := os.ReadDir(many); len(left) != maxResultFiles {
		t.Fatalf("%d files left, want %d", len(left), maxResultFiles)
	}
}

func TestAReportedPathThatLeavesTheFolderIsRefused(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "outside.txt"), "not yours")
	dir := filepath.Join(base, "c1")
	writeFile(t, filepath.Join(dir, "ok.txt"), "fine")
	for _, name := range []string{"../outside.txt", filepath.Join(base, "outside.txt"), "sub/../../outside.txt", "missing.txt"} {
		files, problem := checkResults(dir, 100, []string{name})
		if problem == "" || files != nil {
			t.Errorf("%q: files %v, problem %q", name, files, problem)
		}
	}
	if !exists(filepath.Join(base, "outside.txt")) {
		t.Fatal("a file outside the folder was touched")
	}
}

func TestAFolderReplacedByALinkIsNotFollowed(t *testing.T) {
	outDir := t.TempDir()
	writeFile(t, filepath.Join(outDir, "x.txt"), "x")
	dir := filepath.Join(t.TempDir(), "c1")
	if err := os.Symlink(outDir, dir); err != nil {
		t.Skip("symlinks are not available here")
	}
	_, problem := checkResults(dir, 100, nil)
	if problem == "" || !exists(filepath.Join(outDir, "x.txt")) || exists(dir) {
		t.Fatalf("problem %q", problem)
	}
}

func TestPromoteCopiesAResultIntoTheProject(t *testing.T) {
	wt := t.TempDir()
	writeFile(t, filepath.Join(resultsRoot(wt), "web-fetch", "c1", "page.md"), "# page")
	src := filepath.Join(resultsRoot(wt), "web-fetch", "c1", "page.md")
	to, err := promote(wt, src, "docs/page.md", false, acceptedIn(wt))
	if err != nil || to != "docs/page.md" {
		t.Fatalf("%q %v", to, err)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "docs", "page.md")); string(b) != "# page" {
		t.Fatalf("the copy: %q", b)
	}
	// A path relative to the results folder names the same file.
	if _, err := promote(wt, "web-fetch/c1/page.md", "docs/two.md", false, acceptedIn(wt)); err != nil {
		t.Fatal(err)
	}
	// An existing file is kept unless --force.
	writeFile(t, src, "# newer")
	if _, err := promote(wt, src, "docs/page.md", false, acceptedIn(wt)); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("overwrite without force: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "docs", "page.md")); string(b) != "# page" {
		t.Fatalf("the file was replaced without --force: %q", b)
	}
	if _, err := promote(wt, src, "docs/page.md", true, acceptedIn(wt)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "docs", "page.md")); string(b) != "# newer" {
		t.Fatalf("force did not replace: %q", b)
	}
}

func TestPromoteRefusesWhatLeavesTheWorktreeOrIsNotAResult(t *testing.T) {
	base := t.TempDir()
	wt := filepath.Join(base, "wt")
	outside := filepath.Join(base, "elsewhere")
	os.MkdirAll(outside, 0o755)
	writeFile(t, filepath.Join(wt, "keep.txt"), "project file")
	writeFile(t, filepath.Join(wt, ".handloom", "scope.json"), "{}")
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: /x/y")
	src := filepath.Join(resultsRoot(wt), "a", "c1", "r.txt")
	writeFile(t, src, "r")
	os.Symlink(outside, filepath.Join(wt, "escape"))
	os.Symlink(filepath.Join(wt, "keep.txt"), filepath.Join(resultsRoot(wt), "a", "c1", "l.txt"))

	for _, to := range []string{"../out.txt", "a/../../out.txt", filepath.Join(outside, "out.txt"), "escape/out.txt", ".handloom/out.txt", ".git", ".GIT", ".git/info/exclude", "", "."} {
		if _, err := promote(wt, src, to, true, acceptedIn(wt)); err == nil {
			t.Errorf("destination %q was allowed", to)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(wt, ".git")); string(b) != "gitdir: /x/y" {
		t.Fatalf(".git was changed: %q", b)
	}
	if exists(filepath.Join(outside, "out.txt")) || exists(filepath.Join(base, "out.txt")) {
		t.Fatal("a file was written outside the worktree")
	}
	for _, from := range []string{"keep.txt", filepath.Join(wt, "keep.txt"), "../../scope.json", "a/c1/l.txt", "a/c1/nothere.txt", "a/c1"} {
		if _, err := promote(wt, from, "copy.txt", true, acceptedIn(wt)); err == nil {
			t.Errorf("source %q was allowed", from)
		}
	}
	if exists(filepath.Join(wt, "copy.txt")) {
		t.Fatal("something that is not a result was copied")
	}
}

// A promoted result is an ordinary change of the task: it is in the files the hub is told about and in the diff
// and the scope check sees it. A result that was not promoted is not, however many there are.
func TestAPromotedResultIsAChangeAndAnUnpromotedOneIsNot(t *testing.T) {
	f := newFixture(t)
	ac, dir, task := f.scopedAgent(nil)
	writeFile(t, filepath.Join(resultsRoot(dir), "web-fetch", "c1", "page.md"), "# kept\n")
	writeFile(t, filepath.Join(resultsRoot(dir), "web-fetch", "c1", "other.md"), "# not kept\n")
	rec := f.link.loadScope("alpha")
	if rec == nil {
		t.Fatal("no scope record")
	}
	got, err := f.link.changedFiles(context.Background(), rec)
	if err != nil || len(got) != 0 {
		t.Fatalf("results counted as changes: %v %v", got, err)
	}
	if _, err := promote(dir, filepath.Join("web-fetch", "c1", "page.md"), "docs/page.md", false, acceptedIn(dir)); err != nil {
		t.Fatal(err)
	}
	if got, err := f.link.changedFiles(context.Background(), rec); err != nil || strings.Join(got, ",") != "docs/page.md" {
		t.Fatalf("changed files: %v %v", got, err)
	}
	if err := f.submit(ac, task); err != nil {
		t.Fatal(err)
	}
	var res api.TaskResult
	for i := 0; i < 300 && res.Agent == ""; i++ {
		f.human.Get(fmt.Sprintf("/v1/tasks/%d/result", task.ID), &res)
		if res.Agent == "" {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if len(res.Files) != 1 || res.Files[0].Path != "docs/page.md" || !strings.Contains(res.Diff, "+# kept") || strings.Contains(res.Diff, "not kept") {
		t.Fatalf("result: %+v\n%s", res.Files, res.Diff)
	}
	// What the work was is also what is committed: results never are.
	if out := gitOut(t, dir, "status", "--porcelain", "--untracked-files=all"); strings.Contains(out, ".handloom") {
		t.Fatalf("git sees the results folder:\n%s", out)
	}
}

// ---- through the gateway ----

const filesService = `import json, os, sys
for line in sys.stdin:
    req = json.loads(line)
    if req["tool"] == "_health":
        out = {"ok": True}
    else:
        kind, d = req["args"]["kind"], req["results"]
        os.makedirs(d, exist_ok=True)
        files = ["a.txt"]
        open(os.path.join(d, "a.txt"), "w").write("hello")
        if kind == "link":
            os.symlink(os.path.abspath(__file__), os.path.join(d, "l.txt"))
        elif kind == "big":
            open(os.path.join(d, "big.txt"), "w").write("x" * 5000)
            files.append("big.txt")
        elif kind == "many":
            for i in range(25):
                open(os.path.join(d, "m%02d.txt" % i), "w").write("x")
        elif kind == "trick":
            files.append("../../../../escape.txt")
        out = {"ok": True, "text": "done", "files": files}
    out["id"] = req["id"]
    print(json.dumps(out), flush=True)
`

const filesManifest = `schema: 1
kind: tool
name: filer
version: 0.1.0
api: 1
title: Filer
summary: Saves files for tests.
run: ["python3", "service.py"]
hosts: []
secrets: []
tools:
  - name: make
    summary: Save files of a kind.
    input:
      kind: {type: string, required: true, summary: "ok, link, big, many or trick"}
    leaves_machine: false
    timeout: 10s
    max_output: 1KB
`

func TestTheLinkChecksResultFilesAfterACall(t *testing.T) {
	e := newToolEnv(t)
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "addon.yaml"), filesManifest)
	writeFile(t, filepath.Join(src, "service.py"), filesService)
	if _, err := addon.Install(e.root, addon.NewStore(e.root), src, "", "tester", e.clock()); err != nil {
		t.Fatal(err)
	}
	e.ladder()
	e.profile("scribe", profile.Cloud)
	leaves := false
	e.must(e.human.Post("/v1/profiles/scribe/addon-grants", api.AddonGrantReq{Addon: "filer", Tool: "make", LeavesMachine: &leaves}, nil))
	c, s := e.helper("alpha", "scribe", false)
	dir := filepath.Join(e.link.opt.WorkRoot, fmt.Sprintf("job-%d", *s.Job), "alpha")

	got, err := call(c, "filer.make", map[string]any{"kind": "ok"})
	if err != nil || !got.OK || len(got.Files) != 1 {
		t.Fatalf("ok: %+v %v", got, err)
	}
	okFile := got.Files[0]
	for _, kind := range []string{"link", "big", "many", "trick"} {
		got, err := call(c, "filer.make", map[string]any{"kind": kind})
		if err != nil || got.OK || got.Code != "plugin-error" || len(got.Files) != 0 {
			t.Errorf("%s: %+v %v", kind, got, err)
		}
	}
	// Nothing odd is left in any call's folder.
	filepath.WalkDir(resultsRoot(dir), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !d.Type().IsRegular() {
			t.Errorf("%s is left in the results", p)
		}
		if err == nil && d.Name() == "big.txt" {
			t.Errorf("%s is left in the results", p)
		}
		return nil
	})
	if _, err := os.Stat(filepath.Join(dir, "..", "..", "..", "..", "escape.txt")); err == nil {
		t.Error("the add-on wrote outside its results folder")
	}

	// A promoted file through the helper's own socket.
	var pr api.ToolPromoteResp
	if err := c.Post("/local/tools/promote", api.ToolPromoteReq{File: okFile, To: "notes/a.txt"}, &pr); err != nil || pr.To != "notes/a.txt" || !strings.Contains(pr.Message, "write scope") {
		t.Fatalf("promote: %+v %v", pr, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "notes", "a.txt")); string(b) != "hello" {
		t.Fatalf("the promoted file: %q", b)
	}
	if err := c.Post("/local/tools/promote", api.ToolPromoteReq{File: okFile, To: "../x.txt"}, &pr); err == nil {
		t.Fatal("a destination outside the worktree was allowed")
	}
	// Another helper's token cannot promote this helper's file into its own tree by name: no token, no promote.
	if err := client.Socket(e.link.opt.Socket, "").Post("/local/tools/promote", api.ToolPromoteReq{}, &pr); err == nil {
		t.Fatal("a promote without a token was allowed")
	}

	// The results go when the helper's session ends; the project files stay.
	e.link.gate.reap(func(*toolSession) bool { return false })
	if exists(resultsRoot(dir)) {
		t.Fatal("the results of a finished helper were kept")
	}
	if !exists(filepath.Join(dir, "notes", "a.txt")) {
		t.Fatal("a promoted file was removed with the results")
	}
}

// acceptedIn is what the link would have recorded had every file now in the results folder been passed on.
func acceptedIn(worktree string) map[string]acceptedFile {
	root := resultsRoot(worktree)
	out := map[string]acceptedFile{}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil && fi.Mode().IsRegular() {
				rel, _ := filepath.Rel(root, p)
				sum, _ := fileSum(p)
				out[rel] = acceptedFile{Size: fi.Size(), Sum: sum}
			}
		}
		return nil
	})
	return out
}

// A file that appears after the call returned, or grows after it was accepted, is not promoted, and a link put
// in the place of an accepted file is not followed.
func TestPromoteOnlyTakesWhatTheCallPassedOn(t *testing.T) {
	wt := t.TempDir()
	dir := filepath.Join(resultsRoot(wt), "filer", "c1")
	writeFile(t, filepath.Join(dir, "a.txt"), "first")
	accepted := acceptedIn(wt)
	writeFile(t, filepath.Join(dir, "late.txt"), strings.Repeat("x", 5000))
	if _, err := promote(wt, "filer/c1/late.txt", "late.txt", false, accepted); err == nil || !strings.Contains(err.Error(), "not passed on") {
		t.Fatalf("a late file: %v", err)
	}
	writeFile(t, filepath.Join(dir, "a.txt"), "first and much more")
	if _, err := promote(wt, "filer/c1/a.txt", "a.txt", false, accepted); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("a grown file: %v", err)
	}
	if exists(filepath.Join(wt, "late.txt")) || exists(filepath.Join(wt, "a.txt")) {
		t.Fatal("something that was not accepted was copied")
	}
	os.Remove(filepath.Join(dir, "a.txt"))
	if err := os.Symlink(filepath.Join(wt, "late.txt"), filepath.Join(dir, "a.txt")); err == nil {
		if _, err := promote(wt, "filer/c1/a.txt", "a.txt", false, accepted); err == nil {
			t.Fatal("a link in place of an accepted file was promoted")
		}
	}
}

func TestCheckResultsRemovesEmptyFolders(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "c1")
	os.MkdirAll(filepath.Join(dir, "a", "b", "c", "d"), 0o755)
	writeFile(t, filepath.Join(dir, "keep", "x.txt"), "x")
	files, problem := checkResults(dir, 100, nil)
	if problem != "" || len(files) != 1 || exists(filepath.Join(dir, "a")) || !exists(filepath.Join(dir, "keep", "x.txt")) {
		t.Fatalf("%v %q", files, problem)
	}
}

// A hard link to a file outside the results folder is not a plain result file, and a file rewritten to other
// content of the same size after it was accepted is not promoted.
func TestResultsRefuseHardLinksAndSameSizeSwaps(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.txt")
	writeFile(t, outside, "secret")
	dir := t.TempDir()
	if err := os.Link(outside, filepath.Join(dir, "linked.txt")); err != nil {
		t.Skipf("hard links are not available here: %v", err)
	}
	if files, problem := checkResults(dir, 100, nil); problem == "" || len(files) != 0 {
		t.Fatalf("a hard link was passed on: files %v problem %q", files, problem)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("the file the link pointed to was removed: %v", err)
	}

	wt := t.TempDir()
	res := filepath.Join(resultsRoot(wt), "filer", "c1")
	writeFile(t, filepath.Join(res, "a.txt"), "first")
	accepted := acceptedIn(wt)
	writeFile(t, filepath.Join(res, "a.txt"), "other") // the same size, other content
	if _, err := promote(wt, "filer/c1/a.txt", "a.txt", false, accepted); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("a rewritten file was promoted: %v", err)
	}
}
