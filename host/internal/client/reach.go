package client

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"

	dlnet "desklink/internal/net"
)

// MaxAddresses is how many addresses a profile keeps, the one that answered
// last included; the oldest are dropped first.
const MaxAddresses = 8

// Candidates are the addresses to try for the host: the last one that worked
// first, then the others it reported (other networks, VPNs).
func (p Profile) Candidates() []string {
	out := []string{p.IP}
	for _, a := range p.Addresses {
		if a != "" && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

// UsableAddress reports whether a is an IP address that another machine can
// answer at: not unspecified, loopback, multicast, broadcast, link-local or
// otherwise reserved. Addresses that reach the store from the network (mDNS,
// /health, a pairing code's list) must pass it.
func UsableAddress(a string) bool {
	ip, err := netip.ParseAddr(a)
	if err != nil || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	if ip.Is4() {
		// 0.0.0.0/8 is "this network"; 240.0.0.0/4 is reserved and holds
		// the limited broadcast 255.255.255.255.
		if b := ip.As4(); b[0] == 0 || b[0] >= 240 {
			return false
		}
	}
	return !ip.IsUnspecified() && !ip.IsLoopback() && !ip.IsMulticast() &&
		!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsInterfaceLocalMulticast()
}

// mergeAddresses lists the addresses other than ip, in the order of lists
// (most recent first), without duplicates and at most MaxAddresses-1 of them.
func mergeAddresses(ip string, lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		for _, a := range l {
			if len(out) == MaxAddresses-1 {
				return out
			}
			if a != "" && a != ip && !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}
	return out
}

// WithAddresses returns p reached at ip, with the addresses the host reported
// next and the previous ones after them, at most MaxAddresses in all.
// Reported addresses that are not UsableAddress are dropped; ip and the saved
// ones are kept as they are, since only the user or a pinned answer put them
// there.
func (p Profile) WithAddresses(ip string, reported []string) Profile {
	usable := slices.DeleteFunc(slices.Clone(reported), func(a string) bool { return !UsableAddress(a) })
	p.IP, p.Addresses = ip, mergeAddresses(ip, usable, []string{p.IP}, p.Addresses)
	return p
}

// Reach asks every known address of the host for /health at once, trusting
// only the pinned certificate, and returns the profile reached at the first
// that answers, with the addresses the host reports merged in. On a LAN the
// local address answers before a VPN one. A profile without a fingerprint
// returns ErrNoFingerprint without asking anyone.
func Reach(ctx context.Context, p Profile) (Profile, dlnet.Health, error) {
	if p.Fingerprint == "" {
		return p, dlnet.Health{}, ErrNoFingerprint
	}
	ctx, cancel := context.WithTimeout(ctx, probeTime)
	defer cancel()
	type result struct {
		addr string
		h    dlnet.Health
		err  error
	}
	cands := p.Candidates()
	results := make(chan result, len(cands))
	for _, a := range cands {
		go func() {
			h, err := Health(ctx, a, p.Port, p.Fingerprint)
			results <- result{a, h, err}
		}()
	}
	var errs []error
	for range cands {
		r := <-results
		if r.err == nil {
			return p.WithAddresses(r.addr, r.h.Addresses), r.h, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", r.addr, r.err))
	}
	return p, dlnet.Health{}, errors.Join(errs...)
}

// Confirm asks ip for /health with p's pinned certificate and returns p
// reached there, with the addresses the host reports. An mDNS announcement
// is unauthenticated, so the address it names is kept only once Confirm
// succeeds. p must be paired, and ip must be UsableAddress.
func Confirm(ctx context.Context, p Profile, ip string) (Profile, error) {
	if !UsableAddress(ip) {
		return p, fmt.Errorf("%s cannot be a host's address", ip)
	}
	return confirm(ctx, p, ip)
}

// confirm is Confirm without the address check, which loopback tests skip.
func confirm(ctx context.Context, p Profile, ip string) (Profile, error) {
	if !p.Paired() {
		return p, ErrNoFingerprint
	}
	ctx, cancel := context.WithTimeout(ctx, probeTime)
	defer cancel()
	h, err := Health(ctx, ip, p.Port, p.Fingerprint)
	if err != nil {
		return p, fmt.Errorf("%s: %w", ip, err)
	}
	return p.WithAddresses(ip, h.Addresses), nil
}
