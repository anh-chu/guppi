package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/auth"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/peer"
	"github.com/anh-chu/termyard/pkg/toolevents"
)

var absPathRe = regexp.MustCompile(`((?:href|src|action|srcset|data-src|data-href)=")(/[^/])`)

// handleProxy reverse-proxies a request to a locally-bound port on the termyard
// host. WebSocket upgrade requests are tunnelled over raw TCP so that
// localhost-only dev servers remain accessible through the termyard URL.
//
// Route pattern: /proxy/{port}/{rest...}
func handleProxy(w http.ResponseWriter, r *http.Request, termyardPort int) {
	// Extract port from chi URL params
	portStr := chi.URLParam(r, "port")
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}
	if port == termyardPort {
		http.Error(w, "cannot proxy termyard's own port", http.StatusForbidden)
		return
	}

	// Strip "/proxy/{port}" prefix to get the downstream path
	rest := chi.URLParam(r, "*")
	if !strings.HasPrefix(rest, "/") {
		rest = "/" + rest
	}

	isWebSocket := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
	if isWebSocket {
		proxyWebSocket(w, r, port, rest)
		return
	}

	target := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("127.0.0.1:%d", port),
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	// Override director to rewrite the path correctly
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = rest
		if r.URL.RawQuery != "" {
			req.URL.RawQuery = r.URL.RawQuery
		}
		req.Host = fmt.Sprintf("127.0.0.1:%d", port)
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logrus.WithError(err).WithField("port", port).Debug("port forward proxy error")
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
	}
	proxy.ModifyResponse = makeHTMLRewriter(port)
	proxy.ServeHTTP(w, r)
}

// maxHTMLRewrite caps HTML rewriting to avoid buffering unbounded upstream
// responses in memory. Larger bodies are passed through unchanged.
const maxHTMLRewrite = 8 << 20

// makeHTMLRewriter returns a ModifyResponse function that rewrites absolute
// paths in HTML responses so browsers route asset requests back through the
// termyard proxy rather than directly to the host root.
//
// For example, a Next.js app served at /proxy/8377/ generates:
//
//	<script src="/_next/static/chunks/main.js">
//
// which the browser resolves to devvm:7654/_next/... (a termyard 404). The
// rewriter turns it into src="/proxy/8377/_next/...", which routes correctly.
//
// It also patches the assetPrefix/basePath fields in Next.js __NEXT_DATA__ so
// that client-side navigation and code-splitting also use the proxy prefix.
//
// HTML bodies larger than maxHTMLRewrite are forwarded unchanged: the original
// body is reconstructed with a MultiReader so already-consumed bytes are not
// lost. Compressed responses are re-compressed after rewriting so the
// Content-Encoding header stays consistent.
func makeHTMLRewriter(port int) func(*http.Response) error {
	prefix := fmt.Sprintf("/proxy/%d", port)
	// Replacement: group1 stays, prefix is inserted before the leading slash of group2.
	// Example: href="/foo" → href="/proxy/8377/foo"
	// Example: href="/"   → href="/proxy/8377/"
	repl := []byte("${1}" + prefix + "${2}")

	// Next.js embeds {"assetPrefix":"","basePath":""} in __NEXT_DATA__.
	// Rewriting these makes the React runtime also use the proxy for all
	// dynamically loaded chunks and API calls.
	nextAssetReplace := []byte(`"assetPrefix":""`)
	nextAssetWith := []byte(`"assetPrefix":"` + prefix + `"`)
	nextBaseReplace := []byte(`"basePath":""`)
	nextBaseWith := []byte(`"basePath":"` + prefix + `"`)

	return func(resp *http.Response) error {
		ct := resp.Header.Get("Content-Type")
		if !strings.Contains(ct, "text/html") {
			return nil
		}

		encoded := strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip")

		// Peek at up to max+1 raw bytes. If the body is larger than the cap,
		// put the consumed prefix back and pass everything through unchanged.
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxHTMLRewrite+1))
		if err != nil {
			return err
		}
		if int64(len(raw)) > maxHTMLRewrite {
			resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(raw), resp.Body))
			return nil
		}

		var body []byte = raw
		if encoded {
			gr, err := gzip.NewReader(bytes.NewReader(raw))
			if err != nil {
				// Not a valid gzip stream after all; pass the raw bytes through.
				resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(raw), resp.Body))
				return nil
			}
			body, err = io.ReadAll(io.LimitReader(gr, maxHTMLRewrite+1))
			gr.Close()
			if err != nil {
				return err
			}
			if int64(len(body)) > maxHTMLRewrite {
				resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(raw), resp.Body))
				return nil
			}
		}

		// Rewrite absolute-path attribute values.
		body = absPathRe.ReplaceAll(body, repl)

		// Patch Next.js runtime metadata.
		body = bytes.Replace(body, nextAssetReplace, nextAssetWith, -1)
		body = bytes.Replace(body, nextBaseReplace, nextBaseWith, -1)

		if encoded {
			var buf bytes.Buffer
			gw := gzip.NewWriter(&buf)
			if _, err := gw.Write(body); err != nil {
				return err
			}
			if err := gw.Close(); err != nil {
				return err
			}
			body = buf.Bytes()
			resp.Header.Set("Content-Encoding", "gzip")
		} else {
			resp.Header.Del("Content-Encoding")
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.ContentLength = int64(len(body))
		resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
		return nil
	}
}

