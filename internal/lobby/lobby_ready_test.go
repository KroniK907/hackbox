package lobby_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KroniK907/hackbox/internal/lobby"
	"github.com/KroniK907/hackbox/internal/platform/hub"
	"github.com/KroniK907/hackbox/internal/store"
)

func TestReadyIgnoredBeforeLoadFromWaitersAndAfterStart(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{name: "before Load and waiter", run: testReadyIgnoredBeforeLoadAndWaiter},
		{name: "after Start", run: testReadyIgnoredAfterStart},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.run(t)
		})
	}
}

func testReadyIgnoredBeforeLoadAndWaiter(t *testing.T) {
	t.Helper()
	_, handler, room := testLobby(t)
	host := joinNamed(t, handler, "Host", "correct horse")
	guest := joinNamed(t, handler, "Guest", "")

	ignored := lobbyRequest(t, handler, http.MethodPost, "/lobby/ready", nil, host)
	if ignored.Code != http.StatusSeeOther {
		t.Fatalf("ready before Load = %d", ignored.Code)
	}
	if playerFromCookie(t, room, host).Ready {
		t.Fatal("ready stuck before Load")
	}

	waiter := lobbyRequest(t, handler, http.MethodPost, "/lobby/ready", nil, guest)
	if waiter.Code != http.StatusSeeOther {
		t.Fatalf("waiter ready = %d", waiter.Code)
	}
	if playerFromCookie(t, room, guest).Ready {
		t.Fatal("waiter ready was stored")
	}
}

func testReadyIgnoredAfterStart(t *testing.T) {
	t.Helper()
	_, handler, room := openTestLobby(t, t.TempDir(), func(c *lobby.Config) {
		c.PhoneExtras = func(*http.Request, lobby.Player) lobby.PhoneExtras {
			return lobby.PhoneExtras{LoadedGameID: "fake", Started: true}
		}
	})
	host := joinNamed(t, handler, "Host", "correct horse")
	locked := lobbyRequest(t, handler, http.MethodPost, "/lobby/ready", nil, host)
	if locked.Code != http.StatusSeeOther {
		t.Fatalf("ready after Start = %d", locked.Code)
	}
	if playerFromCookie(t, room, host).Ready {
		t.Fatal("ready after Start was stored")
	}
}

func TestReadyToggleAndBoardCheck(t *testing.T) {
	t.Parallel()
	_, handler, room := testLobbyReady(t, nil)
	host := joinNamed(t, handler, "Host", "correct horse")

	phone := lobbyRequest(t, handler, http.MethodGet, "/", nil, host).Body.String()
	if !strings.Contains(phone, `action="/lobby/ready"`) || !strings.Contains(phone, ">READY<") {
		t.Fatalf("seated ready missing: %q", phone)
	}

	on := lobbyRequest(t, handler, http.MethodPost, "/lobby/ready", nil, host)
	if on.Code != http.StatusSeeOther {
		t.Fatalf("ready = %d", on.Code)
	}
	if !playerFromCookie(t, room, host).Ready {
		t.Fatal("ready did not toggle on")
	}
	board := lobbyRequest(t, handler, http.MethodGet, "/board", nil, nil).Body.String()
	if !strings.Contains(board, `class="ui-check"`) {
		t.Fatalf("board missing ready check: %q", board)
	}

	off := lobbyRequest(t, handler, http.MethodPost, "/lobby/ready", nil, host)
	if off.Code != http.StatusSeeOther {
		t.Fatalf("unready = %d", off.Code)
	}
	if playerFromCookie(t, room, host).Ready {
		t.Fatal("unready did not clear")
	}
}

