// Package netsec implements D-23 transport security: which networks may reach the server
// (constraint 2), how the real client address and connection security are learned behind a
// reverse proxy, the require-TLS mode, and the auto-reverting change of allowed networks (F32, N17).
//
// It is pure policy and state. It makes no network calls, and it does not persist anything itself:
// the server writes the values returned by Settings.Marshal inside the same transaction as the
// admin-log entry, so a change and its log line commit together.
package netsec

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// RevertAfter is how long an unconfirmed allowed-networks change lives before it undoes itself.
const RevertAfter = 60 * time.Second

// Source says where a setting's current value came from, so the console can show "set by
// environment" and refuse to edit it (the environment is also the break-glass path).
type Source string

const (
	FromDefault Source = "default"
	FromConsole Source = "console"
	FromEnv     Source = "environment"
)

// Settings are the transport-security settings in force.
type Settings struct {
	AllowedNets    []netip.Prefix
	ProxyMode      bool
	TrustedProxies []netip.Prefix
	RequireTLS     bool
}

// Defaults is the secured-wifi default: private LAN ranges, loopback and Tailscale's range, no
// proxy, TLS not required.
func Defaults() Settings {
	return Settings{AllowedNets: mustPrefixes(
		"127.0.0.0/8", "::1/128", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
		"169.254.0.0/16", "100.64.0.0/10", "fc00::/7", "fe80::/10")}
}

func mustPrefixes(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(s))
	for i, v := range s {
		out[i] = netip.MustParsePrefix(v)
	}
	return out
}

// Env carries values from the environment; nil means "not set" (the console decides).
type Env struct {
	AllowedNets    *string
	TrustedProxies *string
	ProxyMode      *bool
	RequireTLS     *bool
}

