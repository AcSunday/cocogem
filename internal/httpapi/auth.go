// Package httpapi exposes the local authenticated collaboration API and Web UI.
package httpapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const sessionLifetime = 24 * time.Hour

type localSession struct {
	csrf    string
	expires time.Time
}

type authentication struct {
	mu           sync.Mutex
	path         string
	password     *passwordRecord
	sessions     map[string]localSession
	failures     int
	blockedUntil time.Time
}

func token() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func newAuthentication(path string) (*authentication, error) {
	if path == "" {
		return nil, errors.New("password file path is required")
	}
	password, err := readPassword(path)
	if err != nil {
		return nil, err
	}
	return &authentication{path: path, password: password, sessions: map[string]localSession{}}, nil
}

func sameSecret(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func (a *authentication) session(r *http.Request) (localSession, bool) {
	cookie, err := r.Cookie("cocogem_session")
	if err != nil {
		return localSession{}, false
	}
	session, ok := a.sessions[cookie.Value]
	if !ok || !time.Now().Before(session.expires) {
		delete(a.sessions, cookie.Value)
		return localSession{}, false
	}
	return session, true
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("X-Content-Type-Options", "nosniff")
		rw.Header().Set("Referrer-Policy", "no-referrer")
		rw.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Host != s.config.Host {
			writeError(rw, r, 403, "invalid_host", "本地服务 Host 不匹配")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+s.config.Host {
			writeError(rw, r, 403, "invalid_origin", "请求来源不受信任")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/auth/") {
			if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("Content-Type") != "application/json" {
				writeError(rw, r, 415, "invalid_content_type", "请使用 JSON 请求")
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.auth.mu.Lock()
			session, ok := s.auth.session(r)
			s.auth.mu.Unlock()
			if !ok {
				writeError(rw, r, 401, "unauthorized", "请输入工作空间密码解锁")
				return
			}
			if r.Method != "GET" && r.Method != "HEAD" {
				if r.Header.Get("Content-Type") != "application/json" || !sameSecret(session.csrf, r.Header.Get("X-CSRF-Token")) {
					writeError(rw, r, 403, "csrf_failed", "请求校验失败")
					return
				}
			}
		}
		next.ServeHTTP(rw, r)
	})
}

func (s *Server) authStatus(rw http.ResponseWriter, r *http.Request) {
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	writeJSON(rw, 200, AuthStatus{Initialized: s.auth.password != nil})
}

func (s *Server) authenticate(rw http.ResponseWriter, r *http.Request) {
	var in PasswordInput
	if !decode(rw, r, &in) {
		return
	}
	if !utf8.ValidString(in.Password) || utf8.RuneCountInString(in.Password) < 12 || utf8.RuneCountInString(in.Password) > 128 {
		writeError(rw, r, 400, "invalid_password", "密码需要 12–128 个字符，可以使用短语")
		return
	}
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	a := s.auth
	if r.URL.Path == "/auth/setup" {
		if a.password != nil {
			writeError(rw, r, 409, "already_initialized", "密码已设置，请直接解锁")
			return
		}
		record, err := savePassword(a.path, in.Password)
		if errors.Is(err, os.ErrExist) {
			a.password, err = readPassword(a.path)
			if err == nil {
				writeError(rw, r, 409, "already_initialized", "密码已设置，请直接解锁")
				return
			}
		}
		if err != nil {
			writeError(rw, r, 500, "password_save_failed", "无法保存密码，请检查数据目录权限")
			return
		}
		a.password = record
	} else {
		if a.password == nil {
			writeError(rw, r, 409, "setup_required", "请先设置工作空间密码")
			return
		}
		if time.Now().Before(a.blockedUntil) {
			rw.Header().Set("Retry-After", strconv.Itoa(int(time.Until(a.blockedUntil).Seconds())+1))
			writeError(rw, r, 429, "too_many_attempts", "尝试过于频繁，请稍后再试")
			return
		}
		hash, err := passwordHash(in.Password, a.password.Salt)
		if err != nil || subtle.ConstantTimeCompare(hash, a.password.Hash) != 1 {
			a.failures++
			if a.failures >= 5 {
				a.blockedUntil = time.Now().Add(time.Minute)
			}
			writeError(rw, r, 401, "invalid_password", "密码不正确")
			return
		}
	}
	a.failures = 0
	a.blockedUntil = time.Time{}
	for key, session := range a.sessions {
		if !time.Now().Before(session.expires) {
			delete(a.sessions, key)
		}
	}
	if cookie, err := r.Cookie("cocogem_session"); err == nil {
		delete(a.sessions, cookie.Value)
	}
	// Keep local session storage bounded even if a client discards every cookie.
	if len(a.sessions) >= 64 {
		for key := range a.sessions {
			delete(a.sessions, key)
			break
		}
	}
	session, csrf := token(), token()
	a.sessions[session] = localSession{csrf: csrf, expires: time.Now().Add(sessionLifetime)}
	http.SetCookie(rw, &http.Cookie{Name: "cocogem_session", Value: session, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(sessionLifetime.Seconds())})
	writeJSON(rw, 200, SessionReply{CSRFToken: csrf})
}

func (s *Server) logout(rw http.ResponseWriter, r *http.Request) {
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	cookie, _ := r.Cookie("cocogem_session")
	delete(s.auth.sessions, cookie.Value)
	http.SetCookie(rw, &http.Cookie{Name: "cocogem_session", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writeJSON(rw, 200, AuthStatus{Initialized: true})
}
