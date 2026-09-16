package host

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/KroniK907/hackbox/internal/games"
	"github.com/KroniK907/hackbox/internal/store"
)

func TestGameContractLoadStartStopAndDrawerPick(t *testing.T) {
	t.Parallel()
	db, handler, rt, fake := testGameHandler(t, 0, 0)
	admin := finishAndJoinHost(t, handler)

	phone := requestWithCookie(t, handler, http.MethodGet, "/", nil, admin)
	body := phone.Body.String()
	if !strings.Contains(body, `name="game_id"`) || !strings.Contains(body, `>fake<`) || !strings.Contains(body, ">Load<") {
		t.Fatalf("host drawer missing game pick: %q", body)
	}
	if strings.Contains(body, ">Start<") {
		t.Fatal("Start shown before Load")
	}
	settings := requestWithCookie(t, handler, http.MethodGet, "/settings", nil, admin).Body.String()
	if strings.Contains(settings, "Game Settings") || strings.Contains(settings, "fake-settings") {
		t.Fatalf("Game Settings before Load: %q", settings)
	}

	load := requestWithCookie(t, handler, http.MethodPost, "/settings/load", url.Values{"game_id": {"fake"}}, admin)
	if load.Code != http.StatusSeeOther {
		t.Fatalf("Load status = %d %q", load.Code, load.Body.String())
	}
	if !fake.loaded {
		t.Fatal("fake was not loaded")
	}
	phone = requestWithCookie(t, handler, http.MethodGet, "/", nil, admin)
	body = phone.Body.String()
	if !strings.Contains(body, "Loaded fake") || !strings.Contains(body, ">Start<") {
		t.Fatalf("drawer after Load = %q", body)
	}
	settings = requestWithCookie(t, handler, http.MethodGet, "/settings", nil, admin).Body.String()
	if !strings.Contains(settings, "Game Settings") || !strings.Contains(settings, "fake-settings") {
		t.Fatalf("settings after Load = %q", settings)
	}
	board := requestWithCookie(t, handler, http.MethodGet, "/board", nil, admin).Body.String()
	if strings.Contains(board, "FAKE-BOARD") {
		t.Fatal("board handed to the game before Start")
	}

	start := requestWithCookie(t, handler, http.MethodPost, "/settings/start", nil, admin)
	if start.Code != http.StatusSeeOther {
		t.Fatalf("Start status = %d %q", start.Code, start.Body.String())
	}
	again := requestWithCookie(t, handler, http.MethodPost, "/settings/start", nil, admin)
	if again.Code != http.StatusSeeOther {
		t.Fatalf("second Start status = %d", again.Code)
	}
	board = requestWithCookie(t, handler, http.MethodGet, "/board", nil, admin).Body.String()
	if !strings.Contains(board, "FAKE-BOARD") || !strings.Contains(board, "rtt-zero=yes") {
		t.Fatalf("started board = %q", board)
	}
	phone = requestWithCookie(t, handler, http.MethodGet, "/", nil, admin)
	body = phone.Body.String()
	if !strings.Contains(body, "FAKE-PHONE") || !strings.Contains(body, "ui-gear") || !strings.Contains(body, ">Stop<") {
		t.Fatalf("in-game phone = %q", body)
	}
	play := requestWithCookie(t, handler, http.MethodGet, "/play/ping", nil, admin)
	if play.Code != http.StatusOK || play.Body.String() != "play-ok" {
		t.Fatalf("play = %d %q", play.Code, play.Body.String())
	}

	if err := fake.helper.KVSet("score", []byte("7")); err != nil {
		t.Fatal(err)
	}
	stop := requestWithCookie(t, handler, http.MethodPost, "/settings/stop", nil, admin)
	if stop.Code != http.StatusSeeOther {
		t.Fatalf("Stop status = %d", stop.Code)
	}
	board = requestWithCookie(t, handler, http.MethodGet, "/board", nil, admin).Body.String()
	if strings.Contains(board, "FAKE-BOARD") {
		t.Fatal("Stop did not return Lobby board")
	}
	got, ok, err := db.KVGet(context.Background(), "fake", "score")
	if err != nil || !ok || string(got) != "7" {
		t.Fatalf("KV after Stop = %q ok=%v err=%v", got, ok, err)
	}
	play = requestWithCookie(t, handler, http.MethodGet, "/play/ping", nil, admin)
	if play.Code != http.StatusNotFound {
		t.Fatalf("play after Stop = %d", play.Code)
	}
	phone = requestWithCookie(t, handler, http.MethodGet, "/", nil, admin)
	if !strings.Contains(phone.Body.String(), ">Start<") {
		t.Fatalf("Start missing after Stop: %q", phone.Body.String())
	}

	clear := requestWithCookie(t, handler, http.MethodPost, "/settings/clear-game-data", nil, admin)
	if clear.Code != http.StatusSeeOther {
		t.Fatalf("clear status = %d", clear.Code)
	}
	_, ok, err = db.KVGet(context.Background(), "fake", "score")
	if err != nil || ok {
		t.Fatalf("KV after clear ok=%v err=%v", ok, err)
	}

	off := requestWithCookie(t, handler, http.MethodPost, "/settings/shutdown", nil, admin)
	if off.Code != http.StatusSeeOther {
		t.Fatalf("Shutdown status = %d", off.Code)
	}
	settings = requestWithCookie(t, handler, http.MethodGet, "/settings", nil, admin).Body.String()
	if strings.Contains(settings, "Game Settings") {
		t.Fatalf("Game Settings after Shutdown: %q", settings)
	}
	if rt.loadedID != "" || fake.loaded {
		t.Fatalf("runtime still loaded id=%q loaded=%v", rt.loadedID, fake.loaded)
	}

	logPage := requestWithCookie(t, handler, http.MethodGet, "/settings/log", nil, admin).Body.String()
	if !strings.Contains(logPage, "Load fake") || !strings.Contains(logPage, "Start fake") || !strings.Contains(logPage, "Finish") {
		t.Fatalf("log page = %q", logPage)
	}
}

