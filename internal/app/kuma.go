package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/config"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
	"github.com/cpprhtn/LiteDeck/internal/secret"
)

// Uptime Kuma, in the two places LiteDeck can say something Kuma cannot say for
// itself.
//
// Kuma already has a finished web UI. Redrawing its dashboard inside this
// window would be a worse copy of a thing that exists, so there is no Kuma
// panel here and no Kuma container controls either — the Docker view already
// restarts and tails a container without caring what is inside it.
//
// What is left is what only a tool holding an SSH session can do:
//
//  1. An instance bound to 127.0.0.1 is unreachable by design, and reaching it
//     normally means knowing `ssh -L`. The session is already open; the
//     forward is one click on it (see sshcore/tunnel.go).
//  2. An MCP client can ask whether anything is down, over the same connection,
//     landing in the same Command Log as everything else.
//
// Both are read-only. Creating, editing and deleting monitors is Kuma's own
// job, and its README says plainly that its API is built for its own UI rather
// than for callers like this one.

// DefaultKumaPort is what Uptime Kuma binds out of the box.
const DefaultKumaPort = 3001

// kumaProbeTimeout bounds one HTTP request over the forwarded channel. The
// service is on the server's own loopback, so a slow answer is a busy Kuma
// rather than a slow network.
const kumaProbeTimeout = 8 * time.Second

// kumaMetricsLimit caps how much of /metrics is read. A hundred monitors is
// well under 100KB; a body past this is not Kuma and should not be buffered
// because something answered on the port.
const kumaMetricsLimit = 4 << 20

// kumaProbeLimit caps how much of the landing page is read to identify it. The
// marker is in <head>, so the first few KB either has it or the page is not
// Kuma's.
const kumaProbeLimit = 64 << 10

// kumaMarker is what identifies the landing page.
//
// The product name in the page title, which is as stable a surface as this gets
// without touching /api/*. Being wrong here is cheap in one direction and not
// the other: a false negative costs a user one manual port entry, a false
// positive would open a browser at something that is not theirs to look at.
const kumaMarker = "Uptime Kuma"

// maxKumaCandidates bounds how many ports one detection probes.
//
// Each costs a round trip with an eight second ceiling, and the selection rule
// below already keeps the list to two or three. The cap is what stops a server
// with a dozen listeners calling themselves "kuma" from turning a tab open into
// a minute and a half of waiting.
const maxKumaCandidates = 4

// KumaCandidate is one loopback-only listener that might be Kuma.
type KumaCandidate struct {
	Port    int    `json:"port"`
	Address string `json:"address"`
	Process string `json:"process,omitempty"`
	PID     int    `json:"pid,omitempty"`
	// Confirmed means the port answered with Kuma's own landing page. An
	// unconfirmed candidate is still offered — it is the port the user named —
	// but the UI must not call it Kuma.
	Confirmed bool `json:"confirmed"`
	// Configured marks the port the user pinned, as opposed to the default.
	Configured bool `json:"configured"`
	// Note says why confirmation failed, verbatim (§8).
	Note string `json:"note,omitempty"`
}

// KumaView is what the network tab draws and what the settings form edits.
type KumaView struct {
	// Port is where this host's Kuma is expected to answer.
	Port       int  `json:"port"`
	Configured bool `json:"configured"`
	// HasAPIKey reports that a key is in the OS credential store. The key
	// itself is never sent to the frontend.
	HasAPIKey bool `json:"hasApiKey"`
	// KeychainOK is false where this machine has no credential store, in which
	// case the key cannot be kept at all and the form must not offer to.
	KeychainOK bool            `json:"keychainOk"`
	Candidates []KumaCandidate `json:"candidates"`
	Tunnels    []TunnelView    `json:"tunnels"`
	// Exposed lists ports where something that looks like Kuma answers from
	// off the machine. Reported rather than tunnelled: a browser can already
	// reach those, and opening a tunnel to one would be theatre.
	Exposed  []KumaCandidate `json:"exposed"`
	Warnings []string        `json:"warnings"`
}

// TunnelView is one open forward, as the UI and the Command Log see it.
type TunnelView struct {
	ID     string `json:"id"`
	HostID string `json:"hostId"`
	// URL is the address on this machine that now reaches the server's Kuma.
	URL        string `json:"url"`
	LocalPort  int    `json:"localPort"`
	RemotePort int    `json:"remotePort"`
	Since      string `json:"since"`
}

/* ------------------------------------------------------------- the registry */

