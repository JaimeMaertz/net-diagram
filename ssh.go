package netdiagram

import (
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// DialSSHCLI opens an interactive SSH shell on a switch whose CLI is used like a terminal (TP-Link
// JetStream) and logs in to privileged mode as DialTelnet does. Older key exchanges, ciphers and host
// key types are offered too, since switch SSH servers are often old. The password is never logged or
// returned.
func DialSSHCLI(host, user, password, enablePassword string) (*CLISession, error) {
	supported, insecure := ssh.SupportedAlgorithms(), ssh.InsecureAlgorithms()
	cfg := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = password
				}
				return answers, nil
			}),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
		Config: ssh.Config{
			KeyExchanges: append(supported.KeyExchanges, insecure.KeyExchanges...),
			Ciphers:      append(supported.Ciphers, insecure.Ciphers...),
			MACs:         append(supported.MACs, insecure.MACs...),
		},
		HostKeyAlgorithms: append(supported.HostKeys, insecure.HostKeys...),
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(host, "22"), cfg)
	if err != nil {
		if strings.Contains(err.Error(), "unable to authenticate") {
			return nil, ErrAuth
		}
		return nil, err
	}
	sess, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, err
	}
	// A wide terminal so tables don't wrap; the CLI's own pager is answered as on telnet.
	if err := sess.RequestPty("vt100", 200, 500, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
		client.Close()
		return nil, err
	}
	in, err := sess.StdinPipe()
	if err != nil {
		client.Close()
		return nil, err
	}
	out, err := sess.StdoutPipe()
	if err != nil {
		client.Close()
		return nil, err
	}
	if err := sess.Shell(); err != nil {
		client.Close()
		return nil, err
	}
	c := &sshConn{client: client, in: in, chunks: make(chan []byte, 64)}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := out.Read(buf)
			if n > 0 {
				c.chunks <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				close(c.chunks)
				return
			}
		}
	}()
	return startCLI(c, "\r", firstPrompt, user, password, enablePassword)
}

// sshConn gives an SSH shell channel the read deadline a telnet socket has: the shell's output is
// read by one goroutine into chunks, and Read waits for the next chunk until the deadline.
type sshConn struct {
	client   *ssh.Client
	in       interface{ Write([]byte) (int, error) }
	chunks   chan []byte
	mu       sync.Mutex
	deadline time.Time
	rest     []byte
}

func (c *sshConn) Read(p []byte) (int, error) {
	if len(c.rest) == 0 {
		c.mu.Lock()
		d := c.deadline
		c.mu.Unlock()
		var timeout <-chan time.Time
		if !d.IsZero() {
			t := time.NewTimer(time.Until(d))
			defer t.Stop()
			timeout = t.C
		}
		select {
		case b, ok := <-c.chunks:
			if !ok {
				return 0, errors.New("the switch closed the SSH session")
			}
			c.rest = b
		case <-timeout:
			return 0, os.ErrDeadlineExceeded
		}
	}
	n := copy(p, c.rest)
	c.rest = c.rest[n:]
	return n, nil
}

func (c *sshConn) Write(p []byte) (int, error) { return c.in.Write(p) }
func (c *sshConn) Close() error                { return c.client.Close() }
func (c *sshConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	return nil
}
func (c *sshConn) SetWriteDeadline(time.Time) error { return nil }
