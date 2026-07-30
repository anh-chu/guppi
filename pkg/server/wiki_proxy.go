package server

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"

	"github.com/anh-chu/termyard/pkg/auth"
)

// registerWikiRoutes mounts /wiki, /wiki/*, /_next and /_next/*.
// When auth is enabled the routes are wrapped with the session middleware.
// Must be called BEFORE registerFrontendRoutes so the SPA catch-all does not
// swallow these paths.
func registerWikiRoutes(r chi.Router, opts *Options) {
	if opts.WikiLite == nil {
		return
	}
	h := func(w http.ResponseWriter, r *http.Request) {
		handleWikiProxy(w, r, opts)
	}

	var router chi.Router
	if opts.AuthEnabled {
		authMw := auth.Middleware(opts.SessionMgr)
		router = r.With(authMw)
	} else {
		router = r
	}

	// Use Handle (not Get) so all methods pass through.
	router.Handle("/wiki", http.HandlerFunc(h))
	router.Handle("/wiki/*", http.HandlerFunc(h))
	router.Handle("/_next", http.HandlerFunc(h))
	router.Handle("/_next/*", http.HandlerFunc(h))
}

// handleWikiProxy reverse-proxies the request to the wiki-viewer-lite child
// process. When the child is not running it answers 503 JSON.
func handleWikiProxy(w http.ResponseWriter, r *http.Request, opts *Options) {
	port, ok := opts.WikiLite.Port()
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"wiki not running"}`))
		return
	}

	host := fmt.Sprintf("127.0.0.1:%d", port)
	target := &url.URL{
		Scheme: "http",
		Host:   host,
	}
	proxy := httputil.NewSingleHostReverseProxy(target)

	// Director: trim the mount prefix from the path. /wiki/api/x -> /api/x,
	// /wiki -> /, /_next/... passes through unchanged.
	origDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		origDirector(req)
		req.URL.Path = trimWikiPrefix(req.URL.Path)
		req.URL.RawQuery = r.URL.RawQuery
		req.Host = host
		stripSessionCookie(req)
	}

	// ModifyResponse: rewrite Location headers on 3xx so clients see /wiki
	// prefixes instead of 127.0.0.1:<port>.
	proxy.ModifyResponse = func(resp *http.Response) error {
		loc := resp.Header.Get("Location")
		if loc == "" {
			return nil
		}
		resp.Header.Set("Location", rewriteLocation(loc, port))
		return nil
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logrus.WithError(err).WithField("port", port).Debug("wiki proxy error")
		http.Error(w, "wiki upstream unreachable", http.StatusBadGateway)
	}

	proxy.ServeHTTP(w, r)
}

// trimWikiPrefix strips the /wiki mount prefix from the request path.
// /_next paths are passed through unchanged.
func trimWikiPrefix(p string) string {
	if strings.HasPrefix(p, "/_next") {
		return p
	}
	if p == "/wiki" {
		return "/"
	}
	if strings.HasPrefix(p, "/wiki/") {
		return "/" + p[len("/wiki/"):]
	}
	return p
}

// rewriteLocation adjusts an upstream 3xx Location header so the browser
// routes back through the /wiki mount instead of directly to the internal port.
func rewriteLocation(loc string, port int) string {
	prefix := fmt.Sprintf("http://127.0.0.1:%d", port)
	if strings.HasPrefix(loc, prefix) {
		rest := loc[len(prefix):]
		if rest == "" {
			return "/wiki"
		}
		return "/wiki" + rest
	}
	// Root-relative path not already prefixed with /wiki.
	if strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "/wiki") {
		return "/wiki" + loc
	}
	return loc
}

// stripSessionCookie removes termyard's session cookie from a request bound for
// the wiki-viewer-lite child.
//
// The proxy is same-origin, so the browser attaches termyard's auth cookie to
// every /wiki request alongside wiki-viewer's own cookies. The child runs with
// auth disabled and never reads it, and forwarding a credential that can
// impersonate the user to a separately installed npm package is a trust
// boundary worth keeping closed. Only that one cookie is dropped, so
// wiki-viewer's own same-origin cookies still reach it.
func stripSessionCookie(req *http.Request) {
	cookies := req.Cookies()
	if len(cookies) == 0 {
		return
	}
	req.Header.Del("Cookie")
	for _, c := range cookies {
		if c.Name == auth.CookieName {
			continue
		}
		req.AddCookie(c)
	}
}