func TestHeartbeat204DimUndimAndRTT(t *testing.T) {
	t.Parallel()
	_, handler, room := testLobby(t)
	cookie := joinWithoutBeat(t, handler, "Ada", "correct horse")
	player := playerFromCookie(t, room, cookie)
	if !player.Disconnected {
		t.Fatal("join without heartbeat was already connected")
	}

	first := lobbyRequest(t, handler, http.MethodPost, "/lobby/heartbeat", nil, cookie)
	if first.Code != http.StatusNoContent {
		t.Fatalf("heartbeat = %d", first.Code)
	}
	if playerFromCookie(t, room, cookie).Disconnected {
		t.Fatal("heartbeat did not clear disconnected")
	}
	if room.LastHeartbeatRTT(player.ID) != 0 {
		t.Fatal("first heartbeat should leave RTT empty")
	}

	second := lobbyRequest(t, handler, http.MethodPost, "/lobby/heartbeat", nil, cookie)
	if second.Code != http.StatusNoContent {
		t.Fatalf("second heartbeat = %d", second.Code)
	}
	if room.LastHeartbeatRTT(player.ID) <= 0 {
		t.Fatal("second heartbeat did not sample RTT")
	}

	unknown := lobbyRequest(t, handler, http.MethodPost, "/lobby/heartbeat", nil, nil)
	if unknown.Code != http.StatusNoContent {
		t.Fatalf("cookie-less heartbeat = %d", unknown.Code)
	}
}

