// Package config loads Hushbin's runtime configuration from environment
// variables (see DESIGN.md §5 and server/README.md).
package config

import (
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

// Version is reported by GET /api/config.
const Version = "0.1.0"

// Rate is a token-bucket limit: up to N events per Per, with a burst of N.
// A zero N disables the limit.
type Rate struct {
	N   int
	Per time.Duration
}

// Disabled reports whether the limit is switched off.
func (r Rate) Disabled() bool { return r.N <= 0 || r.Per <= 0 }

func (r Rate) String() string {
	if r.Disabled() {
		return "off"
	}
	return fmt.Sprintf("%d/%s", r.N, r.Per)
}

// Config is the complete server configuration.
type Config struct {
	Addr           string
	DataDir        string
	TrustedProxies []netip.Prefix

	MaxBody             int64 // paste body ciphertext bytes (decoded)
	MaxComment          int64 // comment ciphertext bytes (decoded)
	MaxAttachment       int64 // per attachment data blob
	MaxPasteAttachments int64 // total attachment data per paste
	MaxVersions         int   // per paste / comment
	AllowNever          bool  // allow expiresIn = 0

	// Additional safety limits (not part of the DESIGN.md table; documented
	// in server/README.md).
	MaxAttachmentMeta  int64 // attmeta envelope bytes (decoded)
	MaxAttachmentCount int   // attachments per paste
	MaxComments        int   // comments per paste (tombstones included)
	MaxStreamsPerIP    int   // concurrent event streams per client IP

	// Remote image proxy (DESIGN.md §4 "Remote image proxy").
	ImageProxy             bool     // HB_IMAGE_PROXY
	ImageProxyMaxBytes     int64    // HB_IMAGE_PROXY_MAX_BYTES
	ImageProxyCacheBytes   int64    // HB_IMAGE_PROXY_CACHE_BYTES
	ImageProxyHosts        []string // HB_IMAGE_PROXY_HOSTS: lowercase domain suffixes; empty = any public host
	ImageProxyAllowPrivate bool     // HB_IMAGE_PROXY_ALLOW_PRIVATE: testing only
	MaxImageRefs           int      // HB_MAX_IMAGE_REFS, per paste

	RateCreate  Rate
	RateComment Rate
	RateUpload  Rate
	RateAuth    Rate // failed read-auth / owner-auth attempts
	RateGeneral Rate // every /api request
	RateImage   Rate // POST /api/pastes/{id}/image

	LogLevel  string
	LogFormat string
}

// Default returns the built-in defaults.
func Default() Config {
	return Config{
		Addr:                 ":8080",
		DataDir:              "/data",
		MaxBody:              2097152,
		MaxComment:           262144,
		MaxAttachment:        26214400,
		MaxPasteAttachments:  104857600,
		MaxVersions:          200,
		AllowNever:           true,
		MaxAttachmentMeta:    16384,
		MaxAttachmentCount:   500,
		MaxComments:          1000,
		MaxStreamsPerIP:      16,
		ImageProxy:           true,
		ImageProxyMaxBytes:   10 << 20,
		ImageProxyCacheBytes: 64 << 20,
		MaxImageRefs:         200,
		RateCreate:           Rate{N: 60, Per: time.Hour},
		RateComment:          Rate{N: 60, Per: 10 * time.Minute},
		RateUpload:           Rate{N: 120, Per: 10 * time.Minute},
		RateAuth:             Rate{N: 30, Per: 10 * time.Minute},
		RateGeneral:          Rate{N: 600, Per: time.Minute},
		RateImage:            Rate{N: 600, Per: time.Hour},
		LogLevel:             "info",
		LogFormat:            "text",
	}
}

// Expiries returns the allowed expiresIn values (seconds), in the order
// published by GET /api/config. 0 means "never" and is omitted when the
// operator disallows non-expiring pastes.
func (c *Config) Expiries() []int64 {
	e := []int64{300, 3600, 86400, 604800, 2592000}
	if c.AllowNever {
		e = append(e, 0)
	}
	return e
}

// ExpiryAllowed reports whether expiresIn (seconds) is an allowed preset.
func (c *Config) ExpiryAllowed(sec int64) bool {
	for _, v := range c.Expiries() {
		if v == sec {
			return true
		}
	}
	return false
}

// Load reads the configuration from the process environment.
func Load() (Config, error) { return LoadFrom(os.LookupEnv) }

// LoadFrom reads the configuration using lookup (os.LookupEnv in production).
func LoadFrom(lookup func(string) (string, bool)) (Config, error) {
	c := Default()
	var errs []string
	get := func(k string) (string, bool) {
		v, ok := lookup(k)
		v = strings.TrimSpace(v)
		return v, ok && v != ""
	}
	str := func(k string, dst *string) {
		if v, ok := get(k); ok {
			*dst = v
		}
	}
	posInt64 := func(k string, dst *int64) {
		if v, ok := get(k); ok {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n <= 0 {
				errs = append(errs, fmt.Sprintf("%s: want a positive integer, got %q", k, v))
				return
			}
			*dst = n
		}
	}
	posInt := func(k string, dst *int) {
		var n = int64(*dst)
		posInt64(k, &n)
		*dst = int(n)
	}
	boolean := func(k string, dst *bool) {
		if v, ok := get(k); ok {
			b, err := strconv.ParseBool(v)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: want true/false, got %q", k, v))
				return
			}
			*dst = b
		}
	}
	rate := func(k string, dst *Rate) {
		if v, ok := get(k); ok {
			r, err := ParseRate(v)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", k, err))
				return
			}
			*dst = r
		}
	}

	str("HB_ADDR", &c.Addr)
	str("HB_DATA_DIR", &c.DataDir)
	if v, ok := get("HB_TRUSTED_PROXIES"); ok {
		p, err := ParsePrefixes(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("HB_TRUSTED_PROXIES: %v", err))
		}
		c.TrustedProxies = p
	}
	posInt64("HB_MAX_BODY", &c.MaxBody)
	posInt64("HB_MAX_COMMENT", &c.MaxComment)
	posInt64("HB_MAX_ATTACHMENT", &c.MaxAttachment)
	posInt64("HB_MAX_PASTE_ATTACHMENTS", &c.MaxPasteAttachments)
	posInt("HB_MAX_VERSIONS", &c.MaxVersions)
	boolean("HB_ALLOW_NEVER", &c.AllowNever)
	posInt64("HB_MAX_ATTACHMENT_META", &c.MaxAttachmentMeta)
	posInt("HB_MAX_ATTACHMENT_COUNT", &c.MaxAttachmentCount)
	posInt("HB_MAX_COMMENTS", &c.MaxComments)
	posInt("HB_MAX_STREAMS_PER_IP", &c.MaxStreamsPerIP)
	rate("HB_RATE_CREATE", &c.RateCreate)
	rate("HB_RATE_COMMENT", &c.RateComment)
	rate("HB_RATE_UPLOAD", &c.RateUpload)
	rate("HB_RATE_AUTH", &c.RateAuth)
	rate("HB_RATE_GENERAL", &c.RateGeneral)
	rate("HB_RATE_IMAGE", &c.RateImage)
	boolean("HB_IMAGE_PROXY", &c.ImageProxy)
	posInt64("HB_IMAGE_PROXY_MAX_BYTES", &c.ImageProxyMaxBytes)
	posInt64("HB_IMAGE_PROXY_CACHE_BYTES", &c.ImageProxyCacheBytes)
	boolean("HB_IMAGE_PROXY_ALLOW_PRIVATE", &c.ImageProxyAllowPrivate)
	posInt("HB_MAX_IMAGE_REFS", &c.MaxImageRefs)
	if v, ok := get("HB_IMAGE_PROXY_HOSTS"); ok {
		h, err := ParseHostSuffixes(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("HB_IMAGE_PROXY_HOSTS: %v", err))
		}
		c.ImageProxyHosts = h
	}
	str("HB_LOG_LEVEL", &c.LogLevel)
	str("HB_LOG_FORMAT", &c.LogFormat)

	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Sprintf("HB_LOG_LEVEL: want debug|info|warn|error, got %q", c.LogLevel))
	}
	switch strings.ToLower(c.LogFormat) {
	case "text", "json":
	default:
		errs = append(errs, fmt.Sprintf("HB_LOG_FORMAT: want text|json, got %q", c.LogFormat))
	}
	if len(errs) > 0 {
		return c, fmt.Errorf("invalid configuration: %s", strings.Join(errs, "; "))
	}
	return c, nil
}

