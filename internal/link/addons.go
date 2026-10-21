package link

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"handloom/internal/addon"
	"handloom/internal/api"
)

// Add-ons, link side. Installing is a local act done with the handloom command, in another process, so the
// link looks at the add-on folder each time the ladder runs and tells the hub whenever what it finds is not
// what it last told. It also tells the hub on start, which clears anything the hub still holds for this
// computer that is gone. The hub only registers the report; nothing here starts an add-on.

// maxManifestReport is the longest manifest text sent; the hub refuses more.
const maxManifestReport = 64 << 10

// addonItems is what this computer has installed, in the words of the report.
func (l *Link) addonItems() ([]api.AddonItem, error) {
	root := l.opt.AddonRoot
	list, err := addon.Installed(root, addon.NewStore(root))
	if err != nil {
		return nil, err
	}
	items := []api.AddonItem{}
	for _, in := range list {
		it := api.AddonItem{Name: in.Name, Version: in.Version, PackageHash: in.PackageHash, ManifestHash: in.ManifestHash, Status: in.State, Detail: in.Detail}
		if in.Manifest != nil {
			if text, err := os.ReadFile(filepath.Join(root, in.Name, addon.ManifestFile)); err == nil && len(text) <= maxManifestReport {
				it.Manifest = string(text)
			}
		}
		items = append(items, it)
	}
	return items, nil
}

// runAddons reports the installed add-ons when they differ from the last report the hub accepted.
func (l *Link) runAddons(ctx context.Context) {
	items, err := l.addonItems()
	if err != nil {
		l.opt.Log.Printf("add-ons: %v", err)
		return
	}
	sig, _ := json.Marshal(items)
	l.mu.Lock()
	same := l.addonsSent != nil && string(l.addonsSent) == string(sig)
	l.mu.Unlock()
	if same {
		return
	}
	if err := l.hub.Do(ctx, "POST", "/v1/device/addons", api.AddonReport{Addons: items}, nil); err != nil {
		if ctx.Err() == nil {
			l.opt.Log.Printf("report add-ons: %v", err)
		}
		return
	}
	l.mu.Lock()
	l.addonsSent = sig
	l.mu.Unlock()
}
