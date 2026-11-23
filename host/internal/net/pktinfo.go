package net

import (
	"encoding/binary"
	stdnet "net"
	"net/netip"

	"golang.org/x/sys/unix"
)

// A host with two interfaces on one network (a dock's ethernet and Wi-Fi)
// would answer from whichever interface routing prefers, with that
// interface's address, and clients drop datagrams from an address they did
// not send to. IP_PKTINFO reports the address and interface each client
// datagram arrived on, and sends pass them back so replies leave the way
// requests came in.

// pktinfoSize is sizeof(struct in_pktinfo): ifindex, spec_dst, addr.
const pktinfoSize = 12

// arrival is where a datagram arrived: the local address it was sent to and
// the receiving interface. The zero value means unknown.
type arrival struct {
	local   netip.Addr
	ifindex int32
}

// enablePktinfo asks the kernel for IP_PKTINFO on received datagrams.
func enablePktinfo(conn *stdnet.UDPConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := raw.Control(func(fd uintptr) {
		serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_PKTINFO, 1)
	}); err != nil {
		return err
	}
	return serr
}

// parseArrival reads the IP_PKTINFO control message of a received datagram.
func parseArrival(oob []byte) arrival {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return arrival{}
	}
	for _, m := range msgs {
		if m.Header.Level != unix.IPPROTO_IP || m.Header.Type != unix.IP_PKTINFO || len(m.Data) < pktinfoSize {
			continue
		}
		return arrival{
			ifindex: int32(binary.NativeEndian.Uint32(m.Data[0:4])),
			local:   netip.AddrFrom4([4]byte(m.Data[8:12])),
		}
	}
	return arrival{}
}

// oob returns the control message that sends from a's address and interface,
// or nil when a is unknown.
func (a arrival) oob() []byte {
	if !a.local.IsValid() {
		return nil
	}
	return unix.PktInfo4(&unix.Inet4Pktinfo{Ifindex: a.ifindex, Spec_dst: a.local.As4()})
}