// tunnelCloser is all the registry needs from a forward.
//
// Narrow on purpose: the lifecycle — which paths close a tunnel, and what the
// Command Log says when they do — is the part that has to be right whether or
// not there is a server to forward to, and this is what lets it be tested
// without one.
type tunnelCloser interface{ Close() error }

type openTunnel struct {
	view   TunnelView
	tunnel tunnelCloser
	logSeq int
	opened time.Time
}

// tunnelRegistry owns every forward the app has open.
//
// Centralised for one reason: a tunnel is a port on the user's machine that
// answers, and one whose connection has gone answers by hanging. Every path
// that can end a session — disconnect, reconnect, quit — closes through here.
type tunnelRegistry struct {
	app *App

	mu  sync.Mutex
	seq int
	all map[string]*openTunnel
}

func newTunnelRegistry(a *App) *tunnelRegistry {
	return &tunnelRegistry{app: a, all: make(map[string]*openTunnel)}
}

func (r *tunnelRegistry) add(t *openTunnel) {
	r.mu.Lock()
	r.all[t.view.ID] = t
	r.mu.Unlock()
}

func (r *tunnelRegistry) nextID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	return "tun" + strconv.Itoa(r.seq)
}

func (r *tunnelRegistry) list(hostID string) []TunnelView {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]TunnelView, 0, len(r.all))
	for _, t := range r.all {
		if hostID == "" || t.view.HostID == hostID {
			out = append(out, t.view)
		}
	}
	return out
}

// close ends one tunnel and completes its Command Log row.
func (r *tunnelRegistry) close(id, reason string) bool {
	r.mu.Lock()
	t, ok := r.all[id]
	delete(r.all, id)
	r.mu.Unlock()
	if !ok {
		return false // already gone; closing twice is not an error
	}
	r.finish(t, reason)
	return true
}

func (r *tunnelRegistry) closeHost(hostID, reason string) {
	r.mu.Lock()
	var doomed []*openTunnel
	for id, t := range r.all {
		if t.view.HostID == hostID {
			doomed = append(doomed, t)
			delete(r.all, id)
		}
	}
	r.mu.Unlock()
	for _, t := range doomed {
		r.finish(t, reason)
	}
}

func (r *tunnelRegistry) closeAll() {
	r.mu.Lock()
	doomed := make([]*openTunnel, 0, len(r.all))
	for _, t := range r.all {
		doomed = append(doomed, t)
	}
	r.all = make(map[string]*openTunnel)
	r.mu.Unlock()
	for _, t := range doomed {
		r.finish(t, "")
	}
}

func (r *tunnelRegistry) finish(t *openTunnel, reason string) {
	_ = t.tunnel.Close()
	r.app.log.forwardClosed(t.logSeq, time.Since(t.opened), reason)
	r.app.emit("tunnel:closed", t.view)
	r.app.emit("tunnel:closed:"+t.view.HostID, t.view)
}

/* -------------------------------------------------------------- the reading */

// KumaConfig reports where this host's Kuma is expected, without touching the
// server. The API key is reported as present or absent, never returned.
func (a *App) KumaConfig(hostID string) KumaView {
	view := KumaView{
		Port:       DefaultKumaPort,
		Candidates: []KumaCandidate{},
		Exposed:    []KumaCandidate{},
		Tunnels:    a.tunnels.list(hostID),
		Warnings:   []string{},
		KeychainOK: a.secrets.Available(),
	}
	if a.settings != nil {
		if k, ok := a.settings.Get().Kuma[hostID]; ok && k.Port > 0 {
			view.Port, view.Configured = k.Port, true
		}
	}
	if key, err := a.secrets.Get(hostID, secret.KindKumaAPIKey); err == nil && key != "" {
		view.HasAPIKey = true
	}
	return view
}

