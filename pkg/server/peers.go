package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"

	"github.com/ekristen/guppi/pkg/common"
	"github.com/ekristen/guppi/pkg/identity"
	"github.com/ekristen/guppi/pkg/peer"
)

type peersGetResponse struct {
	Self  selfInfo            `json:"self"`
	Peers []peer.LinkSnapshot `json:"peers"`
}

type selfInfo struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"public_key"`
}

func handleGetPeers(w http.ResponseWriter, r *http.Request, opts *Options) {
	resp := peersGetResponse{}
	if opts.Identity != nil {
		resp.Self = selfInfo{
			Name:        opts.Identity.Name,
			Fingerprint: opts.Identity.Fingerprint(),
			PublicKey:   opts.Identity.PublicKey,
		}
	}
	if opts.LinkSupervisor != nil {
		resp.Peers = opts.LinkSupervisor.Status()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func handlePostPeers(w http.ResponseWriter, r *http.Request, opts *Options) {
	var req struct {
		Address       string `json:"address"`
		Password      string `json:"password"`
		AutoReconnect bool   `json:"auto_reconnect"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	addr, err := peer.NormalizeAddress(req.Address)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if opts.Identity == nil || opts.LinkSupervisor == nil || opts.PeerStore == nil {
		http.Error(w, "peer subsystem not initialised", http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	bootstrapReq := peer.BootstrapRequest{
		Password:    req.Password,
		Name:        opts.Identity.Name,
		PublicKey:   opts.Identity.PublicKey,
		Fingerprint: opts.Identity.Fingerprint(),
		ListenPort:  opts.Port,
	}
	resp, err := peer.SendBootstrap(ctx, addr, bootstrapReq)
	if err != nil {
		logrus.WithError(err).WithField("addr", addr).Warn("bootstrap failed")
		var bErr *peer.BootstrapError
		if errors.As(err, &bErr) {
			http.Error(w, bErr.Message, bErr.Status)
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if resp.PublicKey == opts.Identity.PublicKey {
		http.Error(w, "cannot pair with self", http.StatusBadRequest)
		return
	}

	p := identity.Peer{
		Name:          resp.Name,
		PublicKey:     resp.PublicKey,
		PairedAt:      time.Now(),
		Address:       addr,
		Enabled:       req.AutoReconnect,
		InitiatedByUs: true,
		LastSeen:      time.Time{},
	}
	if err := opts.LinkSupervisor.AddPeer(p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	snap := findSnapshot(opts.LinkSupervisor.Status(), p.Fingerprint())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(snap)
}

func handlePatchPeer(w http.ResponseWriter, r *http.Request, opts *Options) {
	fp := chi.URLParam(r, "fp")
	if opts.PeerStore == nil || opts.LinkSupervisor == nil {
		http.Error(w, "peer subsystem not initialised", http.StatusServiceUnavailable)
		return
	}
	p := opts.PeerStore.GetByFingerprint(fp)
	if p == nil {
		http.Error(w, "peer not found", http.StatusNotFound)
		return
	}
	var req struct {
		Enabled *bool `json:"enabled,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Enabled != nil {
		if err := opts.LinkSupervisor.SetEnabled(p.PublicKey, *req.Enabled); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	snap := findSnapshot(opts.LinkSupervisor.Status(), fp)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snap)
}

func handleReconnectPeer(w http.ResponseWriter, r *http.Request, opts *Options) {
	fp := chi.URLParam(r, "fp")
	if opts.PeerStore == nil || opts.LinkSupervisor == nil {
		http.Error(w, "peer subsystem not initialised", http.StatusServiceUnavailable)
		return
	}
	p := opts.PeerStore.GetByFingerprint(fp)
	if p == nil {
		http.Error(w, "peer not found", http.StatusNotFound)
		return
	}
	opts.LinkSupervisor.ReconnectNow(p.PublicKey)
	w.WriteHeader(http.StatusNoContent)
}

func handleDeletePeer(w http.ResponseWriter, r *http.Request, opts *Options) {
	fp := chi.URLParam(r, "fp")
	if opts.PeerStore == nil || opts.LinkSupervisor == nil {
		http.Error(w, "peer subsystem not initialised", http.StatusServiceUnavailable)
		return
	}
	p := opts.PeerStore.GetByFingerprint(fp)
	if p == nil {
		http.Error(w, "peer not found", http.StatusNotFound)
		return
	}
	if err := opts.LinkSupervisor.RemovePeer(p.PublicKey); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePeersBootstrap is the unauthenticated, password-protected bootstrap
// endpoint. Establishes mutual trust with an incoming peer.
func handlePeersBootstrap(w http.ResponseWriter, r *http.Request, opts *Options) {
	if opts.Identity == nil || opts.PeerStore == nil || opts.LinkSupervisor == nil {
		http.Error(w, "peer subsystem not initialised", http.StatusServiceUnavailable)
		return
	}
	if opts.PasswordStore == nil || !opts.PasswordStore.HasPassword() {
		http.Error(w, "remote machine has no password configured yet", http.StatusServiceUnavailable)
		return
	}

	var req peer.BootstrapRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if !opts.PasswordStore.Verify(req.Password) {
		http.Error(w, "invalid password", http.StatusUnauthorized)
		return
	}
	if req.PublicKey == "" {
		http.Error(w, "public_key required", http.StatusBadRequest)
		return
	}
	if req.PublicKey == opts.Identity.PublicKey {
		http.Error(w, "cannot pair with self", http.StatusBadRequest)
		return
	}

	// Combine the inbound request host with the dialer's listening port to
	// derive a back-reachable address. r.RemoteAddr's port is the ephemeral
	// source port, not the dialer's server, so we ignore it.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	port := peer.DefaultPort
	if req.ListenPort > 0 {
		port = strconv.Itoa(req.ListenPort)
	}
	remoteAddr := net.JoinHostPort(host, port)

	// Idempotent: if already known, refresh address; otherwise add.
	existing := opts.PeerStore.GetByPublicKey(req.PublicKey)
	if existing != nil {
		_ = opts.PeerStore.UpdateAddress(req.PublicKey, remoteAddr)
	} else {
		p := identity.Peer{
			Name:          req.Name,
			PublicKey:     req.PublicKey,
			PairedAt:      time.Now(),
			Address:       remoteAddr,
			Enabled:       true,
			InitiatedByUs: false, // bootstrap arrived inbound; we listen
		}
		if err := opts.LinkSupervisor.AddPeer(p); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	resp := peer.BootstrapResponse{
		Name:        opts.Identity.Name,
		PublicKey:   opts.Identity.PublicKey,
		Fingerprint: opts.Identity.Fingerprint(),
		Version:     common.VERSION,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

func findSnapshot(snaps []peer.LinkSnapshot, fp string) *peer.LinkSnapshot {
	for i := range snaps {
		if snaps[i].Fingerprint == fp {
			return &snaps[i]
		}
	}
	return nil
}


