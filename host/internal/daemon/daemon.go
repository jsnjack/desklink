// Package daemon wires configuration, desktop control, input injection, the
// network servers and the encoder into the running desklinkd service.
package daemon

import (
	"bytes"
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
	"strings"
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
	restoreTimeout = 10 * time.Second
	// errorSendTimeout bounds sending the error that ends or refuses a
	// session, so a client that stops reading cannot keep ServeSession
	// from returning.
	errorSendTimeout = 5 * time.Second
)

// injector is what sessions inject input through: *input.Injector, or a
// fake in tests.
type injector interface {
	Handle(ev input.Event) error
	HandleAt(seq uint32, ev input.Event) error
	Reconcile(seq uint32, h input.Held) error
	ReleaseAll() error
	Close() error
}

// display is how the daemon reads and changes the monitor layout:
// *desktop.Bus, or a fake in tests.
type display interface {
	DisplayState(ctx context.Context) (desktop.DisplayState, error)
	ApplyLayout(ctx context.Context, serial uint32, layout desktop.DisplayLayout) error
	RestoreLayout(ctx context.Context, layout desktop.DisplayLayout) error
}

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
	display     display
	cast        *desktop.ScreenCast
	injector    injector
	media       *dlnet.MediaServer
	pairer      *crypto.Pairer
	fingerprint string
	allow       func(netip.Addr) bool
	clipboard   *desktop.Clipboard
	launcher    *desktop.Launcher

	// stopCast stops a screen cast: (*desktop.Capture).Close, or a fake in
	// tests.
	stopCast func(*desktop.Capture) error
	// ctl decides which session controls the desk.
	ctl *sessionControl
	// awake keeps the computer from suspending for a while after a session.
	awake *keepAwake

	// displayMu serialises monitor changes between sessions and startup.
	displayMu sync.Mutex
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
	d.restoreLeftoverAudio(ctx)
	go d.lockAfterAutologin(ctx)
	if withdraw, err := d.announce(ctx); err != nil {
		slog.Warn("announce on the local network; clients must know the address", "err", err)
	} else {
		defer withdraw()
	}
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
		Forget: func(id string) error {
			_, err := d.RemovePeer(id)
			return err
		},
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
	if active := d.ctl.current(); active != nil {
		active.cancel()
		<-active.done
	}
	d.awake.stop()
	d.saveRemovals()
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
	d.display = d.bus
	d.cast = desktop.NewScreenCast(d.bus.Session)
	d.stopCast = (*desktop.Capture).Close
	d.ctl = &sessionControl{paired: d.isPaired, lock: d.bus.LockSession}
	d.awake = newKeepAwake(func(ctx context.Context) (func() error, error) {
		return d.bus.InhibitSuspend(ctx, inhibitApp, keepAwakeReason)
	})
	in, err := input.OpenInjector(input.DevicePath)
	if err != nil {
		return nil, nil, errors.Join(err, d.bus.Close())
	}
	d.injector = in
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

// restoreLeftoverAudio removes a virtual output that a crashed run left
// behind and makes the saved output the default again.
func (d *Daemon) restoreLeftoverAudio(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, restoreTimeout)
	defer cancel()
	if err := desktop.RemoveLeftoverAudio(ctx, d.state.AudioDefaultSink()); err != nil {
		slog.Debug("remove audio output left by a previous run", "err", err)
		return
	}
	if err := d.state.SetAudioDefaultSink(""); err != nil {
		slog.Debug("clear saved audio output", "err", err)
	}
}

func (d *Daemon) restoreDisplayLocked(ctx context.Context, raw json.RawMessage) error {
	var layout desktop.DisplayLayout
	if err := json.Unmarshal(raw, &layout); err != nil {
		return errors.Join(fmt.Errorf("decode saved layout: %w", err), d.state.SetDisplaySnapshot(nil))
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
	defer cancel()
	if err := d.display.RestoreLayout(ctx, layout); err != nil {
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
	// The running session keeps control until this one has said hello, so a
	// connection that drops or stalls before then cannot end it.
	hello, err := receiveHello(ctx, conn)
	if err != nil {
		slog.Warn("session did not start", "peer", conn.Peer.Name, "from", conn.RemoteIP, "err", err)
		return
	}
	// Admission checks the device is still paired, under the same lock as
	// revocation: unpairing after this connection authenticated refuses it.
	mine, prev, err := d.ctl.admit(conn.Peer, cancel)
	if err != nil {
		slog.Warn("refuse session", "peer", conn.Peer.Name, "from", conn.RemoteIP, "err", err)
		sendFinalError(ctx, conn, err)
		return
	}
	defer d.ctl.finish(ctx, mine)
	if err := d.peers.Touch(conn.Peer.ID, time.Now()); err != nil {
		slog.Debug("record when the peer was last seen", "err", err)
	}
	if prev != nil {
		slog.Info("replacing the running session")
		prev.cancel()
		<-prev.done
	}
	slog.Info("session started", "peer", conn.Peer.Name, "from", conn.RemoteIP)
	// runSession reads the WebSocket with ctx, which ends only after the
	// error below is sent: cancelling a pending read closes the connection.
	err = d.runSession(ctx, conn, mine, hello)
	if err != nil && ctx.Err() == nil {
		slog.Warn("session ended with error", "peer", conn.Peer.Name, "err", err)
		sendFinalError(ctx, conn, err)
		return
	}
	slog.Info("session ended", "peer", conn.Peer.Name)
}

// sendFinalError tells the client why its session ended or was refused. It
// sends even when ctx has ended, but gives up after errorSendTimeout.
func sendFinalError(ctx context.Context, conn *dlnet.ControlConn, err error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), errorSendTimeout)
	defer cancel()
	if serr := conn.Send(ctx, dlnet.MsgError, dlnet.ErrorInfo{Message: err.Error()}); serr != nil {
		slog.Debug("send session error", "err", serr)
	}
}