// proxyWebSocket tunnels a WebSocket upgrade through a raw TCP connection to
// the downstream port, allowing WebSocket-based dev servers to work through
// the termyard port-forward proxy.
func proxyWebSocket(w http.ResponseWriter, r *http.Request, port int, path string) {
	backend, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
	if err != nil {
		logrus.WithError(err).WithField("port", port).Debug("ws port forward: dial failed")
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	defer backend.Close()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		http.Error(w, "hijack failed", http.StatusInternalServerError)
		return
	}
	defer conn.Close()

	// Forward the original WS upgrade request to the backend with the rewritten path
	upgradeReq := r.Clone(r.Context())
	upgradeReq.URL.Path = path
	upgradeReq.URL.Host = fmt.Sprintf("127.0.0.1:%d", port)
	upgradeReq.Host = fmt.Sprintf("127.0.0.1:%d", port)
	if err := upgradeReq.Write(backend); err != nil {
		return
	}

	// Flush any buffered data from the hijacked connection to the backend
	if buf.Reader.Buffered() > 0 {
		buffered := make([]byte, buf.Reader.Buffered())
		_, _ = buf.Read(buffered)
		_, _ = backend.Write(buffered)
	}

	// Tunnel bidirectionally
	done := make(chan struct{}, 2)
	go func() { io.Copy(backend, conn); done <- struct{}{} }() //nolint:errcheck
	go func() { io.Copy(conn, backend); done <- struct{}{} }() //nolint:errcheck
	<-done
}

const fileGrantTTL = 5 * time.Minute

// fileGrants is an in-memory capability store: a token maps to one absolute
// path with an expiry. It replaces open-ended whole-FS read -- the serve
// endpoint can only return paths that were explicitly granted and not expired.
// Eviction is lazy (on access); no background goroutine.
type fileGrants struct {
	mu    sync.Mutex
	byTok map[string]fileGrant
}

type fileGrant struct {
	path    string
	expires time.Time
}

func newFileGrants() *fileGrants { return &fileGrants{byTok: map[string]fileGrant{}} }

func (g *fileGrants) grant(path string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	tok := hex.EncodeToString(b[:])
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, v := range g.byTok { // lazy eviction
		if now.After(v.expires) {
			delete(g.byTok, k)
		}
	}
	g.byTok[tok] = fileGrant{path: path, expires: now.Add(fileGrantTTL)}
	return tok
}

func (g *fileGrants) resolve(tok string) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	v, ok := g.byTok[tok]
	if !ok || time.Now().After(v.expires) {
		delete(g.byTok, tok)
		return "", false
	}
	return v.path, true
}

