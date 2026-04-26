package server

import (
	"context"
	"net/http"
	"time"

	"github.com/greatbody/portly/internal/auth"
	"github.com/greatbody/portly/internal/store"
)

const sessionCookieName = "portly_session"

type ctxKey int

const (
	ctxUserKey ctxKey = iota
)

// requireAuth wraps a handler, redirecting to /login (HTML) or 401 (JSON/api) if missing.
func (s *Server) requireAuth(next http.Handler, jsonAPI bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := s.currentUser(r)
		if err != nil || u == nil {
			if jsonAPI {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			http.Redirect(w, r, "/login?next="+r.URL.RequestURI(), http.StatusFound)
			return
		}
		ctx := context.WithValue(r.Context(), ctxUserKey, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) currentUser(r *http.Request) (*store.User, error) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil, nil
	}
	ses, err := s.Store.GetSession(c.Value)
	if err != nil || ses == nil {
		return nil, err
	}
	u, err := s.Store.GetUserByName("admin") // single-user MVP
	if err != nil {
		return nil, err
	}
	if u == nil || u.ID != ses.UserID {
		return nil, nil
	}
	return u, nil
}

func (s *Server) login(w http.ResponseWriter, r *http.Request, username, password string) error {
	u, err := s.Store.GetUserByName(username)
	if err != nil {
		return err
	}
	if u == nil || !auth.VerifyPassword(password, u.PasswordHash) {
		return auth.ErrInvalidCredentials
	}
	tok, err := auth.RandomToken(32)
	if err != nil {
		return err
	}
	if _, err := s.Store.CreateSession(u.ID, tok, 24*time.Hour); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(24 * time.Hour),
	})
	return nil
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		_ = s.Store.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:    sessionCookieName,
		Value:   "",
		Path:    "/",
		Expires: time.Unix(0, 0),
		MaxAge:  -1,
	})
}