// SetKumaConfig records the port, and stores or clears the API key.
//
// An empty apiKey with keep=false erases the stored one, which is how somebody
// revokes it here after revoking it in Kuma. keep=true leaves whatever is
// stored alone, so the form can be saved without re-typing a secret it never
// received in the first place.
func (a *App) SetKumaConfig(hostID string, port int, apiKey string, keep bool) ActionResult {
	if _, ok := a.hosts.Get(hostID); !ok {
		return failResult(fmt.Errorf("app: no host with ID %q", hostID))
	}
	if port < 0 || port > 65535 {
		return failResult(i18n.Errorf("포트는 1 에서 65535 사이여야 합니다"))
	}
	if a.settings == nil {
		return failResult(i18n.Errorf("설정을 저장할 수 없습니다"))
	}
	if err := a.settings.SetKuma(hostID, config.KumaSettings{Port: port}); err != nil {
		return failResult(err)
	}

	if keep {
		return okResult()
	}
	if strings.TrimSpace(apiKey) == "" {
		if err := a.secrets.Delete(hostID, secret.KindKumaAPIKey); err != nil &&
			!errors.Is(err, secret.ErrNotFound) && !errors.Is(err, secret.ErrUnavailable) {
			return failResult(err)
		}
		return okResult()
	}
	if err := a.secrets.Set(hostID, secret.KindKumaAPIKey, strings.TrimSpace(apiKey)); err != nil {
		if errors.Is(err, secret.ErrUnavailable) {
			// Saying so beats a silent no-op. Without a credential store the
			// key cannot be kept at all, and the app does not have a weaker
			// place to put it (§6).
			return failResult(i18n.Errorf("이 컴퓨터에는 자격증명 저장소가 없어 API 키를 보관할 수 없습니다"))
		}
		return failResult(err)
	}
	return okResult()
}

// kumaPort resolves the port to use for a host.
func (a *App) kumaPort(hostID string) int {
	if a.settings != nil {
		if k, ok := a.settings.Get().Kuma[hostID]; ok && k.Port > 0 {
			return k.Port
		}
	}
	return DefaultKumaPort
}

// DetectKuma finds Kuma instances the server keeps to itself.
//
// The listener list is the network tab's, unchanged: that view already decides
// what "bound to every interface" versus "loopback only" means, and a second
// opinion on the same question is how the two drift apart. What is added here
// is one HTTP request per candidate over the SSH connection — a listening port
// is not evidence of what is behind it, and the whole feature hangs on opening
// a browser at the right thing.
//
// Called when the tab opens and when the user asks again, not on the tab's
// poll. A service does not appear while you watch it, and every probe is
// something the server pays for (§3.2d).
func (a *App) DetectKuma(hostID string) (KumaView, error) {
	view := a.KumaConfig(hostID)

	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return view, err
	}

	net, err := a.HostNetwork(hostID)
	if err != nil {
		return view, err
	}
	view.Warnings = append(view.Warnings, net.Warnings...)

	for _, l := range kumaCandidates(net.Listeners, view.Port) {
		c := KumaCandidate{
			Port:       l.port,
			Address:    l.listener.Address,
			Process:    l.listener.Process,
			PID:        l.listener.PID,
			Configured: l.port == view.Port && view.Configured,
		}
		if l.listener.Exposed {
			// Nothing to solve: a browser reaches this already. It is still
			// worth naming, because "I thought that was internal" is exactly
			// the kind of thing the network tab exists to surface.
			c.Confirmed, c.Note = a.probeKuma(conn.DialRemote, hostID, l.port)
			if c.Confirmed {
				view.Exposed = append(view.Exposed, c)
			}
			continue
		}
		c.Confirmed, c.Note = a.probeKuma(conn.DialRemote, hostID, l.port)
		view.Candidates = append(view.Candidates, c)
	}
	return view, nil
}

// candidate pairs a listener with its parsed port.
type candidate struct {
	listener adapter.Listener
	port     int
}

// kumaCandidates picks the listeners worth spending a probe on.
//
// Not every loopback port: a real server has dozens, and probing all of them
// would be a port scan the user did not ask for and a wait they would notice.
// The rule is "the port Kuma uses, the port the user named, or a listener whose
// own process says so" — anything else, the user types the port and this finds
// it on the next look.
func kumaCandidates(listeners []adapter.Listener, want int) []candidate {
	seen := map[int]bool{}
	var out []candidate
	for _, l := range listeners {
		if l.Protocol != "tcp" {
			continue
		}
		port, err := strconv.Atoi(l.Port)
		if err != nil || seen[port] {
			continue
		}
		if !l.Exposed && !isLoopback(l.Address) {
			// Bound to one specific non-loopback address. Not "local only",
			// and not the wildcard the exposed flag catches either.
			continue
		}
		named := strings.Contains(strings.ToLower(l.Process), "kuma") ||
			strings.Contains(strings.ToLower(l.Process), "uptime")
		if port != want && port != DefaultKumaPort && !named {
			continue
		}
		seen[port] = true
		out = append(out, candidate{listener: l, port: port})
		if len(out) == maxKumaCandidates {
			break
		}
	}
	return out
}