// resolveFilePath turns a user-selected path into an absolute, existing file
// path. Relative paths resolve against the active pane's cwd (see
// toolevents.ResolveSessionCWD) -- no fallback to other panes. Returns an HTTP
// status + message on failure.
func resolveFilePath(p string, opts *Options, r *http.Request) (string, int, string) {
	if p == "" {
		return "", http.StatusBadRequest, "path required"
	}
	if !filepath.IsAbs(p) {
		base := ""
		// ListPanes(session) targets the session's current window; pick its
		// active pane. ListWindows does not populate panes, so we query panes.
		if session := r.URL.Query().Get("session"); session != "" && opts.CWDResolver != nil {
			base = toolevents.ResolveSessionCWD(opts.CWDResolver, session)
		}
		if base == "" {
			return "", http.StatusBadRequest, "cannot resolve relative path: no active pane cwd"
		}
		p = filepath.Clean(filepath.Join(base, p))
	} else {
		p = filepath.Clean(p)
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", http.StatusNotFound, "not found"
	}
	if info.IsDir() {
		return "", http.StatusBadRequest, "path is a directory"
	}
	return p, 0, ""
}

// handleFileGrant resolves and validates a user-selected path, then mints a
// short-lived token the browser exchanges at GET /file?token=... . This is the
// only place a path enters the capability store.
//
// Route: POST /file/grant?path=<abs-or-rel>&session=<name>[&host=<id>]
func handleFileGrant(w http.ResponseWriter, r *http.Request, opts *Options, grants *fileGrants) {
	hostID := r.URL.Query().Get("host")

	// Remote peer -- relay file read through the control link. hostID may be
	// a v2 OwnerID (from a v2-routed pane's SessionRef.Owner, see
	// TiledView.tsx's onOpenFile) or a legacy peer fingerprint;
	// ResolveHostParam accepts either.
	if hostID != "" && opts.PeerMgr != nil {
		resolvedPeerID, isLocal := opts.PeerMgr.ResolveHostParam(hostID)
		if !isLocal {
			handleRemoteFileGrant(w, r, opts, grants, resolvedPeerID)
			return
		}
	}

	p, status, msg := resolveFilePath(r.URL.Query().Get("path"), opts, r)
	if status != 0 {
		http.Error(w, msg, status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": grants.grant(p), "path": p})
}

// handleRemoteFileGrant fetches a file from a remote peer, writes it to a
// local temp file, and grants a token for it.
func handleRemoteFileGrant(w http.ResponseWriter, r *http.Request, opts *Options, grants *fileGrants, hostID string) {
	if opts.FileReadReg == nil {
		http.Error(w, "file read unavailable", http.StatusInternalServerError)
		return
	}
	peerConn := opts.PeerMgr.GetPeerConnection(hostID)
	if peerConn == nil {
		http.Error(w, "peer not connected", http.StatusBadGateway)
		return
	}

	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	token := peer.NewToken()
	msg, err := peer.NewMessage(peer.MsgFileRead, peer.FileReadPayload{
		Token:   token,
		Path:    filePath,
		Session: r.URL.Query().Get("session"),
	})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	ch, cancel := opts.FileReadReg.Register(token)
	defer cancel()
	if !peerConn.Enqueue(msg) {
		http.Error(w, "peer send queue full", http.StatusBadGateway)
		return
	}

	select {
	case res := <-ch:
		if res.Error != "" {
			http.Error(w, "remote file: "+res.Error, http.StatusNotFound)
			return
		}
		data, err := base64.StdEncoding.DecodeString(res.Data)
		if err != nil {
			http.Error(w, "decode error", http.StatusInternalServerError)
			return
		}
		// Write to a private directory so callers get a safe root.
		dir, err := os.MkdirTemp("", "termyard-remote-*")
		if err != nil {
			http.Error(w, "temp dir error", http.StatusInternalServerError)
			return
		}
		fp := filepath.Join(dir, res.FileName)
		if err := os.WriteFile(fp, data, 0o644); err != nil {
			os.RemoveAll(dir)
			http.Error(w, "write error", http.StatusInternalServerError)
			return
		}
		// Schedule cleanup after grant TTL plus a grace minute.
		go func() {
			time.Sleep(fileGrantTTL + time.Minute)
			os.RemoveAll(dir)
		}()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token": grants.grant(fp),
			"path":  fp,
			"root":  dir,
		})
	case <-time.After(10 * time.Second):
		http.Error(w, "peer file read timed out", http.StatusGatewayTimeout)
	}
}

