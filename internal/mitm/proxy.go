package mitm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/doomsday/agent/internal/filter"
)

// Proxy is a lightweight TLS-intercepting HTTP CONNECT proxy that replaces
// the mitmproxy (Python) subprocess entirely. It runs in-process, listens for
// HTTP CONNECT requests, and for target hosts: decrypts, logs, re-encrypts.
// For non-target hosts it tunnels transparently — no payload inspection.
//
// Key properties:
//   - Pure Go, zero external dependencies, instant startup.
//   - Streams responses to the client in real time (SSE/chunked safe) while
//     accumulating a copy for logging — no extra latency for the user.
//   - Forces HTTP/1.1 over TLS (no h2) to keep framing simple; clients fall
//     back transparently.
//   - Target list is read via a function so manifest hot-reloads take effect
//     for every new connection without restarting the proxy.
type Proxy struct {
	port       int
	ca         *CA
	getTargets func() []string    // called per-connection; allows live manifest updates
	flows      chan<- filter.RawFlow // buffered channel; daemon consumes and filters
}

// NewProxy returns a configured proxy (not yet started).
//
// getTargets is called for each new CONNECT request; returning a live view of
// applyCfg.Load().Filter.TargetHosts lets manifest refreshes take effect
// without restarting the proxy.
// flows may be nil to disable event emission (useful in tests).
func NewProxy(port int, ca *CA, getTargets func() []string, flows chan<- filter.RawFlow) *Proxy {
	return &Proxy{port: port, ca: ca, getTargets: getTargets, flows: flows}
}

// ListenAndServe starts accepting on 127.0.0.1:<port> and blocks until ctx
// is cancelled or a fatal listen error occurs. Returns nil on clean shutdown.
func (p *Proxy) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p.port))
	if err != nil {
		return fmt.Errorf("proxy listen :%d: %w", p.port, err)
	}
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil // clean shutdown
			default:
				log.Printf("[mitm] accept: %v", err)
				continue
			}
		}
		go p.handle(conn)
	}
}

// ---- connection dispatch ------------------------------------------------

func (p *Proxy) handle(conn net.Conn) {
	defer conn.Close()
	// Outer deadline: if a client connects but never sends anything, close it.
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	// Clear the outer deadline — per-exchange deadlines are set later.
	_ = conn.SetDeadline(time.Time{})

	if req.Method == http.MethodConnect {
		p.handleCONNECT(conn, req.Host)
		return
	}
	// Plain HTTP (no CONNECT tunnel). Only forward if it's a target host;
	// the PAC file shouldn't route non-target plain-HTTP here, but be safe.
	if p.matchTarget(hostOnly(req.Host)) {
		p.forwardHTTP(conn, req)
	}
}

// handleCONNECT acknowledges the CONNECT then routes to intercept or tunnel.
func (p *Proxy) handleCONNECT(conn net.Conn, hostPort string) {
	// Acknowledge immediately — client starts TLS (or raw TCP) right after.
	_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 Connection established\r\n\r\n")

	host := hostOnly(hostPort)
	if p.matchTarget(host) {
		p.intercept(conn, host, ensurePort(hostPort, "443"))
	} else {
		p.tunnel(conn, ensurePort(hostPort, "443"))
	}
}

// ---- TLS intercept -------------------------------------------------------

