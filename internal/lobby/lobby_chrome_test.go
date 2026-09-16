package lobby_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/KroniK907/hackbox/internal/lobby"
	"github.com/KroniK907/hackbox/internal/ui"
)

func TestNeonCabinetBoardAndPhones(t *testing.T) {
	t.Parallel()
	_, handler, room := testLobby(t)

	join := lobbyRequest(t, handler, http.MethodGet, "/", nil, nil)
	body := join.Body.String()
	if !strings.Contains(body, `data-theme="neon-light"`) {
		t.Fatalf("join default theme = %q", body)
	}
	if !strings.Contains(body, `/static/live.css?v=`+ui.AssetVersion) {
		t.Fatalf("join CSS URL is not versioned: %q", body)
	}
	if !strings.Contains(body, "Reroll face") || !strings.Contains(body, `name="avatar_seed"`) {
		t.Fatalf("join missing avatar reroll: %q", body)
	}
	if !strings.Contains(body, `class="ui-token ui-token-self"`) {
		t.Fatalf("join missing avatar frame: %q", body)
	}
	seed := hiddenValue(t, body, "avatar_seed")
	if seed == "" {
		t.Fatal("join did not mint an avatar seed")
	}

	reroll := lobbyRequest(
		t,
		handler,
		http.MethodPost,
		"/lobby/reroll",
		url.Values{"display_name": {"Maya"}, "avatar_seed": {seed}},
		nil,
	)
	if reroll.Code != http.StatusSeeOther {
		t.Fatalf("reroll status = %d, want %d; body = %q", reroll.Code, http.StatusSeeOther, reroll.Body.String())
	}
	rerollCookie := cookieNamed(t, reroll, lobby.PlayerCookieName)
	after := lobbyRequest(t, handler, http.MethodGet, "/", nil, rerollCookie)
	newSeed := hiddenValue(t, after.Body.String(), "avatar_seed")
	if newSeed == "" || newSeed == seed {
		t.Fatalf("reroll seed = %q, old = %q", newSeed, seed)
	}

	joined := lobbyRequest(
		t,
		handler,
		http.MethodPost,
		"/lobby/join",
		url.Values{
			"display_name":   {"Maya"},
			"admin_password": {"correct horse"},
			"avatar_seed":    {newSeed},
		},
		rerollCookie,
	)
	if joined.Code != http.StatusSeeOther {
		t.Fatalf("Join status = %d; body = %q", joined.Code, joined.Body.String())
	}
	player := playerFromCookie(t, room, cookieNamed(t, joined, lobby.PlayerCookieName))
	if player.AvatarSeed != newSeed {
		t.Fatalf("stored seed = %q, want %q", player.AvatarSeed, newSeed)
	}

	board := lobbyRequest(t, handler, http.MethodGet, "/board", nil, nil).Body.String()
	if !strings.Contains(board, `data-theme="neon-light"`) ||
		!strings.Contains(board, `class="ui-rail"`) ||
		!strings.Contains(board, `class="ui-cluster"`) ||
		!strings.Contains(board, `class="ui-marquee"`) ||
		!strings.Contains(board, "CLOSED") ||
		!strings.Contains(board, "Audience Members") ||
		strings.Contains(board, "IN THE ROOM") ||
		strings.Contains(board, "empty chair") ||
		strings.Contains(board, "<h2>Audience</h2>") {
		t.Fatalf("board chrome = %q", board)
	}
	if !strings.Contains(board, string(ui.AvatarSVG(newSeed))) {
		t.Fatal("board token did not render the stored avatar seed")
	}
	if !strings.Contains(board, `class="ui-qr"`) || !strings.Contains(board, "<svg") {
		t.Fatalf("board missing join QR: %q", board)
	}

	seated := lobbyRequest(t, handler, http.MethodGet, "/", nil, cookieNamed(t, joined, lobby.PlayerCookieName)).Body.String()
	if strings.Contains(seated, `class="ui-plunger"`) ||
		strings.Contains(seated, `action="/lobby/ready"`) ||
		!strings.Contains(seated, "Reroll face") ||
		!strings.Contains(seated, `class="ui-token ui-token-self"`) {
		t.Fatalf("seated phone = %q", seated)
	}
	before := playerFromCookie(t, room, cookieNamed(t, joined, lobby.PlayerCookieName))
	rerolledSeat := lobbyRequest(t, handler, http.MethodPost, "/lobby/reroll", nil, cookieNamed(t, joined, lobby.PlayerCookieName))
	if rerolledSeat.Code != http.StatusSeeOther {
		t.Fatalf("seated reroll status = %d; body = %q", rerolledSeat.Code, rerolledSeat.Body.String())
	}
	afterSeat := playerFromCookie(t, room, cookieNamed(t, rerolledSeat, lobby.PlayerCookieName))
	if afterSeat.AvatarSeed == "" || afterSeat.AvatarSeed == before.AvatarSeed {
		t.Fatalf("seated reroll seed = %q, old = %q", afterSeat.AvatarSeed, before.AvatarSeed)
	}

	guest := joinNamed(t, handler, "Theo", "")
	waiting := lobbyRequest(t, handler, http.MethodGet, "/", nil, guest).Body.String()
	if strings.Contains(waiting, "ui-plunger") || strings.Contains(waiting, "READY") ||
		!strings.Contains(waiting, "Reroll face") {
		t.Fatalf("waiting phone included Ready or missed reroll: %q", waiting)
	}
}

