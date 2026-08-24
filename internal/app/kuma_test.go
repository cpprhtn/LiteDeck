package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/cpprhtn/LiteDeck/internal/config"
	"github.com/cpprhtn/LiteDeck/internal/secret"
)

func testHost(id string) config.Host {
	return config.Host{
		ID: id, Hostname: "box", User: "me",
		Auth: []config.AuthMethod{config.AuthPassword},
	}
}

func listener(proto, addr, port, process string, exposed bool) adapter.Listener {
	return adapter.Listener{
		Protocol: proto, Address: addr, Port: port,
		Process: process, Exposed: exposed,
	}
}

func ports(cs []candidate) []int {
	out := make([]int, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.port)
	}
	return out
}

// Every loopback port is not a candidate. A real server has dozens, and probing
// all of them is a port scan the user did not ask for and a wait they notice.
func TestKumaCandidatesDoesNotProbeEveryLoopbackPort(t *testing.T) {
	got := kumaCandidates([]adapter.Listener{
		listener("tcp", "127.0.0.1", "3001", "node", false),
		listener("tcp", "127.0.0.1", "5432", "postgres", false),
		listener("tcp", "127.0.0.1", "6379", "redis-server", false),
		listener("tcp", "127.0.0.53", "53", "systemd-resolve", false),
	}, DefaultKumaPort)

	if p := ports(got); len(p) != 1 || p[0] != 3001 {
		t.Errorf("candidates = %v, want just the Kuma port", p)
	}
}

// The port somebody typed is always tried, whatever is behind it.
func TestKumaCandidatesHonoursTheConfiguredPort(t *testing.T) {
	got := kumaCandidates([]adapter.Listener{
		listener("tcp", "127.0.0.1", "8080", "node", false),
		listener("tcp", "127.0.0.1", "5432", "postgres", false),
	}, 8080)

	if p := ports(got); len(p) != 1 || p[0] != 8080 {
		t.Errorf("candidates = %v, want the configured port", p)
	}
}

// A listener that names itself is worth a probe on any port — that is how an
// instance on a port nobody configured gets found at all.
func TestKumaCandidatesTakesAProcessNameAsAHint(t *testing.T) {
	got := kumaCandidates([]adapter.Listener{
		listener("tcp", "127.0.0.1", "9999", "uptime-kuma", false),
	}, DefaultKumaPort)

	if p := ports(got); len(p) != 1 || p[0] != 9999 {
		t.Errorf("candidates = %v, want the self-identifying listener", p)
	}
}

// UDP on 3001 is not a web server, and probing it would hang for the timeout.
func TestKumaCandidatesIgnoresUDP(t *testing.T) {
	got := kumaCandidates([]adapter.Listener{
		listener("udp", "127.0.0.1", "3001", "", false),
	}, DefaultKumaPort)

	if len(got) != 0 {
		t.Errorf("candidates = %v, want none", ports(got))
	}
}

// A wildcard bind is reachable already, so there is nothing for a tunnel to
// solve — but it is still worth naming, because "I thought that was internal"
// is exactly what the network tab exists to surface. It has to reach the caller
// flagged, not filtered out.
func TestKumaCandidatesKeepsExposedListenersSeparable(t *testing.T) {
	got := kumaCandidates([]adapter.Listener{
		listener("tcp", "0.0.0.0", "3001", "node", true),
	}, DefaultKumaPort)

	if len(got) != 1 {
		t.Fatalf("candidates = %v, want the exposed one kept for reporting", ports(got))
	}
	if !got[0].listener.Exposed {
		t.Error("the exposed flag was lost, so the caller cannot tell it apart")
	}
}

// Bound to one specific non-loopback address: not local-only, and not the
// wildcard either. Offering a tunnel to it would be theatre.
func TestKumaCandidatesSkipsASpecificExternalBind(t *testing.T) {
	got := kumaCandidates([]adapter.Listener{
		listener("tcp", "10.0.0.5", "3001", "node", false),
	}, DefaultKumaPort)

	if len(got) != 0 {
		t.Errorf("candidates = %v, want none", ports(got))
	}
}

// Each probe costs a round trip with an eight second ceiling. A server whose
// listeners all name themselves must not turn opening a tab into a minute and a
// half of waiting.
func TestKumaCandidatesAreCapped(t *testing.T) {
	var many []adapter.Listener
	for p := 9000; p < 9020; p++ {
		many = append(many, listener("tcp", "127.0.0.1", strconv.Itoa(p), "uptime-kuma", false))
	}
	if got := len(kumaCandidates(many, DefaultKumaPort)); got != maxKumaCandidates {
		t.Errorf("%d candidates, want the cap of %d", got, maxKumaCandidates)
	}
}

