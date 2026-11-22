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
	"time"

	"desklink/internal/config"
	"desklink/internal/crypto"
	"desklink/internal/desktop"
	"desklink/internal/input"
	dlnet "desklink/internal/net"
	"desklink/internal/video"
)

const (
	captureGrantTimeout = 3 * time.Minute
	restoreTimeout      = 10 * time.Second
)

// Capture grant states reported to `desklinkd pair`.
const (
	captureUnknown = "unknown"
	capturePending = "pending"
	captureGranted = "granted"
)

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

	captureMu     sync.Mutex
	captureStatus string
}

type activeSession struct {
	cancel context.CancelFunc
	done   chan struct{}
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
	close(errs)
	var all []error
	for err := range errs {
		all = append(all, err)
	}
	return errors.Join(all...)
}

func setup(opts Options) (*Daemon, *tls.Config, error) {
	cfg := opts.Config
	d := &Daemon{opts: opts, captureStatus: captureUnknown}
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
	d.cast = desktop.NewScreenCast(d.bus.Session, d.state)
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
	err := d.runSession(ctx, conn)
	if err != nil && ctx.Err() == nil {
		slog.Warn("session ended with error", "peer", conn.Peer.Name, "err", err)
		if serr := conn.Send(context.WithoutCancel(ctx), dlnet.MsgError, dlnet.ErrorInfo{Message: err.Error()}); serr != nil {
			slog.Debug("send session error", "err", serr)
		}
		return
	}
	slog.Info("session ended", "peer", conn.Peer.Name)
}

// BeginPairing implements net.PairingControl. Besides the PIN, it starts a
// screen cast so GNOME asks for screen-sharing permission now, while the user
// is at the desk; the restore token then lets remote sessions start silently.
func (d *Daemon) BeginPairing(ctx context.Context) (dlnet.PairingTicket, error) {
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
	d.sessMu.Lock()
	busy := d.active != nil
	d.sessMu.Unlock()
	if busy {
		d.setCaptureStatus(captureGranted)
	} else {
		d.setCaptureStatus(capturePending)
		go d.grantCapture(context.WithoutCancel(ctx))
	}
	return dlnet.PairingTicket{
		IP: d.opts.Config.AdvertiseIP, Port: port, PIN: pin,
		Fingerprint: d.fingerprint, Expires: expires,
	}, nil
}

func (d *Daemon) grantCapture(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, captureGrantTimeout)
	defer cancel()
	c, err := d.cast.Start(ctx)
	if err != nil {
		slog.Warn("screen-sharing permission", "err", err)
		d.setCaptureStatus(err.Error())
		return
	}
	if err := c.Close(); err != nil {
		slog.Debug("close permission screencast", "err", err)
	}
	d.setCaptureStatus(captureGranted)
}

func (d *Daemon) setCaptureStatus(s string) {
	d.captureMu.Lock()
	defer d.captureMu.Unlock()
	d.captureStatus = s
}

// PairingStatus implements net.PairingControl.
func (d *Daemon) PairingStatus() dlnet.PairingStatus {
	state, device := d.pairer.State()
	d.captureMu.Lock()
	defer d.captureMu.Unlock()
	return dlnet.PairingStatus{State: string(state), Device: device, Capture: d.captureStatus}
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
		MAC:      macFor(d.opts.Config.AdvertiseIP),
	}
}

// macFor returns the hardware address of the interface holding ip, for
// Wake-on-LAN from the client.
func macFor(ip string) string {
	want, err := netip.ParseAddr(ip)
	if err != nil {
		return ""
	}
	ifaces, err := stdnet.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifaces {
		addrs, err := ifc.Addrs()
		if err != nil || len(ifc.HardwareAddr) == 0 {
			continue
		}
		for _, a := range addrs {
			if p, err := netip.ParsePrefix(a.String()); err == nil && p.Addr() == want {
				return ifc.HardwareAddr.String()
			}
		}
	}
	return ""
}
