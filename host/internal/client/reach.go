package client

import (
	"context"
	"errors"
	"fmt"
	"slices"

	dlnet "desklink/internal/net"
)

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

// WithAddresses returns p reached at ip, with the host's reported addresses
// and the previous ones kept as alternatives.
func (p Profile) WithAddresses(ip string, reported []string) Profile {
	all := append(append([]string{p.IP}, p.Addresses...), reported...)
	var others []string
	for _, a := range all {
		if a != "" && a != ip && !slices.Contains(others, a) {
			others = append(others, a)
		}
	}
	p.IP, p.Addresses = ip, others
	return p
}

// Reach asks every known address of the host for /health at once, trusting
// only the pinned certificate, and returns the profile reached at the first
// that answers, with the addresses the host reports merged in. On a LAN the
// local address answers before a VPN one.
func Reach(ctx context.Context, p Profile) (Profile, dlnet.Health, error) {
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
