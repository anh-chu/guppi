package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/anh-chu/termyard/pkg/auth"
	"github.com/anh-chu/termyard/pkg/identity"
	"github.com/anh-chu/termyard/pkg/model"
	"github.com/anh-chu/termyard/pkg/peer"
)

func TestHandleUploadLocal(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	body := strings.NewReader("upload-content-here")
	req := httptest.NewRequest(http.MethodPost, "/api/upload?session=test&filename=notes.txt", body)
	rec := httptest.NewRecorder()

	opts := &Options{}
	handleUpload(rec, req, opts)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var res struct {
		Path       string `json:"path"`
		QuotedPath string `json:"quotedPath"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if res.Path == "" {
		t.Fatal("path is empty")
	}
	if res.QuotedPath == "" {
		t.Fatal("quotedPath is empty")
	}
	if res.QuotedPath != model.ShellQuote(res.Path) {
		t.Fatalf("quotedPath = %q, want %q", res.QuotedPath, model.ShellQuote(res.Path))
	}

	// File exists with correct content.
	got, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "upload-content-here" {
		t.Fatalf("contents = %q", string(got))
	}

	// Clean up.
	os.RemoveAll(filepath.Dir(res.Path))
}

func TestHandleUploadMissingParams(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{name: "missing filename", url: "/api/upload?session=test"},
		{name: "missing session", url: "/api/upload?filename=f.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.url, strings.NewReader("x"))
			rec := httptest.NewRecorder()
			handleUpload(rec, req, &Options{})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", rec.Code)
			}
		})
	}
}

func TestHandleUploadRemotePeerMissing(t *testing.T) {
	id, err := identity.Generate("test-upload-rm")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/upload?session=test&host=gone&filename=f.txt", strings.NewReader("x"))
	rec := httptest.NewRecorder()
	opts := &Options{PeerMgr: peer.NewManager(id, nil, nil)}
	handleUpload(rec, req, opts)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
}

func TestHandleUploadRemoteMissingCapability(t *testing.T) {
	id, err := identity.Generate("test-local")
	if err != nil {
		t.Fatal(err)
	}
	pm := peer.NewManager(id, nil, nil)
	pc := peer.NewPeerConnection("peer-1", 128)
	// Do NOT add CapUpload.
	pm.RegisterPeer("peer-1", "peer-one", "pubkey", pc)

	req := httptest.NewRequest(http.MethodPost, "/api/upload?session=test&host=peer-1&filename=f.txt", strings.NewReader("x"))
	rec := httptest.NewRecorder()
	opts := &Options{PeerMgr: pm}
	handleUpload(rec, req, opts)
	if rec.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected 426, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "upload") {
		t.Fatalf("expected error body mentioning upload, got: %q", body)
	}
}

func TestHandleUploadAuthRequired(t *testing.T) {
	sm := auth.NewSessionManager(1 * time.Hour)
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware(sm))
		r.Post("/upload", func(w http.ResponseWriter, r *http.Request) {
			handleUpload(w, r, &Options{})
		})
	})

	req := httptest.NewRequest(http.MethodPost, "/upload?session=test&filename=f.txt", strings.NewReader("data"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleUploadEmptyBody(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	req := httptest.NewRequest(http.MethodPost, "/api/upload?session=test&filename=empty.txt", strings.NewReader(""))
	rec := httptest.NewRecorder()
	handleUpload(rec, req, &Options{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "empty") {
		t.Fatalf("expected error about empty upload, got: %q", rec.Body.String())
	}
}