// ParseNets parses a comma, space or newline separated list of CIDR ranges or bare addresses. It
// refuses a range wide enough to mean "the whole internet", because that would silently undo
// constraint 2.
func ParseNets(s string) ([]netip.Prefix, error) {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' || r == '\t' })
	var out []netip.Prefix
	seen := map[netip.Prefix]bool{}
	for _, f := range fields {
		var p netip.Prefix
		if strings.Contains(f, "/") {
			var err error
			if p, err = netip.ParsePrefix(f); err != nil {
				return nil, fmt.Errorf("%q is not a network such as 192.168.1.0/24", f)
			}
		} else {
			a, err := netip.ParseAddr(f)
			if err != nil {
				return nil, fmt.Errorf("%q is not an address or network", f)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		p = netip.PrefixFrom(p.Addr().Unmap(), unmapBits(p)).Masked()
		if min := minBits(p.Addr()); p.Bits() < min {
			return nil, fmt.Errorf("%s is too wide: it would let most of the internet in (use /%d or narrower)", f, min)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

func unmapBits(p netip.Prefix) int {
	if p.Addr().Is4In6() {
		return p.Bits() - 96
	}
	return p.Bits()
}

func minBits(a netip.Addr) int {
	if a.Is4() {
		return 8
	}
	return 7
}

// FormatNets is the inverse of ParseNets, comma separated.
func FormatNets(ps []netip.Prefix) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = p.String()
	}
	return strings.Join(s, ",")
}

// Marshal is the persisted form (meta keys under "setting.").
func (s Settings) Marshal() map[string]string {
	b := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	return map[string]string{
		"setting.allowed_networks": FormatNets(s.AllowedNets),
		"setting.proxy_mode":       b(s.ProxyMode),
		"setting.trusted_proxies":  FormatNets(s.TrustedProxies),
		"setting.require_tls":      b(s.RequireTLS),
	}
}

// Client is what the server concludes about the connection after proxy handling.
type Client struct {
	IP     netip.Addr // the address the rules apply to (unmapped; invalid if unknown)
	Secure bool       // the connection to the browser is encrypted (TLS here, or at a trusted proxy)
}

// View is a read-only copy of the manager's state for the console.
type View struct {
	Settings
	AllowedSrc, ProxySrc, TLSSrc Source
	PendingUntil                 time.Time // zero if no unconfirmed change
	PendingPrev                  []netip.Prefix
}

// Manager holds the live settings. Safe for concurrent use.
type Manager struct {
	now        func() time.Time
	afterFunc  func(time.Duration, func()) *time.Timer
	onRevert   func(Settings) // called (without locks) when an unconfirmed change undoes itself
	mu         sync.RWMutex
	cur        Settings
	srcAllowed Source
	srcProxy   Source
	srcTLS     Source
	envNets    bool
	envProxy   bool
	envTLS     bool
	pending    *pending
}

type pending struct {
	prev  []netip.Prefix
	src   Source
	until time.Time
	timer *time.Timer
}

// Options configure New. All fields are optional.
type Options struct {
	Now       func() time.Time
	AfterFunc func(time.Duration, func()) *time.Timer
	OnRevert  func(Settings)
}

// ErrEnvLocked means the value is set by the environment and cannot be changed from the console.
var ErrEnvLocked = errors.New("netsec: this value is set by the environment")

// New builds a manager from the defaults, then the saved console values, then the environment
// (which always wins).
func New(saved map[string]string, env Env, opt Options) (*Manager, error) {
	m := &Manager{now: opt.Now, afterFunc: opt.AfterFunc, onRevert: opt.OnRevert,
		cur: Defaults(), srcAllowed: FromDefault, srcProxy: FromDefault, srcTLS: FromDefault}
	if m.now == nil {
		m.now = time.Now
	}
	if m.afterFunc == nil {
		m.afterFunc = time.AfterFunc
	}
	if v, ok := saved["setting.allowed_networks"]; ok {
		ps, err := ParseNets(v)
		if err != nil || len(ps) == 0 {
			ps = Defaults().AllowedNets // a damaged saved value must not lock everyone out
		} else {
			m.srcAllowed = FromConsole
		}
		m.cur.AllowedNets = ps
	}
	if v, ok := saved["setting.proxy_mode"]; ok {
		m.cur.ProxyMode, m.srcProxy = v == "1", FromConsole
	}
	if v, ok := saved["setting.trusted_proxies"]; ok {
		if ps, err := ParseNets(v); err == nil {
			m.cur.TrustedProxies = ps
		}
	}
	if v, ok := saved["setting.require_tls"]; ok {
		m.cur.RequireTLS, m.srcTLS = v == "1", FromConsole
	}
	if env.AllowedNets != nil {
		ps, err := ParseNets(*env.AllowedNets)
		if err != nil {
			return nil, fmt.Errorf("ALCOVE_ALLOWED_NETWORKS: %w", err)
		}
		if len(ps) == 0 {
			return nil, errors.New("ALCOVE_ALLOWED_NETWORKS: at least one network is required")
		}
		m.cur.AllowedNets, m.srcAllowed, m.envNets = ps, FromEnv, true
	}
	if env.TrustedProxies != nil {
		ps, err := ParseNets(*env.TrustedProxies)
		if err != nil {
			return nil, fmt.Errorf("ALCOVE_TRUSTED_PROXIES: %w", err)
		}
		m.cur.TrustedProxies, m.srcProxy, m.envProxy = ps, FromEnv, true
		if env.ProxyMode == nil && len(ps) > 0 {
			m.cur.ProxyMode = true // naming proxies implies using them unless told otherwise
		}
	}
	if env.ProxyMode != nil {
		m.cur.ProxyMode, m.srcProxy, m.envProxy = *env.ProxyMode, FromEnv, true
	}
	if env.RequireTLS != nil {
		m.cur.RequireTLS, m.srcTLS, m.envTLS = *env.RequireTLS, FromEnv, true
	}
	if m.cur.ProxyMode && len(m.cur.TrustedProxies) == 0 {
		return nil, errors.New("proxy mode needs at least one trusted proxy address (ALCOVE_TRUSTED_PROXIES)")
	}
	return m, nil
}

// View returns a copy of the current state.
func (m *Manager) View() View {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v := View{Settings: clone(m.cur), AllowedSrc: m.srcAllowed, ProxySrc: m.srcProxy, TLSSrc: m.srcTLS}
	if m.pending != nil {
		v.PendingUntil, v.PendingPrev = m.pending.until, append([]netip.Prefix(nil), m.pending.prev...)
	}
	return v
}

func clone(s Settings) Settings {
	s.AllowedNets = append([]netip.Prefix(nil), s.AllowedNets...)
	s.TrustedProxies = append([]netip.Prefix(nil), s.TrustedProxies...)
	return s
}

func contains(ps []netip.Prefix, a netip.Addr) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Resolve works out the client address and whether the connection is encrypted. Forwarded headers
// are believed only when the TCP peer is a configured trusted proxy (N17); otherwise they are
// ignored, so a LAN client cannot claim to be someone else.
func (m *Manager) Resolve(r *http.Request) Client {
	m.mu.RLock()
	proxy, trusted := m.cur.ProxyMode, m.cur.TrustedProxies
	m.mu.RUnlock()

	peer := peerAddr(r)
	c := Client{IP: peer, Secure: r.TLS != nil}
	if !proxy || !peer.IsValid() || !contains(trusted, peer) {
		return c
	}
	if strings.EqualFold(strings.TrimSpace(firstHeader(r, "X-Forwarded-Proto")), "https") {
		c.Secure = true
	}
	// Walk X-Forwarded-For from the right, skipping our own proxies; the first address that is not
	// one of them is the client. Entries further left are attacker-controlled and ignored.
	parts := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			break // garbage: fall back to the proxy's own address, which is the safe side
		}
		a = a.Unmap()
		if contains(trusted, a) {
			continue
		}
		c.IP = a
		break
	}
	return c
}

func firstHeader(r *http.Request, k string) string {
	v := r.Header.Values(k)
	if len(v) == 0 {
		return ""
	}
	return strings.Split(v[0], ",")[0]
}

func peerAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	if i := a.Zone(); i != "" {
		a = a.WithZone("")
	}
	return a.Unmap()
}

