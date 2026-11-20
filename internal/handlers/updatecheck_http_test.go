package handlers

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type updateTestAddr string

func (a updateTestAddr) Network() string { return "tcp" }
func (a updateTestAddr) String() string  { return string(a) }

// Exercise real httptest HTTP servers over net.Pipe even when sockets are
// forbidden. Production still uses the shared, TLS-verifying transport.
type updateTestListener struct {
	connections chan net.Conn
	done        chan struct{}
	once        sync.Once
	addr        updateTestAddr
}

func (l *updateTestListener) Accept() (net.Conn, error) {
	select {
	case connection := <-l.connections:
		return connection, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *updateTestListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *updateTestListener) Addr() net.Addr { return l.addr }

func updateTestServers(t *testing.T) func(http.Handler) *httptest.Server {
	t.Helper()
	old := http.DefaultTransport
	transport := old.(*http.Transport).Clone()
	transport.Proxy = nil
	var listeners sync.Map
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		value, ok := listeners.Load(address)
		if !ok {
			return nil, net.ErrClosed
		}
		listener := value.(*updateTestListener)
		client, server := net.Pipe()
		select {
		case listener.connections <- server:
			return client, nil
		case <-listener.done:
			client.Close()
			server.Close()
			return nil, net.ErrClosed
		case <-ctx.Done():
			client.Close()
			server.Close()
			return nil, ctx.Err()
		}
	}
	http.DefaultTransport = transport
	t.Cleanup(func() { transport.CloseIdleConnections(); http.DefaultTransport = old })
	index := 0
	return func(handler http.Handler) *httptest.Server {
		index++
		listener := &updateTestListener{connections: make(chan net.Conn), done: make(chan struct{}), addr: updateTestAddr(fmt.Sprintf("127.0.0.1:%d", 20000+index))}
		listeners.Store(listener.addr.String(), listener)
		server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
		server.Start()
		t.Cleanup(server.Close)
		return server
	}
}