// isLoopback reports whether ss's address column means "this machine only".
func isLoopback(addr string) bool {
	ip := net.ParseIP(strings.Trim(addr, "[]"))
	return ip != nil && ip.IsLoopback()
}

// remoteDialer opens a connection *as the server sees it*.
//
// A function rather than the *sshcore.Conn it always is in production, so the
// HTTP semantics below — what counts as Kuma, what a 401 means, what reaches
// the Command Log — can be tested against a real HTTP server without a real
// SSH server. That the channel itself carries bytes is sshcore's to prove, and
// it does, against a live sshd.
type remoteDialer func(ctx context.Context, addr string) (net.Conn, error)

// probeKuma asks the port whether it is Kuma, over the SSH connection.
//
// Nothing is published on this machine to do it: the request rides a
// direct-tcpip channel straight to the server's loopback. So a candidate that
// turns out to be somebody's private admin panel was never exposed anywhere by
// the act of checking.
func (a *App) probeKuma(dial remoteDialer, hostID string, port int) (bool, string) {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	line := fmt.Sprintf("curl -sS -m %d http://%s/", int(kumaProbeTimeout.Seconds()), addr)
	start := time.Now()

	body, status, err := a.kumaGet(dial, port, "/", "", kumaProbeLimit)
	switch {
	case err != nil:
		a.log.probed(hostID, line, false, err.Error(), time.Since(start))
		return false, err.Error()
	case !strings.Contains(string(body), kumaMarker):
		note := i18n.T("%d 번 포트가 응답했지만 Uptime Kuma 의 화면이 아닙니다 (HTTP %d)", port, status)
		a.log.probed(hostID, line, true, "", time.Since(start))
		return false, note
	}
	a.log.probed(hostID, line, true, "", time.Since(start))
	return true, ""
}

// kumaGet performs one HTTP request against the server's loopback.
func (a *App) kumaGet(
	dial remoteDialer, port int, path, apiKey string, limit int64,
) ([]byte, int, error) {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))

	// A transport of its own per call rather than a shared client: it is bound
	// to one SSH connection, and a reconnect replaces that connection while a
	// pooled idle socket would go on pointing at the dead one.
	tr := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dial(ctx, addr)
		},
	}
	defer tr.CloseIdleConnections()

	ctx, cancel := context.WithTimeout(context.Background(), kumaProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		return nil, 0, err
	}
	if apiKey != "" {
		// Kuma takes the API key as the basic-auth password and ignores the
		// username, which is why the documented form is `curl -u ":<key>"`.
		req.SetBasicAuth("", apiKey)
	}

	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// KumaStatus reads the monitors from /metrics.
//
// /metrics rather than /api/*: it is the one surface Kuma publishes *for*
// outside readers, it is Prometheus text rather than something shaped for its
// own UI, and the README is explicit that the rest carries no compatibility
// promise. Read-only in the strongest sense — there is no write anywhere in
// this file.
func (a *App) KumaStatus(hostID string) (adapter.KumaSnapshot, error) {
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		var snap adapter.KumaSnapshot
		snap.Monitors = []adapter.KumaMonitor{}
		return snap, err
	}
	return a.kumaStatus(conn.DialRemote, hostID, a.kumaPort(hostID))
}

// kumaStatus is KumaStatus with the connection and the port already resolved.
func (a *App) kumaStatus(dial remoteDialer, hostID string, port int) (adapter.KumaSnapshot, error) {
	var snap adapter.KumaSnapshot
	snap.Monitors = []adapter.KumaMonitor{}

	key, err := a.secrets.Get(hostID, secret.KindKumaAPIKey)
	if err != nil && !errors.Is(err, secret.ErrNotFound) && !errors.Is(err, secret.ErrUnavailable) {
		return snap, err
	}

	// The key is a placeholder in the logged line, not the value. The panel is
	// local and never transmitted (§7.4), but it is also the thing people paste
	// into bug reports, and a bearer secret has no business being in one.
	line := fmt.Sprintf("curl -sS -u ':<api-key>' http://127.0.0.1:%d/metrics", port)
	start := time.Now()

	body, status, err := a.kumaGet(dial, port, "/metrics", key, kumaMetricsLimit)
	if err != nil {
		a.log.probed(hostID, line, false, err.Error(), time.Since(start))
		return snap, err
	}
	if status == http.StatusUnauthorized {
		a.log.probed(hostID, line, false, "HTTP 401", time.Since(start))
		return snap, i18n.Errorf("Uptime Kuma 가 인증을 요구합니다 — 네트워크 탭의 Kuma 설정에 API 키를 넣으세요 (Kuma 의 Settings → API Keys)")
	}
	if status != http.StatusOK {
		a.log.probed(hostID, line, false, "HTTP "+strconv.Itoa(status), time.Since(start))
		return snap, i18n.Errorf("Uptime Kuma 의 %d 번 포트가 HTTP %d 로 답했습니다", port, status)
	}
	a.log.probed(hostID, line, true, "", time.Since(start))

	snap, err = adapter.ParseKumaMetrics(body)
	if err != nil {
		return snap, err
	}
	if len(snap.Monitors) == 0 {
		return snap, i18n.Errorf("%d 번 포트가 답했지만 모니터가 하나도 없습니다 — Uptime Kuma 가 아니거나 아직 모니터를 만들지 않았습니다", port)
	}
	return snap, nil
}

