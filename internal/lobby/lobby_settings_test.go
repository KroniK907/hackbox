package lobby_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/KroniK907/hackbox/internal/lobby"
)

func TestGM077SeatCapRefuseLogoutAndLogin(t *testing.T) {
	t.Parallel()
	_, handler, _ := testLobby(t)

	loginPage := lobbyRequest(t, handler, http.MethodGet, "/settings", nil, nil).Body.String()
	if !strings.Contains(loginPage, ">Log in</button>") || strings.Contains(loginPage, "Seat cap") {
		t.Fatalf("logged-out settings = %q", loginPage)
	}

	bad := lobbyRequest(t, handler, http.MethodPost, "/settings/login", url.Values{"password": {"nope"}}, nil)
	if bad.Code != http.StatusUnauthorized || !strings.Contains(bad.Body.String(), "incorrect") {
		t.Fatalf("bad login = %d %q", bad.Code, bad.Body.String())
	}

	loggedIn := lobbyRequest(t, handler, http.MethodPost, "/settings/login", url.Values{"password": {"correct horse"}}, nil)
	if loggedIn.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d; body = %q", loggedIn.Code, loggedIn.Body.String())
	}
	tab := cookieNamed(t, loggedIn, "hackbox_admin")
	list := lobbyRequest(t, handler, http.MethodGet, "/settings", nil, tab).Body.String()
	if !strings.Contains(list, "Seat cap") ||
		!strings.Contains(list, "Cycle seated players") ||
		!strings.Contains(list, "Fill empty seats") ||
		!strings.Contains(list, "Advertised hostname") ||
		!strings.Contains(list, "Log out") {
		t.Fatalf("settings list = %q", list)
	}

	joinNamed(t, handler, "Ada", "correct horse")
	joinNamed(t, handler, "Bea", "")
	lobbyRequest(t, handler, http.MethodPost, "/settings/open", nil, operatorCookie())
	joinNamed(t, handler, "Cal", "")

	refused := lobbyRequest(t, handler, http.MethodPost, "/settings/seat-cap", url.Values{"seat_cap": {"1"}}, operatorCookie())
	if refused.Code != http.StatusBadRequest || !strings.Contains(refused.Body.String(), "seated") {
		t.Fatalf("seat cap refuse = %d %q", refused.Code, refused.Body.String())
	}
	okCap := lobbyRequest(t, handler, http.MethodPost, "/settings/seat-cap", url.Values{"seat_cap": {"4"}}, operatorCookie())
	if okCap.Code != http.StatusSeeOther {
		t.Fatalf("seat cap save = %d %q", okCap.Code, okCap.Body.String())
	}

	blankHost := lobbyRequest(t, handler, http.MethodPost, "/settings/hostname", url.Values{"hostname": {"  "}}, operatorCookie())
	if blankHost.Code != http.StatusSeeOther {
		t.Fatalf("blank hostname status = %d", blankHost.Code)
	}
	board := lobbyRequest(t, handler, http.MethodGet, "/board", nil, nil).Body.String()
	if !strings.Contains(board, "http://192.168.10.24:8654/") {
		t.Fatalf("blank hostname board = %q", board)
	}
	named := lobbyRequest(t, handler, http.MethodPost, "/settings/hostname", url.Values{"hostname": {"party.lan"}}, operatorCookie())
	if named.Code != http.StatusSeeOther {
		t.Fatalf("hostname save = %d", named.Code)
	}
	board = lobbyRequest(t, handler, http.MethodGet, "/board", nil, nil).Body.String()
	if !strings.Contains(board, "http://party.lan/") || strings.Contains(board, "party.lan:8654") {
		t.Fatalf("named hostname board = %q", board)
	}
	withPort := lobbyRequest(t, handler, http.MethodPost, "/settings/hostname", url.Values{"hostname": {"party.lan:8080"}}, operatorCookie())
	if withPort.Code != http.StatusSeeOther {
		t.Fatalf("hostname with port save = %d", withPort.Code)
	}
	board = lobbyRequest(t, handler, http.MethodGet, "/board", nil, nil).Body.String()
	if !strings.Contains(board, "http://party.lan:8080/") {
		t.Fatalf("typed-port hostname board = %q", board)
	}

	lobbyRequest(t, handler, http.MethodPost, "/settings/fill-empty", url.Values{"fill_empty": {"1"}}, operatorCookie())
	lobbyRequest(t, handler, http.MethodPost, "/settings/cycle-mode", url.Values{"cycle_mode": {"cycle"}}, operatorCookie())
	list = lobbyRequest(t, handler, http.MethodGet, "/settings", nil, operatorCookie()).Body.String()
	if !strings.Contains(list, `value="cycle" selected`) {
		t.Fatalf("cycle mode not selected: %q", list)
	}

	other := lobbyRequest(t, handler, http.MethodPost, "/settings/login", url.Values{"password": {"correct horse"}}, nil)
	otherTab := cookieNamed(t, other, "hackbox_admin")
	out := lobbyRequest(t, handler, http.MethodPost, "/settings/logout", nil, tab)
	if out.Code != http.StatusSeeOther {
		t.Fatalf("logout status = %d", out.Code)
	}
	loggedOut := lobbyRequest(t, handler, http.MethodGet, "/settings", nil, tab).Body.String()
	if !strings.Contains(loggedOut, ">Log in</button>") {
		t.Fatalf("after logout = %q", loggedOut)
	}
	still := lobbyRequest(t, handler, http.MethodGet, "/settings", nil, otherTab).Body.String()
	if !strings.Contains(still, "Seat cap") {
		t.Fatalf("other operator tab lost session: %q", still)
	}
}