func TestIsLoopback(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.53", true}, // systemd-resolved; still this machine only
		{"::1", true},
		{"[::1]", true}, // ss brackets IPv6, and the parser may leave them
		{"10.0.0.5", false},
		{"0.0.0.0", false},
		{"*", false},
		{"", false},
	} {
		if got := isLoopback(tc.addr); got != tc.want {
			t.Errorf("isLoopback(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

// The Command Log row is the command somebody would type to do this by hand.
// If it is not one they could paste, it is not worth showing.
func TestKumaTunnelLineIsAPastableCommand(t *testing.T) {
	line := kumaTunnelLine(config.Host{
		Hostname: "prod.example.com", Port: 2222, User: "deploy",
	}, 51234, 3001)

	for _, want := range []string{
		"ssh", "-N",
		"-L 127.0.0.1:51234:127.0.0.1:3001",
		"-p 2222",
		"deploy@prod.example.com",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q is missing %q", line, want)
		}
	}
}

// The default port is left off, because `ssh -p 22` is noise on a line whose
// job is to be read.
func TestKumaTunnelLineOmitsTheDefaultPort(t *testing.T) {
	line := kumaTunnelLine(config.Host{Hostname: "box", User: "me"}, 5000, 3001)
	if strings.Contains(line, "-p ") {
		t.Errorf("line %q spells out the default SSH port", line)
	}
	if !strings.Contains(line, "me@box") {
		t.Errorf("line %q lost the target", line)
	}
}

// A bastion is part of how you would reach it by hand, so it belongs on the
// line too — otherwise the copied command connects to the wrong machine.
func TestKumaTunnelLineCarriesTheBastion(t *testing.T) {
	line := kumaTunnelLine(config.Host{
		Hostname: "inside", User: "me", ProxyJump: "jump@edge",
	}, 5000, 3001)

	if !strings.Contains(line, "-J jump@edge") {
		t.Errorf("line %q dropped the bastion", line)
	}
}

/* --------------------------------------------------------- config and keys */

func TestSetKumaConfigRejectsAnImpossiblePort(t *testing.T) {
	a := appWithSettings(t)
	if err := a.hosts.Upsert(testHost("h")); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if res := a.SetKumaConfig("h", 70000, "", false); res.OK {
		t.Error("a port above 65535 was accepted")
	}
	if res := a.SetKumaConfig("unknown-host", 3001, "", false); res.OK {
		t.Error("a host that does not exist was accepted")
	}
}

func TestKumaConfigDefaultsAndRemembers(t *testing.T) {
	a := appWithSettings(t)
	if err := a.hosts.Upsert(testHost("h")); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	view := a.KumaConfig("h")
	if view.Port != DefaultKumaPort || view.Configured {
		t.Errorf("a host with no entry should default, got %+v", view)
	}

	if res := a.SetKumaConfig("h", 8080, "", false); !res.OK {
		t.Fatalf("SetKumaConfig: %s", res.Error)
	}
	view = a.KumaConfig("h")
	if view.Port != 8080 || !view.Configured {
		t.Errorf("port was not remembered: %+v", view)
	}

	// Zero is how somebody undoes it, and it must fall back rather than leave
	// the app pointing at port 0.
	if res := a.SetKumaConfig("h", 0, "", false); !res.OK {
		t.Fatalf("clearing: %s", res.Error)
	}
	if view = a.KumaConfig("h"); view.Port != DefaultKumaPort || view.Configured {
		t.Errorf("clearing left %+v", view)
	}
}

// The API key never travels to the frontend — only whether one exists. A view
// struct that carried it would put a bearer secret into the webview and into
// every screenshot of it.
func TestKumaConfigNeverReturnsTheAPIKey(t *testing.T) {
	a := appWithSettings(t)
	a.secrets = &fakeSecrets{store: map[string]string{}}
	if err := a.hosts.Upsert(testHost("h")); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if res := a.SetKumaConfig("h", 3001, "uk1_supersecret", false); !res.OK {
		t.Fatalf("SetKumaConfig: %s", res.Error)
	}
	view := a.KumaConfig("h")
	if !view.HasAPIKey {
		t.Fatal("a stored key is not reported as present")
	}

	// Nothing in the struct may contain it, whatever field is added later.
	if strings.Contains(dump(t, view), "supersecret") {
		t.Error("the API key reached the frontend view")
	}
}

// Saving the form again must not wipe a key the form never received.
func TestSetKumaConfigKeepsTheStoredKeyWhenAsked(t *testing.T) {
	a := appWithSettings(t)
	a.secrets = &fakeSecrets{store: map[string]string{}}
	if err := a.hosts.Upsert(testHost("h")); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	_ = a.SetKumaConfig("h", 3001, "uk1_key", false)
	if res := a.SetKumaConfig("h", 3002, "", true); !res.OK {
		t.Fatalf("SetKumaConfig: %s", res.Error)
	}
	if !a.KumaConfig("h").HasAPIKey {
		t.Error("saving the port erased the key")
	}

	// And an explicit empty save does clear it — that is how somebody revokes.
	if res := a.SetKumaConfig("h", 3002, "", false); !res.OK {
		t.Fatalf("SetKumaConfig: %s", res.Error)
	}
	if a.KumaConfig("h").HasAPIKey {
		t.Error("an explicit empty save left the old key in place")
	}
}

/* ------------------------------------------------------------ the registry */

// A forward is a port on *this* machine. One that outlives its session goes on
// accepting browsers and then hangs them, so every path that can end a session
// has to close it.
func TestTunnelRegistryClosesByHost(t *testing.T) {
	a := appWithSettings(t)

	closedA := newFakeTunnel(t)
	closedB := newFakeTunnel(t)
	other := newFakeTunnel(t)

	a.tunnels.add(&openTunnel{view: TunnelView{ID: "t1", HostID: "a"}, tunnel: closedA})
	a.tunnels.add(&openTunnel{view: TunnelView{ID: "t2", HostID: "a"}, tunnel: closedB})
	a.tunnels.add(&openTunnel{view: TunnelView{ID: "t3", HostID: "b"}, tunnel: other})

	a.tunnels.closeHost("a", "gone")

	if got := len(a.tunnels.list("a")); got != 0 {
		t.Errorf("%d tunnels left on the disconnected host", got)
	}
	if got := len(a.tunnels.list("b")); got != 1 {
		t.Errorf("closing one host left %d tunnels on another, want 1", got)
	}
	closedA.mustBeClosed(t)
	closedB.mustBeClosed(t)
	other.mustBeOpen(t)
}

func TestTunnelRegistryCloseIsIdempotent(t *testing.T) {
	a := appWithSettings(t)
	f := newFakeTunnel(t)
	a.tunnels.add(&openTunnel{view: TunnelView{ID: "t1", HostID: "a"}, tunnel: f})

	if !a.tunnels.close("t1", "") {
		t.Fatal("first close reported nothing to do")
	}
	// Every cleanup path closes; a second one is normal, not an error.
	if a.tunnels.close("t1", "") {
		t.Error("closing twice reported a second tunnel")
	}
}

// Quitting has to take the forwards with it, for the same reason: they are
// listening sockets on the user's machine.
func TestShutdownClosesEveryTunnel(t *testing.T) {
	a := appWithSettings(t)
	f := newFakeTunnel(t)
	a.tunnels.add(&openTunnel{view: TunnelView{ID: "t1", HostID: "a"}, tunnel: f})

	a.Shutdown(context.Background())
	f.mustBeClosed(t)
}

// The panel's promise is that nothing is hidden. A tunnel is the app reaching a
// service on the user's behalf, which is precisely the question §4.6 answers.
func TestTunnelIsRecordedInTheCommandLog(t *testing.T) {
	a := appWithSettings(t)
	f := newFakeTunnel(t)

	seq := a.log.forwardOpened("h", "ssh -N -L 127.0.0.1:5000:127.0.0.1:3001 me@box")
	a.tunnels.add(&openTunnel{
		view: TunnelView{ID: "t1", HostID: "h"}, tunnel: f, logSeq: seq,
	})

	entries := a.CommandLog()
	if len(entries) != 1 {
		t.Fatalf("%d rows, want 1", len(entries))
	}
	row := entries[0]
	if row.Status != "running" {
		t.Errorf("status %q while the tunnel is carrying traffic", row.Status)
	}
	// The marker is what stops a line LiteDeck did not literally execute from
	// passing for one it did.
	if row.Origin != "tunnel" {
		t.Errorf("origin %q — the row reads as a command that ran", row.Origin)
	}
	// Not folded away as a background read: the user asked for this.
	if row.Kind != "" {
		t.Errorf("kind %q hides the row behind the background toggle", row.Kind)
	}
	if !strings.Contains(row.Line, "ssh -N -L") {
		t.Errorf("line %q is not the command somebody would type", row.Line)
	}

	a.tunnels.close("t1", "the connection dropped")
	entries = a.CommandLog()
	if entries[0].Status != "failed" {
		t.Errorf("status %q — a tunnel taken away reads the same as one closed on purpose",
			entries[0].Status)
	}
	if !strings.Contains(entries[0].Stderr, "dropped") {
		t.Errorf("the reason was lost: %q", entries[0].Stderr)
	}
}

// Probing a port to see whether Kuma is behind it is a capability check, and
// "nothing there" is an answer. Counting those as failures would put a
// permanent red number on the panel and train the user to stop reading it.
func TestKumaProbesDoNotCountAsFailures(t *testing.T) {
	a := appWithSettings(t)
	a.log.probed("h", "curl http://127.0.0.1:3001/", false, "connection refused", 0)

	entries := a.CommandLog()
	if len(entries) != 1 {
		t.Fatalf("%d rows, want 1", len(entries))
	}
	if entries[0].Status != "probe" {
		t.Errorf("status %q — the panel will count this as a failure", entries[0].Status)
	}
	if entries[0].Kind != "probe" {
		t.Errorf("kind %q — the row will not fold with its repeats", entries[0].Kind)
	}

	// Repeats of the same probe fold, exactly like a background read.
	a.log.probed("h", "curl http://127.0.0.1:3001/", false, "connection refused", 0)
	if got := len(a.CommandLog()); got != 1 {
		t.Errorf("%d rows after a repeat probe, want 1", got)
	}
}

/* -------------------------------------------------- the HTTP half, for real */

// fakeKuma serves what a Kuma answers with, on a real socket.
//
// The dialer it returns stands in for the SSH channel. Everything above that
// line — how a landing page is identified, what a 401 means, what reaches the
// Command Log — is exercised against a genuine HTTP server here; that the
// channel itself carries bytes is proven in sshcore against a live sshd.
func fakeKuma(t *testing.T, h http.Handler) (remoteDialer, int) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse %q: %v", srv.URL, err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("port of %q: %v", srv.URL, err)
	}
	// The address the caller asks for is the *server's* loopback; the test
	// server is on this machine's. Rewriting it here is exactly what the SSH
	// channel does in production.
	return func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", u.Host)
	}, port
}

