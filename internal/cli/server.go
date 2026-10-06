package cli

import (
	"context"
	"fmt"
	"handloom/internal/setting"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"handloom/internal/api"
	"handloom/internal/client"
	"handloom/internal/hub"
	"handloom/internal/link"
	"handloom/internal/notify"
	"handloom/internal/store"
)

func dataDir(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if d := setting.Get("DATA"); d != "" {
		return d
	}
	if _, err := os.Stat("handloom-data"); err != nil {
		if _, oldErr := os.Stat("handloom-data"); oldErr == nil {
			return "handloom-data" // a data directory made under the old name
		}
	}
	return "handloom-data"
}

// dbFile is the database in dir. A hub created under the old name has
// handloom.db; it keeps using it. New hubs get handloom.db.
func dbFile(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, "handloom.db")); err != nil {
		if _, oldErr := os.Stat(filepath.Join(dir, "handloom.db")); oldErr == nil {
			return filepath.Join(dir, "handloom.db")
		}
	}
	return filepath.Join(dir, "handloom.db")
}

func (e *env) hub(args []string) error {
	sub := "serve"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		sub, args = args[0], args[1:]
	}
	fs := e.flags("hub " + sub)
	data := fs.String("data", "", "data directory (default $HANDLOOM_DATA or ./handloom-data)")
	defAddr := setting.Get("ADDR")
	if defAddr == "" {
		defAddr = "127.0.0.1:7420"
	}
	addr := fs.String("addr", defAddr, "listen address (default $HANDLOOM_ADDR or 127.0.0.1:7420)")
	lease := fs.Duration("lease", 15*time.Minute, "task lease")
	sweep := fs.Duration("sweep", 5*time.Second, "how often expired leases are collected")
	unclaimed := fs.Duration("unclaimed", 10*time.Minute, "tell the lead when an assigned task stays unclaimed this long")
	agentLease := fs.Duration("agent-lease", 90*time.Second, "mark an agent with a terminal offline when its link has not vouched for it this long")
	autoInit := fs.Bool("auto-init", false, "serve: create the database on first start and print the admin token to the log (containers)")
	to := fs.String("to", "", "backup: file to write")
	from := fs.String("from", "", "restore: backup file to restore")
	force := fs.Bool("force", false, "restore: replace an existing database (stop the hub first)")
	pos, err := fs.need(args, 0, 1, "hub init|serve|backup|restore|reset-password|install|uninstall [--data DIR] [--addr A] [--lease D]")
	if err != nil {
		return err
	}
	if len(pos) > 0 && sub != "reset-password" {
		return usageErr("usage: handloom hub %s takes no arguments", sub)
	}
	dir := dataDir(*data)
	dbPath := dbFile(dir)

	switch sub {
	case "install":
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		if _, err := os.Stat(dbFile(abs)); err != nil {
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
	case "backup":
		if *to == "" {
			return usageErr("usage: handloom hub backup --to FILE [--data DIR]   (safe while the hub runs)")
		}
		db, err := store.Open(dbPath)
		if err != nil {
			return err
		}
		defer db.Close()
		if err := store.Backup(db, *to); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Backup written to %s (mode 0600). It holds hashed tokens and all hub data: keep it private.\n", *to)
		return nil
	case "reset-password":
		// Local recovery: whoever can read the database may set a password.
		name := ""
		if len(pos) > 0 {
			name = pos[0]
		}
		if name == "" {
			return usageErr("usage: handloom hub reset-password <name> [--data DIR]   (prints a new password once)")
		}
		db, err := store.Open(dbPath)
		if err != nil {
			return err
		}
		defer db.Close()
		pw, err := hub.ResetPassword(db, name, time.Now())
		if err != nil {
			return err
		}
		fmt.Fprintf(e.out, "New password for %s (shown once; every session of that human is signed out):\n%s\n", name, pw)
		return nil
	case "restore":
		if *from == "" {
			return usageErr("usage: handloom hub restore --from FILE [--data DIR] [--force]   (stop the hub first)")
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := store.Restore(*from, dbPath, *force); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Restored %s from %s. Start the hub again.\n", dbPath, *from)
		return nil
	case "serve":
		logger := log.New(e.err, "hub ", log.LstdFlags)
		if _, err := os.Stat(dbPath); err != nil {
			if !*autoInit {
				return fmt.Errorf("no database at %s: run `handloom hub init --data %s` first", dbPath, dir)
			}
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			idb, err := store.Open(dbPath)
			if err != nil {
				return err
			}
			tok, err := store.Init(idb, time.Now())
			idb.Close()
			if err != nil {
				return err
			}
			logger.Printf("first start: database created at %s. Admin token (shown once, store it safely): %s", dbPath, tok)
		}
		db, err := store.Open(dbPath)
		if err != nil {
			return err
		}
		defer db.Close()
		notifier, err := notifierFromEnv()
		if err != nil {
			return err
		}
		h := hub.New(db, hub.Options{Lease: *lease, Sweep: *sweep, Unclaimed: *unclaimed, Log: logger,
			Notifier: notifier, BaseURL: setting.Get("BASE_URL"),
			Insecure: setting.Bool("INSECURE"), TrustProxy: setting.Bool("TRUST_PROXY"), NtfyToken: setting.Get("NTFY_TOKEN"), AllowConfidential: setting.Bool("ALLOW_CONFIDENTIAL"), RequireRunToken: setting.Bool("REQUIRE_RUN_TOKEN"), AgentLease: *agentLease})
		if ok, why := h.WebEnabled(); !ok {
			logger.Printf("web UI is off: %s", why)
		} else if code, err := h.PrepareSetup(); err != nil {
			return err
		} else if code != "" {
			logger.Printf("this hub has no owner yet. Open %s/setup and enter the setup code: %s", strings.TrimRight(setting.Get("BASE_URL"), "/"), code)
		}
		ln, err := net.Listen("tcp", *addr)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		go h.Run(ctx)
		// No WriteTimeout: event streams and the device long-poll outlive any fixed
		// one; streams clear their own write deadline and bound their own life.
		srv := &http.Server{Handler: h.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
			IdleTimeout: 120 * time.Second, MaxHeaderBytes: 64 << 10}
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
	return usageErr("usage: handloom hub init|serve|backup|restore|install|uninstall")
}

// notifierFromEnv reads the notification settings. Secrets stay out of argv:
// HANDLOOM_NTFY_URL, HANDLOOM_NTFY_TOPIC, HANDLOOM_NTFY_TOKEN, HANDLOOM_WEBHOOK_URL.
func notifierFromEnv() (notify.Notifier, error) {
	var m notify.Multi
	if u := setting.Get("NTFY_URL"); u != "" {
		n, err := notify.NewNtfy(u, setting.Get("NTFY_TOPIC"), setting.Get("NTFY_TOKEN"))
		if err != nil {
			return nil, err
		}
		m = append(m, n)
	}
	if u := setting.Get("WEBHOOK_URL"); u != "" {
		w, err := notify.NewWebhook(u)
		if err != nil {
			return nil, err
		}
		m = append(m, w)
	}
	if len(m) == 0 {
		return nil, nil
	}
	return m, nil
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

// healthcheck is for container HEALTHCHECK: distroless images have no curl.
func (e *env) healthcheck(args []string) error {
	fs := e.flags("healthcheck")
	addr := fs.String("addr", "", "hub address (default $HANDLOOM_ADDR or 127.0.0.1:7420)")
	if _, err := fs.need(args, 0, 0, "healthcheck [--addr A]"); err != nil {
		return err
	}
	a := *addr
	if a == "" {
		a = setting.Get("ADDR")
	}
	if a == "" {
		a = "127.0.0.1:7420"
	}
	if h, p, err := net.SplitHostPort(a); err == nil && (h == "" || h == "0.0.0.0" || h == "::") {
		a = net.JoinHostPort("127.0.0.1", p)
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + a + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("healthz answered %s", resp.Status)
	}
	fmt.Fprintln(e.out, "ok")
	return nil
}
