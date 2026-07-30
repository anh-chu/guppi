package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/anh-chu/termyard/pkg/auth"
	"github.com/anh-chu/termyard/pkg/wikilite"
)

func newTestRouter(wl *wikilite.Supervisor, authEnabled bool) chi.Router {
	r := chi.NewRouter()
	r.Use(chimiddleware.StripSlashes)

	opts := &Options{WikiLite: wl}
	if authEnabled {
		opts.AuthEnabled = true
		var err error
		opts.PasswordStore, err = auth.NewPasswordStore()
		if err != nil {
			panic(err)
		}
		opts.SessionMgr = auth.NewSessionManager(24 * 3600)
	}

	registerWikiRoutes(r, opts)
	return r
}

func TestWikiProxyPathTrim(t *testing.T) {
	// Create an upstream that echoes path+query, and serves redirects
	// for the redirect-me endpoints. The proxy trims /wiki prefix, so the
	// upstream receives /redirect-me and /foo/redirect-me.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect-me" {
			w.Header().Set("Location", "http://127.0.0.1:"+strings.Split(r.Host, ":")[1]+"/s/1-target")
			w.WriteHeader(http.StatusFound)
			return
		}
		if r.URL.Path == "/foo/redirect-me" {
			w.Header().Set("Location", "/bar")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		body := "path=" + r.URL.Path
		if r.URL.RawQuery != "" {
			body += " query=" + r.URL.RawQuery
		}
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()

	port := extractPort(t, upstream.URL)

	sup := wikilite.NewSupervisor()
	sup.SetTestPort(port)

	r := newTestRouter(sup, false)

	cases := []struct {
		name       string
		reqPath    string
		wantCode   int
		wantBody   string
		wantHeader string // optional Location check
	}{
		{"/wiki/api/wiki/content?path=x arrives as /api/wiki/content?path=x", "/wiki/api/wiki/content?path=x", 200, "path=/api/wiki/content query=path=x", ""},
		{"/wiki arrives as /", "/wiki", 200, "path=/", ""},
		{"/wiki/ keeps trailing slash", "/wiki/", 200, "path=/", ""},
		{"/wiki/foo/ keeps trailing slash", "/wiki/foo/", 200, "path=/foo/", ""},
		{"/_next/static/x.js unchanged", "/_next/static/x.js", 200, "path=/_next/static/x.js", ""},
		{"302 absolute becomes /wiki prefix", "/wiki/redirect-me", 302, "", "/wiki/s/1-target"},
		{"302 root-relative gets /wiki prefix", "/wiki/foo/redirect-me", 302, "", "/wiki/bar"},
	}

	for _, c := range cases {
		t.Run("", func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, c.reqPath, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != c.wantCode {
				t.Fatalf("%s: code=%d want %d", c.name, w.Code, c.wantCode)
			}
			if c.wantBody != "" && w.Body.String() != c.wantBody {
				t.Fatalf("%s: body=%q want %q", c.name, w.Body.String(), c.wantBody)
			}
			if c.wantHeader != "" {
				loc := w.Header().Get("Location")
				if loc != c.wantHeader {
					t.Fatalf("%s: Location=%q want %q", c.name, loc, c.wantHeader)
				}
			}
		})
	}
}

func TestWikiProxy503WhenNoPort(t *testing.T) {
	sup := wikilite.NewSupervisor()
	r := newTestRouter(sup, false)
	req := httptest.NewRequest(http.MethodGet, "/wiki", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no port: code=%d want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "wiki not running") {
		t.Fatalf("no port: body=%q want 'wiki not running'", w.Body.String())
	}
}

func TestWikiProxyAuth401(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	sup := wikilite.NewSupervisor()
	sup.SetTestPort(extractPort(t, upstream.URL))

	r := chi.NewRouter()
	r.Use(chimiddleware.StripSlashes)
	ps, _ := auth.NewPasswordStore()
	sm := auth.NewSessionManager(24 * 3600)
	opts := &Options{WikiLite: sup, AuthEnabled: true, PasswordStore: ps, SessionMgr: sm}
	registerWikiRoutes(r, opts)

	req := httptest.NewRequest(http.MethodGet, "/wiki", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("auth enabled without cookie: code=%d want 401", w.Code)
	}
}

func TestWikiProxyNotServesSPA(t *testing.T) {
	// Without wiki routes, chi's StripSlashes redirects /wiki to /wiki/ and
	// eventually the SPA catch-all would serve index.html. Verify the wiki
	// route captures /wiki/x instead.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("wiki content"))
	}))
	defer upstream.Close()

	sup := wikilite.NewSupervisor()
	sup.SetTestPort(extractPort(t, upstream.URL))

	r := newTestRouter(sup, false)

	// Add a fake SPA catch-all to prove /wiki/x does not reach it.
	spaHit := false
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		spaHit = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("spa index"))
	})

	req := httptest.NewRequest(http.MethodGet, "/wiki/x", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if spaHit {
		t.Fatal("/wiki/x hit SPA catch-all instead of wiki proxy")
	}
	if w.Body.String() != "wiki content" {
		t.Fatalf("body=%q want 'wiki content'", w.Body.String())
	}
}

func extractPort(t *testing.T, urlStr string) int {
	t.Helper()
	// urlStr is like "http://127.0.0.1:54321"
	colon := strings.LastIndex(urlStr, ":")
	if colon < 0 {
		t.Fatal("no port in URL")
	}
	port := 0
	for _, c := range urlStr[colon+1:] {
		if c < '0' || c > '9' {
			break
		}
		port = port*10 + int(c-'0')
	}
	return port
}

// The proxy is same-origin, so the browser attaches termyard's session cookie
// to every /wiki request. The child must not receive it: it is a credential
// that can impersonate the user, and the child never reads it. wiki-viewer's
// own cookies must still get through, since it relies on them same-origin.
func TestWikiProxyStripsSessionCookieOnly(t *testing.T) {
	var got string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Cookie")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	sup := wikilite.NewSupervisor()
	sup.SetTestPort(extractPort(t, upstream.URL))

	r := chi.NewRouter()
	r.Use(chimiddleware.StripSlashes)
	registerWikiRoutes(r, &Options{WikiLite: sup})

	req := httptest.NewRequest(http.MethodGet, "/wiki", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "super-secret"})
	req.AddCookie(&http.Cookie{Name: "wiki-skin", Value: "editorial"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if strings.Contains(got, auth.CookieName) || strings.Contains(got, "super-secret") {
		t.Fatalf("session cookie reached the child: %q", got)
	}
	if !strings.Contains(got, "wiki-skin=editorial") {
		t.Fatalf("wiki-viewer's own cookie was dropped: %q", got)
	}
}
