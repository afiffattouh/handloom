package cli

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"handloom/internal/api"
	"handloom/internal/client"
	"handloom/internal/hub"
	"handloom/internal/link"
	"handloom/internal/store"
)

func dataDir(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if d := os.Getenv("HANDLOOM_DATA"); d != "" {
		return d
	}
	return "handloom-data"
}

func (e *env) hub(args []string) error {
	sub := "serve"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		sub, args = args[0], args[1:]
	}
	fs := e.flags("hub " + sub)
	data := fs.String("data", "", "data directory (default $HANDLOOM_DATA or ./handloom-data)")
	addr := fs.String("addr", "127.0.0.1:7420", "listen address")
	lease := fs.Duration("lease", 15*time.Minute, "task lease")
	sweep := fs.Duration("sweep", 5*time.Second, "how often expired leases are collected")
	unclaimed := fs.Duration("unclaimed", 10*time.Minute, "tell the lead when an assigned task stays unclaimed this long")
	if _, err := fs.need(args, 0, 0, "hub init|serve|install|uninstall [--data DIR] [--addr A] [--lease D]"); err != nil {
		return err
	}
	dir := dataDir(*data)
	dbPath := filepath.Join(dir, "handloom.db")

	switch sub {
	case "install":
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(abs, "handloom.db")); err != nil {
			return fmt.Errorf("no database in %s: run `handloom hub init --data %s` first", abs, abs)
		}
		return e.installService(service{
			name: "handloom-hub", description: "handloom hub",
			args: []string{"hub", "serve", "--data", abs, "--addr", *addr, "--lease", lease.String(), "--unclaimed", unclaimed.String()},
		})
	case "uninstall":
		return e.uninstallService("handloom-hub")
	case "init":
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		db, err := store.Open(dbPath)
		if err != nil {
			return err
		}
		defer db.Close()
		tok, err := store.Init(db, time.Now())
		if err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Hub database created at %s\n\nAdmin token (shown once, store it safely):\n%s\n", dbPath, tok)
		return nil
	case "serve":
		if _, err := os.Stat(dbPath); err != nil {
			return fmt.Errorf("no database at %s: run `handloom hub init --data %s` first", dbPath, dir)
		}
		db, err := store.Open(dbPath)
		if err != nil {
			return err
		}
		defer db.Close()
		logger := log.New(e.err, "hub ", log.LstdFlags)
		h := hub.New(db, hub.Options{Lease: *lease, Sweep: *sweep, Unclaimed: *unclaimed, Log: logger})
		ln, err := net.Listen("tcp", *addr)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		go h.Run(ctx)
		srv := &http.Server{Handler: h.Handler(), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			<-ctx.Done()
			shut, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			srv.Shutdown(shut)
		}()
		logger.Printf("listening on %s, protocol %s, lease %s", ln.Addr(), api.Version, *lease)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	}
	return usageErr("usage: handloom hub init|serve|install|uninstall")
}

func (e *env) link(args []string) error {
	sub := "run"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "join":
		fs := e.flags("link join")
		pos, err := fs.need(args, 2, 2, "link join <hub-url> <join-token>")
		if err != nil {
			return err
		}
		var resp api.JoinResp
		if err := client.Direct(pos[0], "").Post("/v1/devices/join", api.JoinReq{JoinToken: pos[1]}, &resp); err != nil {
			return err
		}
		cfg := &client.LinkConfig{Hub: pos[0], Device: resp.Device, Credential: resp.Credential}
		if err := client.SaveLinkConfig(cfg); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Joined %s as device %s. Credential saved in %s (mode 0600).\nStart the link with: handloom link run\n",
			pos[0], resp.Device, client.Home())
		return nil
	case "run":
		fs := e.flags("link run")
		nudge := fs.Duration("nudge-every", 0, "at most one nudge per agent in this time (default 60s)")
		renudge := fs.Duration("renudge", 0, "nudge again when mail stays unread this long (default 5m)")
		unknown := fs.Duration("unknown-after", 0, "mark an agent unknown after two unanswered nudges for this long (default 10m)")
		heartbeat := fs.Duration("heartbeat", 0, "extend leases of an active agent this often (default 2m)")
		if _, err := fs.need(args, 0, 0, "link run"); err != nil {
			return err
		}
		cfg, err := client.LoadLinkConfig()
		if err != nil {
			return err
		}
		l := link.New(link.Options{
			Hub: cfg.Hub, Device: cfg.Device, Credential: cfg.Credential, Socket: client.SocketPath(),
			NudgeEvery: *nudge, Renudge: *renudge, UnknownAfter: *unknown, Heartbeat: *heartbeat,
			Log: log.New(e.err, "link ", log.LstdFlags),
		})
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return l.Run(ctx)
	case "install":
		fs := e.flags("link install")
		if _, err := fs.need(args, 0, 0, "link install"); err != nil {
			return err
		}
		if _, err := client.LoadLinkConfig(); err != nil {
			return err
		}
		home, err := filepath.Abs(client.Home())
		if err != nil {
			return err
		}
		return e.installService(service{
			name: "handloom-link", description: "handloom link (device daemon)",
			args: []string{"link", "run"}, env: map[string]string{"HANDLOOM_HOME": home},
		})
	case "uninstall":
		return e.uninstallService("handloom-link")
	case "status":
		fs := e.flags("link status")
		if _, err := fs.need(args, 0, 0, "link status"); err != nil {
			return err
		}
		var st map[string]any
		if err := client.Socket(client.SocketPath(), "").Get("/local/status", &st); err != nil {
			return fmt.Errorf("the link is not running (%s): %v", client.SocketPath(), err)
		}
		e.print(st, func() {
			fmt.Fprintf(e.out, "link is running: device %v, hub %v, %v\n", st["device"], st["hub"], st["protocol"])
		})
		return nil
	}
	return usageErr("usage: handloom link join|run|install|uninstall|status")
}