// handleFile serves a previously granted, non-expired file to the browser,
// which renders it by content-type. It can ONLY serve paths minted by
// handleFileGrant -- there is no arbitrary-path read.
//
// Route: GET /file?token=<token>
func handleFile(w http.ResponseWriter, r *http.Request, grants *fileGrants) {
	p, ok := grants.resolve(r.URL.Query().Get("token"))
	if !ok {
		http.Error(w, "invalid or expired file token", http.StatusForbidden)
		return
	}
	info, err := os.Stat(p)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	f, err := os.Open(p)
	if err != nil {
		http.Error(w, "cannot open", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	// Inline so the browser renders instead of downloading. ServeContent does
	// content-type sniffing, range requests and caching for free.
	w.Header().Set("Content-Disposition", "inline; filename=\""+filepath.Base(p)+"\"")
	http.ServeContent(w, r, filepath.Base(p), info.ModTime(), f)
}

// handleUpload streams a browser-supplied file into private temp storage on
// the session's host and returns {"path","quotedPath"}. No product size cap.
// Route: POST /api/upload?session=<name>&host=<id>&filename=<name>
func handleUpload(w http.ResponseWriter, r *http.Request, opts *Options) {
	filename := r.URL.Query().Get("filename")
	if filename == "" {
		http.Error(w, "filename required", http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("session") == "" {
		http.Error(w, "session required", http.StatusBadRequest)
		return
	}
	hostID := r.URL.Query().Get("host")
	if hostID != "" && opts.PeerMgr != nil {
		resolvedPeerID, isLocal := opts.PeerMgr.ResolveHostParam(hostID)
		if !isLocal {
			handleRemoteUpload(w, r, opts, resolvedPeerID, filename)
			return
		}
	}
	path, err := model.StoreUploadedFile(r.Body, filename)
	if err != nil {
		if r.Context().Err() != nil {
			return // client gone, nothing to write
		}
		if errors.Is(err, model.ErrEmptyUpload) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, "store upload: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"path": path, "quotedPath": model.ShellQuote(path),
	})
}

// handleRemoteUpload relays a browser file upload to a peer host over a
// dedicated /ws/peer-stream data connection.
func handleRemoteUpload(w http.ResponseWriter, r *http.Request, opts *Options, hostID, filename string) {
	if opts == nil || opts.PeerMgr == nil {
		http.Error(w, "peer routing unavailable", http.StatusInternalServerError)
		return
	}
	peerConn := opts.PeerMgr.GetPeerConnection(hostID)
	if peerConn == nil {
		http.Error(w, "peer not connected", http.StatusBadGateway)
		return
	}
	if !peerConn.HasCapability(peer.CapUpload) {
		http.Error(w, "peer does not support uploads -- upgrade the peer first", http.StatusUpgradeRequired)
		return
	}
	if opts.Identity == nil || opts.StreamReg == nil {
		http.Error(w, "peer routing unavailable", http.StatusInternalServerError)
		return
	}

	streamID := peer.GenerateStreamID()
	token := peer.NewToken()
	log := logrus.WithFields(logrus.Fields{"stream": streamID, "file": filename, "host": hostID})
	openMsg, _ := peer.NewMessage(peer.MsgOpenUpload, peer.OpenUploadPayload{
		StreamID:     streamID,
		Token:        token,
		Filename:     filename,
		ViewerHostID: opts.PeerMgr.LocalID(),
	})

	dial := peerConn.Role == peer.RoleDialer
	var conn *websocket.Conn
	if dial {
		addr := opts.PeerMgr.GetPeerAddress(hostID)
		c, err := peer.DialPeerStream(context.Background(), addr, opts.Identity, token)
		if err != nil {
			log.WithError(err).Debug("upload stream dial failed")
			http.Error(w, "upload stream setup failed", http.StatusBadGateway)
			return
		}
		conn = c
		if !peerConn.EnqueueHi(openMsg) {
			conn.Close()
			http.Error(w, "peer send queue full", http.StatusBadGateway)
			return
		}
	} else {
		ps := peer.NewPendingStream(streamID, "", 0, 0, hostID, opts.PeerMgr.LocalID(), hostID)
		opts.StreamReg.Register(token, ps)
		if !peerConn.EnqueueHi(openMsg) {
			http.Error(w, "peer send queue full", http.StatusBadGateway)
			return
		}
		// Context-aware setup wait -- honour browser cancellation (xhr.abort).
		resolvedCh := make(chan struct {
			conn *websocket.Conn
			ok   bool
		}, 1)
		go func() {
			c, ok := ps.WaitResolved(peer.StreamSetupTimeout())
			resolvedCh <- struct {
				conn *websocket.Conn
				ok   bool
			}{c, ok}
		}()
		select {
		case <-r.Context().Done():
			return
		case rc := <-resolvedCh:
			if !rc.ok {
				http.Error(w, "upload stream setup failed", http.StatusBadGateway)
				return
			}
			conn = rc.conn
		}
	}
	defer conn.Close()
	conn.EnableWriteCompression(false)

	// Pump body to peer as binary frames (256 KiB chunks).
	buf := make([]byte, 256*1024)
	for {
		n, readErr := r.Body.Read(buf)
		if n > 0 {
			if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				log.WithError(err).Debug("upload relay write failed")
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"upload-abort"}`))
				return
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			// Body read error (client disconnected)
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"upload-abort"}`))
			return
		}
	}
	// Send EOF frame.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"upload-eof"}`)); err != nil {
		log.WithError(err).Debug("upload relay eof failed")
		return
	}

	// Read result from peer with context awareness so browser cancellation
	// (e.g. xhr.abort) is honoured even after the body has been fully sent.
	type wsReadResult struct {
		data []byte
		err  error
	}
	readCh := make(chan wsReadResult, 1)
	go func() {
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, data, err := conn.ReadMessage()
		readCh <- wsReadResult{data, err}
	}()
	var result []byte
	select {
	case <-r.Context().Done():
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"upload-abort"}`))
		conn.Close()
		return
	case rr := <-readCh:
		if rr.err != nil {
			log.WithError(rr.err).Debug("upload relay result failed")
			http.Error(w, "peer upload timed out", http.StatusGatewayTimeout)
			return
		}
		result = rr.data
	}

	var res struct {
		Path       string `json:"path"`
		QuotedPath string `json:"quotedPath"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal(result, &res); err != nil {
		log.WithError(err).Debug("upload relay bad result")
		http.Error(w, "invalid peer response", http.StatusBadGateway)
		return
	}
	if res.Error != "" {
		code := http.StatusInternalServerError
		if model.IsEmptyUploadMessage(res.Error) {
			code = http.StatusBadRequest
		}
		http.Error(w, "remote upload: "+res.Error, code)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"path": res.Path, "quotedPath": res.QuotedPath,
	})
}

