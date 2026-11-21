package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Tharkis/bragi/internal/auth"
	"github.com/Tharkis/bragi/internal/store"
)

const (
	sessionCookie = "bragi_session"
	sessionTTL    = 30 * 24 * time.Hour
)

type ctxKey struct{}

func userFrom(ctx context.Context) *store.User {
	u, _ := ctx.Value(ctxKey{}).(*store.User)
	return u
}

// withUser attaches the signed-in user, if any, to the request context.
// API clients send X-Api-Key; the web UI uses the session cookie.
func (s *Server) withUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var u *store.User
		var err error
		if key := r.Header.Get("X-Api-Key"); key != "" {
			u, err = s.store.GetUserByAPIKey(r.Context(), key)
		} else if c, cerr := r.Cookie(sessionCookie); cerr == nil && c.Value != "" {
			u, err = s.store.GetSessionUser(r.Context(), auth.HashToken(c.Value))
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.log.Error("auth lookup failed", "err", err)
		}
		// The Jellyfin plugin signs in with one admin key and names the
		// Jellyfin user it acts for; Bragi maps that to a requester account.
		if jf := strings.TrimSpace(r.Header.Get("X-Bragi-User")); jf != "" && u != nil && u.Role == "admin" && r.Header.Get("X-Api-Key") != "" {
			name := strings.TrimSpace(r.Header.Get("X-Bragi-User-Name"))
			if mapped, err := s.userForJellyfinID(r.Context(), jf, name); err == nil {
				u = mapped
			} else {
				s.log.Error("mapping Jellyfin user failed", "err", err)
				u = nil
			}
		}
		if u != nil {
			r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, u))
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireUser(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if userFrom(r.Context()) == nil {
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		h(w, r)
	}
}

func (s *Server) requireAdmin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := userFrom(r.Context())
		if u == nil {
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		if u.Role != "admin" {
			writeError(w, http.StatusForbidden, "admins only")
			return
		}
		h(w, r)
	}
}

type userResponse struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	Role        string `json:"role"`
	APIKey      string `json:"apiKey,omitempty"`
	AutoApprove bool   `json:"autoApprove"`
}

func toUserResponse(u *store.User, withKey bool) userResponse {
	r := userResponse{ID: u.ID, Username: u.Username, Role: u.Role, AutoApprove: u.AutoApprove}
	if withKey {
		r.APIKey = u.APIKey
	}
	return r
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.CountUsers(r.Context())
	if err != nil {
		s.storeError(w, err, "users")
		return
	}
	done, err := s.store.GetSetting(r.Context(), settingSetupComplete)
	if err != nil {
		s.storeError(w, err, "settings")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"needsAdmin":    n == 0,
		"setupComplete": done == "true",
		"version":       s.version,
	})
}

// handleSetup creates the first admin account. It only works once.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !readJSON(w, r, &c) {
		return
	}
	c.Username = strings.TrimSpace(c.Username)
	if c.Username == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	if len(c.Password) < auth.MinPasswordLength {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	hash, err := auth.HashPassword(c.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, "password cannot be used")
		return
	}
	u, err := s.store.CreateFirstAdmin(r.Context(), c.Username, hash, auth.NewAPIKey())
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "setup is already done; sign in instead")
		return
	}
	if err != nil {
		s.storeError(w, err, "user")
		return
	}
	if err := s.startSession(w, r, u); err != nil {
		s.storeError(w, err, "session")
		return
	}
	s.log.Info("admin account created", "username", u.Username)
	writeJSON(w, http.StatusCreated, toUserResponse(u, true))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !readJSON(w, r, &c) {
		return
	}
	u, err := s.store.GetUserByUsername(r.Context(), strings.TrimSpace(c.Username))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.storeError(w, err, "user")
		return
	}
	hash := ""
	if u != nil {
		hash = u.PasswordHash
	}
	if !auth.CheckPassword(hash, c.Password) || u == nil {
		s.log.Warn("failed sign-in", "username", c.Username, "remote", r.RemoteAddr)
		writeError(w, http.StatusUnauthorized, "wrong username or password")
		return
	}
	if err := s.startSession(w, r, u); err != nil {
		s.storeError(w, err, "session")
		return
	}
	writeJSON(w, http.StatusOK, toUserResponse(u, true))
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User) error {
	token := auth.NewToken()
	expires := time.Now().Add(sessionTTL)
	if err := s.store.CreateSession(r.Context(), auth.HashToken(token), u.ID, expires); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	})
	return nil
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		s.store.DeleteSession(r.Context(), auth.HashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, toUserResponse(userFrom(r.Context()), true))
}

func (s *Server) handleRotateAPIKey(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	key := auth.NewAPIKey()
	if err := s.store.SetAPIKey(r.Context(), u.ID, key); err != nil {
		s.storeError(w, err, "user")
		return
	}
	u.APIKey = key
	writeJSON(w, http.StatusOK, toUserResponse(u, true))
}
