package providers

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	imapclient "github.com/emersion/go-imap/client"
)

type singleConnDialer struct {
	mu   sync.Mutex
	conn net.Conn
}

func (d *singleConnDialer) Dial(string, string) (net.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn == nil {
		return nil, errors.New("test connection already used")
	}
	conn := d.conn
	d.conn = nil
	return conn, nil
}

func TestHTTPConnectDialerPreservesBufferedIMAPGreeting(t *testing.T) {
	clientConn, proxyConn := net.Pipe()
	proxyURL, err := url.Parse("http://fixture-user:fixture-pass@proxy.fixture.test:8080")
	if err != nil {
		t.Fatal(err)
	}
	dialer := &httpConnectDialer{
		proxyURL: proxyURL,
		base:     &singleConnDialer{conn: clientConn},
	}
	proxyDone := make(chan error, 1)
	go func() {
		defer proxyConn.Close()
		reader := bufio.NewReader(proxyConn)
		request, err := http.ReadRequest(reader)
		if err != nil {
			proxyDone <- err
			return
		}
		defer request.Body.Close()
		wantAuthorization := "Basic " + base64.StdEncoding.EncodeToString([]byte("fixture-user:fixture-pass"))
		if request.Method != http.MethodConnect || request.RequestURI != "imap.fixture.test:143" || request.Host != "imap.fixture.test:143" {
			proxyDone <- fmt.Errorf("CONNECT request method=%q uri=%q host=%q", request.Method, request.RequestURI, request.Host)
			return
		}
		if got := request.Header.Get("Proxy-Authorization"); got != wantAuthorization {
			proxyDone <- fmt.Errorf("proxy authorization = %q", got)
			return
		}
		// A single write makes the IMAP greeting available to the same
		// buffered read which parses the CONNECT response headers.
		_, err = io.WriteString(proxyConn, "HTTP/1.1 200 Connection Established\r\n\r\n* OK [CAPABILITY IMAP4rev1] fixture ready\r\n")
		time.Sleep(50 * time.Millisecond)
		proxyDone <- err
	}()

	client, err := imapclient.DialWithDialer(dialer, "imap.fixture.test:143")
	if err != nil {
		t.Fatalf("dial IMAP through CONNECT proxy: %v", err)
	}
	client.ErrorLog = log.New(io.Discard, "", 0)
	_ = client.Terminate()
	if err := <-proxyDone; err != nil {
		t.Fatal(err)
	}
}

func TestHTTPSConnectDialerTunnelsIMAPGreeting(t *testing.T) {
	requests := make(chan error, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		wantAuthorization := "Basic " + base64.StdEncoding.EncodeToString([]byte("tls-user:tls-pass"))
		if request.Method != http.MethodConnect || request.RequestURI != "imap.fixture.test:993" || request.Header.Get("Proxy-Authorization") != wantAuthorization {
			requests <- fmt.Errorf("HTTPS CONNECT method=%q uri=%q authorization=%q", request.Method, request.RequestURI, request.Header.Get("Proxy-Authorization"))
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			requests <- errors.New("HTTPS proxy response writer does not support hijacking")
			return
		}
		conn, buffer, err := hijacker.Hijack()
		if err != nil {
			requests <- err
			return
		}
		defer conn.Close()
		if _, err := buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n* OK fixture TLS proxy ready\r\n"); err != nil {
			requests <- err
			return
		}
		if err := buffer.Flush(); err != nil {
			requests <- err
			return
		}
		requests <- nil
	}))
	t.Cleanup(server.Close)
	proxyURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL.User = url.UserPassword("tls-user", "tls-pass")
	transport, ok := server.Client().Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil {
		t.Fatal("test HTTPS proxy client has no TLS configuration")
	}
	dialer := &httpConnectDialer{
		proxyURL:       proxyURL,
		base:           &net.Dialer{Timeout: time.Second},
		proxyTLSConfig: transport.TLSClientConfig,
	}
	client, err := imapclient.DialWithDialer(dialer, "imap.fixture.test:993")
	if err != nil {
		t.Fatalf("dial IMAP through HTTPS CONNECT proxy: %v", err)
	}
	client.ErrorLog = log.New(io.Discard, "", 0)
	_ = client.Terminate()
	if err := <-requests; err != nil {
		t.Fatal(err)
	}
}