func registerProxyFileRoutes(r chi.Router, opts *Options) {
	// Port-forward proxy -- exposes localhost-bound services through termyard's URL.
	// Requires auth (same rule as other protected routes) so remote users can't
	// reach internal services without a valid session.
	if opts.PortForwardStore != nil {
		proxyHandler := func(w http.ResponseWriter, r *http.Request) {
			handleProxy(w, r, opts.Port)
		}
		proxyMethods := []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
		for _, m := range proxyMethods {
			if opts.AuthEnabled {
				authMw := auth.Middleware(opts.SessionMgr)
				r.With(authMw).Method(m, "/proxy/{port}", http.HandlerFunc(proxyHandler))
				r.With(authMw).Method(m, "/proxy/{port}/*", http.HandlerFunc(proxyHandler))
			} else {
				r.Method(m, "/proxy/{port}", http.HandlerFunc(proxyHandler))
				r.Method(m, "/proxy/{port}/*", http.HandlerFunc(proxyHandler))
			}
		}
	}
	// File open -- capability-based: POST /file/grant mints a short-lived token
	// for one explicitly-opened path; GET /file?token=... serves it. Same auth as
	// the proxy above; not gated on PortForwardStore since it needs no port config.
	grants := newFileGrants()
	grantHandler := func(w http.ResponseWriter, r *http.Request) { handleFileGrant(w, r, opts, grants) }
	fileHandler := func(w http.ResponseWriter, r *http.Request) { handleFile(w, r, grants) }
	uploadHandler := func(w http.ResponseWriter, r *http.Request) { handleUpload(w, r, opts) }
	if opts.AuthEnabled {
		authMw := auth.Middleware(opts.SessionMgr)
		r.With(authMw).Post("/file/grant", grantHandler)
		r.With(authMw).Get("/file", fileHandler)
		r.With(authMw).Post("/api/upload", uploadHandler)
	} else {
		r.Post("/file/grant", grantHandler)
		r.Get("/file", fileHandler)
		r.Post("/api/upload", uploadHandler)
	}
}
