package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"handloom/internal/client"
	"handloom/kit"
)

// kitCmd runs the project kit that ships inside this program:
//
//	handloom kit init [DIR] [--preset web]    add the check, the gate and the agent guide to a project
//	handloom kit handover [DIR]               prepare a finished project to be looked after by others
//	handloom kit task new|seal SLUG, lessons, upgrade, accept-tests
//
// The kit is Python (3.8 or newer) and is unpacked once per version under the link's folder. A project
// that has been set up carries its own copy, so it never needs this program to run its check.
func (e *env) kitCmd(args []string) int {
	if e.dial != nil {
		fmt.Fprintln(e.err, "handloom: the kit works on files on this machine, not through a hub")
		return 2
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(e.out, "usage: handloom kit init|handover|upgrade|accept-tests|lessons|task ... (the same arguments as the kit's own `factory` command)\n")
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	py := findPython()
	if py == "" {
		fmt.Fprintln(e.err, "handloom: the kit needs Python 3.8 or newer (python3 --version). Install it (macOS: xcode-select --install; Windows: python.org; Linux: your package manager) and try again.")
		return 2
	}
	dir, err := unpackKit()
	if err != nil {
		fmt.Fprintln(e.err, "handloom: unpack the kit:", err)
		return 1
	}
	cmd := exec.Command(py, append([]string{filepath.Join(dir, "bin", "factory")}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, e.out, e.err
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		fmt.Fprintln(e.err, "handloom:", err)
		return 1
	}
	return 0
}

// findPython is the first of python3 and python that is 3.8 or newer.
func findPython() string {
	for _, name := range []string{"python3", "python"} {
		p, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		out, err := exec.Command(p, "-c", "import sys; print(int(sys.version_info >= (3, 8)))").Output()
		if err == nil && strings.TrimSpace(string(out)) == "1" {
			return p
		}
	}
	return ""
}

// unpackKit writes the embedded kit under <link folder>/kit/<content hash> unless it is already there,
// and returns that folder. The folder name comes from the content, so an upgraded program unpacks afresh.
func unpackKit() (string, error) {
	h := sha256.New()
	var names []string
	err := fs.WalkDir(kit.Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || skipKitFile(p) {
			return err
		}
		b, err := kit.Files.ReadFile(p)
		if err != nil {
			return err
		}
		h.Write([]byte(p))
		h.Write(b)
		names = append(names, p)
		return nil
	})
	if err != nil {
		return "", err
	}
	root := filepath.Join(client.Home(), "kit", hex.EncodeToString(h.Sum(nil))[:12])
	if _, err := os.Stat(filepath.Join(root, ".complete")); err == nil {
		return root, nil
	}
	for _, p := range names {
		b, _ := kit.Files.ReadFile(p)
		dst := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(p, "bin/") {
			mode = 0o755
		}
		if err := os.WriteFile(dst, b, mode); err != nil {
			return "", err
		}
	}
	return root, os.WriteFile(filepath.Join(root, ".complete"), nil, 0o644)
}

func skipKitFile(p string) bool {
	return strings.Contains(p, "__pycache__/") || strings.HasSuffix(p, ".pyc") || strings.HasSuffix(p, ".go")
}
