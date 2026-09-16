package lobby_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/KroniK907/hackbox/internal/lobby"
	"github.com/KroniK907/hackbox/internal/platform/hub"
	"github.com/KroniK907/hackbox/internal/store"
)

func TestRoomFillBehaviors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*testing.T, http.Handler, *lobby.Lobby)
	}{
		{name: "closed join seats host and waits others", run: testClosedJoinToWait},
		{name: "open drains wait up to cap and close pauses fill", run: testOpenCloseFill},
		{name: "wait toggle drops wait and fill skips audience-only", run: testWaitOptOut},
		{name: "kick matches leave and requires admin cookie", run: testKickAndLeave},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, handler, room := testLobby(t)
			tt.run(t, handler, room)
		})
	}
}

func testClosedJoinToWait(t *testing.T, handler http.Handler, room *lobby.Lobby) {
	t.Helper()
	hostCookie := joinNamed(t, handler, "Host", "correct horse")
	host := playerFromCookie(t, room, hostCookie)
	if !host.Seated || host.Waiting {
		t.Fatalf("claimed host = %#v, want seated and not waiting", host)
	}

	guestCookie := joinNamed(t, handler, "Guest", "")
	guest := playerFromCookie(t, room, guestCookie)
	if guest.Seated || !guest.Waiting {
		t.Fatalf("closed-room guest = %#v, want audience and waiting", guest)
	}

	board := lobbyRequest(t, handler, http.MethodGet, "/board", nil, nil).Body.String()
	assertBoardLists(t, board, []string{"Host"}, []string{"Guest"})
	if strings.Contains(board, "<h2>Audience</h2>") {
		t.Fatal("board showed an audience name list")
	}

	phone := lobbyRequest(t, handler, http.MethodGet, "/", nil, guestCookie).Body.String()
	if !strings.Contains(phone, "Guest") ||
		!strings.Contains(phone, `action="/lobby/wait"`) ||
		!strings.Contains(phone, "Leave wait list") ||
		strings.Contains(phone, "ui-plunger") ||
		strings.Contains(phone, "READY") {
		t.Fatalf("waiting phone body = %q", phone)
	}
	hostPhone := lobbyRequest(t, handler, http.MethodGet, "/", nil, hostCookie).Body.String()
	if !strings.Contains(hostPhone, "Host") ||
		strings.Contains(hostPhone, `class="ui-plunger"`) ||
		strings.Contains(hostPhone, `action="/lobby/wait"`) {
		t.Fatalf("seated host phone body = %q", hostPhone)
	}
}

func TestBoardListsHostTokenFirst(t *testing.T) {
	t.Parallel()
	_, handler, room := testLobby(t)
	open := lobbyRequest(t, handler, http.MethodPost, "/settings/open", nil, operatorCookie())
	if open.Code != http.StatusSeeOther {
		t.Fatalf("Open status = %d, want %d; body = %q", open.Code, http.StatusSeeOther, open.Body.String())
	}

	earlyCookie := joinNamed(t, handler, "Early", "")
	hostCookie := joinNamed(t, handler, "Host", "correct horse")
	early := playerFromCookie(t, room, earlyCookie)
	host := playerFromCookie(t, room, hostCookie)
	if !early.Seated || early.ClaimedHost {
		t.Fatalf("early joiner = %#v, want seated guest", early)
	}
	if !host.Seated || !host.ClaimedHost {
		t.Fatalf("late host = %#v, want seated claimed host", host)
	}

	board := lobbyRequest(t, handler, http.MethodGet, "/board", nil, nil).Body.String()
	clusterAt := strings.Index(board, `class="ui-cluster"`)
	marqueeAt := strings.Index(board, `class="ui-marquee"`)
	if clusterAt < 0 || marqueeAt < 0 || marqueeAt < clusterAt {
		t.Fatalf("board missing token cluster: %q", board)
	}
	seatedBody := board[clusterAt:marqueeAt]
	hostAt := strings.Index(seatedBody, `class="ui-token-name">Host</div>`)
	earlyAt := strings.Index(seatedBody, `class="ui-token-name">Early</div>`)
	if hostAt < 0 || earlyAt < 0 || hostAt > earlyAt {
		t.Fatalf("host token was not first: %q", seatedBody)
	}
}