func TestHostSitStandBumpAndQueuedSit(t *testing.T) {
	t.Parallel()
	db, handler, room := testLobby(t)

	hostJoin := lobbyRequest(t, handler, http.MethodPost, "/lobby/join", url.Values{
		"display_name":   {"Host"},
		"admin_password": {"correct horse"},
	}, nil)
	hostPlayer := cookieNamed(t, hostJoin, lobby.PlayerCookieName)
	hostAdmin := cookieNamed(t, hostJoin, "hackbox_admin")

	phone := lobbyRequestAll(t, handler, http.MethodGet, "/", nil, hostPlayer, hostAdmin).Body.String()
	if !strings.Contains(phone, `id="host-drawer"`) ||
		!strings.Contains(phone, `ui-drawer-handle`) ||
		!strings.Contains(phone, `ui-drawer-scrim`) ||
		!strings.Contains(phone, ">Back</button>") ||
		!strings.Contains(phone, "Kick Players") ||
		!strings.Contains(phone, `action="/settings/stand"`) {
		t.Fatalf("host phone missing drawer: %q", phone)
	}
	inner := lobbyRequestAll(t, handler, http.MethodGet, "/lobby/partials/phone", nil, hostPlayer, hostAdmin).Body.String()
	if strings.Contains(inner, `id="host-drawer"`) {
		t.Fatalf("phone SSE swap included the host drawer: %q", inner)
	}
	if !strings.Contains(inner, `id="phone-inner"`) {
		t.Fatalf("phone SSE swap missing room inner: %q", inner)
	}

	stand := lobbyRequestAll(t, handler, http.MethodPost, "/settings/stand", nil, hostPlayer, hostAdmin)
	if stand.Code != http.StatusSeeOther {
		t.Fatalf("stand status = %d %q", stand.Code, stand.Body.String())
	}
	host := playerFromCookie(t, room, hostPlayer)
	if host.Seated || host.Waiting {
		t.Fatalf("after stand = %#v", host)
	}

	lobbyRequest(t, handler, http.MethodPost, "/settings/open", nil, operatorCookie())
	lobbyRequest(t, handler, http.MethodPost, "/settings/seat-cap", url.Values{"seat_cap": {"1"}}, operatorCookie())
	guest := joinNamed(t, handler, "Guest", "")
	guestPlayer := playerFromCookie(t, room, guest)
	if !guestPlayer.Seated {
		t.Fatalf("guest should take the only seat: %#v", guestPlayer)
	}

	sitNeedBump := lobbyRequestAll(t, handler, http.MethodPost, "/settings/sit", nil, hostPlayer, hostAdmin)
	if sitNeedBump.Code != http.StatusConflict {
		t.Fatalf("sit without bump = %d %q", sitNeedBump.Code, sitNeedBump.Body.String())
	}
	sit := lobbyRequestAll(t, handler, http.MethodPost, "/settings/sit", url.Values{"bump_player_id": {guestPlayer.ID}}, hostPlayer, hostAdmin)
	if sit.Code != http.StatusSeeOther {
		t.Fatalf("sit bump = %d %q", sit.Code, sit.Body.String())
	}
	host = playerFromCookie(t, room, hostPlayer)
	guestPlayer = playerFromCookie(t, room, guest)
	if !host.Seated || guestPlayer.Seated || !guestPlayer.Waiting {
		t.Fatalf("after bump host=%#v guest=%#v", host, guestPlayer)
	}

	if _, err := db.SQL().Exec(`UPDATE room_state SET round_active = 1`); err != nil {
		t.Fatal(err)
	}
	queued := lobbyRequestAll(t, handler, http.MethodPost, "/settings/stand", nil, hostPlayer, hostAdmin)
	if queued.Code != http.StatusSeeOther {
		t.Fatalf("queued stand = %d", queued.Code)
	}
	host = playerFromCookie(t, room, hostPlayer)
	if !host.Seated {
		t.Fatal("queued stand applied during a round")
	}
	var queue string
	if err := db.SQL().QueryRow(`SELECT host_queue FROM room_state WHERE id = 1`).Scan(&queue); err != nil {
		t.Fatal(err)
	}
	if queue != "stand" {
		t.Fatalf("host_queue = %q, want stand", queue)
	}
}