func TestKumaProbeIdentifiesTheLandingPage(t *testing.T) {
	a := appWithSettings(t)
	dial, port := fakeKuma(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<!DOCTYPE html><html><head><title>Uptime Kuma</title></head><body></body></html>`)
	}))

	ok, note := a.probeKuma(dial, "h", port)
	if !ok {
		t.Fatalf("Kuma's own page was not recognised: %s", note)
	}
}

// A port answering is not evidence of what is behind it. Opening a browser at
// somebody's router because it happened to be on 3001 is the failure this
// prevents.
func TestKumaProbeRefusesToGuess(t *testing.T) {
	a := appWithSettings(t)
	dial, port := fakeKuma(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<html><head><title>RT-AC68U Login</title></head></html>`)
	}))

	ok, note := a.probeKuma(dial, "h", port)
	if ok {
		t.Fatal("something that is not Kuma was confirmed as Kuma")
	}
	if !strings.Contains(note, strconv.Itoa(port)) {
		t.Errorf("note %q does not say which port answered", note)
	}
}

// Nothing listening is an answer, not a fault — and it must not put a red row
// in the Command Log.
func TestKumaProbeOnADeadPort(t *testing.T) {
	a := appWithSettings(t)
	dial := func(ctx context.Context, _ string) (net.Conn, error) {
		return nil, errors.New("connect failed: connection refused")
	}

	ok, note := a.probeKuma(dial, "h", 3001)
	if ok {
		t.Fatal("a refused connection was read as Kuma")
	}
	if !strings.Contains(note, "refused") {
		t.Errorf("note %q lost the original reason", note)
	}
	if entries := a.CommandLog(); len(entries) != 1 || entries[0].Status != "probe" {
		t.Errorf("log rows = %+v, want one row that does not count as a failure", entries)
	}
}

