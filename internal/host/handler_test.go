package host

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/KroniK907/hackbox/internal/store"
)

func TestSetupLifecycle(t *testing.T) {
	t.Parallel()
	db, handler := testHandler(t)

	assertRedirect(t, handler, http.MethodGet, "/", nil, "/setup")
	assertStatus(t, handler, http.MethodGet, "/setup", nil, http.StatusOK)

	bad := url.Values{"password": {"abcdefgh"}, "confirm": {"different"}}
	assertStatus(t, handler, http.MethodPost, "/setup", bad, http.StatusBadRequest)
	hasHash, err := db.HasAdminHash(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hasHash {
		t.Fatal("mismatched passwords wrote a hash")
	}

	short := url.Values{"password": {"short"}, "confirm": {"short"}}
	assertStatus(t, handler, http.MethodPost, "/setup", short, http.StatusBadRequest)
	hasHash, err = db.HasAdminHash(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hasHash {
		t.Fatal("short password wrote a hash")
	}

	form := url.Values{"password": {"correct horse"}, "confirm": {"correct horse"}}
	rec := request(t, handler, http.MethodPost, "/setup", form, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("Finish status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if got := rec.Header().Get("Location"); got != "/board" {
		t.Fatalf("Finish Location = %q, want /board", got)
	}

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("Finish cookies = %d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != adminCookieName || cookie.Value == "" {
		t.Fatalf("admin cookie = %#v", cookie)
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatalf("admin cookie flags = %#v", cookie)
	}
	if cookie.Secure {
		t.Fatal("plain LAN HTTP cookie must not be Secure")
	}
	hasSession, err := db.HasAdminSession(context.Background(), cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if !hasSession {
		t.Fatal("cookie does not identify a server-side session")
	}

	hash, err := db.AdminHash(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") || strings.Contains(hash, "correct horse") {
		t.Fatalf("stored value is not an argon2id hash: %q", hash)
	}

	assertStatus(t, handler, http.MethodPost, "/setup", form, http.StatusConflict)
	assertRedirect(t, handler, http.MethodGet, "/setup", nil, "/board")
	for _, path := range []string{"/", "/board", "/settings"} {
		assertStatus(t, handler, http.MethodGet, path, nil, http.StatusOK)
	}

	board := request(t, handler, http.MethodGet, "/board", nil, "192.168.10.24:8654")
	if !strings.Contains(board.Body.String(), "http://192.168.10.24:8654/") {
		t.Fatalf("board page missing LAN join URL: %q", board.Body.String())
	}
	phone := request(t, handler, http.MethodGet, "/", nil, "192.168.10.24:8654")
	if strings.Contains(phone.Body.String(), "http://192.168.10.24:8654/") {
		t.Fatal("phone stub should not show the LAN join URL")
	}
}

func TestJoinUsesSetupPasswordAndHostCookiePolicy(t *testing.T) {
	t.Parallel()
	db, handler := testHandler(t)
	setup := url.Values{"password": {"correct horse"}, "confirm": {"correct horse"}}
	request(t, handler, http.MethodPost, "/setup", setup, "localhost:8654")

	wrong := url.Values{"display_name": {"Alice"}, "admin_password": {"wrong"}}
	rec := request(t, handler, http.MethodPost, "/lobby/join", wrong, "localhost:8654")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-password Join status = %d, want %d; body = %q", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
	if cookies := rec.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("wrong-password Join cookies = %#v, want none", cookies)
	}

	correct := url.Values{"display_name": {"Alice"}, "admin_password": {"correct horse"}}
	rec = request(t, handler, http.MethodPost, "/lobby/join", correct, "localhost:8654")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("correct-password Join status = %d, want %d; body = %q", rec.Code, http.StatusSeeOther, rec.Body.String())
	}
	if len(rec.Result().Cookies()) != 2 {
		t.Fatalf("correct-password Join cookies = %#v, want player and admin", rec.Result().Cookies())
	}
	for _, cookie := range rec.Result().Cookies() {
		if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode ||
			cookie.Path != "/" || cookie.Domain != "" || cookie.MaxAge != 30*24*60*60 {
			t.Fatalf("Join cookie flags = %#v", cookie)
		}
		if cookie.Name == adminCookieName {
			hasSession, err := db.HasAdminSession(context.Background(), cookie.Value)
			if err != nil {
				t.Fatal(err)
			}
			if !hasSession {
				t.Fatal("host-phone admin cookie has no server-side session")
			}
		}
	}
}

func TestLiveAssetsAreLocalAndSettingsDoesNotSubscribe(t *testing.T) {
	t.Parallel()
	_, handler := testHandler(t)

	for asset, contentType := range map[string]string{
		"/static/htmx.min.js": "javascript",
		"/static/live.css":    "text/css",
		"/static/sse.min.js":  "javascript",
	} {
		rec := request(t, handler, http.MethodGet, asset, nil, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d", asset, rec.Code, http.StatusOK)
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), contentType) {
			t.Fatalf("GET %s Content-Type = %q", asset, rec.Header().Get("Content-Type"))
		}
		if asset == "/static/live.css" {
			css := rec.Body.String()
			if !strings.Contains(css, "position: fixed") {
				t.Fatalf("GET %s did not contain fixed overlay styling", asset)
			}
			if !strings.Contains(css, "appearance: none") || !strings.Contains(css, `[data-theme="neon-dark"] .ui-field select`) {
				t.Fatalf("GET %s missing themed select caret", asset)
			}
		}
	}

	setup := url.Values{"password": {"correct horse"}, "confirm": {"correct horse"}}
	request(t, handler, http.MethodPost, "/setup", setup, "")
	settings := request(t, handler, http.MethodGet, "/settings", nil, "")
	if strings.Contains(settings.Body.String(), "sse:roster") ||
		strings.Contains(settings.Body.String(), "/lobby/partials/board-roster") {
		t.Fatalf("settings live-updated the player list: %q", settings.Body.String())
	}
}

func TestAdminCookieIsSecureOnLocalhost(t *testing.T) {
	t.Parallel()
	form := url.Values{"password": {"correct horse"}, "confirm": {"correct horse"}}
	for _, host := range []string{"localhost:8654", "127.0.0.1:8654", "[::1]:8654"} {
		_, handler := testHandler(t)
		rec := request(t, handler, http.MethodPost, "/setup", form, host)
		cookies := rec.Result().Cookies()
		if len(cookies) != 1 || !cookies[0].Secure {
			t.Fatalf("%s admin cookie = %#v, want Secure", host, cookies)
		}
	}
}

func TestBoardJoinURLUsesPublicHostname(t *testing.T) {
	t.Parallel()
	_, handler := testHandler(t)
	form := url.Values{"password": {"correct horse"}, "confirm": {"correct horse"}}
	request(t, handler, http.MethodPost, "/setup", form, "hackbox.thekranichs.com")

	req := httptest.NewRequest(http.MethodGet, "https://hackbox.thekranichs.com/board", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("board status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "https://hackbox.thekranichs.com/") {
		t.Fatalf("board page missing public join URL: %q", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "http://192.168.10.24:8654/") {
		t.Fatal("public board page still showed the LAN join URL")
	}

	phone := httptest.NewRequest(http.MethodGet, "https://hackbox.thekranichs.com/", nil)
	phone.Header.Set("X-Forwarded-Proto", "https")
	phoneRec := httptest.NewRecorder()
	handler.ServeHTTP(phoneRec, phone)
	if strings.Contains(phoneRec.Body.String(), "https://hackbox.thekranichs.com/") {
		t.Fatal("phone stub should not show the join URL")
	}
}

func TestBoardJoinURLKeepsLANOnLocalhost(t *testing.T) {
	t.Parallel()
	_, handler := testHandler(t)
	form := url.Values{"password": {"correct horse"}, "confirm": {"correct horse"}}
	request(t, handler, http.MethodPost, "/setup", form, "localhost:8654")

	board := request(t, handler, http.MethodGet, "/board", nil, "localhost:8654")
	if !strings.Contains(board.Body.String(), "http://192.168.10.24:8654/") {
		t.Fatalf("localhost board missing LAN join URL: %q", board.Body.String())
	}
	if strings.Contains(board.Body.String(), "localhost") {
		t.Fatal("board advertised localhost")
	}
}

func TestAdminCookieIsSecureBehindHTTPSProxy(t *testing.T) {
	t.Parallel()
	_, handler := testHandler(t)
	form := url.Values{"password": {"correct horse"}, "confirm": {"correct horse"}}
	req := httptest.NewRequest(http.MethodPost, "https://hackbox.thekranichs.com/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("proxied HTTPS admin cookie = %#v, want Secure", cookies)
	}
}

func TestCrossOriginFinishIsForbidden(t *testing.T) {
	t.Parallel()
	db, handler := testHandler(t)
	form := url.Values{"password": {"correct horse"}, "confirm": {"correct horse"}}
	req := httptest.NewRequest(http.MethodPost, "http://hackbox.test/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	hasHash, err := db.HasAdminHash(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hasHash {
		t.Fatal("cross-origin Finish wrote a hash")
	}
}

func testHandler(t *testing.T) (*store.DB, http.Handler) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	handler, err := NewHandler(db, "http://192.168.10.24:8654/")
	if err != nil {
		t.Fatal(err)
	}
	return db, handler
}

func assertRedirect(t *testing.T, handler http.Handler, method, path string, form url.Values, location string) {
	t.Helper()
	rec := request(t, handler, method, path, form, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("%s %s status = %d, want %d", method, path, rec.Code, http.StatusSeeOther)
	}
	if got := rec.Header().Get("Location"); got != location {
		t.Fatalf("%s %s Location = %q, want %q", method, path, got, location)
	}
}

func assertStatus(t *testing.T, handler http.Handler, method, path string, form url.Values, status int) {
	t.Helper()
	rec := request(t, handler, method, path, form, "")
	if rec.Code != status {
		t.Fatalf("%s %s status = %d, want %d; body = %q", method, path, rec.Code, status, rec.Body.String())
	}
}

func request(t *testing.T, handler http.Handler, method, path string, form url.Values, host string) *httptest.ResponseRecorder {
	t.Helper()
	var body *strings.Reader
	if form == nil {
		body = strings.NewReader("")
	} else {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, "http://hackbox.test"+path, body)
	if host != "" {
		req.Host = host
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
