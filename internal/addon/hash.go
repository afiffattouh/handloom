package addon

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// ManifestFile is the name of the manifest inside an add-on's folder.
const ManifestFile = "addon.yaml"

// ManifestHash is the sha256 of the manifest's exact text, so a changed tool, host, secret list or
// leaves_machine value gives a new hash.
func ManifestHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// PackageHash is the sha256 over a folder's files: for each regular file, in order of its path with
// slashes, the line "PATH SHA256\n". A link or any other kind of file is an error, because what
// it points to is not part of the folder and could change without the hash noticing.
func PackageHash(dir string) (string, error) {
	type entry struct{ path, sum string }
	var files []entry
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a plain file (links are not allowed in an add-on)", filepath.ToSlash(rel))
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		files = append(files, entry{filepath.ToSlash(rel), hex.EncodeToString(h.Sum(nil))})
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", fmt.Errorf("%s has no files", dir)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	all := sha256.New()
	for _, f := range files {
		io.WriteString(all, f.path+" "+f.sum+"\n")
	}
	return hex.EncodeToString(all.Sum(nil)), nil
}

// Load reads and checks the add-on in dir and returns it with both hashes.
func Load(dir string) (*Manifest, string, string, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", "", fmt.Errorf("%s has no %s", dir, ManifestFile)
		}
		return nil, "", "", err
	}
	m, err := Parse(data)
	if err != nil {
		return nil, "", "", err
	}
	ph, err := PackageHash(dir)
	if err != nil {
		return nil, "", "", err
	}
	return m, ph, ManifestHash(data), nil
}