// intercept performs full TLS MITM:
//  1. Presents a forged leaf cert (signed by our CA) to the client.
//  2. Opens a real TLS connection to the upstream server.
//  3. Proxies HTTP/1.1 request/response pairs, emitting a RawFlow per pair.
//
// The response body is streamed to the client as it arrives (real-time SSE
// works), while a copy is accumulated for logging. Only after the full
// response body is consumed is the RawFlow emitted.
func (p *Proxy) intercept(conn net.Conn, host, hostPort string) {
	leafCert, err := p.ca.LeafCert(host)
	if err != nil {
		log.Printf("[mitm] leaf cert %s: %v", host, err)
		return
	}

	// TLS handshake with client.
	// Only advertise http/1.1 — avoids h2 framing complexity; clients fall back.
	clientTLS := tls.Server(conn, &tls.Config{
		Certificates: []tls.Certificate{*leafCert},
		NextProtos:   []string{"http/1.1"},
	})
	_ = clientTLS.SetDeadline(time.Now().Add(15 * time.Second))
	if err := clientTLS.Handshake(); err != nil {
		// Client rejected our CA, network error, or timeout — not actionable.
		return
	}
	_ = clientTLS.SetDeadline(time.Time{}) // clear; per-exchange deadlines below
	defer clientTLS.Close()

	// Real TLS to upstream.
	upstream, err := tls.Dial("tcp", hostPort, &tls.Config{ServerName: host})
	if err != nil {
		log.Printf("[mitm] upstream dial %s: %v", hostPort, err)
		return
	}
	defer upstream.Close()

	clientBR := bufio.NewReaderSize(clientTLS, 64*1024)
	upstreamBR := bufio.NewReaderSize(upstream, 64*1024)

	for {
		// Per-exchange idle deadline (client side).
		_ = clientTLS.SetDeadline(time.Now().Add(120 * time.Second))

		req, err := http.ReadRequest(clientBR)
		if err != nil {
			return // EOF or client closed
		}

		start := time.Now()

		// Buffer request body so we can (a) log it and (b) replay it.
		var reqBuf bytes.Buffer
		if req.Body != nil {
			_, _ = io.Copy(&reqBuf, req.Body)
			req.Body.Close()
		}

		// Remove Accept-Encoding so upstream returns plain (non-gzip) bodies.
		// Clients accept the uncompressed response; logging stays simple.
		req.Header.Del("Accept-Encoding")

		// Clear RequestURI so http.Request.Write uses req.URL (path+query),
		// which is what an HTTP/1.1 server expects (not the full CONNECT URI).
		req.RequestURI = ""
		req.Body = io.NopCloser(bytes.NewReader(reqBuf.Bytes()))

		_ = upstream.SetDeadline(time.Now().Add(120 * time.Second))
		if err := req.Write(upstream); err != nil {
			return
		}

		resp, err := http.ReadResponse(upstreamBR, req)
		if err != nil {
			return
		}

		// Tee response body:
		//   goroutine reads from upstream → writes to both a pipe and respBuf.
		//   resp.Write reads from the pipe → forwards to client in real time.
		// When upstream closes the body, the goroutine closes the pipe write
		// end → resp.Write sees EOF → client gets the complete response.
		// After resp.Write returns, respBuf holds the full body for logging.
		var respBuf bytes.Buffer
		pr, pw := io.Pipe()
		go func() {
			_, _ = io.Copy(io.MultiWriter(&respBuf, pw), resp.Body)
			_ = resp.Body.Close()
			_ = pw.Close()
		}()
		resp.Body = io.NopCloser(pr)

		writeErr := resp.Write(clientTLS)
		// Drain the pipe so the background goroutine can always finish,
		// even if the client disconnected mid-stream.
		_, _ = io.Copy(io.Discard, pr)

		p.emit(req, resp, host, reqBuf.String(), respBuf.String(),
			int(time.Since(start).Milliseconds()))

		if writeErr != nil || req.Close || resp.Close {
			return
		}
	}
}

// ---- transparent tunnel --------------------------------------------------

// tunnel pipes raw bytes bidirectionally without inspection.
// Used for non-target hosts so we don't add any latency to those connections.
func (p *Proxy) tunnel(client net.Conn, hostPort string) {
	upstream, err := net.DialTimeout("tcp", hostPort, 10*time.Second)
	if err != nil {
		return
	}
	defer upstream.Close()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
}

// ---- plain HTTP forward --------------------------------------------------