func TestSettingsThemeToggle(t *testing.T) {
	t.Parallel()
	_, handler, _ := testLobby(t)

	settings := lobbyRequest(t, handler, http.MethodGet, "/settings", nil, operatorCookie()).Body.String()
	if !strings.Contains(settings, `data-theme="neon-light"`) || !strings.Contains(settings, ">Light</button>") {
		t.Fatalf("settings default theme = %q", settings)
	}
	if !strings.Contains(settings, `id="logout-modal" class="ui-modal" hidden`) {
		t.Fatalf("logout modal is not hidden by default: %q", settings)
	}
	if !strings.Contains(settings, ">Closed</button>") || strings.Contains(settings, ">Open</button>") {
		t.Fatalf("closed room toggle = %q", settings)
	}
	if !strings.Contains(settings, `class="ui-page ui-page-compact"`) {
		t.Fatalf("settings is not compact: %q", settings)
	}
	if !strings.Contains(settings, `class="ui-caret"`) || !strings.Contains(settings, `class="ui-accordion-body"`) {
		t.Fatalf("kick accordion chrome missing: %q", settings)
	}

	login := lobbyRequest(t, handler, http.MethodGet, "/settings", nil, nil).Body.String()
	if !strings.Contains(login, ">Log in</button>") || strings.Contains(login, `action="/settings/open"`) {
		t.Fatalf("logged-out settings = %q", login)
	}

	denied := lobbyRequest(t, handler, http.MethodPost, "/settings/theme", nil, nil)
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("theme without admin status = %d, want %d", denied.Code, http.StatusUnauthorized)
	}

	toggled := lobbyRequest(t, handler, http.MethodPost, "/settings/theme", nil, operatorCookie())
	if toggled.Code != http.StatusSeeOther {
		t.Fatalf("theme toggle status = %d; body = %q", toggled.Code, toggled.Body.String())
	}
	darkSettings := lobbyRequest(t, handler, http.MethodGet, "/settings", nil, operatorCookie()).Body.String()
	if !strings.Contains(darkSettings, `data-theme="neon-dark"`) || !strings.Contains(darkSettings, ">Dark</button>") {
		t.Fatalf("settings after toggle = %q", darkSettings)
	}
	board := lobbyRequest(t, handler, http.MethodGet, "/board", nil, nil).Body.String()
	if !strings.Contains(board, `data-theme="neon-dark"`) {
		t.Fatalf("board did not pick up dark theme: %q", board)
	}

	live := httptest.NewRequest(http.MethodPost, "http://hackbox.test/settings/theme", nil)
	live.Header.Set("HX-Request", "true")
	live.AddCookie(operatorCookie())
	liveRec := httptest.NewRecorder()
	handler.ServeHTTP(liveRec, live)
	if liveRec.Code != http.StatusOK ||
		!strings.Contains(liveRec.Body.String(), `data-theme="neon-light"`) ||
		!strings.Contains(liveRec.Body.String(), ">Light</button>") {
		t.Fatalf("htmx theme toggle = %d %q", liveRec.Code, liveRec.Body.String())
	}
}

func hiddenValue(t *testing.T, body, name string) string {
	t.Helper()
	re := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]*)"`)
	match := re.FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("missing hidden %s in %q", name, body)
	}
	return match[1]
}
