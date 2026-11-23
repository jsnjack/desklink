// Package daemon wires configuration, desktop control, input injection, the
// network servers and the encoder into the running desklinkd service.
package daemon

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	stdnet "net"
	"net/netip"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"desklink/internal/config"
	"desklink/internal/crypto"
	"desklink/internal/desktop"
	"desklink/internal/input"
	dlnet "desklink/internal/net"
	"desklink/internal/video"
)

const restoreTimeout = 10 * time.Second

// Options configure Run.
type Options struct {
	Config  config.Config
	Paths   config.Paths
	Version string
	// OnListen is called with every bound address.
	OnListen func(network, addr string)
}

// Daemon is the running service.
type Daemon struct {
	opts        Options
	peers       *config.PeerStore
	state       *config.StateStore
	bus         *desktop.Bus
	cast        *desktop.ScreenCast
	injector    *input.Injector
	media       *dlnet.MediaServer
	pairer      *crypto.Pairer
	fingerprint string
	allow       func(netip.Addr) bool
	clipboard   *desktop.Clipboard
	launcher    *desktop.Launcher

	sessMu sync.Mutex
	active *activeSession

	// displayMu serialises monitor changes between sessions and startup.
	displayMu sync.Mutex

	// relockMu guards inheritedLock: a replaced session that unlocked a locked
	// desk passes the relock on instead of locking between two sessions,
	// which would flash the lock screen and empty GNOME's clipboard.
	relockMu      sync.Mutex
	inheritedLock bool
}

func (d *Daemon) passRelock() {
	d.relockMu.Lock()
	defer d.relockMu.Unlock()
	d.inheritedLock = true
}

func (d *Daemon) takeRelock() bool {
	d.relockMu.Lock()
	defer d.relockMu.Unlock()
	locked := d.inheritedLock
	d.inheritedLock = false
	return locked
}

type activeSession struct {
	cancel context.CancelFunc
	done   chan struct{}
	// replaced is set when a new connection takes over; the old session then
	// leaves the desk unlocked and hands its relock duty to the new one.
	replaced atomic.Bool
}

// Run sets everything up, failing fast on any setup error, and serves until
// ctx ends or a server fails.
func Run(ctx context.Context, opts Options) error {
	d, tlsCfg, err := setup(opts)
	if err != nil {
		return err
	}
	defer d.close()
	d.restoreLeftoverDisplay(ctx)
	go d.lockAfterAutologin(ctx)
	if ip, err := d.advertiseIP(); err != nil {
		slog.Warn("pairing address", "err", err)
	} else {
		slog.Info("pairing address", "ip", ip, "mac", macFor(ip))
		if note := wakeNote(ip); note != "" {
			slog.Warn("clients may not be able to wake this computer", "reason", note)
		}
	}

	socketPath, err := dlnet.LocalSocketPath()
	if err != nil {
		return err
	}
	control := &dlnet.ControlServer{
		Addr:     opts.Config.ControlAddr,
		TLS:      tlsCfg,
		Peers:    d.peers,
		Pairer:   d.pairer,
		Sessions: d,
		Allow:    d.allow,
		Health:   d.health,
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 3)
	var wg sync.WaitGroup
	start := func(name string, fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil {
				errs <- fmt.Errorf("%s: %w", name, err)
			}
			cancel()
		}()
	}
	opts.OnListen("udp", d.media.Addr().String())
	start("media server", func() error { return d.media.Serve(ctx) })
	start("control server", func() error {
		return control.Serve(ctx, func(a stdnet.Addr) { opts.OnListen("tcp", a.String()) })
	})
	start("local server", func() error {
		return dlnet.ServeLocal(ctx, socketPath, d, func(a stdnet.Addr) { opts.OnListen("unix", a.String()) })
	})
	go func() {
		<-ctx.Done()
		if err := d.media.Close(); err != nil {
			slog.Debug("close media socket", "err", err)
		}
	}()
	wg.Wait()
	// Shutdown does not wait for hijacked WebSocket connections; the session's
	// cleanup still needs D-Bus and the injector, which close after Run returns.
	d.sessMu.Lock()
	active := d.active
	d.sessMu.Unlock()
	if active != nil {
		active.cancel()
		<-active.done
	}
	close(errs)
	var all []error
	for err := range errs {
		all = append(all, err)
	}
	return errors.Join(all...)
}

func setup(opts Options) (*Daemon, *tls.Config, error) {
	cfg := opts.Config
	d := &Daemon{opts: opts}
	subnets, err := cfg.Subnets()
	if err != nil {
		return nil, nil, err
	}
	d.allow = dlnet.AllowList(subnets)
	if d.peers, err = config.OpenPeers(opts.Paths.Peers()); err != nil {
		return nil, nil, err
	}
	if d.state, err = config.OpenState(opts.Paths.State()); err != nil {
		return nil, nil, err
	}
	tlsCfg, fingerprint, err := dlnet.LoadOrCreateCert(opts.Paths.TLSCert(), opts.Paths.TLSKey())
	if err != nil {
		return nil, nil, err
	}
	d.fingerprint = fingerprint
	d.pairer = crypto.NewPairer(fingerprint, time.Now)

	video.Init()
	if !video.Available(video.CodecAV1) && !video.Available(video.CodecHEVC) {
		return nil, nil, errors.New("neither vaav1enc nor vah265enc is available: install gstreamer1-plugins-bad-free and the Mesa VA-API driver")
	}
	if d.bus, err = desktop.ConnectBus(); err != nil {
		return nil, nil, err
	}
	d.cast = desktop.NewScreenCast(d.bus.Session)
	if d.injector, err = input.OpenInjector(input.DevicePath); err != nil {
		return nil, nil, errors.Join(err, d.bus.Close())
	}
	if d.media, err = dlnet.ListenMedia(cfg.MediaAddr, d.allow); err != nil {
		return nil, nil, errors.Join(err, d.injector.Close(), d.bus.Close())
	}
	d.clipboard = desktop.NewClipboard(cfg.WaylandDisplay)
	d.launcher = desktop.NewLauncher(cfg.WaylandDisplay)
	return d, tlsCfg, nil
}

