package lobby_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KroniK907/hackbox/internal/lobby"
	"github.com/KroniK907/hackbox/internal/platform/hub"
	"github.com/KroniK907/hackbox/internal/store"
)

func TestJoinMintsPlayerAndReconnects(t *testing.T) {
	t.Parallel()
	db, handler, room := testLobby(t)

	get := lobbyRequest(t, handler, http.MethodGet, "/", nil, nil)
	if get.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", get.Code, http.StatusOK)
	}
	if cookies := get.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("GET / cookies = %#v, want none", cookies)
	}
	if !strings.Contains(get.Body.String(), `name="admin_password"`) {
		t.Fatal("join page did not offer host claim when no host exists")
	}

	form := url.Values{"display_name": {"  Alice  "}}
	joined := lobbyRequest(t, handler, http.MethodPost, "/lobby/join", form, nil)
	if joined.Code != http.StatusSeeOther {
		t.Fatalf("Join status = %d, want %d; body = %q", joined.Code, http.StatusSeeOther, joined.Body.String())
	}
	playerCookie := cookieNamed(t, joined, lobby.PlayerCookieName)
	if playerCookie.Value == "" {
		t.Fatal("Join wrote an empty player cookie")
	}
	if !playerCookie.HttpOnly || playerCookie.SameSite != http.SameSiteLaxMode ||
		playerCookie.Path != "/" || playerCookie.Domain != "" || playerCookie.MaxAge != 30*24*60*60 {
		t.Fatalf("player cookie flags = %#v", playerCookie)
	}

	req := httptest.NewRequest(http.MethodGet, "http://hackbox.test/", nil)
	req.AddCookie(playerCookie)
	player, ok, err := room.PlayerFromRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("player cookie did not resolve to a live roster row")
	}
	if player.ID == "" || player.DisplayName != "Alice" || player.AvatarSeed == "" || player.ClaimedHost {
		t.Fatalf("joined player = %#v", player)
	}

	reconnected := lobbyRequest(t, handler, http.MethodGet, "/", nil, playerCookie)
	if reconnected.Code != http.StatusOK {
		t.Fatalf("reconnect status = %d, want %d", reconnected.Code, http.StatusOK)
	}
	if !strings.Contains(reconnected.Body.String(), "Alice") ||
		!strings.Contains(reconnected.Body.String(), `action="/lobby/leave"`) {
		t.Fatalf("reconnect body = %q", reconnected.Body.String())
	}
	if strings.Contains(reconnected.Body.String(), `action="/lobby/join"`) {
		t.Fatal("live player cookie still showed Join")
	}

	board := lobbyRequest(t, handler, http.MethodGet, "/board", nil, nil)
	if !strings.Contains(board.Body.String(), "Alice") {
		t.Fatalf("board body = %q, want Alice", board.Body.String())
	}

	hasOperator, err := db.HasAdminSession(context.Background(), "operator-session")
	if err != nil {
		t.Fatal(err)
	}
	if !hasOperator {
		t.Fatal("Join revoked the setup operator session")
	}
}

func TestJoinValidationCreatesNoIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		display    string
		password   string
		wantStatus int
		wantError  string
	}{
		{
			name:       "empty after trim",
			display:    "   ",
			wantStatus: http.StatusBadRequest,
			wantError:  "Name is required.",
		},
		{
			name:       "same spelling",
			display:    "Alice",
			wantStatus: http.StatusConflict,
			wantError:  "already in use",
		},
		{
			name:       "case insensitive and trimmed",
			display:    "  aLiCe ",
			wantStatus: http.StatusConflict,
			wantError:  "already in use",
		},
		{
			name:       "wrong claim password",
			display:    "Bob",
			password:   "wrong",
			wantStatus: http.StatusUnauthorized,
			wantError:  "Admin password is incorrect.",
		},
	}

	_, handler, _ := testLobby(t)
	first := lobbyRequest(
		t,
		handler,
		http.MethodPost,
		"/lobby/join",
		url.Values{"display_name": {"Alice"}},
		nil,
	)
	if first.Code != http.StatusSeeOther {
		t.Fatalf("first Join status = %d, want %d", first.Code, http.StatusSeeOther)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			form := url.Values{
				"display_name":   {tt.display},
				"admin_password": {tt.password},
			}
			rec := lobbyRequest(t, handler, http.MethodPost, "/lobby/join", form, nil)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %q", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantError) {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tt.wantError)
			}
			if !strings.Contains(rec.Body.String(), `role="alert"`) {
				t.Fatalf("join error missing alert: %q", rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), `hx-get="/lobby/partials/phone"`) {
				t.Fatal("join error page still live-swaps and would clear the alert")
			}
			if cookies := rec.Result().Cookies(); len(cookies) != 0 {
				t.Fatalf("cookies = %#v, want none", cookies)
			}
		})
	}
}

