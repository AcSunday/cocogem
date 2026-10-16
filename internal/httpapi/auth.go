// Package httpapi exposes the local authenticated collaboration API and Web UI.
package httpapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"time"
)

type authentication struct {
	mu        sync.Mutex
	bootstrap string
	expires   time.Time
	sessions  map[string]string
}

func token() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func newAuthentication() *authentication {
	return &authentication{bootstrap: token(), expires: time.Now().Add(15 * time.Minute), sessions: map[string]string{}}
}
func sameSecret(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("X-Content-Type-Options", "nosniff")
		rw.Header().Set("Referrer-Policy", "no-referrer")
		rw.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'")
		if r.Host != s.config.Host {
			writeError(rw, r, 403, "invalid_host", "本地服务 Host 不匹配")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+s.config.Host {
			writeError(rw, r, 403, "invalid_origin", "请求来源不受信任")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			cookie, err := r.Cookie("cocogem_session")
			if err != nil {
				writeError(rw, r, 401, "unauthorized", "请从服务启动链接建立本地会话")
				return
			}
			s.auth.mu.Lock()
			csrf, ok := s.auth.sessions[cookie.Value]
			s.auth.mu.Unlock()
			if !ok {
				writeError(rw, r, 401, "unauthorized", "本地会话已失效")
				return
			}
			if r.Method != "GET" && r.Method != "HEAD" {
				if r.Header.Get("Content-Type") != "application/json" || !sameSecret(csrf, r.Header.Get("X-CSRF-Token")) {
					writeError(rw, r, 403, "csrf_failed", "请求校验失败")
					return
				}
			}
		}
		next.ServeHTTP(rw, r)
	})
}

func (s *Server) bootstrap(rw http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if !decode(rw, r, &in) {
		return
	}
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	if s.auth.bootstrap == "" || time.Now().After(s.auth.expires) || !sameSecret(in.Token, s.auth.bootstrap) {
		writeError(rw, r, 401, "invalid_bootstrap", "启动链接已使用或过期，请重新启动服务")
		return
	}
	session, csrf := token(), token()
	s.auth.bootstrap = ""
	s.auth.sessions[session] = csrf
	http.SetCookie(rw, &http.Cookie{Name: "cocogem_session", Value: session, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 86400})
	writeJSON(rw, 200, map[string]any{"csrf_token": csrf})
}
