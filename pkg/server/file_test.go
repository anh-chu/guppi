package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveFilePath(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(fp, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		path string
		want int // 0 == success
	}{
		{"ok", fp, 0},
		{"relative", "hello.txt", http.StatusBadRequest},
		{"empty", "", http.StatusBadRequest},
		{"missing", filepath.Join(dir, "nope.txt"), http.StatusNotFound},
		{"dir", dir, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/x", nil)
			_, isDir, status, _ := resolveFilePath(c.path, &Options{}, r)
			if status != c.want {
				t.Fatalf("path=%q got %d want %d", c.path, status, c.want)
			}
			if c.name == "dir" && !isDir {
				t.Fatalf("path=%q got isDir=false want true", c.path)
			}
		})
	}
}

func TestFileGrantServe(t *testing.T) {
	t.Run("local grant returns path and empty root", func(t *testing.T) {
		dir := t.TempDir()
		fp := filepath.Join(dir, "hello.txt")
		if err := os.WriteFile(fp, []byte("hi"), 0o644); err != nil {
			t.Fatal(err)
		}
		grants := newFileGrants()

		// Bad/absent token -> forbidden, never reads FS.
		r := httptest.NewRequest(http.MethodGet, "/file?token=bogus", nil)
		w := httptest.NewRecorder()
		handleFile(w, r, grants)
		if w.Code != http.StatusForbidden {
			t.Fatalf("bad token got %d want 403", w.Code)
		}

		// Granted token -> serves the file.
		tok := grants.grant(fp)
		r = httptest.NewRequest(http.MethodGet, "/file?token="+tok, nil)
		w = httptest.NewRecorder()
		handleFile(w, r, grants)
		if w.Code != http.StatusOK || w.Body.String() != "hi" {
			t.Fatalf("granted got %d body=%q", w.Code, w.Body.String())
		}

		// Expired grant -> forbidden.
		grants.byTok[tok] = fileGrant{path: fp, expires: time.Now().Add(-time.Second)}
		r = httptest.NewRequest(http.MethodGet, "/file?token="+tok, nil)
		w = httptest.NewRecorder()
		handleFile(w, r, grants)
		if w.Code != http.StatusForbidden {
			t.Fatalf("expired got %d want 403", w.Code)
		}
	})

	t.Run("local grant response has path and empty root", func(t *testing.T) {
		dir := t.TempDir()
		fp := filepath.Join(dir, "hello.txt")
		if err := os.WriteFile(fp, []byte("hi"), 0o644); err != nil {
			t.Fatal(err)
		}
		grants := newFileGrants()

		r := httptest.NewRequest(http.MethodPost, "/file/grant?path="+fp, nil)
		w := httptest.NewRecorder()
		// Use a minimal Options to exercise the local grant path.
		opts := &Options{}
		handleFileGrant(w, r, opts, grants)
		if w.Code != http.StatusOK {
			t.Fatalf("grant got %d", w.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if tok, ok := body["token"]; !ok || tok == "" {
			t.Fatal("missing token")
		}
		if p, ok := body["path"]; !ok || p != fp {
			t.Fatalf("path=%q want %q", p, fp)
		}
		// Local grants have no root.
		if root, ok := body["root"]; ok && root != "" {
			t.Fatalf("local grant has non-empty root: %q", root)
		}

		// The token must still resolve to the file.
		r2 := httptest.NewRequest(http.MethodGet, "/file?token="+body["token"], nil)
		w2 := httptest.NewRecorder()
		handleFile(w2, r2, grants)
		if w2.Code != http.StatusOK {
			t.Fatalf("token from grant does not resolve: %d", w2.Code)
		}
	})
}
