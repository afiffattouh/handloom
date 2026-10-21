package link

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"handloom/internal/addon"
	"handloom/internal/api"
)

// Add-ons, link side. Installing is a local act done with the handloom command, in another process, so the
// link looks at the add-on folder each time the ladder runs and tells the hub whenever what it finds is not
// what it last told. It also tells the hub on start, which clears anything the hub still holds for this
// computer that is gone. The hub only registers the report; nothing here starts an add-on.

// maxManifestTotal is the most manifest text one report carries in all. Past it the rest are sent without
// their text, so a few large manifests cannot keep the whole report from being accepted.
const maxManifestTotal = 4 << 20

// addonsResendEvery is how long an accepted report stands before the full list is sent again anyway. The link
// cannot see what the hub holds, so this is what puts the rows right after the hub lost or cleared them.
const addonsResendEvery = 5 * time.Minute

// addonItems is what this computer has installed, in the words of the report.
func (l *Link) addonItems() ([]api.AddonItem, error) {
	root := l.opt.AddonRoot
	list, err := addon.Installed(root, addon.NewStore(root))
	if err != nil {
		return nil, err
	}
	items := []api.AddonItem{}
	total := 0
	for _, in := range list {
		it := api.AddonItem{Name: in.Name, Version: in.Version, PackageHash: in.PackageHash, ManifestHash: in.ManifestHash, Status: in.State, Detail: in.Detail}
		if in.Manifest != nil {
			if text, err := os.ReadFile(filepath.Join(root, in.Name, addon.ManifestFile)); err == nil && len(text) <= addon.MaxManifestBytes && total+len(text) <= maxManifestTotal {
				it.Manifest = string(text)
				total += len(text)
			}
		}
		items = append(items, it)
	}
	return items, nil
}

// runAddons reports the installed add-ons when they differ from the last report the hub accepted, or when that
// report is older than addonsResendEvery.
func (l *Link) runAddons(ctx context.Context) {
	items, err := l.addonItems()
	if err != nil {
		l.opt.Log.Printf("add-ons: %v", err)
		return
	}
	sig, _ := json.Marshal(items)
	l.mu.Lock()
	now := l.opt.Now()
	same := l.addonsSent != nil && string(l.addonsSent) == string(sig) && now.Sub(l.addonsAt) < addonsResendEvery
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
	l.addonsAt = now
	l.mu.Unlock()
}