func TestStartRefusedBeforeLoadOrZeroSeatedOrBelowMin(t *testing.T) {
	t.Parallel()
	_, handler, _, _ := testGameHandler(t, 2, 8)
	admin := cookieAfterSetup(t, handler)

	refused := requestWithCookie(t, handler, http.MethodPost, "/settings/start", nil, admin)
	if refused.Code != http.StatusConflict {
		t.Fatalf("Start before Load = %d", refused.Code)
	}

	join := requestWithCookie(t, handler, http.MethodPost, "/lobby/join", url.Values{
		"display_name":   {"Ada"},
		"admin_password": {"correct horse"},
	}, admin)
	host := join.Result().Cookies()
	load := requestWithCookie(t, handler, http.MethodPost, "/settings/load", url.Values{"game_id": {"fake"}}, host)
	if load.Code != http.StatusSeeOther {
		t.Fatalf("Load = %d %q", load.Code, load.Body.String())
	}
	start := requestWithCookie(t, handler, http.MethodPost, "/settings/start", nil, host)
	if start.Code != http.StatusConflict {
		t.Fatalf("Start below min = %d %q", start.Code, start.Body.String())
	}
}

func TestAutoStartUsesExistingStartWrite(t *testing.T) {
	t.Parallel()
	_, handler, rt, fake := testGameHandler(t, 0, 0)
	admin := finishAndJoinHost(t, handler)
	if beat := requestWithCookie(t, handler, http.MethodPost, "/lobby/heartbeat", nil, admin); beat.Code != http.StatusNoContent {
		t.Fatalf("heartbeat = %d", beat.Code)
	}
	requestWithCookie(t, handler, http.MethodPost, "/settings/load", url.Values{"game_id": {"fake"}}, admin)
	requestWithCookie(t, handler, http.MethodPost, "/settings/auto-start", url.Values{"enabled": {"1"}}, admin)
	ready := requestWithCookie(t, handler, http.MethodPost, "/lobby/ready", nil, admin)
	if ready.Code != http.StatusSeeOther {
		t.Fatalf("ready = %d %q", ready.Code, ready.Body.String())
	}
	if !fake.started || !rt.started {
		t.Fatalf("auto-start did not Start fake=%v runtime=%v", fake.started, rt.started)
	}
}

func TestLoadRefusedAboveGameMax(t *testing.T) {
	t.Parallel()
	_, handler, _, _ := testGameHandler(t, 0, 1)
	admin := finishAndJoinHost(t, handler)
	requestWithCookie(t, handler, http.MethodPost, "/settings/open", nil, admin)
	requestWithCookie(t, handler, http.MethodPost, "/lobby/join", url.Values{"display_name": {"Bea"}}, nil)

	load := requestWithCookie(t, handler, http.MethodPost, "/settings/load", url.Values{"game_id": {"fake"}}, admin)
	if load.Code != http.StatusConflict {
		t.Fatalf("Load above max = %d %q", load.Code, load.Body.String())
	}
}