func (d *Daemon) close() {
	if err := d.injector.Close(); err != nil {
		slog.Warn("destroy virtual input devices", "err", err)
	}
	if err := d.bus.Close(); err != nil {
		slog.Debug("close D-Bus", "err", err)
	}
}

// restoreLeftoverDisplay undoes a resolution change left by a crashed run.
func (d *Daemon) restoreLeftoverDisplay(ctx context.Context) {
	raw := d.state.DisplaySnapshot()
	if raw == nil {
		return
	}
	d.displayMu.Lock()
	defer d.displayMu.Unlock()
	if err := d.restoreDisplayLocked(ctx, raw); err != nil {
		slog.Warn("restore monitor layout left by a previous run", "err", err)
	}
}

func (d *Daemon) restoreDisplayLocked(ctx context.Context, raw json.RawMessage) error {
	var layout desktop.DisplayLayout
	if err := json.Unmarshal(raw, &layout); err != nil {
		return errors.Join(fmt.Errorf("decode saved layout: %w", err), d.state.SetDisplaySnapshot(nil))
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
	defer cancel()
	if err := d.bus.RestoreLayout(ctx, layout); err != nil {
		if errors.Is(err, desktop.ErrStaleLayout) {
			// It can never apply again; keeping it would block fresh snapshots.
			return errors.Join(err, d.state.SetDisplaySnapshot(nil))
		}
		return err
	}
	return d.state.SetDisplaySnapshot(nil)
}

// ServeSession implements net.SessionHandler. A new connection replaces the
// running session, so reconnecting after a network change needs no timeout.
func (d *Daemon) ServeSession(ctx context.Context, conn *dlnet.ControlConn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	mine := &activeSession{cancel: cancel, done: make(chan struct{})}
	d.sessMu.Lock()
	prev := d.active
	d.active = mine
	d.sessMu.Unlock()
	if prev != nil {
		slog.Info("replacing the running session")
		prev.replaced.Store(true)
		prev.cancel()
		<-prev.done
	}
	defer func() {
		d.sessMu.Lock()
		if d.active == mine {
			d.active = nil
		}
		d.sessMu.Unlock()
		close(mine.done)
	}()
	slog.Info("session started", "peer", conn.Peer.Name, "from", conn.RemoteIP)
	// runSession reads the WebSocket with ctx, which ends only after the
	// error below is sent: cancelling a pending read closes the connection.
	err := d.runSession(ctx, conn, mine)
	if err != nil && ctx.Err() == nil {
		slog.Warn("session ended with error", "peer", conn.Peer.Name, "err", err)
		if serr := conn.Send(context.WithoutCancel(ctx), dlnet.MsgError, dlnet.ErrorInfo{Message: err.Error()}); serr != nil {
			slog.Debug("send session error", "err", serr)
		}
		return
	}
	slog.Info("session ended", "peer", conn.Peer.Name)
}

// BeginPairing implements net.PairingControl: it opens a pairing window with
// a fresh PIN.
func (d *Daemon) BeginPairing(context.Context) (dlnet.PairingTicket, error) {
	pin, expires, err := d.pairer.Begin()
	if err != nil {
		return dlnet.PairingTicket{}, err
	}
	_, portText, err := stdnet.SplitHostPort(d.opts.Config.ControlAddr)
	if err != nil {
		return dlnet.PairingTicket{}, fmt.Errorf("control_addr: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return dlnet.PairingTicket{}, fmt.Errorf("control_addr port: %w", err)
	}
	ip, err := d.advertiseIP()
	if err != nil {
		return dlnet.PairingTicket{}, err
	}
	return dlnet.PairingTicket{
		IP: ip.String(), Port: port, PIN: pin, WakeNote: wakeNote(ip),
		Fingerprint: d.fingerprint, Expires: expires,
	}, nil
}

// PairingStatus implements net.PairingControl.
func (d *Daemon) PairingStatus() dlnet.PairingStatus {
	state, device := d.pairer.State()
	return dlnet.PairingStatus{State: string(state), Device: device}
}

func (d *Daemon) health() dlnet.Health {
	host, err := os.Hostname()
	if err != nil {
		host = ""
	}
	return dlnet.Health{
		Name:     "desklinkd",
		Version:  d.opts.Version,
		Hostname: host,
		TimeMs:   time.Now().UnixMilli(),
		MAC:      d.mac(),
	}
}

// mac returns the hardware address of the interface holding the advertised
// address, for Wake-on-LAN from the client.
func (d *Daemon) mac() string {
	ip, err := d.advertiseIP()
	if err != nil {
		return ""
	}
	return macFor(ip)
}
