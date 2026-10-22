package headless_browser

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"

	"github.com/go-rod/rod/lib/cdp"
)

type handshakeDialer struct {
	secure bool
	mu     sync.Mutex
	conn   *handshakeConn
}

type handshakeConn struct {
	net.Conn
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func (d *handshakeDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	var conn net.Conn
	var err error
	if d.secure {
		conn, err = (&tls.Dialer{NetDialer: &net.Dialer{}}).DialContext(ctx, network, address)
	} else {
		conn, err = (&net.Dialer{}).DialContext(ctx, network, address)
	}
	if err != nil {
		return nil, err
	}
	wrapped := &handshakeConn{Conn: conn, stop: make(chan struct{}), done: make(chan struct{})}
	d.mu.Lock()
	d.conn = wrapped
	d.mu.Unlock()
	go func() {
		defer close(wrapped.done)
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-wrapped.stop:
		}
	}()
	return wrapped, nil
}

func (c *handshakeConn) stopCancellation() {
	c.stopOnce.Do(func() { close(c.stop) })
	<-c.done
}

func (c *handshakeConn) close() error {
	c.stopCancellation()
	return c.Conn.Close()
}

func (d *handshakeDialer) connection() *handshakeConn {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conn
}

func connectCancelableWebSocket(ctx context.Context, wsURL string, header http.Header) (*cdp.WebSocket, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	u, err := url.Parse(wsURL)
	if err != nil {
		return nil, err
	}
	secure := u.Scheme == "wss"
	if u.Scheme != "ws" && !secure {
		return nil, errors.New("unsupported CDP websocket scheme")
	}
	if secure && u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), "443")
	}
	dialer := &handshakeDialer{secure: secure}
	ws := &cdp.WebSocket{Dialer: dialer}
	if err := ws.Connect(ctx, u.String(), header); err != nil {
		if conn := dialer.connection(); conn != nil {
			_ = conn.close()
		}
		return nil, err
	}
	conn := dialer.connection()
	if conn == nil {
		return nil, errors.New("CDP websocket connected without a transport")
	}
	if err := ctx.Err(); err != nil {
		_ = conn.close()
		return nil, err
	}
	conn.stopCancellation()
	if err := ctx.Err(); err != nil {
		_ = ws.Close()
		return nil, err
	}
	return ws, nil
}
