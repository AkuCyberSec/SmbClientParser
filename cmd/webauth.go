package main

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"math"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	webKeyEnvVariable = "SMBCLIENTPARSER_WEB_KEY"
	sessionCookieName = "smbclientparser_session"

	// The browser sends this header on automatic refreshes (polling): they don't count as user activity,
	// otherwise a forgotten open tab would keep the session alive forever.
	pollingHeader = "X-Polling"

	maxFailedLogins    = 5
	loginLockoutPeriod = 15 * time.Minute
)

type authManager struct {
	key               string
	sessionExpiration time.Duration // 0 means sessions never expire

	mu       sync.Mutex
	sessions map[string]time.Time      // session token -> last user activity
	failures map[string]*loginFailures // client IP -> failed login attempts
}

type loginFailures struct {
	count       int
	lockedUntil time.Time
}

func newAuthManager(key string, sessionExpirationMinutes int) *authManager {
	return &authManager{
		key:               key,
		sessionExpiration: time.Duration(sessionExpirationMinutes) * time.Minute,
		sessions:          make(map[string]time.Time),
		failures:          make(map[string]*loginFailures),
	}
}

// generateSecret returns a random string, used for generated keys and session tokens.
func generateSecret() string {
	return rand.Text()
}

// lockedFor returns how long the client IP is still locked out after too many failed logins (0 if not locked).
func (a *authManager) lockedFor(clientIP string) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	failures, ok := a.failures[clientIP]
	if !ok {
		return 0
	}
	return max(time.Until(failures.lockedUntil), 0)
}

// login checks the key. On success it returns a new session token.
// On failure it returns an empty token and the number of consecutive failures of the client IP;
// when that number reaches maxFailedLogins, the IP is locked out for loginLockoutPeriod.
func (a *authManager) login(key string, clientIP string) (token string, failedAttempts int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Constant time comparison: response time doesn't reveal how much of the key is right
	if subtle.ConstantTimeCompare([]byte(key), []byte(a.key)) != 1 {
		failures, ok := a.failures[clientIP]
		if !ok {
			failures = &loginFailures{}
			a.failures[clientIP] = failures
		}
		failures.count++
		failedAttempts = failures.count
		if failures.count >= maxFailedLogins {
			failures.lockedUntil = time.Now().Add(loginLockoutPeriod)
			failures.count = 0
		}
		return "", failedAttempts
	}

	delete(a.failures, clientIP)
	token = generateSecret()
	a.sessions[token] = time.Now()
	return token, 0
}

// checkSession returns true if the token belongs to a session that has not expired.
// If isUserActivity is true, the session expiration starts again from now.
func (a *authManager) checkSession(token string, isUserActivity bool) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	lastActivity, ok := a.sessions[token]
	if !ok {
		return false
	}
	if a.sessionExpiration > 0 && time.Since(lastActivity) > a.sessionExpiration {
		delete(a.sessions, token)
		return false
	}
	if isUserActivity {
		a.sessions[token] = time.Now()
	}
	return true
}

func (a *authManager) logout(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, token)
}

// requireLogin lets the request through only with a valid session cookie.
func (s *webServer) requireLogin(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "sign in first")
			return
		}
		isUserActivity := r.Header.Get(pollingHeader) == ""
		if !s.auth.checkSession(cookie.Value, isUserActivity) {
			writeJSON(w, http.StatusUnauthorized, apiError{Error: "your session expired, sign in again", SessionExpired: true})
			return
		}
		handler(w, r)
	}
}

func (s *webServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Key string `json:"key"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	ip := clientIP(r)

	if wait := s.auth.lockedFor(ip); wait > 0 {
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("too many failed attempts, try again in %d minutes", minutesRoundedUp(wait)))
		return
	}

	token, failedAttempts := s.auth.login(request.Key, ip)
	if token == "" {
		if failedAttempts >= maxFailedLogins {
			s.activity.add(levelError, "%d failed sign-ins from %s: locked out for %d minutes", failedAttempts, ip, minutesRoundedUp(loginLockoutPeriod))
		} else {
			s.activity.add(levelWarning, "Failed sign-in from %s (%d of %d before lockout)", ip, failedAttempts, maxFailedLogins)
		}
		writeError(w, http.StatusUnauthorized, "wrong access key")
		return
	}

	// HttpOnly: page scripts can't read it. SameSite: other web sites can't use it.
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	s.activity.add(levelInfo, "Signed in from %s", ip)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *webServer) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		s.auth.logout(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1, // deletes the cookie
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// clientIP returns the IP address of the client (no proxies are expected in front of this server).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func minutesRoundedUp(duration time.Duration) int {
	return int(math.Ceil(duration.Minutes()))
}