func TestSilentHeartbeatDoesNotPublish(t *testing.T) {
	t.Parallel()
	_, handler, _ := testLobby(t)
	cookie := joinNamed(t, handler, "Ada", "correct horse")

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/lobby/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	events := bufio.NewReader(resp.Body)
	readSSEEvent(t, events, ": connected")

	if beat := lobbyRequest(t, handler, http.MethodPost, "/lobby/heartbeat", nil, cookie); beat.Code != http.StatusNoContent {
		t.Fatalf("silent beat = %d", beat.Code)
	}
	got := make(chan string, 1)
	go func() {
		var b strings.Builder
		for {
			line, err := events.ReadString('\n')
			if err != nil {
				return
			}
			b.WriteString(line)
			if line == "\n" {
				got <- b.String()
				return
			}
		}
	}()
	select {
	case ev := <-got:
		t.Fatalf("silent heartbeat published %q", ev)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestLivenessSettingsPersist(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, handler, _ := openTestLobby(t, dir, nil)

	save := lobbyRequest(t, handler, http.MethodPost, "/settings/disconnect-after", url.Values{"disconnect_after": {"3"}}, operatorCookie())
	if save.Code != http.StatusSeeOther {
		t.Fatalf("disconnect-after = %d %q", save.Code, save.Body.String())
	}
	lobbyRequest(t, handler, http.MethodPost, "/settings/kick-timeout", url.Values{"kick_timeout": {"12"}}, operatorCookie())
	lobbyRequest(t, handler, http.MethodPost, "/settings/protect-host", url.Values{"enabled": {"0"}}, operatorCookie())
	lobbyRequest(t, handler, http.MethodPost, "/settings/seat-disconnected-waiters", url.Values{"enabled": {"1"}}, operatorCookie())
	lobbyRequest(t, handler, http.MethodPost, "/settings/auto-start", url.Values{"enabled": {"1"}, "return": {"/settings"}}, operatorCookie())
	lobbyRequest(t, handler, http.MethodPost, "/settings/reset-ready", url.Values{"reset_ready": {"every"}}, operatorCookie())

	page := lobbyRequest(t, handler, http.MethodGet, "/settings", nil, operatorCookie()).Body.String()
	if !strings.Contains(page, `value="3"`) ||
		!strings.Contains(page, `value="12"`) ||
		!strings.Contains(page, "Protect host") ||
		!strings.Contains(page, "Seat disconnected waiters") ||
		!strings.Contains(page, "Reset Player Ready State") ||
		!strings.Contains(page, `value="every"`) {
		t.Fatalf("settings page = %q", page)
	}

	_, handler, _ = openTestLobby(t, dir, nil)
	again := lobbyRequest(t, handler, http.MethodGet, "/settings", nil, operatorCookie()).Body.String()
	if !strings.Contains(again, `value="3"`) || !strings.Contains(again, `value="12"`) || !strings.Contains(again, `value="every" selected`) {
		t.Fatalf("persisted settings = %q", again)
	}
}

func TestAutoStartOnlyWhenSeatedReadyAndConnected(t *testing.T) {
	t.Parallel()
	var starts atomic.Int32
	_, handler, room := testLobbyReady(t, func(ctx context.Context) error {
		starts.Add(1)
		return nil
	})
	host := joinNamed(t, handler, "Host", "correct horse")
	lobbyRequest(t, handler, http.MethodPost, "/settings/open", nil, operatorCookie())
	guest := joinNamed(t, handler, "Bea", "")
	lobbyRequest(t, handler, http.MethodPost, "/settings/auto-start", url.Values{"enabled": {"1"}}, operatorCookie())

	lobbyRequest(t, handler, http.MethodPost, "/lobby/ready", nil, host)
	if starts.Load() != 0 {
		t.Fatal("auto-start fired with one unready seat")
	}
	if err := room.SetConnected(context.Background(), playerFromCookie(t, room, guest).ID, false); err != nil {
		t.Fatal(err)
	}
	lobbyRequest(t, handler, http.MethodPost, "/lobby/ready", nil, guest)
	if starts.Load() != 0 {
		t.Fatal("auto-start fired with a dim ready seat")
	}
	lobbyRequest(t, handler, http.MethodPost, "/lobby/ready", nil, guest)
	if err := room.SetConnected(context.Background(), playerFromCookie(t, room, guest).ID, true); err != nil {
		t.Fatal(err)
	}
	lobbyRequest(t, handler, http.MethodPost, "/lobby/ready", nil, guest)
	if starts.Load() != 1 {
		t.Fatalf("auto-start starts = %d, want 1", starts.Load())
	}
}

func TestKickClockAfterStartupDebounceAndProtectHost(t *testing.T) {
	t.Parallel()
	clk := &testClock{now: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	_, handler, room := openTestLobby(t, t.TempDir(), func(c *lobby.Config) {
		c.Clock = clk.Now
	})
	host := joinNamed(t, handler, "Host", "correct horse")
	wait := joinWithoutBeat(t, handler, "Wait", "")
	lobbyRequest(t, handler, http.MethodPost, "/settings/open", nil, operatorCookie())
	guest := joinNamed(t, handler, "Bea", "")
	lobbyRequest(t, handler, http.MethodPost, "/settings/kick-timeout", url.Values{"kick_timeout": {"1"}}, operatorCookie())
	lobbyRequest(t, handler, http.MethodPost, "/settings/disconnect-after", url.Values{"disconnect_after": {"0"}}, operatorCookie())

	clk.advance(2 * time.Second)
	if err := room.TickLiveness(context.Background(), clk.Now()); err != nil {
		t.Fatal(err)
	}
	if playerFromCookie(t, room, guest).Disconnected == false {
		t.Fatal("guest was not dimmed after a miss")
	}
	if _, ok := findPlayer(t, room, wait); !ok {
		t.Fatal("disconnected waiter was auto-kicked")
	}

	clk.advance(3 * time.Second)
	if err := room.TickLiveness(context.Background(), clk.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := findPlayer(t, room, guest); !ok {
		t.Fatal("guest was kicked before kick timeout after debounce")
	}

	clk.advance(time.Second)
	if err := room.TickLiveness(context.Background(), clk.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := findPlayer(t, room, guest); ok {
		t.Fatal("guest was not auto-kicked after debounce and timeout")
	}
	if _, ok := findPlayer(t, room, host); !ok {
		t.Fatal("protect-host dropped the claimed host")
	}
	if _, ok := findPlayer(t, room, wait); !ok {
		t.Fatal("waiter was auto-kicked")
	}
}

func TestKickDoesNotFireDuringRound(t *testing.T) {
	t.Parallel()
	clk := &testClock{now: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)}
	_, handler, room := openTestLobby(t, t.TempDir(), func(c *lobby.Config) {
		c.Clock = clk.Now
	})
	joinNamed(t, handler, "Host", "correct horse")
	lobbyRequest(t, handler, http.MethodPost, "/settings/open", nil, operatorCookie())
	guest := joinNamed(t, handler, "Bea", "")
	lobbyRequest(t, handler, http.MethodPost, "/settings/kick-timeout", url.Values{"kick_timeout": {"1"}}, operatorCookie())
	lobbyRequest(t, handler, http.MethodPost, "/settings/disconnect-after", url.Values{"disconnect_after": {"0"}}, operatorCookie())
	if err := room.SetRoundActive(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	clk.advance(lobby.StartupDebounce + 3*time.Second)
	if err := room.TickLiveness(context.Background(), clk.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := findPlayer(t, room, guest); !ok {
		t.Fatal("auto-kick fired during a round")
	}
}

func TestResetReadyOptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mode   string
		reason string
		want   bool
	}{
		{mode: lobby.ResetReadySwitch, reason: lobby.ReadyResetStop, want: true},
		{mode: lobby.ResetReadySwitch, reason: lobby.ReadyResetSwitch, want: false},
		{mode: lobby.ResetReadyEvery, reason: lobby.ReadyResetStop, want: false},
		{mode: lobby.ResetReadyNever, reason: lobby.ReadyResetSwitch, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.mode+"/"+tt.reason, func(t *testing.T) {
			t.Parallel()
			_, handler, room := testLobbyReady(t, nil)
			host := joinNamed(t, handler, "Host", "correct horse")
			lobbyRequest(t, handler, http.MethodPost, "/lobby/ready", nil, host)
			lobbyRequest(t, handler, http.MethodPost, "/settings/reset-ready", url.Values{"reset_ready": {tt.mode}}, operatorCookie())
			if err := room.ApplyReadyReset(context.Background(), tt.reason); err != nil {
				t.Fatal(err)
			}
			if got := playerFromCookie(t, room, host).Ready; got != tt.want {
				t.Fatalf("ready = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSeatDisconnectedWaitersDefaultOff(t *testing.T) {
	t.Parallel()
	_, handler, room := testLobby(t)
	joinNamed(t, handler, "Host", "correct horse")
	waiter := joinWithoutBeat(t, handler, "Wait", "")
	lobbyRequest(t, handler, http.MethodPost, "/settings/open", nil, operatorCookie())
	if playerFromCookie(t, room, waiter).Seated {
		t.Fatal("disconnected waiter was seated")
	}
	lobbyRequest(t, handler, http.MethodPost, "/settings/seat-disconnected-waiters", url.Values{"enabled": {"1"}}, operatorCookie())
	lobbyRequest(t, handler, http.MethodPost, "/settings/open", nil, operatorCookie())
	if !playerFromCookie(t, room, waiter).Seated {
		t.Fatal("seat-disconnected-waiters did not sit the waiter")
	}
}

func testLobbyReady(t *testing.T, start func(context.Context) error) (*store.DB, http.Handler, *lobby.Lobby) {
	t.Helper()
	return openTestLobby(t, t.TempDir(), func(c *lobby.Config) {
		c.PhoneExtras = func(*http.Request, lobby.Player) lobby.PhoneExtras {
			return lobby.PhoneExtras{LoadedGameID: "fake"}
		}
		c.StartRound = start
	})
}

func openTestLobby(t *testing.T, dir string, wrap func(*lobby.Config)) (*store.DB, http.Handler, *lobby.Lobby) {
	t.Helper()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if ok, err := db.HasAdminHash(context.Background()); err != nil {
		t.Fatal(err)
	} else if !ok {
		if err := db.FinishSetup(context.Background(), "stored-hash", "operator-session"); err != nil {
			t.Fatal(err)
		}
	}
	cfg := lobby.Config{
		AdminCookieName: "hackbox_admin",
		Events:          hub.New(),
		JoinURL: func(*http.Request) string {
			return "http://192.168.10.24:8654/"
		},
		PasswordMatches: func(hash, password string) bool {
			return hash == "stored-hash" && password == "correct horse"
		},
		SecureCookie: func(*http.Request) bool { return false },
	}
	if wrap != nil {
		wrap(&cfg)
	}
	room, err := lobby.New(db, cfg)
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

func joinWithoutBeat(t *testing.T, handler http.Handler, name, password string) *http.Cookie {
	t.Helper()
	form := url.Values{"display_name": {name}}
	if password != "" {
		form.Set("admin_password", password)
	}
	rec := lobbyRequest(t, handler, http.MethodPost, "/lobby/join", form, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("Join %s status = %d; body = %q", name, rec.Code, rec.Body.String())
	}
	return cookieNamed(t, rec, lobby.PlayerCookieName)
}

type testClock struct {
	now time.Time
}

func (c *testClock) Now() time.Time { return c.now }

func (c *testClock) advance(d time.Duration) { c.now = c.now.Add(d) }
