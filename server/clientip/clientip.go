// Package clientip determines the client address of an HTTP request,
// honouring X-Forwarded-For only from trusted proxies (DESIGN.md §5).
package clientip

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Resolver extracts client IPs.
type Resolver struct {
	trusted []netip.Prefix
}

// New returns a Resolver trusting the given proxy prefixes.
func New(trusted []netip.Prefix) *Resolver {
	return &Resolver{trusted: trusted}
}

func (r *Resolver) isTrusted(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range r.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ClientIP returns the client address for req.
//
// The peer address (RemoteAddr) is used unless it belongs to a trusted proxy.
// In that case X-Forwarded-For is walked from right to left, skipping trusted
// proxies; the first (i.e. rightmost) untrusted address wins. If every entry
// is trusted, the leftmost one is used. A malformed entry stops the walk and
// the last trusted hop is used instead, so a client can never choose its own
// address.
func (r *Resolver) ClientIP(req *http.Request) netip.Addr {
	peer := parseHostPort(req.RemoteAddr)
	if !peer.IsValid() {
		return netip.IPv6Unspecified()
	}
	if len(r.trusted) == 0 || !r.isTrusted(peer) {
		return peer
	}
	values := req.Header.Values("X-Forwarded-For")
	if len(values) == 0 {
		return peer
	}
	var entries []string
	for _, v := range values {
		for _, e := range strings.Split(v, ",") {
			entries = append(entries, strings.TrimSpace(e))
		}
	}
	last := peer
	for i := len(entries) - 1; i >= 0; i-- {
		a := parseXFFEntry(entries[i])
		if !a.IsValid() {
			return last
		}
		if !r.isTrusted(a) {
			return a
		}
		last = a
	}
	return last
}

// parseHostPort parses "host:port" or a bare host.
func parseHostPort(s string) netip.Addr {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap()
	}
	host, _, err := net.SplitHostPort(s)
	if err != nil {
		host = s
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if i := strings.IndexByte(host, '%'); i >= 0 { // drop IPv6 zone
		host = host[:i]
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

// parseXFFEntry accepts "1.2.3.4", "1.2.3.4:80", "2001:db8::1",
// "[2001:db8::1]" and "[2001:db8::1]:80".
func parseXFFEntry(s string) netip.Addr {
	s = strings.Trim(strings.TrimSpace(s), `"`)
	if s == "" {
		return netip.Addr{}
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return a.WithZone("").Unmap()
	}
	return parseHostPort(s)
}

// Key returns the rate-limiting key for a: the address itself for IPv4 and
// the /64 prefix for IPv6 (a single host typically controls a whole /64).
func Key(a netip.Addr) string {
	a = a.Unmap()
	if a.Is6() {
		p, err := a.WithZone("").Prefix(64)
		if err == nil {
			return p.String()
		}
	}
	return a.String()
}