/* --------------------------------------------------------------- the tunnel */

// OpenKumaTunnel forwards a local port to the server's Kuma and opens it.
//
// port 0 means the configured one. The local end is always loopback and always
// whatever the OS hands out — the address goes straight into a browser, so it
// never has to be memorable, and a fixed number is one more thing that can
// already be taken.
func (a *App) OpenKumaTunnel(hostID string, port int) (TunnelView, error) {
	var view TunnelView

	h, ok := a.hosts.Get(hostID)
	if !ok {
		return view, fmt.Errorf("app: no host with ID %q", hostID)
	}
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return view, err
	}
	if port <= 0 {
		port = a.kumaPort(hostID)
	}
	if port > 65535 {
		return view, i18n.Errorf("포트는 1 에서 65535 사이여야 합니다")
	}

	// One tunnel per host and port. Clicking twice should land on the tab that
	// is already open, not leave a second forward behind it that nothing will
	// ever close.
	if existing, ok := a.existingTunnel(hostID, port); ok {
		a.openURL(existing.URL)
		return existing, nil
	}

	remote := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	tunnel, err := conn.OpenTunnel(remote, 0)
	if err != nil {
		return view, err
	}

	view = TunnelView{
		ID:         a.tunnels.nextID(),
		HostID:     hostID,
		URL:        "http://" + tunnel.LocalAddr() + "/",
		LocalPort:  tunnel.LocalPort(),
		RemotePort: port,
		Since:      tunnel.Opened().Format(time.RFC3339),
	}

	// The Command Log gets the line somebody would have typed to do this by
	// hand. LiteDeck does not run it — the forward is a channel on the session
	// that is already open — and the row is marked so it does not claim
	// otherwise. The line is still the honest answer to "what just happened",
	// and it is the one people need when they come to do it without the app.
	logLine := kumaTunnelLine(h, tunnel.LocalPort(), port)

	a.tunnels.add(&openTunnel{
		view:   view,
		tunnel: tunnel,
		logSeq: a.log.forwardOpened(hostID, logLine),
		opened: tunnel.Opened(),
	})

	a.emit("tunnel:opened", view)
	a.emit("tunnel:opened:"+hostID, view)
	a.openURL(view.URL)
	return view, nil
}

func (a *App) existingTunnel(hostID string, port int) (TunnelView, bool) {
	for _, v := range a.tunnels.list(hostID) {
		if v.RemotePort == port {
			return v, true
		}
	}
	return TunnelView{}, false
}

// kumaTunnelLine writes the equivalent OpenSSH invocation.
func kumaTunnelLine(h config.Host, local, remote int) string {
	args := []string{"ssh", "-N", "-L",
		fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", local, remote)}
	if h.ProxyJump != "" {
		args = append(args, "-J", h.ProxyJump)
	}
	if h.Port != 0 && h.Port != 22 {
		args = append(args, "-p", strconv.Itoa(h.Port))
	}
	target := h.Hostname
	if h.User != "" {
		target = h.User + "@" + h.Hostname
	}
	return strings.Join(append(args, target), " ")
}

// CloseKumaTunnel ends one forward.
func (a *App) CloseKumaTunnel(id string) ActionResult {
	a.tunnels.close(id, "")
	return okResult()
}

// ListTunnels reports the open forwards for a host, or all of them for "".
func (a *App) ListTunnels(hostID string) []TunnelView {
	return a.tunnels.list(hostID)
}
