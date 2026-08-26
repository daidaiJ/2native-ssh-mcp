package manager

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/net/proxy"

	"2native-ssh-mcp/internal/config"
	"2native-ssh-mcp/internal/logger"
)

// dial establishes the SSH connection, optionally through a proxy.
func (m *Manager) dial(key string, cfg *config.SSHConfig) (*ssh.Client, error) {
	authMethods, err := buildAuthMethods(cfg)
	if err != nil {
		return nil, err
	}
	if len(authMethods) == 0 {
		return nil, newToolError(CodeSSHAuthMissing,
			fmt.Sprintf("No valid authentication method provided for [%s] (agent, password, private key, or tryKeyboard)", key), false)
	}

	hkcb, err := buildHostKeyCallback(cfg)
	if err != nil {
		return nil, newToolError(CodeSSHConnectionFailed,
			fmt.Sprintf("SSH connection [%s] failed: %v", key, err), false)
	}
	clientConfig := &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            authMethods,
		HostKeyCallback: hkcb,
		Timeout:         time.Duration(cfg.ConnectionTimeoutMs) * time.Millisecond,
	}
	if cfg.Algorithms != nil {
		clientConfig.Config = ssh.Config{
			KeyExchanges: cfg.Algorithms.Kex,
			Ciphers:      cfg.Algorithms.Cipher,
			MACs:         cfg.Algorithms.Hmac,
		}
		clientConfig.HostKeyAlgorithms = cfg.Algorithms.ServerHostKey
	}

	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
	timeout := time.Duration(cfg.ConnectionTimeoutMs) * time.Millisecond

	conn, err := m.dialTCP(addr, cfg, timeout)
	if err != nil {
		return nil, newToolError(CodeSSHConnectionFailed,
			fmt.Sprintf("SSH connection [%s] failed: %v", key, err), true)
	}
	// OS-level keepalive detects dead links so the heartbeat can react
	// instead of hanging on a half-open connection.
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.SetKeepAlive(true)
		_ = tcpConn.SetKeepAlivePeriod(time.Duration(cfg.KeepaliveIntervalMs) * time.Millisecond)
	}

	// ssh.NewClientConn does not honor ClientConfig.Timeout during the
	// handshake (golang/go#21941), so bound it with a deadline on the
	// underlying conn and clear it afterwards, mirroring Fuchsia
	// connectToSSH.
	_ = conn.SetDeadline(time.Now().Add(timeout))
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientConfig)
	_ = conn.SetDeadline(time.Time{})
	if err != nil {
		conn.Close()
		var te *ToolError
		if errors.As(err, &te) {
			return nil, te
		}
		return nil, newToolError(CodeSSHConnectionFailed,
			fmt.Sprintf("SSH connection [%s] failed: %v", key, err), true)
	}
	return ssh.NewClient(sshConn, chans, reqs), nil
}

// dialTCP opens the TCP connection, going through the configured proxy when
// present. Returns nil when no proxy is configured (direct dial).
func (m *Manager) dialTCP(addr string, cfg *config.SSHConfig, timeout time.Duration) (net.Conn, error) {
	proxyValue := cfg.Proxy
	if proxyValue == "" {
		proxyValue = cfg.SocksProxy
	}
	if proxyValue == "" {
		return net.DialTimeout("tcp", addr, timeout)
	}

	u, err := url.Parse(proxyValue)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy URL: %v", err)
	}
	logger.Info("Using proxy for [%s]: %s", cfg.Name, redactProxyURL(u))

	switch u.Scheme {
	case "socks", "socks5":
		var auth *proxy.Auth
		if u.User != nil {
			auth = &proxy.Auth{User: u.User.Username()}
			if pass, ok := u.User.Password(); ok {
				auth.Password = pass
			}
		}
		dialer, err := proxy.SOCKS5("tcp", u.Host, auth, proxy.Direct)
		if err != nil {
			return nil, err
		}
		return dialer.Dial("tcp", addr)
	case "http", "https":
		return httpProxyDial(u, addr, timeout)
	default:
		return nil, fmt.Errorf("unsupported proxy protocol '%s'. Use socks://, socks5://, http://, or https://", u.Scheme)
	}
}