// forwardHTTP handles a non-tunnelled HTTP (plain-text) request. Uncommon for
// AI APIs (all use HTTPS), but handled for completeness.
func (p *Proxy) forwardHTTP(conn net.Conn, req *http.Request) {
	start := time.Now()

	var reqBuf bytes.Buffer
	if req.Body != nil {
		_, _ = io.Copy(&reqBuf, req.Body)
		req.Body.Close()
	}
	req.Header.Del("Accept-Encoding")
	req.Body = io.NopCloser(bytes.NewReader(reqBuf.Bytes()))

	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		_, _ = fmt.Fprintf(conn, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}

	var respBuf bytes.Buffer
	pr, pw := io.Pipe()
	go func() {
		_, _ = io.Copy(io.MultiWriter(&respBuf, pw), resp.Body)
		_ = resp.Body.Close()
		_ = pw.Close()
	}()
	resp.Body = io.NopCloser(pr)

	_ = resp.Write(conn)
	_, _ = io.Copy(io.Discard, pr)

	p.emit(req, resp, hostOnly(req.Host), reqBuf.String(), respBuf.String(),
		int(time.Since(start).Milliseconds()))
}

// ---- event emission ------------------------------------------------------

func (p *Proxy) emit(req *http.Request, resp *http.Response, host, reqBody, respBody string, durationMs int) {
	if p.flows == nil {
		return
	}

	// Reconstruct a full URL from the host and req.URL (which only has
	// path+query after http.ReadRequest inside a CONNECT tunnel).
	reqURI := req.URL.RequestURI()
	if reqURI == "" {
		reqURI = "/"
	}
	fullURL := "https://" + host + reqURI

	flow := filter.RawFlow{
		EventID:    uuid.New().String(),
		CapturedAt: time.Now().UTC().Format(time.RFC3339),
		DurationMs: durationMs,
		Request: filter.RawRequest{
			Method:    req.Method,
			Scheme:    "https",
			Host:      host,
			Path:      req.URL.Path,
			URL:       fullURL,
			Headers:   flatHeaders(req.Header),
			Body:      reqBody,
			BodyBytes: len(reqBody),
		},
	}
	if resp != nil {
		flow.Response = filter.RawResponse{
			StatusCode: resp.StatusCode,
			Headers:    flatHeaders(resp.Header),
			Body:       respBody,
			BodyBytes:  len(respBody),
		}
	}

	// Non-blocking send: if the filter pipeline is behind, drop rather than
	// stall the proxy goroutine (which would back-pressure the client).
	select {
	case p.flows <- flow:
	default:
		log.Printf("[mitm] flow channel full, dropping event for %s%s", host, req.URL.Path)
	}
}

// ---- helpers -------------------------------------------------------------

// matchTarget returns true if host matches any entry returned by getTargets.
// Entries starting with '.' are suffix-matched; others require exact equality.
func (p *Proxy) matchTarget(host string) bool {
	h := strings.ToLower(host)
	for _, t := range p.getTargets() {
		t = strings.ToLower(t)
		if strings.HasPrefix(t, ".") {
			// ".foo.com" matches "sub.foo.com" and "foo.com"
			if strings.HasSuffix(h, t) || h == t[1:] {
				return true
			}
		} else if h == t {
			return true
		}
	}
	return false
}

// hostOnly strips the port component from "host:port"; returns host as-is if
// there is no port or parsing fails.
func hostOnly(hostPort string) string {
	h, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return hostPort
	}
	return h
}

// ensurePort returns "host:port" unchanged if it already has a port, otherwise
// appends defaultPort.
func ensurePort(hostPort, defaultPort string) string {
	_, _, err := net.SplitHostPort(hostPort)
	if err == nil {
		return hostPort // already has a port
	}
	return net.JoinHostPort(hostPort, defaultPort)
}

// flatHeaders collapses http.Header into the map[string]string shape expected
// by filter.RawRequest / RawResponse. Only the first value per key is kept;
// keys are lowercased to match the tap's output format.
func flatHeaders(h http.Header) map[string]string {
	m := make(map[string]string, len(h))
	for k, vs := range h {
		if len(vs) > 0 {
			m[strings.ToLower(k)] = vs[0]
		}
	}
	return m
}