// ParseRate parses "N/period" where period is a Go duration ("10m", "1h30m")
// or a bare unit ("s", "m", "h", "d"). "0" or "off" disables the limit.
func ParseRate(s string) (Rate, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "off" || s == "0" || s == "none" {
		return Rate{}, nil
	}
	n, per, ok := strings.Cut(s, "/")
	if !ok {
		return Rate{}, fmt.Errorf("want N/period (e.g. 30/10m) or off, got %q", s)
	}
	count, err := strconv.Atoi(strings.TrimSpace(n))
	if err != nil || count < 0 {
		return Rate{}, fmt.Errorf("bad count in %q", s)
	}
	per = strings.TrimSpace(per)
	var d time.Duration
	switch per {
	case "s":
		d = time.Second
	case "m":
		d = time.Minute
	case "h":
		d = time.Hour
	case "d":
		d = 24 * time.Hour
	default:
		if strings.HasSuffix(per, "d") {
			days, err := strconv.Atoi(strings.TrimSuffix(per, "d"))
			if err != nil || days <= 0 {
				return Rate{}, fmt.Errorf("bad period in %q", s)
			}
			d = time.Duration(days) * 24 * time.Hour
		} else {
			d, err = time.ParseDuration(per)
			if err != nil || d <= 0 {
				return Rate{}, fmt.Errorf("bad period in %q", s)
			}
		}
	}
	if count == 0 {
		return Rate{}, nil
	}
	return Rate{N: count, Per: d}, nil
}

// ParsePrefixes parses a comma-separated list of CIDRs; bare addresses are
// accepted as single-host prefixes.
func ParsePrefixes(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "/") {
			p, err := netip.ParsePrefix(part)
			if err != nil {
				return nil, fmt.Errorf("bad CIDR %q", part)
			}
			p = p.Masked()
			if p.Addr().Is4In6() && p.Bits() >= 96 {
				p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
			}
			out = append(out, p)
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("bad address %q", part)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// ParseHostSuffixes parses a comma-separated list of domain suffixes
// ("example.com", ".example.com" and "*.example.com" are equivalent). Hosts
// must be ASCII (use punycode for internationalized names).
func ParseHostSuffixes(s string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(s, ",") {
		h := strings.ToLower(strings.TrimSpace(part))
		h = strings.TrimPrefix(h, "*")
		h = strings.Trim(h, ".")
		if h == "" {
			continue
		}
		for i := 0; i < len(h); i++ {
			c := h[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
				return nil, fmt.Errorf("bad host suffix %q (ASCII letters, digits, '-' and '.' only; use punycode)", part)
			}
		}
		if strings.Contains(h, "..") {
			return nil, fmt.Errorf("bad host suffix %q", part)
		}
		out = append(out, h)
	}
	return out, nil
}
