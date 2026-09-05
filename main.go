// 2native-ssh-mcp is an SSH-based MCP server: it exposes SSH command
// execution and file transfer as MCP tools over stdio or streamable HTTP.
package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"2native-ssh-mcp/internal/config"
	"2native-ssh-mcp/internal/daemon"
	"2native-ssh-mcp/internal/logger"
	"2native-ssh-mcp/internal/manager"
	"2native-ssh-mcp/internal/tools"
)

// version is overridden at build time via -ldflags "-X main.version=<tag>".
var version = "1.0.0"

func main() {
	args := os.Args[1:]

	if config.IsHelpRequest(args) {
		fmt.Println(config.Usage)
		return
	}
	if config.IsVersionRequest(args) {
		fmt.Println(version)
		return
	}

	if len(args) > 0 {
		switch args[0] {
		case "start":
			runStart(args[1:])
			return
		case "stop":
			runStop()
			return
		case "kill":
			runKill()
			return
		case "status":
			runStatus()
			return
		case "install":
			if err := daemon.Install(); err != nil {
				fmt.Fprintf(os.Stderr, "install failed: %v\n", err)
				os.Exit(1)
			}
			return
		case "uninstall":
			if err := daemon.Uninstall(); err != nil {
				fmt.Fprintf(os.Stderr, "uninstall failed: %v\n", err)
				os.Exit(1)
			}
			return
		case "version":
			fmt.Println(version)
			return
		case "help":
			fmt.Println(config.Usage)
			return
		}
	}

	runForeground(args)
}

// runForeground runs the server in the foreground: stdio by default, or
// streamable HTTP with --transport http.
func runForeground(args []string) {
	opts, err := config.ParseArgs(args)
	if err != nil {
		logger.Error("%v", err)
		os.Exit(1)
	}
	if len(opts.Configs) == 0 {
		fmt.Fprintln(os.Stderr, "No SSH configuration provided. Use --help for usage.")
		os.Exit(1)
	}

	m, err := manager.New(opts.Configs, opts.CommandLogDir)
	if err != nil {
		logger.Error("%v", err)
		os.Exit(1)
	}
	if opts.PreConnect {
		preConnect(m)
	}

	s := newMCPServer(m)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if opts.Transport == "http" {
		runHTTPServer(ctx, s, m, opts.HTTPAddr, opts.HTTPToken, nil)
		return
	}

	logger.Info("Starting SSH MCP server (stdio)")
	stdioServer := server.NewStdioServer(s)
	if err := stdioServer.Listen(ctx, os.Stdin, os.Stdout); err != nil {
		logger.Error("stdio server error: %v", err)
	}
	m.DisconnectAll()
}

