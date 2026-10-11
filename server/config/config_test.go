package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestDefaults(t *testing.T) {
	c, err := LoadFrom(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":8080" || c.DataDir != "/data" || c.MaxBody != 2097152 || c.MaxComment != 262144 ||
		c.MaxAttachment != 26214400 || c.MaxPasteAttachments != 104857600 || c.MaxVersions != 200 || !c.AllowNever {
		t.Fatalf("defaults %+v", c)
	}
	if got := c.Expiries(); len(got) != 6 || got[5] != 0 {
		t.Fatalf("expiries %v", got)
	}
}

func TestOverrides(t *testing.T) {
	c, err := LoadFrom(env(map[string]string{
		"HB_ADDR":            "127.0.0.1:9000",
		"HB_DATA_DIR":        "/tmp/x",
		"HB_TRUSTED_PROXIES": "10.0.0.0/8, 192.168.1.1 ,::1, ::ffff:172.16.0.0/108",
		"HB_MAX_BODY":        "100",
		"HB_MAX_VERSIONS":    "5",
		"HB_ALLOW_NEVER":     "false",
		"HB_RATE_CREATE":     "5/h",
		"HB_RATE_COMMENT":    "10/30s",
		"HB_RATE_UPLOAD":     "off",
		"HB_RATE_AUTH":       "7/2d",
		"HB_RATE_GENERAL":    "0",
		"HB_LOG_FORMAT":      "json",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != "127.0.0.1:9000" || c.DataDir != "/tmp/x" || c.MaxBody != 100 || c.MaxVersions != 5 || c.AllowNever {
		t.Fatalf("%+v", c)
	}
	if len(c.TrustedProxies) != 4 || c.TrustedProxies[1].String() != "192.168.1.1/32" ||
		c.TrustedProxies[2].String() != "::1/128" || c.TrustedProxies[3].String() != "172.16.0.0/12" {
		t.Fatalf("proxies %v", c.TrustedProxies)
	}
	if c.RateCreate != (Rate{5, time.Hour}) || c.RateComment != (Rate{10, 30 * time.Second}) ||
		!c.RateUpload.Disabled() || c.RateAuth != (Rate{7, 48 * time.Hour}) || !c.RateGeneral.Disabled() {
		t.Fatalf("rates %v %v %v %v %v", c.RateCreate, c.RateComment, c.RateUpload, c.RateAuth, c.RateGeneral)
	}
	for _, e := range c.Expiries() {
		if e == 0 {
			t.Fatal("never must be disallowed")
		}
	}
	if c.ExpiryAllowed(0) || !c.ExpiryAllowed(300) || c.ExpiryAllowed(301) {
		t.Fatal("ExpiryAllowed")
	}
}

func TestInvalid(t *testing.T) {
	for k, v := range map[string]string{
		"HB_MAX_BODY":        "-1",
		"HB_MAX_VERSIONS":    "many",
		"HB_ALLOW_NEVER":     "perhaps",
		"HB_TRUSTED_PROXIES": "10.0.0.0/33",
		"HB_RATE_CREATE":     "5",
		"HB_LOG_LEVEL":       "loud",
	} {
		if _, err := LoadFrom(env(map[string]string{k: v})); err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("%s=%s: err %v", k, v, err)
		}
	}
	for _, s := range []string{"x/m", "5/", "5/xyz", "-1/m", "5/0s"} {
		if _, err := ParseRate(s); err == nil {
			t.Errorf("ParseRate(%q) should fail", s)
		}
	}
}

func TestImageProxyConfig(t *testing.T) {
	c, _ := LoadFrom(env(nil))
	if !c.ImageProxy || c.ImageProxyMaxBytes != 10485760 || c.ImageProxyCacheBytes != 67108864 ||
		c.ImageProxyAllowPrivate || len(c.ImageProxyHosts) != 0 || c.MaxImageRefs != 200 ||
		c.RateImage != (Rate{600, time.Hour}) {
		t.Fatalf("defaults %+v", c)
	}
	c, err := LoadFrom(env(map[string]string{
		"HB_IMAGE_PROXY":               "false",
		"HB_IMAGE_PROXY_MAX_BYTES":     "1000",
		"HB_IMAGE_PROXY_CACHE_BYTES":   "2000",
		"HB_IMAGE_PROXY_HOSTS":         "Example.com, *.imgur.com ,.githubusercontent.com.",
		"HB_IMAGE_PROXY_ALLOW_PRIVATE": "true",
		"HB_MAX_IMAGE_REFS":            "7",
		"HB_RATE_IMAGE":                "10/m",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.ImageProxy || c.ImageProxyMaxBytes != 1000 || c.ImageProxyCacheBytes != 2000 || !c.ImageProxyAllowPrivate ||
		c.MaxImageRefs != 7 || c.RateImage != (Rate{10, time.Minute}) ||
		strings.Join(c.ImageProxyHosts, ",") != "example.com,imgur.com,githubusercontent.com" {
		t.Fatalf("overrides %+v", c)
	}
	for _, bad := range []string{"bücher.de", "exa mple.com", "a..b", "a/b"} {
		if _, err := LoadFrom(env(map[string]string{"HB_IMAGE_PROXY_HOSTS": bad})); err == nil {
			t.Errorf("HB_IMAGE_PROXY_HOSTS=%q accepted", bad)
		}
	}
}
