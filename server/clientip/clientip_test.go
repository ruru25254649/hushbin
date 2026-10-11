package clientip

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("127.0.0.1/32"),
		netip.MustParsePrefix("fd00::/8"),
	}
	cases := []struct {
		name    string
		trusted []netip.Prefix
		remote  string
		xff     []string
		want    string
	}{
		{"no proxies configured ignores XFF", nil, "198.51.100.1:5555", []string{"203.0.113.9"}, "198.51.100.1"},
		{"untrusted peer ignores XFF", trusted, "198.51.100.1:5555", []string{"203.0.113.9"}, "198.51.100.1"},
		{"trusted peer without XFF", trusted, "10.0.0.2:1234", nil, "10.0.0.2"},
		{"trusted peer single entry", trusted, "10.0.0.2:1234", []string{"203.0.113.9"}, "203.0.113.9"},
		{"rightmost untrusted wins", trusted, "10.0.0.2:1234", []string{"1.1.1.1, 203.0.113.9"}, "203.0.113.9"},
		{"skips trusted hops", trusted, "127.0.0.1:1234", []string{"1.1.1.1, 203.0.113.9, 10.9.9.9, 10.0.0.1"}, "203.0.113.9"},
		{"multiple headers concatenate", trusted, "10.0.0.2:1", []string{"1.1.1.1", "203.0.113.9, 10.1.1.1"}, "203.0.113.9"},
		{"all trusted -> leftmost", trusted, "10.0.0.2:1", []string{"10.0.0.7, 10.0.0.8"}, "10.0.0.7"},
		{"garbage stops the walk", trusted, "10.0.0.2:1", []string{"203.0.113.9, garbage"}, "10.0.0.2"},
		{"garbage behind trusted hop", trusted, "10.0.0.2:1", []string{"evil, 10.0.0.5"}, "10.0.0.5"},
		{"entry with port", trusted, "10.0.0.2:1", []string{"203.0.113.9:4711"}, "203.0.113.9"},
		{"ipv6 entry", trusted, "10.0.0.2:1", []string{"2001:db8::1"}, "2001:db8::1"},
		{"bracketed ipv6 with port", trusted, "10.0.0.2:1", []string{"[2001:db8::1]:443"}, "2001:db8::1"},
		{"ipv6 peer trusted", trusted, "[fd00::1]:80", []string{"203.0.113.9"}, "203.0.113.9"},
		{"ipv4-mapped peer", trusted, "[::ffff:10.0.0.2]:80", []string{"203.0.113.9"}, "203.0.113.9"},
		{"ipv4-mapped entry", trusted, "10.0.0.2:80", []string{"::ffff:203.0.113.9"}, "203.0.113.9"},
		{"empty entries", trusted, "10.0.0.2:80", []string{"203.0.113.9, "}, "10.0.0.2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.remote
			for _, v := range tc.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			got := New(tc.trusted).ClientIP(r)
			if got.String() != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestKey(t *testing.T) {
	if k := Key(netip.MustParseAddr("203.0.113.9")); k != "203.0.113.9" {
		t.Fatal(k)
	}
	a := Key(netip.MustParseAddr("2001:db8:1:2:aaaa::1"))
	b := Key(netip.MustParseAddr("2001:db8:1:2:bbbb::2"))
	c := Key(netip.MustParseAddr("2001:db8:1:3::1"))
	if a != b || a == c || a != "2001:db8:1:2::/64" {
		t.Fatalf("ipv6 keys %s %s %s", a, b, c)
	}
	if k := Key(netip.MustParseAddr("::ffff:1.2.3.4")); k != "1.2.3.4" {
		t.Fatal(k)
	}
}
