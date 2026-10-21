package addon

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Install copies the add-on in src into root/NAME and records the approval. expect, when set, is the
// package hash the person was shown or pinned; the copy must have exactly that hash, so the bytes that
// run are the bytes that were read. An add-on whose service is running is not replaced.
func Install(root string, store *Store, src, expect, by string, now time.Time) (Approval, error) {
	m, ph, mh, err := Load(src)
	if err != nil {
		return Approval{}, err
	}
	if expect != "" && expect != ph {
		return Approval{}, fmt.Errorf("the folder's hash is %s, not the %s you gave; nothing was installed", ph, expect)
	}
	if pid, ok := RunningPID(root, m.Name); ok {
		return Approval{}, fmt.Errorf("%s is running (process %d); let it stop on its own after ten idle minutes, or stop the link, then install again", m.Name, pid)
	}
	dest := filepath.Join(root, m.Name)
	if same, _ := sameFolder(src, dest); same {
		return Approval{}, fmt.Errorf("%s is already the installed copy", src)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Approval{}, err
	}
	tmp, err := os.MkdirTemp(root, ".incoming-")
	if err != nil {
		return Approval{}, err
	}
	defer os.RemoveAll(tmp)
	if err := copyTree(src, tmp); err != nil {
		return Approval{}, err
	}
	if got, err := PackageHash(tmp); err != nil || got != ph {
		return Approval{}, fmt.Errorf("the folder changed while it was being copied; nothing was installed")
	}
	old := ""
	if _, err := os.Stat(dest); err == nil {
		old = tmp + ".old"
		if err := os.Rename(dest, old); err != nil {
			return Approval{}, err
		}
	}
	if err := os.Rename(tmp, dest); err != nil {
		if old != "" {
			os.Rename(old, dest)
		}
		return Approval{}, err
	}
	if old != "" {
		os.RemoveAll(old)
	}
	a := Approval{Name: m.Name, Version: m.Version, PackageHash: ph, ManifestHash: mh, By: by, At: now.UTC()}
	if err := store.Put(a); err != nil {
		return Approval{}, err
	}
	return a, nil
}

// Remove deletes an installed add-on and its approval.
func Remove(root string, store *Store, name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("%q is not an add-on name", name)
	}
	_, approved, err := store.Get(name)
	if err != nil {
		return err
	}
	dir := filepath.Join(root, name)
	if _, err := os.Stat(dir); err != nil && !approved {
		return fmt.Errorf("this computer does not have the add-on %s", name)
	}
	if pid, ok := RunningPID(root, name); ok {
		return fmt.Errorf("%s is running (process %d); let it stop on its own after ten idle minutes, or stop the link, then remove it", name, pid)
	}
	if err := store.Delete(name); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// Info is one installed add-on as the list and the doctor show it.
type Info struct {
	Name         string
	Version      string
	Manifest     *Manifest
	PackageHash  string
	ManifestHash string
	// State is ready, changed or missing: the same words the hub shows.
	State  string
	Detail string
}

// Installed lists the add-ons on this computer, by name: every folder, and every approval whose folder is gone.
func Installed(root string, store *Store) ([]Info, error) {
	approvals, err := store.List()
	if err != nil {
		return nil, err
	}
	have := map[string]Approval{}
	for _, a := range approvals {
		have[a.Name] = a
	}
	names := map[string]bool{}
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() && nameRe.MatchString(e.Name()) {
			names[e.Name()] = true
		}
	}
	for n := range have {
		names[n] = true
	}
	var order []string
	for n := range names {
		order = append(order, n)
	}
	sort.Strings(order)
	var out []Info
	for _, n := range order {
		in := Info{Name: n}
		ap, approved := have[n]
		m, ph, mh, err := Load(filepath.Join(root, n))
		switch {
		case err != nil && !dirExists(filepath.Join(root, n)):
			in.State, in.Detail = "missing", "approved here, but its folder is gone"
			in.Version = ap.Version
		case err != nil:
			in.State, in.Detail = "changed", err.Error()
		case !approved:
			in.State, in.Detail = "changed", "not approved on this computer"
		case ph != ap.PackageHash || mh != ap.ManifestHash || m.Name != n:
			in.State, in.Detail = "changed", "differs from what was approved"
		default:
			in.State = "ready"
		}
		if m != nil {
			in.Manifest, in.Version, in.PackageHash, in.ManifestHash = m, m.Version, ph, mh
		}
		out = append(out, in)
	}
	return out, nil
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func sameFolder(a, b string) (bool, error) {
	sa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(sa, sb), nil
}

// copyTree copies regular files and folders, keeping the executable bit. Anything else is refused.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o700)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a plain file (links are not allowed in an add-on)", filepath.ToSlash(rel))
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600|info.Mode().Perm()&0o100)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}