// runStart starts the HTTP daemon with refcount management and admin
// endpoints, then waits for a shutdown request or signal.
func runStart(args []string) {
	opts, err := config.ParseArgs(args)
	if err != nil {
		logger.Error("%v", err)
		os.Exit(1)
	}
	if len(opts.Configs) == 0 {
		fmt.Fprintln(os.Stderr, "No SSH configuration provided. Use --help for usage.")
		os.Exit(1)
	}

	port, err := portFromAddr(opts.HTTPAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// If the daemon is already running, just increase the refcount.
	if _, err := daemon.GetHealth(port); err == nil {
		refResp, err := daemon.PostRefCount(port, 1)
		if err != nil {
			fmt.Fprintf(os.Stderr, "server is running, but failed to increase refcount: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("server is already running, refcount increased to %d\n", refResp.RefCount)
		return
	}
	_ = daemon.RemovePID()

	m, err := manager.New(opts.Configs, opts.CommandLogDir)
	if err != nil {
		logger.Error("%v", err)
		os.Exit(1)
	}
	if opts.PreConnect {
		preConnect(m)
	}

	s := newMCPServer(m)
	admin := daemon.NewAdmin(1, opts.HTTPAddr)
	if err := daemon.WritePID(os.Getpid(), port); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write PID file: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("Starting SSH MCP server (HTTP daemon) on %s", opts.HTTPAddr)
	// Stop the HTTP server when the refcount reaches zero or a signal arrives.
	go func() {
		select {
		case <-admin.ShutdownCh():
			logger.Info("Refcount reached zero, initiating graceful shutdown")
			stop()
		case <-ctx.Done():
		}
	}()

	runHTTPServer(ctx, s, m, opts.HTTPAddr, opts.HTTPToken, admin)
	_ = daemon.RemovePID()
}

// preConnect establishes every configured connection up front. The point of
// --pre-connect is fail-fast: any failure exits non-zero instead of starting
// a server that cannot reach its hosts.
func preConnect(m *manager.Manager) {
	for _, name := range m.ConfigNames() {
		if _, err := m.EnsureConnected(name); err != nil {
			logger.Error("pre-connect failed for [%s]: %v", name, err)
			os.Exit(1)
		}
	}
}

// runHTTPServer serves the MCP endpoint at /mcp and, when admin is set, the
// daemon admin API under /__admin/. A non-loopback listen address requires a
// token (fail closed): without one the server refuses to start.
func runHTTPServer(ctx context.Context, s *server.MCPServer, m *manager.Manager, addr, httpToken string, admin *daemon.Admin) {
	if httpToken == "" && !isLoopbackBind(addr) {
		logger.Error("refusing to start HTTP server on %s: non-loopback listen requires a token (--http-token, SSH_MCP_HTTP_TOKEN, or $global.httpToken)", addr)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	httpServer := &http.Server{Addr: addr, Handler: mux}
	streamable := server.NewStreamableHTTPServer(s, server.WithStreamableHTTPServer(httpServer))
	var mcpHandler http.Handler = streamable
	if admin != nil {
		// Authorized /mcp traffic keeps guest leases alive; the daemon's
		// own owner lease never expires.
		mcpHandler = touchGuests(mcpHandler, admin)
	}
	if httpToken != "" {
		mcpHandler = requireToken(mcpHandler, httpToken)
	}
	mux.Handle("/mcp", mcpHandler)
	if admin != nil {
		mux.Handle("/__admin/", admin.Handler())
		go admin.StartLeaseTicker(ctx)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- streamable.Start(addr) }()

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server error: %v", err)
		}
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = streamable.Shutdown(shutdownCtx)
	m.DisconnectAll()
}

// newMCPServer builds the MCP server with all tools registered. WithElicitation
// advertises the server side of the elicitation capability; whether a prompt
// actually appears depends on the client declaring its side at initialize.
func newMCPServer(m *manager.Manager) *server.MCPServer {
	s := server.NewMCPServer("2native-ssh-mcp", version, server.WithElicitation())
	tools.RegisterAll(s, m)
	return s
}

// runStop decreases the refcount; the daemon exits when it reaches zero.
func runStop() {
	port := daemonPort()
	if _, err := daemon.GetHealth(port); err != nil {
		fmt.Println("server is not running")
		_ = daemon.RemovePID()
		return
	}
	refResp, err := daemon.PostRefCount(port, -1)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to decrease refcount: %v\n", err)
		os.Exit(1)
	}
	if refResp.RefCount > 0 {
		fmt.Printf("refcount decreased to %d, server continues running\n", refResp.RefCount)
		return
	}
	fmt.Println("refcount reached zero, waiting for server to exit...")
	if daemon.WaitForExit(port, 10*time.Second) {
		fmt.Println("server exited gracefully")
		_ = daemon.RemovePID()
	} else {
		fmt.Println("timeout waiting for graceful exit, use 'kill' to force stop")
		os.Exit(1)
	}
}

// runKill force-stops the daemon, falling back to the PID file.
func runKill() {
	port := daemonPort()
	if _, err := daemon.GetHealth(port); err != nil {
		fmt.Println("server is not running")
		_ = daemon.RemovePID()
		return
	}
	_ = daemon.PostShutdown(port)
	if daemon.WaitForExit(port, 3*time.Second) {
		fmt.Println("server exited gracefully")
		_ = daemon.RemovePID()
		return
	}
	info, pidErr := daemon.ReadPID()
	if pidErr == nil && info != nil && daemon.IsRunning(info.PID) {
		if err := daemon.KillProcess(info.PID); err != nil {
			fmt.Fprintf(os.Stderr, "failed to kill process %d: %v\n", info.PID, err)
			os.Exit(1)
		}
		fmt.Printf("server (PID %d) killed\n", info.PID)
		_ = daemon.RemovePID()
		return
	}
	fmt.Println("server did not respond to shutdown request")
	os.Exit(1)
}

// runStatus prints the daemon status.
func runStatus() {
	port := daemonPort()
	resp, err := daemon.GetHealth(port)
	if err != nil {
		fmt.Println("server status: stopped")
		_ = daemon.RemovePID()
		return
	}
	fmt.Printf("server status: running (port %d, refcount %d)\n", port, resp.RefCount)
}

// daemonPort returns the port from the PID file, falling back to the default.
func daemonPort() int {
	if info, err := daemon.ReadPID(); err == nil && info.Port > 0 {
		return info.Port
	}
	port, _ := portFromAddr(config.DefaultHTTPAddr)
	return port
}

// portFromAddr extracts the port from a host:port address. It returns an
// error instead of silently falling back to a default so a mistyped
// --http-addr cannot target the wrong daemon.
func portFromAddr(addr string) (int, error) {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("invalid --http-addr %q: %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("invalid --http-addr %q: port %q is not a number", addr, portStr)
	}
	return port, nil
}

// isLoopbackBind reports whether the listen address binds a loopback
// interface only. An empty host (":8338") binds all interfaces and is not
// loopback.
func isLoopbackBind(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// touchGuests refreshes guest lease expiries after each authorized /mcp
// request, so an actively used daemon never drops its extra refcounts.
func touchGuests(next http.Handler, admin *daemon.Admin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		admin.TouchGuests()
		next.ServeHTTP(w, r)
	})
}

// requireToken wraps a handler with Bearer token authentication. Requests
// without a matching Authorization: Bearer <token> header get 401.
func requireToken(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, prefix) ||
			subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, prefix)), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="2native-ssh-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