func testOpenCloseFill(t *testing.T, handler http.Handler, room *lobby.Lobby) {
	t.Helper()
	joinNamed(t, handler, "Host", "correct horse")
	var waiters []*http.Cookie
	for _, name := range []string{"A", "B", "C", "D", "E", "F", "G", "H", "I"} {
		waiters = append(waiters, joinNamed(t, handler, name, ""))
	}

	open := lobbyRequest(t, handler, http.MethodPost, "/settings/open", nil, operatorCookie())
	if open.Code != http.StatusSeeOther {
		t.Fatalf("Open status = %d, want %d; body = %q", open.Code, http.StatusSeeOther, open.Body.String())
	}

	board := lobbyRequest(t, handler, http.MethodGet, "/board", nil, nil).Body.String()
	assertBoardLists(t, board,
		[]string{"Host", "A", "B", "C", "D", "E", "F", "G"},
		[]string{"H", "I"},
	)

	closeRoom := lobbyRequest(t, handler, http.MethodPost, "/settings/close", nil, operatorCookie())
	if closeRoom.Code != http.StatusSeeOther {
		t.Fatalf("Close status = %d, want %d", closeRoom.Code, http.StatusSeeOther)
	}

	late := joinNamed(t, handler, "Late", "")
	if p := playerFromCookie(t, room, late); p.Seated || !p.Waiting {
		t.Fatalf("join while closed after Open = %#v, want waiting", p)
	}
	seated := playerFromCookie(t, room, waiters[0])
	if !seated.Seated {
		t.Fatal("Close unseated a player")
	}

	board = lobbyRequest(t, handler, http.MethodGet, "/board", nil, nil).Body.String()
	assertBoardLists(t, board,
		[]string{"Host", "A", "B", "C", "D", "E", "F", "G"},
		[]string{"H", "I", "Late"},
	)

	unauthorized := lobbyRequest(t, handler, http.MethodPost, "/settings/open", nil, nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("Open without admin status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}
}

func testWaitOptOut(t *testing.T, handler http.Handler, room *lobby.Lobby) {
	t.Helper()
	joinNamed(t, handler, "Host", "correct horse")
	guestCookie := joinNamed(t, handler, "Guest", "")

	optOut := lobbyRequest(t, handler, http.MethodPost, "/lobby/wait", nil, guestCookie)
	if optOut.Code != http.StatusSeeOther {
		t.Fatalf("wait-toggle status = %d, want %d", optOut.Code, http.StatusSeeOther)
	}
	guest := playerFromCookie(t, room, guestCookie)
	if guest.Seated || guest.Waiting {
		t.Fatalf("opt-out guest = %#v, want audience-only", guest)
	}
	audiencePhone := lobbyRequest(t, handler, http.MethodGet, "/", nil, guestCookie).Body.String()
	if !strings.Contains(audiencePhone, "You are watching") ||
		!strings.Contains(audiencePhone, "Join wait list") {
		t.Fatalf("audience phone body = %q", audiencePhone)
	}

	lobbyRequest(t, handler, http.MethodPost, "/settings/open", nil, operatorCookie())
	guest = playerFromCookie(t, room, guestCookie)
	if guest.Seated || guest.Waiting {
		t.Fatalf("fill consumed audience-only guest = %#v", guest)
	}
	board := lobbyRequest(t, handler, http.MethodGet, "/board", nil, nil).Body.String()
	assertBoardLists(t, board, []string{"Host"}, nil)
	if strings.Contains(board, "Guest") {
		t.Fatal("audience-only name appeared on the board")
	}

	optIn := lobbyRequest(t, handler, http.MethodPost, "/lobby/wait", nil, guestCookie)
	if optIn.Code != http.StatusSeeOther {
		t.Fatalf("opt-in status = %d, want %d", optIn.Code, http.StatusSeeOther)
	}
	guest = playerFromCookie(t, room, guestCookie)
	if !guest.Seated || guest.Waiting {
		t.Fatalf("open-room opt-in = %#v, want seated from fill", guest)
	}
}

func testKickAndLeave(t *testing.T, handler http.Handler, room *lobby.Lobby) {
	t.Helper()
	hostCookie := joinNamed(t, handler, "Host", "correct horse")
	guestCookie := joinNamed(t, handler, "Guest", "")
	guest := playerFromCookie(t, room, guestCookie)

	noAdmin := lobbyRequest(t, handler, http.MethodPost, "/settings/kick", url.Values{"player_id": {guest.ID}}, nil)
	if noAdmin.Code != http.StatusUnauthorized {
		t.Fatalf("Kick without admin status = %d, want %d", noAdmin.Code, http.StatusUnauthorized)
	}
	if _, ok := findPlayer(t, room, guestCookie); !ok {
		t.Fatal("unauthorized Kick removed the guest")
	}

	kick := lobbyRequest(t, handler, http.MethodPost, "/settings/kick", url.Values{"player_id": {guest.ID}}, operatorCookie())
	if kick.Code != http.StatusSeeOther {
		t.Fatalf("Kick status = %d, want %d; body = %q", kick.Code, http.StatusSeeOther, kick.Body.String())
	}
	if _, ok := findPlayer(t, room, guestCookie); ok {
		t.Fatal("Kick left the guest on the roster")
	}

	kickedJoin := lobbyRequest(t, handler, http.MethodGet, "/", nil, guestCookie)
	if !strings.Contains(kickedJoin.Body.String(), `action="/lobby/join"`) {
		t.Fatalf("post-Kick phone = %q, want Join", kickedJoin.Body.String())
	}

	otherCookie := joinNamed(t, handler, "Other", "")
	leave := lobbyRequest(t, handler, http.MethodPost, "/lobby/leave", nil, otherCookie)
	if leave.Code != http.StatusSeeOther {
		t.Fatalf("Leave status = %d, want %d", leave.Code, http.StatusSeeOther)
	}
	if _, ok := findPlayer(t, room, otherCookie); ok {
		t.Fatal("Leave left the player on the roster")
	}
	if _, ok := findPlayer(t, room, hostCookie); !ok {
		t.Fatal("Leave removed someone else")
	}

	settings := lobbyRequest(t, handler, http.MethodGet, "/settings", nil, operatorCookie()).Body.String()
	if strings.Contains(settings, "sse:roster") || strings.Contains(settings, "/lobby/partials/board-roster") {
		t.Fatal("settings live-updated the player list")
	}
	openAt := strings.Index(settings, `action="/settings/open"`)
	kickAt := strings.Index(settings, "Kick Players")
	if openAt < 0 || kickAt < 0 || kickAt < openAt ||
		!strings.Contains(settings, `action="/settings/kick"`) ||
		!strings.Contains(settings, "Host") {
		t.Fatalf("settings stub = %q", settings)
	}
}

func TestRoomFillPersistsAcrossReopen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.FinishSetup(context.Background(), "stored-hash", "operator-session"); err != nil {
		t.Fatal(err)
	}
	handler, room := lobbyHandler(t, db)
	joinNamed(t, handler, "Host", "correct horse")
	joinNamed(t, handler, "First", "")
	joinNamed(t, handler, "Second", "")
	lobbyRequest(t, handler, http.MethodPost, "/settings/open", nil, operatorCookie())
	_ = db.Close()

	db, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	handler, room = lobbyHandler(t, db)
	board := lobbyRequest(t, handler, http.MethodGet, "/board", nil, nil).Body.String()
	assertBoardLists(t, board, []string{"Host", "First", "Second"}, nil)

	late := joinNamed(t, handler, "Late", "")
	if p := playerFromCookie(t, room, late); !p.Seated {
		t.Fatalf("open room after reopen seated Late? %#v", p)
	}
}

func TestRoomFillPublishesRosterEvents(t *testing.T) {
	t.Parallel()
	_, handler, room := testLobby(t)
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
	events := bufio.NewReader(response.Body)
	readSSEEvent(t, events, ": connected")

	joinNamed(t, handler, "Host", "correct horse")
	readSSEEvent(t, events, "event: roster")

	guest := joinNamed(t, handler, "Guest", "")
	readSSEEvent(t, events, "event: roster")

	lobbyRequest(t, handler, http.MethodPost, "/lobby/wait", nil, guest)
	readSSEEvent(t, events, "event: roster")

	lobbyRequest(t, handler, http.MethodPost, "/settings/open", nil, operatorCookie())
	readSSEEvent(t, events, "event: roster")

	lobbyRequest(t, handler, http.MethodPost, "/lobby/wait", nil, guest)
	readSSEEvent(t, events, "event: roster")

	guestPlayer := playerFromCookie(t, room, guest)
	lobbyRequest(t, handler, http.MethodPost, "/settings/kick", url.Values{"player_id": {guestPlayer.ID}}, operatorCookie())
	readSSEEvent(t, events, "event: roster")

	lobbyRequest(t, handler, http.MethodPost, "/settings/close", nil, operatorCookie())
	readSSEEvent(t, events, "event: roster")
}

func joinNamed(t *testing.T, handler http.Handler, name, password string) *http.Cookie {
	t.Helper()
	form := url.Values{"display_name": {name}}
	if password != "" {
		form.Set("admin_password", password)
	}
	rec := lobbyRequest(t, handler, http.MethodPost, "/lobby/join", form, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("Join %s status = %d, want %d; body = %q", name, rec.Code, http.StatusSeeOther, rec.Body.String())
	}
	cookie := cookieNamed(t, rec, lobby.PlayerCookieName)
	beat := lobbyRequest(t, handler, http.MethodPost, "/lobby/heartbeat", nil, cookie)
	if beat.Code != http.StatusNoContent {
		t.Fatalf("Join %s heartbeat status = %d, want %d", name, beat.Code, http.StatusNoContent)
	}
	return cookie
}

func operatorCookie() *http.Cookie {
	return &http.Cookie{Name: "hackbox_admin", Value: "operator-session"}
}

func assertBoardLists(t *testing.T, body string, seated, waiting []string) {
	t.Helper()
	clusterAt := strings.Index(body, `class="ui-cluster"`)
	marqueeAt := strings.Index(body, `class="ui-marquee"`)
	if clusterAt < 0 || marqueeAt < 0 || marqueeAt < clusterAt {
		t.Fatalf("board missing token cluster then wait marquee: %q", body)
	}
	seatedBody := body[clusterAt:marqueeAt]
	waitingBody := body[marqueeAt:]
	if strings.Contains(body, "<h2>Audience</h2>") {
		t.Fatal("board showed an audience name list")
	}
	for _, name := range seated {
		if !strings.Contains(seatedBody, `class="ui-token-name">`+name+`</div>`) {
			t.Fatalf("seated cluster missing %q in %q", name, seatedBody)
		}
		if strings.Contains(waitingBody, `</svg> `+name+`</span>`) {
			t.Fatalf("%q listed as waiting: %q", name, waitingBody)
		}
	}
	for _, name := range waiting {
		if !strings.Contains(waitingBody, `</svg> `+name+`</span>`) {
			t.Fatalf("wait marquee missing %q in %q", name, waitingBody)
		}
		if strings.Contains(seatedBody, `class="ui-token-name">`+name+`</div>`) {
			t.Fatalf("%q listed as seated: %q", name, seatedBody)
		}
	}
}

func lobbyHandler(t *testing.T, db *store.DB) (http.Handler, *lobby.Lobby) {
	t.Helper()
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
	return mux, room
}