func TestHTTPConnectDialerSupportsImplicitTLSTarget(t *testing.T) {
	certificateServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	certificate := certificateServer.TLS.Certificates[0]
	transport, ok := certificateServer.Client().Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil {
		certificateServer.Close()
		t.Fatal("test IMAP TLS server has no client TLS configuration")
	}
	targetClientTLS := transport.TLSClientConfig.Clone()
	certificateServer.Close()

	clientConn, proxyConn := net.Pipe()
	proxyURL, _ := url.Parse("http://proxy.fixture.test:8080")
	dialer := &httpConnectDialer{proxyURL: proxyURL, base: &singleConnDialer{conn: clientConn}}
	proxyDone := make(chan error, 1)
	go func() {
		defer proxyConn.Close()
		request, err := http.ReadRequest(bufio.NewReader(proxyConn))
		if err != nil {
			proxyDone <- err
			return
		}
		_ = request.Body.Close()
		if request.Method != http.MethodConnect || request.RequestURI != "example.com:993" {
			proxyDone <- fmt.Errorf("TLS target CONNECT method=%q uri=%q", request.Method, request.RequestURI)
			return
		}
		if _, err := io.WriteString(proxyConn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			proxyDone <- err
			return
		}
		target := tls.Server(proxyConn, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
		if err := target.Handshake(); err != nil {
			proxyDone <- err
			return
		}
		if _, err := io.WriteString(target, "* OK fixture implicit TLS ready\r\n"); err != nil {
			proxyDone <- err
			return
		}
		time.Sleep(50 * time.Millisecond)
		proxyDone <- nil
	}()

	client, err := imapclient.DialWithDialerTLS(dialer, "example.com:993", targetClientTLS)
	if err != nil {
		t.Fatalf("dial implicit TLS IMAP through CONNECT proxy: %v", err)
	}
	client.ErrorLog = log.New(io.Discard, "", 0)
	_ = client.Terminate()
	if err := <-proxyDone; err != nil {
		t.Fatal(err)
	}
}

func TestHTTPConnectDialerClassifiesIncompleteResponse(t *testing.T) {
	clientConn, proxyConn := net.Pipe()
	proxyURL, _ := url.Parse("http://fixture-user:fixture-pass@proxy.fixture.test:8080")
	dialer := &httpConnectDialer{proxyURL: proxyURL, base: &singleConnDialer{conn: clientConn}}
	proxyDone := make(chan error, 1)
	go func() {
		defer proxyConn.Close()
		request, err := http.ReadRequest(bufio.NewReader(proxyConn))
		if err == nil {
			_ = request.Body.Close()
			_, err = io.WriteString(proxyConn, "HTTP/1.1 200 Connection Established\r\n")
		}
		proxyDone <- err
	}()
	_, err := dialer.Dial("tcp", "imap.fixture.test:993")
	if err == nil || !errors.Is(err, io.ErrUnexpectedEOF) || !strings.Contains(err.Error(), "before completing HTTP headers") {
		t.Fatalf("incomplete CONNECT response error = %v", err)
	}
	if strings.Contains(err.Error(), "fixture-user") || strings.Contains(err.Error(), "fixture-pass") {
		t.Fatalf("proxy error exposed credentials: %v", err)
	}
	if err := <-proxyDone; err != nil {
		t.Fatal(err)
	}
}

func TestHTTPConnectDialerTimesOutStalledResponse(t *testing.T) {
	clientConn, proxyConn := net.Pipe()
	proxyURL, _ := url.Parse("http://proxy.fixture.test:8080")
	dialer := &httpConnectDialer{
		proxyURL:         proxyURL,
		base:             &singleConnDialer{conn: clientConn},
		handshakeTimeout: 50 * time.Millisecond,
	}
	proxyDone := make(chan error, 1)
	go func() {
		defer proxyConn.Close()
		request, err := http.ReadRequest(bufio.NewReader(proxyConn))
		if err == nil {
			_ = request.Body.Close()
			buffer := make([]byte, 1)
			_, err = proxyConn.Read(buffer)
		}
		proxyDone <- err
	}()
	started := time.Now()
	_, err := dialer.Dial("tcp", "imap.fixture.test:993")
	if err == nil || !strings.Contains(err.Error(), "read IMAP proxy CONNECT response") {
		t.Fatalf("stalled CONNECT response error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stalled CONNECT response took %s", elapsed)
	}
	if err := <-proxyDone; err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}
