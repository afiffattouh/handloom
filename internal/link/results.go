package link

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// Result files. An add-on saves files only into the results folder the link made for the call:
// <worktree>/.handloom/results/ADDON/CALL/. After the call the link looks at what is there. Only regular files
// are kept, at most maxResultFiles of them, and together with the text reply no more than the tool's output
// limit. What does not fit is removed and the call ends as plugin-error, so nothing odd reaches the agent or
// the project. The folder is under .handloom/, which is never part of a task's changes; a file reaches the
// project only through `handloom tool promote`.

const maxResultFiles = 20

// resultsRoot is where the results of every add-on call of a spawn live.
func resultsRoot(worktree string) string { return filepath.Join(worktree, ".handloom", "results") }

// checkResults looks at the results folder of one call and returns the files that may be passed on, as paths
// relative to dir. room is how many bytes the files may hold in all. Anything that is not allowed is removed
// (never anything outside dir) and named in the returned problem, which is empty when all is well. reported is
// what the add-on said it saved: a name that is not a plain path inside dir, or not a file that is there, is a
// problem too.
func checkResults(dir string, room int64, reported []string) (files []string, problem string) {
	info, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, checkReported(dir, nil, reported)
		}
		return nil, fmt.Sprintf("the results folder could not be read: %v", err)
	}
	if !info.IsDir() { // the add-on replaced the folder with something else; it is not followed
		os.Remove(dir)
		return nil, "the results folder was replaced by something that is not a folder, so it was removed"
	}
	var bad []string
	var kept []string
	var sizes = map[string]int64{}
	var remove []string
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			return nil // a real folder; the files in it count
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() || hasOtherNames(fi) {
			bad = append(bad, rel+" (not a plain file: links and other kinds are not allowed)")
			remove = append(remove, p)
			return nil
		}
		kept = append(kept, rel)
		sizes[rel] = fi.Size()
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Sprintf("the results folder could not be read: %v", walkErr)
	}
	sort.Strings(kept)
	var total int64
	var passed []string
	for _, rel := range kept {
		switch {
		case len(passed) >= maxResultFiles:
			bad = append(bad, rel+fmt.Sprintf(" (more than %d files)", maxResultFiles))
			remove = append(remove, filepath.Join(dir, rel))
		case total+sizes[rel] > room:
			bad = append(bad, rel+" (over the output limit of the tool)")
			remove = append(remove, filepath.Join(dir, rel))
		default:
			total += sizes[rel]
			passed = append(passed, rel)
		}
	}
	for _, p := range remove {
		os.Remove(p) // a link is removed itself, never what it points to
	}
	pruneEmptyFolders(dir)
	if len(bad) > 0 {
		return nil, "the add-on saved files that are not allowed and they were removed: " + strings.Join(bad, "; ")
	}
	if p := checkReported(dir, passed, reported); p != "" {
		return nil, p
	}
	return passed, ""
}

// pruneEmptyFolders removes the folders under dir that hold nothing, deepest first. dir itself stays.
func pruneEmptyFolders(dir string) {
	var dirs []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && p != dir {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		os.Remove(dirs[i]) // fails, and is left, when the folder is not empty
	}
}

// checkReported checks the file names an add-on said it saved against the files that were found.
func checkReported(dir string, found []string, reported []string) string {
	have := map[string]bool{}
	for _, f := range found {
		have[f] = true
	}
	for _, name := range reported {
		if !filepath.IsLocal(name) {
			return fmt.Sprintf("the add-on named a file (%q) that is not inside its results folder", name)
		}
		if !have[filepath.Clean(name)] {
			return fmt.Sprintf("the add-on named a file (%q) that is not there", name)
		}
	}
	return ""
}

// removeResults deletes the results of a spawn that is over. The worktree stays; only what add-ons saved goes.
func removeResults(worktree string) {
	if worktree != "" {
		os.RemoveAll(resultsRoot(worktree))
	}
}

