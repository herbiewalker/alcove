package netsec

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

func str(s string) *string { return &s }
func yes(b bool) *bool     { return &b }

func mgr(t *testing.T, saved map[string]string, env Env, opt Options) *Manager {
	t.Helper()
	m, err := New(saved, env, opt)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func req(remote string, hdr map[string]string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = remote
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

// N17: with allowed networks at the default, a request from a public address is refused.
func TestDefaultsAdmitPrivateAndRefusePublic(t *testing.T) {
	m := mgr(t, nil, Env{}, Options{})
	for _, ok := range []string{"192.168.1.20", "10.1.2.3", "172.16.5.5", "172.31.255.1", "127.0.0.1", "::1",
		"100.64.1.1", "169.254.1.1", "fd12:3456::1", "fe80::1", "::ffff:192.168.1.9"} {
		c := m.Resolve(req(netip.AddrPortFrom(addr(ok), 5000).String(), nil))
		if !m.Allowed(c) {
			t.Errorf("%s should be allowed by default", ok)
		}
	}
	for _, no := range []string{"8.8.8.8", "1.1.1.1", "172.32.0.1", "100.128.0.1", "2001:4860:4860::8888", "192.169.0.1", "11.0.0.1"} {
		c := m.Resolve(req(netip.AddrPortFrom(addr(no), 5000).String(), nil))
		if m.Allowed(c) {
			t.Errorf("%s must be refused by default", no)
		}
	}
	if m.Allowed(Client{}) {
		t.Error("an unknown address must be refused")
	}
}

func TestForwardedHeadersAreIgnoredWithoutAProxy(t *testing.T) {
	m := mgr(t, nil, Env{}, Options{})
	c := m.Resolve(req("8.8.8.8:1", map[string]string{"X-Forwarded-For": "192.168.1.5", "X-Forwarded-Proto": "https"}))
	if c.IP != addr("8.8.8.8") || c.Secure || m.Allowed(c) {
		t.Fatalf("a public peer must not be able to claim a LAN address or TLS: %+v", c)
	}
}

func proxyMgr(t *testing.T) *Manager {
	return mgr(t, nil, Env{TrustedProxies: str("10.0.0.2")}, Options{})
}

func TestProxyModeTrustsHeadersOnlyFromConfiguredProxies(t *testing.T) {
	m := proxyMgr(t)
	if !m.View().ProxyMode {
		t.Fatal("naming trusted proxies should turn proxy mode on")
	}
	// From the proxy: believe the client address and the protocol.
	c := m.Resolve(req("10.0.0.2:4000", map[string]string{"X-Forwarded-For": "192.168.1.77", "X-Forwarded-Proto": "https"}))
	if c.IP != addr("192.168.1.77") || !c.Secure {
		t.Fatalf("trusted proxy headers ignored: %+v", c)
	}
	// The client's own spoofed entry sits LEFT of what the proxy appended; the rightmost
	// untrusted address wins, so it cannot pass as a LAN host.
	c = m.Resolve(req("10.0.0.2:4000", map[string]string{"X-Forwarded-For": "192.168.1.5, 8.8.8.8"}))
	if c.IP != addr("8.8.8.8") || m.Allowed(c) {
		t.Fatalf("spoofed left-hand entry was trusted: %+v", c)
	}
	// A chain of our own proxies is skipped.
	m2 := mgr(t, nil, Env{TrustedProxies: str("10.0.0.2, 10.0.0.3")}, Options{})
	c = m2.Resolve(req("10.0.0.2:1", map[string]string{"X-Forwarded-For": "192.168.1.9, 10.0.0.3"}))
	if c.IP != addr("192.168.1.9") {
		t.Fatalf("proxy chain: %+v", c)
	}
	// Garbage falls back to the proxy's own address, never to an attacker-chosen one.
	c = m.Resolve(req("10.0.0.2:1", map[string]string{"X-Forwarded-For": "not-an-ip"}))
	if c.IP != addr("10.0.0.2") {
		t.Fatalf("garbage header: %+v", c)
	}
	// From anyone else: headers are ignored even though proxy mode is on.
	c = m.Resolve(req("192.168.1.50:1", map[string]string{"X-Forwarded-For": "192.168.1.99", "X-Forwarded-Proto": "https"}))
	if c.IP != addr("192.168.1.50") || c.Secure {
		t.Fatalf("an untrusted peer's headers were believed: %+v", c)
	}
	// Plain http from the proxy is not secure.
	if c = m.Resolve(req("10.0.0.2:1", map[string]string{"X-Forwarded-For": "192.168.1.9", "X-Forwarded-Proto": "http"})); c.Secure {
		t.Fatal("http behind a proxy reported as secure")
	}
}

func TestProxyModeNeedsAProxy(t *testing.T) {
	if _, err := New(nil, Env{ProxyMode: yes(true)}, Options{}); err == nil {
		t.Fatal("proxy mode with no trusted proxy should be refused at startup")
	}
}

func TestParseNets(t *testing.T) {
	got, err := ParseNets("192.168.1.0/24, 10.0.0.5\n fd00::/8 ;192.168.1.0/24")
	if err != nil || FormatNets(got) != "192.168.1.0/24,10.0.0.5/32,fd00::/8" {
		t.Fatalf("parse: %v %v", FormatNets(got), err)
	}
	for _, bad := range []string{"0.0.0.0/0", "::/0", "8.0.0.0/7", "1.2.3.4/33", "hello", "999.1.1.1", "2000::/3"} {
		if _, err := ParseNets(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if ps, err := ParseNets("  "); err != nil || len(ps) != 0 {
		t.Errorf("empty: %v %v", ps, err)
	}
	if ps, _ := ParseNets("192.168.1.77/24"); FormatNets(ps) != "192.168.1.0/24" {
		t.Errorf("host bits should be masked, got %v", FormatNets(ps))
	}
}

func TestEnvironmentWinsAndLocksTheConsole(t *testing.T) {
	saved := map[string]string{"setting.allowed_networks": "192.168.9.0/24", "setting.require_tls": "1"}
	m := mgr(t, saved, Env{AllowedNets: str("10.1.0.0/16"), RequireTLS: yes(false)}, Options{})
	v := m.View()
	if FormatNets(v.AllowedNets) != "10.1.0.0/16" || v.AllowedSrc != FromEnv || v.RequireTLS || v.TLSSrc != FromEnv {
		t.Fatalf("environment did not win: %+v", v)
	}
	if _, err := m.ProposeNetworks([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}); err != ErrEnvLocked {
		t.Fatalf("propose over env: %v", err)
	}
	if _, err := m.SetRequireTLS(true, Client{Secure: true}); err != ErrEnvLocked {
		t.Fatalf("require-tls over env: %v", err)
	}
	if _, err := m.SetProxy(true, []netip.Prefix{netip.MustParsePrefix("10.0.0.2/32")}); err != nil {
		t.Fatalf("proxy was not set by env, so the console may change it: %v", err)
	}
}

func TestSavedValuesLoadAndDamagedOnesFallBackSafely(t *testing.T) {
	m := mgr(t, map[string]string{"setting.allowed_networks": "192.168.9.0/24", "setting.require_tls": "1",
		"setting.proxy_mode": "1", "setting.trusted_proxies": "10.0.0.2/32"}, Env{}, Options{})
	v := m.View()
	if FormatNets(v.AllowedNets) != "192.168.9.0/24" || !v.RequireTLS || !v.ProxyMode || v.AllowedSrc != FromConsole {
		t.Fatalf("saved: %+v", v)
	}
	bad := mgr(t, map[string]string{"setting.allowed_networks": "0.0.0.0/0"}, Env{}, Options{})
	if FormatNets(bad.View().AllowedNets) != FormatNets(Defaults().AllowedNets) {
		t.Fatal("a damaged saved list must fall back to the defaults, not to everyone")
	}
}

type fakeTimer struct {
	mu sync.Mutex
	fn func()
	t  *time.Timer
}

func (f *fakeTimer) after(d time.Duration, fn func()) *time.Timer {
	f.mu.Lock()
	f.fn = fn
	f.mu.Unlock()
	f.t = time.NewTimer(time.Hour) // never fires on its own; the test calls fire()
	return f.t
}

func (f *fakeTimer) fire() {
	f.mu.Lock()
	fn := f.fn
	f.mu.Unlock()
	fn()
}

// F32: an unconfirmed allowed-network change reverts by itself; a confirmed one sticks.
func TestAllowedNetworkChangeRevertsUnlessConfirmed(t *testing.T) {
	ft := &fakeTimer{}
	var reverted []Settings
	m := mgr(t, nil, Env{}, Options{AfterFunc: ft.after, OnRevert: func(s Settings) { reverted = append(reverted, s) }})
	before := FormatNets(m.View().AllowedNets)

	until, err := m.ProposeNetworks([]netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")})
	if err != nil || until.IsZero() {
		t.Fatal(err)
	}
	if m.Allowed(Client{IP: addr("192.168.1.5")}) || !m.Allowed(Client{IP: addr("203.0.113.9")}) {
		t.Fatal("the proposed list should be live straight away")
	}
	if m.View().PendingUntil.IsZero() {
		t.Fatal("the view should show a change waiting")
	}
	if _, err := m.ProposeNetworks([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}); err == nil {
		t.Fatal("a second proposal while one waits should be refused")
	}
	ft.fire() // 60 seconds pass with no confirmation
	if FormatNets(m.View().AllowedNets) != before || !m.View().PendingUntil.IsZero() {
		t.Fatal("the unconfirmed change did not revert")
	}
	if len(reverted) != 1 || FormatNets(reverted[0].AllowedNets) != before {
		t.Fatalf("OnRevert not called with the restored settings: %v", reverted)
	}
	if _, err := m.ConfirmNetworks(); err == nil {
		t.Fatal("confirming after the revert must fail")
	}

	// Confirmed: it sticks, the timer is stopped, and the persisted form carries the new list.
	if _, err := m.ProposeNetworks([]netip.Prefix{netip.MustParsePrefix("192.168.7.0/24")}); err != nil {
		t.Fatal(err)
	}
	s, err := m.ConfirmNetworks()
	if err != nil || s.Marshal()["setting.allowed_networks"] != "192.168.7.0/24" {
		t.Fatalf("confirm: %+v %v", s, err)
	}
	ft.fire() // a late timer must not undo a confirmed change
	if FormatNets(m.View().AllowedNets) != "192.168.7.0/24" {
		t.Fatal("a late timer undid a confirmed change")
	}

	// Cancel puts it back immediately.
	if _, err := m.ProposeNetworks([]netip.Prefix{netip.MustParsePrefix("10.9.0.0/16")}); err != nil {
		t.Fatal(err)
	}
	m.CancelNetworks()
	if FormatNets(m.View().AllowedNets) != "192.168.7.0/24" {
		t.Fatal("cancel did not restore")
	}
}

func TestRequireTLS(t *testing.T) {
	m := mgr(t, nil, Env{}, Options{})
	plain, secure := Client{IP: addr("192.168.1.5")}, Client{IP: addr("192.168.1.5"), Secure: true}
	if m.TLSRequiredButMissing(plain) {
		t.Fatal("off by default")
	}
	if _, err := m.SetRequireTLS(true, plain); err == nil || !strings.Contains(err.Error(), "lock you out") {
		t.Fatalf("enabling over plain http must be refused: %v", err)
	}
	if _, err := m.SetRequireTLS(true, secure); err != nil {
		t.Fatal(err)
	}
	if !m.TLSRequiredButMissing(plain) || m.TLSRequiredButMissing(secure) {
		t.Fatal("require-TLS should refuse plain and admit secure")
	}
	if _, err := m.SetRequireTLS(false, plain); err != nil {
		t.Fatalf("turning it off must always work: %v", err)
	}
}

func TestLoopbackPeer(t *testing.T) {
	if !LoopbackPeer(req("127.0.0.1:9", nil)) || !LoopbackPeer(req("[::1]:9", nil)) || LoopbackPeer(req("192.168.1.2:9", nil)) {
		t.Fatal("loopback detection wrong")
	}
	// A forwarded header must not make a LAN peer look like loopback.
	if LoopbackPeer(req("192.168.1.2:9", map[string]string{"X-Forwarded-For": "127.0.0.1"})) {
		t.Fatal("header spoofed loopback")
	}
}