func TestClaimHostAndLeave(t *testing.T) {
	t.Parallel()
	db, handler, room := testLobby(t)

	hostJoin := lobbyRequest(
		t,
		handler,
		http.MethodPost,
		"/lobby/join",
		url.Values{
			"display_name":   {"Host"},
			"admin_password": {"correct horse"},
		},
		nil,
	)
	if hostJoin.Code != http.StatusSeeOther {
		t.Fatalf("host Join status = %d, want %d; body = %q", hostJoin.Code, http.StatusSeeOther, hostJoin.Body.String())
	}
	hostPlayerCookie := cookieNamed(t, hostJoin, lobby.PlayerCookieName)
	hostAdminCookie := cookieNamed(t, hostJoin, "hackbox_admin")
	if hostAdminCookie.MaxAge != 30*24*60*60 || !hostAdminCookie.HttpOnly {
		t.Fatalf("host-phone admin cookie flags = %#v", hostAdminCookie)
	}
	host := playerFromCookie(t, room, hostPlayerCookie)
	if !host.ClaimedHost {
		t.Fatalf("host player = %#v, want claimed host", host)
	}
	hasHostSession, err := db.HasAdminSession(context.Background(), hostAdminCookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if !hasHostSession {
		t.Fatal("host-phone admin session was not stored")
	}

	joinPage := lobbyRequest(t, handler, http.MethodGet, "/", nil, nil)
	if strings.Contains(joinPage.Body.String(), `name="admin_password"`) {
		t.Fatal("join page offered host claim while a claimed host exists")
	}

	guestJoin := lobbyRequest(
		t,
		handler,
		http.MethodPost,
		"/lobby/join",
		url.Values{
			"display_name":   {"Guest"},
			"admin_password": {"wrong but ignored while host exists"},
		},
		nil,
	)
	if guestJoin.Code != http.StatusSeeOther {
		t.Fatalf("guest Join status = %d, want %d; body = %q", guestJoin.Code, http.StatusSeeOther, guestJoin.Body.String())
	}
	if cookieByName(guestJoin, "hackbox_admin") != nil {
		t.Fatal("later joiner received an admin cookie")
	}
	guestPlayerCookie := cookieNamed(t, guestJoin, lobby.PlayerCookieName)
	guest := playerFromCookie(t, room, guestPlayerCookie)

	leave := lobbyRequest(
		t,
		handler,
		http.MethodPost,
		"/lobby/leave",
		url.Values{"player_id": {guest.ID}},
		hostPlayerCookie,
	)
	if leave.Code != http.StatusSeeOther {
		t.Fatalf("Leave status = %d, want %d", leave.Code, http.StatusSeeOther)
	}
	if _, ok := findPlayer(t, room, hostPlayerCookie); ok {
		t.Fatal("Leave kept the cookie owner in the roster")
	}
	if remaining, ok := findPlayer(t, room, guestPlayerCookie); !ok || remaining.ID != guest.ID {
		t.Fatal("Leave trusted the posted player_id instead of the player cookie")
	}
	hasHostSession, err = db.HasAdminSession(context.Background(), hostAdminCookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if hasHostSession {
		t.Fatal("host Leave did not revoke the host-phone admin session")
	}

	stale := lobbyRequest(t, handler, http.MethodGet, "/", nil, hostPlayerCookie)
	if !strings.Contains(stale.Body.String(), `value="Host"`) ||
		!strings.Contains(stale.Body.String(), `name="admin_password"`) {
		t.Fatalf("post-Leave Join body = %q", stale.Body.String())
	}

	rejoin := lobbyRequest(
		t,
		handler,
		http.MethodPost,
		"/lobby/join",
		url.Values{"display_name": {"Host"}},
		hostPlayerCookie,
	)
	if rejoin.Code != http.StatusSeeOther {
		t.Fatalf("rejoin status = %d, want %d; body = %q", rejoin.Code, http.StatusSeeOther, rejoin.Body.String())
	}
	rejoined := playerFromCookie(t, room, cookieNamed(t, rejoin, lobby.PlayerCookieName))
	if rejoined.ID == host.ID {
		t.Fatal("Join after Leave reused the retired player UUID")
	}
	if rejoined.AvatarSeed != host.AvatarSeed {
		t.Fatalf("avatar seed = %q, want leftover %q", rejoined.AvatarSeed, host.AvatarSeed)
	}

	postedIdentity := httptest.NewRequest(http.MethodPost, "http://hackbox.test/lobby/leave", strings.NewReader(url.Values{
		"player_id": {guest.ID},
	}.Encode()))
	postedIdentity.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, ok, err := room.PlayerFromRequest(postedIdentity); err != nil || ok {
		t.Fatalf("posted identity resolved without a cookie: ok=%v err=%v", ok, err)
	}
}

func TestClaimHostRaceHasOneWinner(t *testing.T) {
	t.Parallel()
	_, handler, room := testLobby(t)

	type result struct {
		name string
		rec  *httptest.ResponseRecorder
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var workers sync.WaitGroup
	for _, name := range []string{"Alice", "Bob"} {
		workers.Add(1)
		go func(name string) {
			defer workers.Done()
			<-start
			form := url.Values{
				"display_name":   {name},
				"admin_password": {"correct horse"},
			}
			req := httptest.NewRequest(
				http.MethodPost,
				"http://hackbox.test/lobby/join",
				strings.NewReader(form.Encode()),
			)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			results <- result{name: name, rec: rec}
		}(name)
	}
	close(start)
	workers.Wait()
	close(results)

	adminCookies := 0
	claimedHosts := 0
	for result := range results {
		if result.rec.Code != http.StatusSeeOther {
			t.Fatalf("%s status = %d, want %d; body = %q", result.name, result.rec.Code, http.StatusSeeOther, result.rec.Body.String())
		}
		if cookieByName(result.rec, "hackbox_admin") != nil {
			adminCookies++
		}
		playerCookie := cookieByName(result.rec, lobby.PlayerCookieName)
		if playerCookie == nil {
			t.Fatalf("%s response has no player cookie", result.name)
		}
		if playerFromCookie(t, room, playerCookie).ClaimedHost {
			claimedHosts++
		}
	}
	if adminCookies != 1 || claimedHosts != 1 {
		t.Fatalf("race winners: admin cookies=%d claimed hosts=%d, want 1 and 1", adminCookies, claimedHosts)
	}
}

func TestJoinAndLeavePublishRosterEvents(t *testing.T) {
	t.Parallel()
	_, handler, _ := testLobby(t)

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/lobby/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	if got := response.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("event stream content type = %q, want text/event-stream", got)
	}
	events := bufio.NewReader(response.Body)
	readSSEEvent(t, events, ": connected")

	joined := lobbyRequest(
		t,
		handler,
		http.MethodPost,
		"/lobby/join",
		url.Values{
			"display_name":   {"Host"},
			"admin_password": {"correct horse"},
		},
		nil,
	)
	readSSEEvent(t, events, "event: roster")

	lobbyRequest(
		t,
		handler,
		http.MethodPost,
		"/lobby/leave",
		nil,
		cookieNamed(t, joined, lobby.PlayerCookieName),
	)
	readSSEEvent(t, events, "event: roster")
}

func TestPagesRefetchPartialsOnRosterEventAndReconnect(t *testing.T) {
	t.Parallel()
	_, handler, _ := testLobby(t)

	board := lobbyRequest(t, handler, http.MethodGet, "/board", nil, nil)
	assertLivePage(t, board.Body.String(), "/lobby/partials/board-roster")

	phone := lobbyRequest(t, handler, http.MethodGet, "/", nil, nil)
	assertLivePage(t, phone.Body.String(), "/lobby/partials/phone")

	joined := lobbyRequest(
		t,
		handler,
		http.MethodPost,
		"/lobby/join",
		url.Values{"display_name": {"Alice"}},
		nil,
	)
	inRoom := lobbyRequest(
		t,
		handler,
		http.MethodGet,
		"/",
		nil,
		cookieNamed(t, joined, lobby.PlayerCookieName),
	)
	assertLivePage(t, inRoom.Body.String(), "/lobby/partials/phone")

	boardPartial := lobbyRequest(
		t,
		handler,
		http.MethodGet,
		"/lobby/partials/board-roster",
		nil,
		nil,
	)
	if strings.Contains(boardPartial.Body.String(), "<!doctype html>") ||
		!strings.Contains(boardPartial.Body.String(), "Alice") {
		t.Fatalf("board partial body = %q", boardPartial.Body.String())
	}
}

func assertLivePage(t *testing.T, body, partialPath string) {
	t.Helper()
	for _, want := range []string{
		`src="/static/htmx.min.js?v=`,
		`src="/static/sse.min.js?v=`,
		`href="/static/live.css?v=`,
		`hx-ext="sse"`,
		`sse-connect="/lobby/events"`,
		`hx-get="` + partialPath + `"`,
		`hx-trigger="sse:roster, htmx:sseOpen from:body"`,
		`hx-on::sse-error=`,
		`hx-on::sse-open=`,
		`id="connection-overlay" class="connection-overlay"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body does not contain %q: %s", want, body)
		}
	}
	if got := strings.Count(body, `sse-connect="/lobby/events"`); got != 1 {
		t.Fatalf("EventSource count = %d, want 1", got)
	}
}

func readSSEEvent(t *testing.T, reader *bufio.Reader, want string) {
	t.Helper()
	var event strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		event.WriteString(line)
		if line == "\n" {
			break
		}
	}
	if !strings.Contains(event.String(), want) {
		t.Fatalf("event = %q, want %q", event.String(), want)
	}
}

func testLobby(t *testing.T) (*store.DB, http.Handler, *lobby.Lobby) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.FinishSetup(context.Background(), "stored-hash", "operator-session"); err != nil {
		t.Fatal(err)
	}
	room, err := lobby.New(db, lobby.Config{
		AdminCookieName: "hackbox_admin",
		Events:          hub.New(),
		JoinURL: func(*http.Request) string {
			return "http://192.168.10.24:8654/"
		},
		PasswordMatches: func(hash, password string) bool {
			return hash == "stored-hash" && password == "correct horse"
		},
		SecureCookie: func(*http.Request) bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", room.Phone)
	mux.HandleFunc("GET /board", func(w http.ResponseWriter, r *http.Request) {
		room.Board(w, r, "http://192.168.10.24:8654/")
	})
	room.Register(mux)
	return db, mux, room
}

func lobbyRequest(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	form url.Values,
	cookie *http.Cookie,
) *httptest.ResponseRecorder {
	t.Helper()
	body := strings.NewReader("")
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, "http://hackbox.test"+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func lobbyRequestAll(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	form url.Values,
	cookies ...*http.Cookie,
) *httptest.ResponseRecorder {
	t.Helper()
	body := strings.NewReader("")
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, "http://hackbox.test"+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, cookie := range cookies {
		if cookie != nil {
			req.AddCookie(cookie)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func cookieNamed(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	if cookie := cookieByName(rec, name); cookie != nil {
		return cookie
	}
	t.Fatalf("response cookies = %#v, want %q", rec.Result().Cookies(), name)
	return nil
}

func cookieByName(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func playerFromCookie(t *testing.T, room *lobby.Lobby, cookie *http.Cookie) lobby.Player {
	t.Helper()
	player, ok := findPlayer(t, room, cookie)
	if !ok {
		t.Fatalf("cookie %q did not resolve to a live player", cookie.Name)
	}
	return player
}

func findPlayer(t *testing.T, room *lobby.Lobby, cookie *http.Cookie) (lobby.Player, bool) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://hackbox.test/", nil)
	req.AddCookie(cookie)
	player, ok, err := room.PlayerFromRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	return player, ok
}
