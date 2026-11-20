package auth

import (
	"net"
	"net/netip"
	"sync"
	"time"
)

// ifaceCacheTTL is how long the interface table is trusted.
//
// A tailscale address can appear or disappear while the daemon runs, so the table
// cannot be built once at startup; it also must not be rebuilt per request, which
// would be a syscall on every segment fetch.
const ifaceCacheTTL = 15 * time.Second

type ifaceCache struct {
	mu    sync.Mutex
	at    time.Time
	table map[string][]netip.Prefix
}

// interfaceOf reports which trusted interface owns addr, or "".
//
// The address checked is the *local* address of the accepted connection, i.e. the
// address on this host that the socket was bound to. For a frame arriving over
// WireGuard that is the tailscale address; for the same frame arriving on the LAN
// port it is the LAN address. A client cannot influence it.
func (p *Policy) interfaceOf(addr netip.Addr) string {
	if len(p.ifaces) == 0 || !addr.IsValid() {
		return ""
	}
	addr = addr.Unmap()

	p.ifaceCache.mu.Lock()
	if time.Since(p.ifaceCache.at) > ifaceCacheTTL || p.ifaceCache.table == nil {
		p.ifaceCache.table = readInterfaces()
		p.ifaceCache.at = time.Now()
	}
	table := p.ifaceCache.table
	p.ifaceCache.mu.Unlock()

	for _, want := range p.ifaces {
		for _, prefix := range table[want] {
			if prefix.Contains(addr) {
				return want
			}
		}
	}
	return ""
}

// readInterfaces maps interface name to the prefixes assigned to it.
func readInterfaces() map[string][]netip.Prefix {
	table := make(map[string][]netip.Prefix)
	ifaces, err := net.Interfaces()
	if err != nil {
		return table
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			prefix, ok := prefixOf(ipNet)
			if !ok {
				continue
			}
			table[iface.Name] = append(table[iface.Name], prefix)
		}
	}
	return table
}

func prefixOf(ipNet *net.IPNet) (netip.Prefix, bool) {
	ones, bits := ipNet.Mask.Size()
	if bits == 0 {
		return netip.Prefix{}, false
	}
	if bits == net.IPv4len*8 {
		addr, ok := netip.AddrFromSlice(ipNet.IP.To4())
		if !ok {
			return netip.Prefix{}, false
		}
		return netip.PrefixFrom(addr.Unmap(), ones), true
	}
	addr, ok := netip.AddrFromSlice(ipNet.IP.To16())
	if !ok {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(addr, ones), true
}