func TestKumaStatusReadsTheMetricsEndpoint(t *testing.T) {
	a := appWithSettings(t)
	metrics, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "kuma", "metrics.txt"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}

	var gotPath, gotAuth string
	dial, port := fakeKuma(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = w.Write(metrics)
	}))

	snap, err := a.kumaStatus(dial, "h", port)
	if err != nil {
		t.Fatalf("kumaStatus: %v", err)
	}
	// /metrics and nothing else. /api/* is the surface Kuma's own README says
	// carries no promise to callers like this one.
	if gotPath != "/metrics" {
		t.Errorf("read %q, want /metrics", gotPath)
	}
	if gotAuth != "" {
		t.Errorf("sent an Authorization header with no key stored: %q", gotAuth)
	}
	if snap.Down != 1 || snap.Up != 1 {
		t.Errorf("snapshot = %+v", snap)
	}
}

// Kuma takes the API key as the basic-auth password and ignores the username,
// which is why its own documentation shows `curl -u ":<key>"`.
func TestKumaStatusSendsTheKeyAsTheBasicAuthPassword(t *testing.T) {
	a := appWithSettings(t)
	a.secrets = &fakeSecrets{store: map[string]string{}}
	if err := a.secrets.Set("h", secret.KindKumaAPIKey, "uk1_secret"); err != nil {
		t.Fatalf("store key: %v", err)
	}

	var user, pass string
	var okBasic bool
	dial, port := fakeKuma(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, okBasic = r.BasicAuth()
		_, _ = io.WriteString(w, `monitor_status{monitor_name="x",monitor_type="http",monitor_url="https://x"} 1`+"\n")
	}))

	if _, err := a.kumaStatus(dial, "h", port); err != nil {
		t.Fatalf("kumaStatus: %v", err)
	}
	if !okBasic || pass != "uk1_secret" {
		t.Errorf("basic auth = (%q, %q, %v), want the key as the password", user, pass, okBasic)
	}

	// The Command Log is local, but it is also what people paste into bug
	// reports. A bearer secret has no business being in one.
	for _, e := range a.CommandLog() {
		if strings.Contains(e.Line, "uk1_secret") || strings.Contains(e.Stderr, "uk1_secret") {
			t.Fatalf("the API key reached the Command Log: %q", e.Line)
		}
	}
}

