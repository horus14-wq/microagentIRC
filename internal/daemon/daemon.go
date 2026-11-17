// Package daemon is `mairc daemon`: the one long-running process. It owns the database, serves MCP over Streamable
// HTTP, answers the hook endpoint, reloads settings and prunes old messages.
package daemon

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/horus14-wq/microagentIRC/internal/chat"
	"github.com/horus14-wq/microagentIRC/internal/config"
	"github.com/horus14-wq/microagentIRC/internal/core"
	"github.com/horus14-wq/microagentIRC/internal/mcp"
	"github.com/horus14-wq/microagentIRC/internal/store"
	"github.com/horus14-wq/microagentIRC/internal/tools"
)

// serverName is the MCP server name.
const serverName = "mairc"

// pruneEvery is the retention ticker's period.
const pruneEvery = time.Minute

// shutdownGrace is how long in-flight requests may run after a stop.
const shutdownGrace = 2 * time.Second

// The HTTP server timeouts.
const (
	readHeaderTimeout = 2 * time.Second
	readTimeout       = 5 * time.Second
	writeTimeout      = 5 * time.Second
	idleTimeout       = 60 * time.Second
)

// Options configure a daemon. Zero values mean production defaults.
type Options struct {
	Paths   config.Paths
	Version string
	Clock   core.Clock                       // default: wall clock
	Logf    func(format string, args ...any) // default: writes to Paths.Log
}

// wallClock is the production clock.
type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

// Run starts the daemon and blocks until ctx is cancelled or a fatal error. It returns nil on a clean stop and
// also when another daemon already holds the listen address ("already running").
func Run(ctx context.Context, o Options) error {
	if o.Clock == nil {
		o.Clock = wallClock{}
	}
	if err := config.EnsureHome(o.Paths); err != nil {
		return err
	}
	logf := o.Logf
	if logf == nil {
		f, err := os.OpenFile(o.Paths.Log, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		logf = fileLogger(f, o.Clock)
	}
	err := run(ctx, o, logf)
	if err != nil {
		logf("fatal: %v", err)
	}
	return err
}

// fileLogger writes one timestamped line per call.
func fileLogger(w io.Writer, clock core.Clock) func(format string, args ...any) {
	var mu sync.Mutex
	return func(format string, args ...any) {
		line := clock.Now().UTC().Format(time.RFC3339) + " " + fmt.Sprintf(format, args...) + "\n"
		mu.Lock()
		defer mu.Unlock()
		_, _ = io.WriteString(w, line)
	}
}

func run(ctx context.Context, o Options, logf func(format string, args ...any)) error {
	settings, err := config.Load(o.Paths.Config)
	if err != nil {
		return err
	}
	if err := ensureToken(o.Paths.Token); err != nil {
		return err
	}
	token, err := config.ReadToken(o.Paths.Token)
	if err != nil {
		return err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", settings.Listen)
	if err != nil {
		if isAddrInUse(err) && healthy(ctx, settings.Listen) {
			logf("already running")
			return nil
		}
		return err
	}
	st, err := store.Open(ctx, o.Paths.DB)
	if err != nil {
		_ = ln.Close()
		return err
	}
	defer func() { _ = st.Close() }()

	cur := &current{settings: settings}
	service := chat.New(ctx, st, o.Clock, settings, rand.IntN)
	reload := reloader(o.Paths.Config, service, cur, logf)
	srv := newServer(service, mcp.New(serverName, o.Version, tools.All(service)), token, o.Version, reload,
		o.Clock, logf)
	// The machine tokens beside the hub token, now and on every reload (`mairc token rotate|add|revoke`).
	srv.loadTokens = func() (string, map[string]string, error) { return loadTokens(o.Paths.Token, o.Clock.Now()) }
	hub, machines, err := srv.loadTokens()
	if err != nil {
		_ = ln.Close()
		return err
	}
	srv.setTokens(hub, machines)
	// Which name each agent CLI session's hook runs for survives a restart: it is in the same database.
	if err := srv.useBindings(ctx, st); err != nil {
		_ = ln.Close()
		return err
	}
	if err := os.WriteFile(o.Paths.PID, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		_ = ln.Close()
		return err
	}
	defer func() { _ = os.Remove(o.Paths.PID) }()

	httpServer := &http.Server{
		Handler:           srv,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
	// The shims' watches never end by themselves; a shutdown must not wait for them.
	httpServer.RegisterOnShutdown(srv.stop)
	served := make(chan error, 1)
	go func() { served <- httpServer.Serve(ln) }()
	logf("start %s %s", o.Version, settings.Listen)

	ticker := time.NewTicker(pruneEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := o.Clock.Now()
			s := cur.get()
			if _, err := st.Prune(ctx, now, s.KeepHours, s.KeepMessages); err != nil {
				logf("error: prune: %v", err)
			}
			srv.dropIdle(ctx, now)
			// Tokens with an end stop working within a minute of it, with no reload asked for.
			if hub, machines, err := srv.loadTokens(); err == nil {
				srv.setTokens(hub, machines)
			}
		case err := <-served:
			return err
		case <-ctx.Done():
			// ctx is already cancelled here; the grace period keeps its values but not its cancellation.
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
			err := httpServer.Shutdown(stopCtx)
			cancel()
			if err != nil {
				_ = httpServer.Close()
			}
			if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
				logf("error: serve: %v", err)
			}
			logf("stop")
			return nil
		}
	}
}

// reloader returns the reload function: it re-reads and validates the settings file and puts it in force. An
// invalid file is an error and the settings in force stay.
func reloader(path string, c core.Chat, cur *current, logf func(format string, args ...any)) func() error {
	return func() error {
		s, err := config.Load(path)
		if err != nil {
			logf("reload failed: %v", err)
			return err
		}
		c.Reload(s)
		cur.set(s)
		logf("reload")
		return nil
	}
}

// current holds the settings in force for the retention ticker.
type current struct {
	mu       sync.Mutex
	settings core.Settings
}

func (c *current) get() core.Settings {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.settings
}

func (c *current) set(s core.Settings) {
	c.mu.Lock()
	c.settings = s
	c.mu.Unlock()
}

// ensureToken creates the token file, 32 random bytes as 64 hex characters, mode 0600, when it is missing. As
// with config.json, 0600 is the Unix guarantee; on Windows the file is private because the data directory is per-user.
// loadTokens reads what the daemon accepts: the hub token, and for each machine token its SHA-256 and the machine
// it belongs to.
func loadTokens(tokenPath string, now time.Time) (hub string, machines map[string]string, err error) {
	if hub, err = config.ReadToken(tokenPath); err != nil {
		return "", nil, err
	}
	if hub == "" {
		return "", nil, errors.New(tokenPath + " is empty")
	}
	listed, err := config.LoadMachines(config.MachinesPath(tokenPath))
	if err != nil {
		return "", nil, err
	}
	machines = make(map[string]string, len(listed))
	for name, m := range listed {
		// A token with an end (a rotated hub token kept for a while) is dropped once the end has passed.
		if config.ValidMachine(name) && len(m.SHA256) == 64 && m.Live(now) {
			machines[m.SHA256] = name
		}
	}
	return hub, machines, nil
}

func ensureToken(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var raw [32]byte
	if _, err := cryptorand.Read(raw[:]); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, hex.EncodeToString(raw[:])); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// healthy reports whether a daemon answers `ok` on /healthz at addr.
func healthy(ctx context.Context, addr string) bool {
	ctx, cancel := context.WithTimeout(ctx, readHeaderTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16))
	return err == nil && resp.StatusCode == http.StatusOK && string(body) == "ok"
}