// isPaired reports whether the device is still paired with this key.
func (d *Daemon) isPaired(id string, key []byte) bool {
	p, ok := d.peers.Get(id)
	return ok && bytes.Equal(p.Key, key)
}

// ListPeers implements net.LocalControl.
func (d *Daemon) ListPeers() []dlnet.PeerInfo {
	connected := ""
	if active := d.ctl.current(); active != nil {
		connected = active.peerID
	}
	var out []dlnet.PeerInfo
	for _, p := range d.peers.List() {
		out = append(out, peerInfo(p, p.ID == connected))
	}
	for _, p := range d.peers.Unsaved() {
		info := peerInfo(p, false)
		info.RemovalNotSaved = true
		out = append(out, info)
	}
	return out
}

// RemovePeer implements net.LocalControl: the device can no longer connect,
// and a session it has running ends now. If peers.json cannot be saved, the
// error says the removal is incomplete; removing the device again retries.
func (d *Daemon) RemovePeer(id string) (dlnet.PeerInfo, error) {
	var p config.Peer
	var found bool
	// Unpairing and ending the device's session happen together, so a
	// session still starting for it cannot become active afterwards.
	connected, err := d.ctl.revoke(id, func() error {
		var err error
		p, found, err = d.peers.Remove(id)
		return err
	})
	if !found {
		return dlnet.PeerInfo{}, dlnet.ErrUnknownPeer
	}
	// Keeping the computer awake was the device's wish; it ends with the
	// pairing, also when the device's last session ended long ago.
	d.awake.forget(p.ID)
	info := peerInfo(p, connected)
	if err != nil {
		slog.Error("forgot paired device, but peers.json still lists it", "peer", p.Name, "id", p.ID, "ended_session", connected, "err", err)
		info.RemovalNotSaved = true
		return info, fmt.Errorf("%s (%s) can no longer connect, but the removal was not saved, so it could connect again after desklinkd restarts; "+
			"run \"desklinkd peers remove %s\" again once the cause is fixed: %w", p.Name, p.ID, p.ID, err)
	}
	slog.Info("forgot paired device", "peer", p.Name, "id", p.ID, "ended_session", connected)
	return info, nil
}

// saveRemovals retries saving removals that failed before the daemon exits,
// and says loudly when the next start will accept those devices again.
func (d *Daemon) saveRemovals() {
	unsaved := d.peers.Unsaved()
	if len(unsaved) == 0 {
		return
	}
	if err := d.peers.Save(); err != nil {
		var names []string
		for _, p := range unsaved {
			names = append(names, p.Name+" ("+p.ID+")")
		}
		slog.Error("removed devices are still in peers.json and can connect after a restart; remove them again with desklinkd peers remove",
			"devices", strings.Join(names, ", "), "err", err)
	}
}

func peerInfo(p config.Peer, connected bool) dlnet.PeerInfo {
	return dlnet.PeerInfo{ID: p.ID, Name: p.Name, Paired: p.Paired, LastSeen: p.LastSeen, Connected: connected}
}

// announce publishes the control port with mDNS as "DeskLink on <host>".
func (d *Daemon) announce(ctx context.Context) (func(), error) {
	port, err := d.controlPort()
	if err != nil {
		return nil, err
	}
	host, err := os.Hostname()
	if err != nil {
		host = "desklinkd"
	}
	return d.bus.Announce(ctx, "DeskLink on "+host, uint16(port), d.fingerprint)
}

func (d *Daemon) controlPort() (int, error) {
	_, portText, err := stdnet.SplitHostPort(d.opts.Config.ControlAddr)
	if err != nil {
		return 0, fmt.Errorf("control_addr: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return 0, fmt.Errorf("control_addr port: %w", err)
	}
	return port, nil
}

// BeginPairing implements net.PairingControl: it opens a pairing window with
// a fresh PIN.
func (d *Daemon) BeginPairing(context.Context) (dlnet.PairingTicket, error) {
	pin, expires, err := d.pairer.Begin()
	if err != nil {
		return dlnet.PairingTicket{}, err
	}
	port, err := d.controlPort()
	if err != nil {
		return dlnet.PairingTicket{}, err
	}
	ip, err := d.advertiseIP()
	if err != nil {
		return dlnet.PairingTicket{}, err
	}
	return dlnet.PairingTicket{
		IP: ip.String(), Port: port, PIN: pin, WakeNote: wakeNote(ip), Addresses: d.addresses(),
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
		Name:      "desklinkd",
		Version:   d.opts.Version,
		Hostname:  host,
		TimeMs:    time.Now().UnixMilli(),
		MAC:       d.mac(),
		Addresses: d.addresses(),
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