// A 401 is the one failure with a fix the user can act on, and the raw status
// says nothing about where to go.
func TestKumaStatusExplainsA401(t *testing.T) {
	a := appWithSettings(t)
	dial, port := fakeKuma(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))

	_, err := a.kumaStatus(dial, "h", port)
	if err == nil {
		t.Fatal("a 401 was reported as success")
	}
	if !strings.Contains(err.Error(), "API") {
		t.Errorf("error %q does not point at the API key", err)
	}
}

// Something answered, but it is not Kuma. Handing a model an empty monitor list
// would let it conclude that nothing is being watched, which is worse than an
// error — it is a wrong answer delivered confidently.
func TestKumaStatusRefusesAnEmptyReading(t *testing.T) {
	a := appWithSettings(t)
	dial, port := fakeKuma(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "# nothing here\nprocess_cpu_seconds_total 12.5\n")
	}))

	if _, err := a.kumaStatus(dial, "h", port); err == nil {
		t.Fatal("a reading with no monitors was reported as success")
	}
}

/* ------------------------------------------------------------------- fakes */

// fakeTunnel stands in for a forward. The registry only ever calls Close, and
// what has to be tested is which paths call it — not the socket underneath.
type fakeTunnel struct {
	mu     sync.Mutex
	closed int
}

func newFakeTunnel(*testing.T) *fakeTunnel { return &fakeTunnel{} }

func (f *fakeTunnel) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}

func (f *fakeTunnel) mustBeClosed(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed == 0 {
		t.Error("a tunnel was dropped from the registry without being closed — " +
			"its port would go on accepting connections")
	}
}

func (f *fakeTunnel) mustBeOpen(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed != 0 {
		t.Error("an unrelated tunnel was closed")
	}
}

// fakeSecrets is a credential store that works, for tests about what is stored
// rather than about where. secret.Ephemeral refuses every write, which is the
// right default for the app and useless here.
type fakeSecrets struct {
	mu    sync.Mutex
	store map[string]string
}

func (f *fakeSecrets) key(hostID string, k secret.Kind) string {
	return string(k) + ":" + hostID
}

func (f *fakeSecrets) Get(hostID string, k secret.Kind) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.store[f.key(hostID, k)]
	if !ok {
		return "", secret.ErrNotFound
	}
	return v, nil
}

func (f *fakeSecrets) Set(hostID string, k secret.Kind, v string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.store[f.key(hostID, k)] = v
	return nil
}

func (f *fakeSecrets) Delete(hostID string, k secret.Kind) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.store, f.key(hostID, k))
	return nil
}

func (f *fakeSecrets) Available() bool { return true }

// dump serialises a view the way the frontend receives it, so a test can assert
// about the whole struct rather than the fields it remembered to check.
func dump(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
