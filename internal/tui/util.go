package tui

import "strconv"

func itoa(n int) string { return strconv.Itoa(n) }

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return itoa(n) + " " + many
}

// listState is the cursor of a list: the selected row, remembered by a key so
// it stays on the same item when the data refreshes underneath it.
type listState struct {
	sel int
	off int
	key string
}

// clamp keeps the cursor inside n rows and the window of vis rows around it.
func (l *listState) clamp(n, vis int) {
	if l.sel >= n {
		l.sel = n - 1
	}
	if l.sel < 0 {
		l.sel = 0
	}
	if vis < 1 {
		vis = 1
	}
	if l.sel < l.off {
		l.off = l.sel
	}
	if l.sel >= l.off+vis {
		l.off = l.sel - vis + 1
	}
	if l.off < 0 {
		l.off = 0
	}
}

func (l *listState) move(d, n int) {
	l.sel += d
	if l.sel < 0 {
		l.sel = 0
	}
	if l.sel > n-1 {
		l.sel = n - 1
	}
	if l.sel < 0 {
		l.sel = 0
	}
}

// follow finds the remembered key in keys after a refresh.
func (l *listState) follow(keys []string) {
	if l.key != "" {
		for i, k := range keys {
			if k == l.key {
				l.sel = i
				return
			}
		}
	}
	if l.sel >= len(keys) {
		l.sel = len(keys) - 1
	}
	if l.sel < 0 {
		l.sel = 0
	}
}

func (l *listState) remember(keys []string) {
	if l.sel >= 0 && l.sel < len(keys) {
		l.key = keys[l.sel]
	} else {
		l.key = ""
	}
}

// panes splits the body into a list and a detail pane: side by side on a wide
// terminal, one above the other on a narrow one.
func panes(w, h int) (lw, lh, rw, rh int) {
	if w >= 110 {
		lw = w * 58 / 100
		return lw, h, w - lw, h
	}
	rh = 9
	if h < 20 {
		rh = h / 2
	}
	return w, h - rh, w, rh
}