func TestPauseResumeAndAutoPause(t *testing.T) {
	t.Parallel()
	_, handler, rt, fake := testGameHandler(t, 0, 0)
	admin := finishAndJoinHost(t, handler)
	requestWithCookie(t, handler, http.MethodPost, "/settings/load", url.Values{"game_id": {"fake"}}, admin)
	requestWithCookie(t, handler, http.MethodPost, "/settings/start", nil, admin)
	requestWithCookie(t, handler, http.MethodPost, "/settings/pause", nil, admin)
	if !fake.paused {
		t.Fatal("Pause did not reach the game")
	}
	requestWithCookie(t, handler, http.MethodPost, "/settings/resume", nil, admin)
	if fake.paused {
		t.Fatal("Resume left the game paused")
	}

	auto := requestWithCookie(t, handler, http.MethodPost, "/settings/auto-pause", url.Values{"enabled": {"1"}}, admin)
	if auto.Code != http.StatusSeeOther {
		t.Fatalf("auto-pause = %d", auto.Code)
	}
	player, ok, err := rt.room.PlayerFromRequest(requestFromCookies(admin))
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := rt.markDisconnected(context.Background(), player.ID); err != nil {
		t.Fatal(err)
	}
	if !fake.paused {
		t.Fatal("auto-pause did not pause on seated disconnect")
	}
}

func TestMidGameDisconnectReservesSeatUntilStop(t *testing.T) {
	t.Parallel()
	db, handler, rt, _ := testGameHandler(t, 0, 0)
	admin := finishAndJoinHost(t, handler)
	requestWithCookie(t, handler, http.MethodPost, "/settings/load", url.Values{"game_id": {"fake"}}, admin)
	requestWithCookie(t, handler, http.MethodPost, "/settings/start", nil, admin)
	requestWithCookie(t, handler, http.MethodPost, "/settings/open", nil, admin)

	host, ok, err := rt.room.PlayerFromRequest(requestFromCookies(admin))
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := rt.room.SetConnected(context.Background(), host.ID, false); err != nil {
		t.Fatal(err)
	}
	requestWithCookie(t, handler, http.MethodPost, "/lobby/join", url.Values{"display_name": {"Bea"}}, nil)

	var seated int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM roster WHERE seated = 1`).Scan(&seated); err != nil {
		t.Fatal(err)
	}
	if seated != 1 {
		t.Fatalf("seated after mid-game join = %d, want reserved host only", seated)
	}

	requestWithCookie(t, handler, http.MethodPost, "/settings/stop", nil, admin)
	var n int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM roster WHERE player_id = ?`, host.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("disconnected host still on the roster after Stop")
	}
}

func testGameHandler(t *testing.T, min, max int) (*store.DB, http.Handler, *runtime, *fakeGame) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fake := &fakeGame{min: min, max: max}
	handler, rt, err := newHandler(db, "http://192.168.10.24:8654/", []games.Factory{{
		ID:  "fake",
		New: func() games.Game { return fake },
	}})
	if err != nil {
		t.Fatal(err)
	}
	return db, handler, rt, fake
}

func finishAndJoinHost(t *testing.T, handler http.Handler) []*http.Cookie {
	t.Helper()
	setup := url.Values{"password": {"correct horse"}, "confirm": {"correct horse"}}
	request(t, handler, http.MethodPost, "/setup", setup, "")
	join := request(t, handler, http.MethodPost, "/lobby/join", url.Values{
		"display_name":   {"Ada"},
		"admin_password": {"correct horse"},
	}, "")
	cookies := join.Result().Cookies()
	if beat := requestWithCookie(t, handler, http.MethodPost, "/lobby/heartbeat", nil, cookies); beat.Code != http.StatusNoContent {
		t.Fatalf("host heartbeat = %d", beat.Code)
	}
	return cookies
}

func cookieAfterSetup(t *testing.T, handler http.Handler) []*http.Cookie {
	t.Helper()
	setup := url.Values{"password": {"correct horse"}, "confirm": {"correct horse"}}
	rec := request(t, handler, http.MethodPost, "/setup", setup, "")
	return rec.Result().Cookies()
}

func requestWithCookie(t *testing.T, handler http.Handler, method, path string, form url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	body := strings.NewReader("")
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, "http://hackbox.test"+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func requestFromCookies(cookies []*http.Cookie) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "http://hackbox.test/", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return req
}