// promote copies one result file into the project tree of the spawn's worktree. file is a full path inside the
// results folder or a path relative to it; to is a path inside the worktree. An existing file is replaced only
// with force. It does not look at the profile's write scope: that is checked when the helper submits, as for any
// file it changes.
//
// Only a file the link accepted after a call may be promoted: accepted holds those, by path relative to the
// results folder, with the size they had then. A file that was written later, or has grown since, is refused.
func promote(worktree, file, to string, force bool, accepted map[string]acceptedFile) (string, error) {
	root := resultsRoot(worktree)
	src := file
	if !filepath.IsAbs(src) {
		src = filepath.Join(root, src)
	}
	rel, err := filepath.Rel(root, filepath.Clean(src))
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%s is not a result file of this helper: name a file under %s", file, root)
	}
	src = filepath.Join(root, rel)
	if err := noLinksBelow(root, rel); err != nil {
		return "", err
	}
	fi, err := os.Lstat(src)
	if err != nil {
		return "", fmt.Errorf("there is no result file %s", rel)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a plain file, so it cannot be promoted", rel)
	}
	want, ok := accepted[rel]
	if !ok {
		return "", fmt.Errorf("%s was not passed on by a tool call of this helper, so it cannot be promoted", rel)
	}

	if to == "" || filepath.IsAbs(to) || !filepath.IsLocal(to) {
		return "", fmt.Errorf("the destination %q is not inside the project: give a path relative to the project folder, without ..", to)
	}
	to = filepath.Clean(to)
	if to == ".handloom" || strings.HasPrefix(to, ".handloom"+string(filepath.Separator)) {
		return "", fmt.Errorf("the destination %q is in .handloom, which is not part of the project", to)
	}
	if first, _, _ := strings.Cut(filepath.ToSlash(to), "/"); strings.EqualFold(first, ".git") {
		return "", fmt.Errorf("the destination %q is in .git, which is not part of the project", to)
	}
	dst := filepath.Join(worktree, to)
	// No part of the destination's folders may be a link: a link could point out of the worktree.
	if err := noLinksBelow(worktree, filepath.Dir(to)); err != nil {
		return "", fmt.Errorf("the destination %q is not allowed: %w", to, err)
	}
	if d, err := os.Lstat(dst); err == nil {
		if !d.Mode().IsRegular() {
			return "", fmt.Errorf("%s exists and is not a plain file, so it is not replaced", to)
		}
		if !force {
			return "", fmt.Errorf("%s already exists: use --force to replace it", to)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := noLinksBelow(worktree, filepath.Dir(to)); err != nil {
		return "", fmt.Errorf("the destination %q is not allowed: %w", to, err)
	}
	// Opened without following a link, and looked at again once open: what is copied is the file that was accepted.
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("%s could not be opened as a plain file: %w", rel, err)
	}
	defer in.Close()
	if fi, err := in.Stat(); err != nil || !fi.Mode().IsRegular() || hasOtherNames(fi) || fi.Size() != want.Size {
		return "", fmt.Errorf("%s has changed since the tool call passed it on, so it cannot be promoted", rel)
	}
	// Same size is not the same file: the add-on may still be running and can rewrite what it saved.
	if sum, err := sumOf(in); err != nil || sum != want.Sum {
		return "", fmt.Errorf("%s has changed since the tool call passed it on, so it cannot be promoted", rel)
	} else if _, err := in.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	out, err := os.OpenFile(dst, flags, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return "", fmt.Errorf("%s already exists: use --force to replace it", to)
		}
		return "", err
	}
	if _, err := io.Copy(out, io.LimitReader(in, want.Size)); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return to, nil
}

// noLinksBelow fails when a folder on the way from base down to rel (or the file at the end) is a link.
func noLinksBelow(base, rel string) error {
	if rel == "." || rel == "" {
		return nil
	}
	p := base
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a link", part)
		}
	}
	return nil
}

// hasOtherNames says whether a regular file has more than one name: a hard link to a file elsewhere would
// show that file's content as a result.
func hasOtherNames(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1
}

func sumOf(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileSum(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return sumOf(f)
}