// Allowed reports whether a request from this client may be served. An unknown address is
// refused. (The server separately lets a direct loopback peer through, see LoopbackPeer, so the
// container healthcheck keeps working whatever the list says.)
func (m *Manager) Allowed(c Client) bool {
	if !c.IP.IsValid() {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return contains(m.cur.AllowedNets, c.IP)
}

// LoopbackPeer reports whether the direct peer is loopback (healthcheck, local operator).
func LoopbackPeer(r *http.Request) bool {
	a := peerAddr(r)
	return a.IsValid() && a.IsLoopback()
}

// TLSRequiredButMissing is true when require-TLS is on and this connection is not encrypted.
func (m *Manager) TLSRequiredButMissing(c Client) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cur.RequireTLS && !c.Secure
}

// ProposeNetworks applies a new allowed-networks list tentatively. Unless ConfirmNetworks is
// called (from a request that got through the NEW rules) within RevertAfter, the previous list is
// restored automatically. Nothing is persisted until it is confirmed.
func (m *Manager) ProposeNetworks(nets []netip.Prefix) (time.Time, error) {
	if len(nets) == 0 {
		return time.Time{}, errors.New("at least one network is required")
	}
	m.mu.Lock()
	if m.envNets {
		m.mu.Unlock()
		return time.Time{}, ErrEnvLocked
	}
	if m.pending != nil {
		m.mu.Unlock()
		return time.Time{}, errors.New("a change is already waiting for confirmation")
	}
	until := m.now().Add(RevertAfter)
	p := &pending{prev: m.cur.AllowedNets, src: m.srcAllowed, until: until}
	p.timer = m.afterFunc(RevertAfter, m.revert)
	m.pending = p
	m.cur.AllowedNets = append([]netip.Prefix(nil), nets...)
	m.srcAllowed = FromConsole
	m.mu.Unlock()
	return until, nil
}

func (m *Manager) revert() {
	m.mu.Lock()
	p := m.pending
	if p == nil {
		m.mu.Unlock()
		return
	}
	m.cur.AllowedNets, m.srcAllowed = p.prev, p.src
	m.pending = nil
	cur := clone(m.cur)
	m.mu.Unlock()
	if m.onRevert != nil {
		m.onRevert(cur)
	}
}

// ConfirmNetworks keeps the tentative list and returns the settings to persist.
func (m *Manager) ConfirmNetworks() (Settings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == nil {
		return Settings{}, errors.New("there is no change waiting for confirmation")
	}
	m.pending.timer.Stop()
	m.pending = nil
	return clone(m.cur), nil
}

// CancelNetworks undoes a tentative change at once.
func (m *Manager) CancelNetworks() {
	m.mu.Lock()
	p := m.pending
	if p != nil {
		p.timer.Stop()
		m.cur.AllowedNets, m.srcAllowed = p.prev, p.src
		m.pending = nil
	}
	m.mu.Unlock()
}

// SetProxy changes reverse-proxy mode and the trusted proxy list. Turning it on needs at least one
// proxy address. It cannot lock anyone out: with no trusted peer, forwarded headers are ignored.
func (m *Manager) SetProxy(on bool, proxies []netip.Prefix) (Settings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.envProxy {
		return Settings{}, ErrEnvLocked
	}
	if on && len(proxies) == 0 {
		return Settings{}, errors.New("name at least one proxy address, or turn proxy mode off")
	}
	m.cur.ProxyMode, m.cur.TrustedProxies, m.srcProxy = on, append([]netip.Prefix(nil), proxies...), FromConsole
	return clone(m.cur), nil
}

// SetRequireTLS switches require-TLS mode. Turning it ON is accepted only from a connection that
// is already encrypted, so the admin cannot lock themselves out of the console by enabling it
// before a proxy is working. (The environment value remains the break-glass override.)
func (m *Manager) SetRequireTLS(on bool, c Client) (Settings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.envTLS {
		return Settings{}, ErrEnvLocked
	}
	if on && !c.Secure {
		return Settings{}, errors.New("you are connected without encryption right now, so turning this on would lock you out; open the console through your HTTPS proxy first")
	}
	m.cur.RequireTLS, m.srcTLS = on, FromConsole
	return clone(m.cur), nil
}