func TestMakeHostAndTakeHost(t *testing.T) {
	t.Parallel()
	db, handler, room := testLobby(t)

	hostJoin := lobbyRequest(t, handler, http.MethodPost, "/lobby/join", url.Values{
		"display_name":   {"Host"},
		"admin_password": {"correct horse"},
	}, nil)
	hostCookie := cookieNamed(t, hostJoin, lobby.PlayerCookieName)
	if beat := lobbyRequest(t, handler, http.MethodPost, "/lobby/heartbeat", nil, hostCookie); beat.Code != http.StatusNoContent {
		t.Fatalf("host heartbeat = %d", beat.Code)
	}
	guestCookie := joinNamed(t, handler, "Maya", "")
	maya := playerFromCookie(t, room, guestCookie)

	live := lobbyRequest(t, handler, http.MethodPost, "/settings/make-host", url.Values{"player_id": {maya.ID}}, operatorCookie())
	if live.Code != http.StatusConflict {
		t.Fatalf("make host while live = %d %q", live.Code, live.Body.String())
	}

	if _, err := db.SQL().Exec(`UPDATE roster SET disconnected = 1 WHERE claimed_host = 1`); err != nil {
		t.Fatal(err)
	}
	made := lobbyRequest(t, handler, http.MethodPost, "/settings/make-host", url.Values{"player_id": {maya.ID}}, operatorCookie())
	if made.Code != http.StatusSeeOther {
		t.Fatalf("make host = %d %q", made.Code, made.Body.String())
	}
	maya = playerFromCookie(t, room, guestCookie)
	if !maya.PendingDesignation {
		t.Fatalf("pending designation = %#v", maya)
	}
	phone := lobbyRequest(t, handler, http.MethodGet, "/", nil, guestCookie).Body.String()
	if !strings.Contains(phone, "Take host") || !strings.Contains(phone, `action="/lobby/take-host"`) {
		t.Fatalf("take host popup missing: %q", phone)
	}

	wrong := lobbyRequest(t, handler, http.MethodPost, "/lobby/take-host", url.Values{"password": {"wrong"}}, guestCookie)
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong take host = %d", wrong.Code)
	}
	ok := lobbyRequest(t, handler, http.MethodPost, "/lobby/take-host", url.Values{"password": {"correct horse"}}, guestCookie)
	if ok.Code != http.StatusSeeOther {
		t.Fatalf("take host = %d %q", ok.Code, ok.Body.String())
	}
	admin := cookieNamed(t, ok, "hackbox_admin")
	maya = playerFromCookie(t, room, guestCookie)
	if !maya.ClaimedHost || maya.PendingDesignation {
		t.Fatalf("after take host = %#v", maya)
	}
	oldHost := playerFromCookie(t, room, cookieNamed(t, hostJoin, lobby.PlayerCookieName))
	if oldHost.ClaimedHost {
		t.Fatal("old host still claimed")
	}
	panel := lobbyRequestAll(t, handler, http.MethodGet, "/", nil, guestCookie, admin).Body.String()
	if !strings.Contains(panel, `id="host-drawer"`) || !strings.Contains(panel, "Kick Players") {
		t.Fatalf("new host phone = %q", panel)
	}
}

func TestCycleSeatedPlayersLeavesAudienceWaitersFirst(t *testing.T) {
	t.Parallel()
	_, handler, room := testLobby(t)
	joinNamed(t, handler, "Host", "correct horse")
	lobbyRequest(t, handler, http.MethodPost, "/settings/open", nil, operatorCookie())
	lobbyRequest(t, handler, http.MethodPost, "/settings/seat-cap", url.Values{"seat_cap": {"2"}}, operatorCookie())
	joinNamed(t, handler, "Two", "")
	waiter := joinNamed(t, handler, "Wait", "")
	waitPlayer := playerFromCookie(t, room, waiter)
	if waitPlayer.Seated {
		t.Fatal("third joiner sat")
	}
	cycled := lobbyRequest(t, handler, http.MethodPost, "/settings/cycle", nil, operatorCookie())
	if cycled.Code != http.StatusSeeOther {
		t.Fatalf("cycle = %d %q", cycled.Code, cycled.Body.String())
	}
	waitPlayer = playerFromCookie(t, room, waiter)
	if !waitPlayer.Seated {
		t.Fatalf("existing waiter should refill first: %#v", waitPlayer)
	}
}
