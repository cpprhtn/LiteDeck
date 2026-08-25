// Command litedeck-server runs LiteDeck headless and serves its UI over HTTP,
// so a browser can drive the same core the desktop app does (arch/08).
//
// It links no webview: pure Go, CGO-free, one static binary you drop on a
// server. The frontend talks to it through exactly the two seams it uses under
// Wails — method calls (POST /rpc/<Method>) and events (a WebSocket) — filled
// by frontend/src/webTransport.ts.
//
// # Exposure is the operator's job
//
// It binds 127.0.0.1 by default and expects a reverse proxy (oauth2-proxy,
// Cloudflare Access, Tailscale) to add TLS and authentication for anything
// reachable from off the box — the same stance Grafana takes. Binding beyond
// loopback without a --token is refused, because this endpoint can open SSH
// sessions to production servers, and (headless, with no OS keychain) it will
// ask for those servers' passwords in the browser: over plain HTTP that
// credential is cleartext on the wire. See arch/08 §5.
package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cpprhtn/LiteDeck/frontend"
	"github.com/cpprhtn/LiteDeck/internal/app"
	"github.com/cpprhtn/LiteDeck/internal/webrpc"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8765", "address to listen on")
	token := flag.String("token", "", "require this bearer token on every request (needed to bind beyond loopback)")
	selfUser := flag.String("self", "", "manage the box this runs on: connect to its own sshd on boot as this user (Grafana/Cockpit style)")
	selfPort := flag.Int("self-port", 22, "port of the local sshd for --self")
	selfKey := flag.String("self-key", "", "identity file for --self (a passphrase-less key is the clean credential)")
	selfAgent := flag.Bool("self-agent", false, "use ssh-agent for --self")
	flag.Parse()

	var self *app.SelfConfig
	if *selfUser != "" {
		self = &app.SelfConfig{
			User:     *selfUser,
			Port:     *selfPort,
			KeyFile:  *selfKey,
			Password: os.Getenv("LITEDECK_SELF_PASSWORD"), // never on argv (visible in ps)
			UseAgent: *selfAgent,
		}
	}

	if err := run(*addr, *token, self); err != nil {
		log.Fatalf("litedeck-server: %v", err)
	}
}

func run(addr, token string, self *app.SelfConfig) error {
	if err := guardExposure(addr, token); err != nil {
		return err
	}

	a := app.New()
	srv := webrpc.NewServer(webrpc.New(a), token)
	srv.SetUploader(a) // POST /upload streams browser files to the remote over SFTP

	// Events reach the browser over the WebSocket instead of the Wails runtime.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a.StartupHeadless(ctx, srv.Emit)

	// "This server" mode: connect to the local box's own sshd so opening the UI
	// lands already inside it. A failure here is logged, not fatal — the UI
	// still comes up and the user can connect by hand.
	if self != nil {
		if err := a.ConnectSelf(ctx, *self); err != nil {
			log.Printf("litedeck-server: --self connect failed: %v", err)
		} else {
			log.Printf("litedeck-server: connected to this server as %s@127.0.0.1:%d", self.User, self.Port)
		}
	}

	mux := srv.Handler()          // owns /rpc/ and /ws
	mux.Handle("/", staticSite()) // the embedded UI, with SPA fallback

	httpSrv := &http.Server{
		Addr:    addr,
		Handler: mux,
		// No WriteTimeout: a synchronous /rpc/ConnectHost can legitimately hold
		// the request open for minutes while a host-key or password prompt is
		// answered in the browser. ReadHeaderTimeout still bounds a slow-loris.
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		log.Printf("litedeck-server: shutting down")
		// Quiesce HTTP first, then the app (closes SSH, PTYs, log streams, MCP).
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(sctx)
		a.Shutdown(context.Background())
	}()

	log.Printf("litedeck-server: listening on http://%s (bind %s)", addr, bindNote(addr, token))
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// guardExposure refuses to bind beyond loopback without a token. Same principle
// as the MCP endpoint's loopback-only rule: the thing keeping an SSH-driving
// endpoint safe is that it cannot be reached, or that reaching it needs a
// secret. A reverse proxy in front satisfies neither by itself, so the operator
// opts in explicitly with --token when they front it.
func guardExposure(addr, token string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("addr %q is not host:port: %w", addr, err)
	}
	if token != "" {
		return nil
	}
	ip := net.ParseIP(host)
	loopback := host == "localhost" || (ip != nil && ip.IsLoopback())
	if !loopback {
		return fmt.Errorf("refusing to bind %s without --token: this endpoint can open SSH "+
			"sessions to your servers, so a non-loopback bind must require a token (or put it "+
			"behind a reverse proxy and bind 127.0.0.1)", addr)
	}
	return nil
}

func bindNote(addr, token string) string {
	if token != "" {
		return "token required"
	}
	return "loopback, no token — put a reverse proxy in front to expose it"
}

// staticSite serves the embedded UI. Anything that is not a real asset falls
// back to index.html so the single-page app renders (its tabs are state, not
// routes, but a stray path should still load the app rather than 404).
func staticSite() http.Handler {
	dist, err := fs.Sub(frontend.Assets, "dist")
	if err != nil {
		log.Fatalf("litedeck-server: embedded assets: %v", err)
	}
	fileServer := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(dist, trimLeadingSlash(r.URL.Path)); err != nil && r.URL.Path != "/" {
			// Not a file we have — serve the app shell.
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}

func trimLeadingSlash(p string) string {
	if p == "/" {
		return "."
	}
	if len(p) > 0 && p[0] == '/' {
		return p[1:]
	}
	return p
}
