package web

import (
	"net/http"
	"strings"

	"rsc.io/qr"
)

// connAddDiscord creates a Discord connection from a posted bot token.
func (s *Server) connAddDiscord(w http.ResponseWriter, r *http.Request) {
	if s.conn == nil {
		http.Error(w, "connection manager unavailable", http.StatusServiceUnavailable)
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
	if token == "" {
		setFlash(w, "danger", "Token required", "Paste a Discord bot token to add the connection.")
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	if err := s.conn.AddDiscord(r.Context(), token); err != nil {
		setFlash(w, "danger", "Add failed", err.Error())
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	setFlash(w, "ok", "Discord connected", "The bot token was stored and the connection started.")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// connAddWeChat creates a WeChat connection; the login QR appears on /settings.
func (s *Server) connAddWeChat(w http.ResponseWriter, r *http.Request) {
	if s.conn == nil {
		http.Error(w, "connection manager unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.conn.AddWeChat(r.Context()); err != nil {
		setFlash(w, "danger", "Add failed", err.Error())
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	setFlash(w, "ok", "WeChat connection created", "Scan the login QR below with the WeChat mobile app.")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (s *Server) connEnable(w http.ResponseWriter, r *http.Request) {
	s.connMutate(w, r, func(id string) error { return s.conn.Enable(r.Context(), id) }, "Connection enabled")
}

func (s *Server) connDisable(w http.ResponseWriter, r *http.Request) {
	s.connMutate(w, r, func(id string) error { return s.conn.Disable(r.Context(), id) }, "Connection disabled")
}

func (s *Server) connDelete(w http.ResponseWriter, r *http.Request) {
	s.connMutate(w, r, func(id string) error { return s.conn.Delete(r.Context(), id) }, "Connection deleted")
}

// connMutate runs a manager mutation for the {id} path value, flashes, and
// redirects back to settings.
func (s *Server) connMutate(w http.ResponseWriter, r *http.Request, fn func(id string) error, okTitle string) {
	if s.conn == nil {
		http.Error(w, "connection manager unavailable", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	if err := fn(id); err != nil {
		setFlash(w, "danger", "Action failed", err.Error())
	} else {
		setFlash(w, "ok", okTitle, id)
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// connQR renders the pending login QR for a connection as a PNG. Returns 404
// when there is no QR pending (not in the QR state, or already connected).
func (s *Server) connQR(w http.ResponseWriter, r *http.Request) {
	if s.conn == nil {
		http.Error(w, "connection manager unavailable", http.StatusServiceUnavailable)
		return
	}
	_, payload := s.conn.Status(r.PathValue("id"))
	if payload == "" {
		http.NotFound(w, r)
		return
	}
	code, err := qr.Encode(payload, qr.M)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(code.PNG())
}
