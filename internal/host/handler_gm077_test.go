package host

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestGM077FinishSetupRedirectsSeatCapAndHostname(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{name: "setup redirects after hash exists", run: testGM077SetupRedirects},
		{name: "finish first wins with setup knobs", run: testGM077FinishAtomicSettings},
		{name: "blank hostname falls back to LAN join URL", run: testGM077BlankHostnameFallback},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.run(t)
		})
	}
}

func testGM077SetupRedirects(t *testing.T) {
	t.Helper()
	_, handler := testHandler(t)
	assertRedirect(t, handler, http.MethodGet, "/", nil, "/setup")
	page := request(t, handler, http.MethodGet, "/setup", nil, "")
	if page.Code != http.StatusOK {
		t.Fatalf("GET /setup = %d", page.Code)
	}
	body := page.Body.String()
	if !strings.Contains(body, "NEW GAME") ||
		!strings.Contains(body, "Advertised hostname") ||
		!strings.Contains(body, "Seat cap") ||
		!strings.Contains(body, "After a game") ||
		!strings.Contains(body, "Log to stdout") ||
		!strings.Contains(body, ">Finish</button>") {
		t.Fatalf("setup chrome = %q", body)
	}
	form := url.Values{"password": {"correct horse"}, "confirm": {"correct horse"}, "seat_cap": {"8"}}
	request(t, handler, http.MethodPost, "/setup", form, "")
	assertRedirect(t, handler, http.MethodGet, "/setup", nil, "/board")
}

func testGM077FinishAtomicSettings(t *testing.T) {
	t.Helper()
	db, handler := testHandler(t)
	form := url.Values{
		"password":   {"correct horse"},
		"confirm":    {"correct horse"},
		"hostname":   {"booth.lan"},
		"seat_cap":   {"12"},
		"cycle_mode": {"cycle"},
		"log_stdout": {"on"},
	}
	rec := request(t, handler, http.MethodPost, "/setup", form, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("Finish status = %d %q", rec.Code, rec.Body.String())
	}
	var hostname string
	var cap, cycle, stdout int
	if err := db.SQL().QueryRowContext(
		context.Background(),
		`SELECT advertised_hostname, seat_cap, cycle_seats, log_stdout FROM room_state WHERE id = 1`,
	).Scan(&hostname, &cap, &cycle, &stdout); err != nil {
		t.Fatal(err)
	}
	if hostname != "booth.lan" || cap != 12 || cycle == 0 || stdout == 0 {
		t.Fatalf("stored setup knobs hostname=%q cap=%d cycle=%d stdout=%d", hostname, cap, cycle, stdout)
	}
	form.Set("hostname", "other.lan")
	assertStatus(t, handler, http.MethodPost, "/setup", form, http.StatusConflict)
	if err := db.SQL().QueryRowContext(
		context.Background(),
		`SELECT advertised_hostname FROM room_state WHERE id = 1`,
	).Scan(&hostname); err != nil {
		t.Fatal(err)
	}
	if hostname != "booth.lan" {
		t.Fatalf("second Finish changed hostname to %q", hostname)
	}
}

func testGM077BlankHostnameFallback(t *testing.T) {
	t.Helper()
	_, handler := testHandler(t)
	form := url.Values{"password": {"correct horse"}, "confirm": {"correct horse"}, "hostname": {"  "}}
	request(t, handler, http.MethodPost, "/setup", form, "localhost:8654")
	board := request(t, handler, http.MethodGet, "/board", nil, "localhost:8654")
	if !strings.Contains(board.Body.String(), "http://192.168.10.24:8654/") {
		t.Fatalf("blank hostname board = %q", board.Body.String())
	}
}
