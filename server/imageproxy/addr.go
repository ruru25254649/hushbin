package imageproxy

import "net/netip"

// Ranges that are never fetched from, beyond what the netip predicates
// (loopback, private, link-local, multicast, unspecified) already cover.
var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		// IPv4
		"0.0.0.0/8",          // "this network"
		"100.64.0.0/10",      // CGNAT shared address space
		"192.0.0.0/24",       // IETF protocol assignments
		"192.0.2.0/24",       // TEST-NET-1
		"192.88.99.0/24",     // 6to4 relay anycast (deprecated)
		"198.18.0.0/15",      // benchmarking
		"198.51.100.0/24",    // TEST-NET-2
		"203.0.113.0/24",     // TEST-NET-3
		"240.0.0.0/4",        // reserved
		"255.255.255.255/32", // broadcast
		// IPv6
		"::/128",          // unspecified
		"::1/128",         // loopback
		"::/96",           // IPv4-compatible (deprecated)
		"::ffff:0:0:0/96", // IPv4-translated (RFC 2765)
		"64:ff9b::/96",    // NAT64 well-known prefix
		"64:ff9b:1::/48",  // NAT64 local-use prefix
		"100::/64",        // discard-only
		"2001::/23",       // IETF protocol assignments (Teredo, benchmarking, ORCHID...)
		"2001:db8::/32",   // documentation
		"3fff::/20",       // documentation (RFC 9637)
		"5f00::/16",       // SRv6 SIDs
		"fc00::/7",        // unique local
		"fe80::/10",       // link-local
		"fec0::/10",       // site-local (deprecated)
		"ff00::/8",        // multicast
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

var (
	globalUnicast6 = netip.MustParsePrefix("2000::/3")
	nat64          = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour      = netip.MustParsePrefix("2002::/16")
)

// IsPublicAddr reports whether a is a public unicast address the proxy may
// connect to. IPv4-mapped IPv6 addresses are judged by their IPv4 address;
// IPv6 must lie in global unicast space (2000::/3) outside the special
// ranges, and 6to4 addresses must embed a public IPv4 address.
func IsPublicAddr(a netip.Addr) bool {
	if !a.IsValid() {
		return false
	}
	a = a.WithZone("").Unmap()
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	if a.Is6() {
		if nat64.Contains(a) { // already blocked above; kept explicit for clarity
			return false
		}
		if !globalUnicast6.Contains(a) {
			return false
		}
		if sixToFour.Contains(a) {
			b := a.As16()
			return IsPublicAddr(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}))
		}
	}
	return true
}

// EmbeddedIPv4 returns the IPv4 address embedded in a NAT64 (64:ff9b::/96)
// or 6to4 (2002::/16) address, for diagnostics and tests.
func EmbeddedIPv4(a netip.Addr) (netip.Addr, bool) {
	a = a.WithZone("")
	if !a.Is6() || a.Is4In6() {
		return netip.Addr{}, false
	}
	b := a.As16()
	switch {
	case nat64.Contains(a):
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), true
	case sixToFour.Contains(a):
		return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), true
	}
	return netip.Addr{}, false
}
