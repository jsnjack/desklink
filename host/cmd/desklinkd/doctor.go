package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	stdnet "net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"desklink/internal/config"
	"desklink/internal/daemon"
	"desklink/internal/desktop"
	"desklink/internal/doctor"
	"desklink/internal/input"
	"desklink/internal/video"
)

// doctorProbeTimeout bounds each question doctor asks another program
// (D-Bus, the daemon's socket, systemctl, firewall-cmd), so one that hangs
// cannot hold the report.
const doctorProbeTimeout = 5 * time.Second

// errDoesNotWork is doctor's exit status when something keeps DeskLink from
// working; the report has said what.
var errDoesNotWork = errors.New("DeskLink does not work on this computer")

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check whether this computer is ready for DeskLink",
	Long: `doctor checks what DeskLink needs on this computer, in four groups: Desk
(GNOME on Wayland, Mutter's screen cast, the AV1 and HEVC encoders,
/dev/uinput), Network (desklinkd's ports, the firewall, Wake-on-LAN),
Staying up (automatic login, the systemd user service) and Devices (the
paired devices and when each was last seen). Each line starts with ✓, ! or
✗, followed by the command that fixes what is missing when one does; the
last line says whether DeskLink works.

doctor only reads: it changes nothing, and it works whether desklinkd runs
or not. It exits with status 1 when something keeps DeskLink from working
at all.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, paths, err := loadConfig()
		if err != nil {
			return err
		}
		host, err := os.Hostname()
		if err != nil {
			host = "this computer"
		}
		report := doctor.Build(readFacts(cmd.Context(), cfg, paths))
		out := cmd.OutOrStdout()
		if err := writeReport(out, paletteFor(out), "DeskLink "+Version+" on "+host, report); err != nil {
			return err
		}
		if report.Blocked() {
			cmd.SilenceErrors = true
			return errDoesNotWork
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}

// writeReport prints the report under title: each group's name and its
// checks, their details in one column, then the summary sentence.
func writeReport(w io.Writer, p palette, title string, r doctor.Report) error {
	width := nameWidth(r.Checks())
	var b strings.Builder
	b.WriteString(p.bold(title) + "\n\n")
	for _, g := range r.Groups {
		b.WriteString(p.bold(g.Title) + "\n")
		if err := writeChecks(&b, p, "  ", g.Checks, width); err != nil {
			return err
		}
	}
	b.WriteString("\n" + r.Summary() + "\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// readFacts reads what doctor judges. It only reads: no question it asks
// starts or changes anything.
func readFacts(ctx context.Context, cfg config.Config, paths config.Paths) doctor.Facts {
	f := doctor.Facts{Uinput: input.DevicePath, Now: time.Now()}
	readDesktopFacts(ctx, &f)

	video.Init()
	f.AV1, f.HEVC = video.ProbeEncoder(video.CodecAV1), video.ProbeEncoder(video.CodecHEVC)
	f.UinputErr, f.UdevRule = input.CheckAccess(input.DevicePath), doctor.UdevRuleInstalled()

	readDaemonFacts(ctx, &f, paths)
	ports, err := daemonPorts(cfg)
	f.PortsErr = err
	for _, p := range ports {
		used, err := doctor.PortInUse(p)
		if err != nil {
			f.PortsErr = err
			break
		}
		f.Ports = append(f.Ports, doctor.PortState{Port: p, InUse: used})
	}

	if ip, err := daemon.AdvertiseIP(cfg); err != nil {
		f.WakeErr = err
	} else {
		f.Wake = desktop.WakeFor(ip)
	}
	if len(ports) == 0 {
		f.FirewallErr = err
	} else {
		probe, cancel := context.WithTimeout(ctx, doctorProbeTimeout)
		f.Firewall, f.FirewallErr = doctor.ReadFirewall(probe, doctor.Exec, f.Wake.Interface, ports)
		cancel()
	}

	f.Autologin, f.AutologinErr = readAutologin()
	probe, cancel := context.WithTimeout(ctx, doctorProbeTimeout)
	f.Service, f.ServiceErr = doctor.ReadService(probe, doctor.Exec)
	cancel()
	return f
}

// readDesktopFacts asks logind about the graphical session and the session
// bus about GNOME's services.
func readDesktopFacts(ctx context.Context, f *doctor.Facts) {
	bus, err := desktop.ConnectBus()
	if err != nil {
		f.SessionErr, f.GNOMEErr = err, err
		return
	}
	defer func() {
		if err := bus.Close(); err != nil {
			slog.Debug("close the D-Bus connections", "err", err)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, doctorProbeTimeout)
	defer cancel()
	f.Session, f.SessionErr = bus.GraphicalSession(ctx)
	f.GNOME, f.GNOMEErr = bus.GNOMEServices(ctx)
}

// readDaemonFacts asks the running daemon for the paired devices, which
// also says that it runs; without it, peers.json lists them.
func readDaemonFacts(ctx context.Context, f *doctor.Facts, paths config.Paths) {
	if client, err := localClient(); err == nil {
		ctx, cancel := context.WithTimeout(ctx, doctorProbeTimeout)
		peers, err := client.Peers(ctx)
		cancel()
		if err == nil {
			f.Running = true
			for _, p := range peers {
				if !p.RemovalNotSaved {
					f.Devices = append(f.Devices, doctor.Device{Name: p.Name, LastSeen: p.LastSeen, Connected: p.Connected})
				}
			}
			return
		}
	}
	store, err := config.OpenPeers(paths.Peers())
	if err != nil {
		f.DevicesErr = err
		return
	}
	for _, p := range store.List() {
		f.Devices = append(f.Devices, doctor.Device{Name: p.Name, LastSeen: p.LastSeen})
	}
}

// daemonPorts are the control (TCP) and media (UDP) ports in cfg.
func daemonPorts(cfg config.Config) ([]doctor.Port, error) {
	var ports []doctor.Port
	for _, a := range []struct{ addr, proto, key string }{
		{cfg.ControlAddr, "tcp", "control_addr"},
		{cfg.MediaAddr, "udp", "media_addr"},
	} {
		_, text, err := stdnet.SplitHostPort(a.addr)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", a.key, err)
		}
		n, err := strconv.Atoi(text)
		if err != nil {
			return nil, fmt.Errorf("%s port: %w", a.key, err)
		}
		ports = append(ports, doctor.Port{Number: n, Proto: a.proto})
	}
	return ports, nil
}