// bufferedConn preserves bytes buffered by the CONNECT response reader.
type bufferedConn struct {
	reader *bufio.Reader
	net.Conn
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func httpProxyDial(u *url.URL, target string, timeout time.Duration) (net.Conn, error) {
	proxyAddr := u.Host
	if u.Port() == "" {
		if u.Scheme == "https" {
			proxyAddr += ":443"
		} else {
			proxyAddr += ":80"
		}
	}

	conn, err := net.DialTimeout("tcp", proxyAddr, timeout)
	if err != nil {
		return nil, err
	}
	cleanup := func() { conn.Close() }

	if u.Scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: u.Hostname()})
		if err := tlsConn.Handshake(); err != nil {
			cleanup()
			return nil, err
		}
		conn = tlsConn
	}

	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	if u.User != nil {
		cred := base64.StdEncoding.EncodeToString([]byte(u.User.String()))
		req += "Proxy-Authorization: Basic " + cred + "\r\n"
	}
	req += "\r\n"

	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write([]byte(req)); err != nil {
		cleanup()
		return nil, err
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: "CONNECT"})
	if err != nil {
		cleanup()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		cleanup()
		return nil, fmt.Errorf("HTTP proxy CONNECT failed with status %d", resp.StatusCode)
	}
	_ = conn.SetDeadline(time.Time{})
	return &bufferedConn{reader: br, Conn: conn}, nil
}

// buildAuthMethods assembles the SSH auth methods in preference order.
func buildAuthMethods(cfg *config.SSHConfig) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	if cfg.PrivateKey != "" {
		signer, err := loadPrivateKey(cfg.PrivateKey, cfg.Passphrase)
		if err != nil {
			return nil, newToolError(CodeLocalFileReadFailed,
				fmt.Sprintf("Failed to read private key file for [%s]: %v", cfg.Name, err), false)
		}
		methods = append(methods, ssh.PublicKeys(signer))
		logger.Info("Using SSH private key authentication for [%s]", cfg.Name)
	}

	if cfg.Agent != "" {
		signers, err := agentSigners(cfg.Agent)
		if err != nil {
			return nil, newToolError(CodeSSHConnectionFailed,
				fmt.Sprintf("Failed to connect to SSH agent for [%s]: %v", cfg.Name, err), true)
		}
		methods = append(methods, ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
			return signers, nil
		}))
		logger.Info("Using SSH agent authentication for [%s]: %s", cfg.Name, cfg.Agent)
	}

	if cfg.Password != "" {
		methods = append(methods, ssh.Password(cfg.Password))
		logger.Info("Using password authentication for [%s]", cfg.Name)
	}

	if cfg.TryKeyboard {
		methods = append(methods, ssh.KeyboardInteractive(keyboardInteractive(cfg)))
		logger.Info("Using keyboard-interactive authentication for [%s]", cfg.Name)
	}

	return methods, nil
}

// loadPrivateKey parses a PEM private key, optionally encrypted.
func loadPrivateKey(path, passphrase string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if passphrase != "" {
		return ssh.ParsePrivateKeyWithPassphrase(data, []byte(passphrase))
	}
	return ssh.ParsePrivateKey(data)
}

// agentSigners returns the signers from an SSH agent socket or Windows
// Pageant.
func agentSigners(agentPath string) ([]ssh.Signer, error) {
	var conn net.Conn
	var err error
	if agentPath == "pageant" {
		conn, err = pageantConn()
	} else {
		conn, err = net.Dial("unix", agentPath)
	}
	if err != nil {
		return nil, err
	}
	agentClient := agent.NewClient(conn)
	return agentClient.Signers()
}

// keyboardInteractive answers keyboard-interactive prompts: password prompts
// use the configured password, other prompts (e.g. OTP) use the
// SSH_MCP_2FA_CODE environment variable.
func keyboardInteractive(cfg *config.SSHConfig) ssh.KeyboardInteractiveChallenge {
	return func(name, instruction string, questions []string, echos []bool) ([]string, error) {
		otpCode := os.Getenv("SSH_MCP_2FA_CODE")
		answers := make([]string, len(questions))
		for i, q := range questions {
			lower := strings.ToLower(q)
			switch {
			case cfg.Password != "" && (strings.Contains(lower, "password") || strings.Contains(lower, "密码")):
				answers[i] = cfg.Password
			case otpCode != "":
				answers[i] = otpCode
			case cfg.Password != "" && len(questions) == 1 && !echos[i]:
				answers[i] = cfg.Password
			default:
				answers[i] = ""
			}
		}
		return answers, nil
	}
}

func redactProxyURL(u *url.URL) string {
	redacted := *u
	if redacted.User != nil {
		redacted.User = url.UserPassword("***", "***")
	}
	return redacted.String()
}
