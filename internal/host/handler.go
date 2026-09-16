package host

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/KroniK907/hackbox/internal/games"
	"github.com/KroniK907/hackbox/internal/lobby"
	"github.com/KroniK907/hackbox/internal/platform/hub"
	"github.com/KroniK907/hackbox/internal/store"
	"github.com/KroniK907/hackbox/internal/ui"
)

const adminCookieName = "hackbox_admin"

//go:embed templates/*.html
var templateFiles embed.FS

var pageTemplates = ui.MustParse(templateFiles, "templates/*.html")

// NewHandler returns the host routes wrapped in Go's cross-origin protection.
// lanJoinURL is the fallback join address shown on /board when the request
// host is loopback. It may be empty when no usable LAN IPv4 exists. A public
// hostname such as a Cloudflare tunnel replaces that fallback.
func NewHandler(db *store.DB, lanJoinURL string) (http.Handler, error) {
	handler, _, err := newHandler(db, lanJoinURL, games.Catalog())
	return handler, err
}

func newHandler(db *store.DB, lanJoinURL string, catalog []games.Factory) (http.Handler, *runtime, error) {
	events := hub.New()
	rt := newRuntime(db, events, catalog)
	room, err := lobby.New(db, lobby.Config{
		AdminCookieName: adminCookieName,
		Events:          events,
		JoinURL: func(r *http.Request) string {
			return joinURLForRequest(r, lanJoinURL)
		},
		PasswordMatches: passwordMatches,
		SecureCookie:    secureAdminCookie,
		Notice:          rt.log.Write,
		LogSinksChanged: rt.log.SetSinks,
		SettingsExtras:  rt.extras,
		PhoneExtras:     rt.phoneExtras,
		StartRound:      rt.start,
		AfterDisconnect: rt.afterDisconnect,
	})
	if err != nil {
		return nil, nil, err
	}
	rt.room = room
	room.StartLiveness()
	row, err := readRoomSettingsFlags(context.Background(), db)
	if err != nil {
		return nil, nil, err
	}
	rt.log.SetSinks(row.stdout, row.file)
	rt.restore(context.Background())

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", ui.StaticHandler()))
	mux.HandleFunc("GET /setup", getSetup(db, lanJoinURL))
	mux.HandleFunc("POST /setup", finishSetup(db, lanJoinURL, rt))
	mux.HandleFunc("POST /setup/theme", setupTheme(db, lanJoinURL))
	protected := http.NewServeMux()
	protected.HandleFunc("GET /{$}", rt.phone)
	protected.HandleFunc("GET /board", func(w http.ResponseWriter, r *http.Request) {
		rt.board(w, r, joinURLForRequest(r, lanJoinURL))
	})
	room.Register(protected)
	rt.registerGameRoutes(protected)
	mux.Handle("/", requireSetup(db, protected))
	return http.NewCrossOriginProtection().Handler(accessLog(mux, rt.log.Write)), rt, nil
}

func readRoomSettingsFlags(ctx context.Context, db *store.DB) (struct{ stdout, file bool }, error) {
	var stdout, file int
	err := db.SQL().QueryRowContext(ctx, `SELECT log_stdout, log_file FROM room_state WHERE id = 1`).Scan(&stdout, &file)
	if err != nil {
		return struct{ stdout, file bool }{}, err
	}
	return struct{ stdout, file bool }{stdout: stdout != 0, file: file != 0}, nil
}

func getSetup(db *store.DB, lanJoinURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hasHash, err := db.HasAdminHash(r.Context())
		if err != nil {
			http.Error(w, "Could not read setup state.", http.StatusInternalServerError)
			return
		}
		if hasHash {
			http.Redirect(w, r, "/board", http.StatusSeeOther)
			return
		}
		writeSetupPage(w, r, db, lanJoinURL, "", http.StatusOK)
	}
}

func finishSetup(db *store.DB, lanJoinURL string, rt *runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hasHash, err := db.HasAdminHash(r.Context())
		if err != nil {
			http.Error(w, "Could not read setup state.", http.StatusInternalServerError)
			return
		}
		if hasHash {
			http.Error(w, "Setup is already finished.", http.StatusConflict)
			return
		}
		if err := r.ParseForm(); err != nil {
			writeSetupPage(w, r, db, lanJoinURL, "Could not read the form.", http.StatusBadRequest)
			return
		}
		password := r.PostFormValue("password")
		switch {
		case password != r.PostFormValue("confirm"):
			writeSetupPage(w, r, db, lanJoinURL, "Passwords do not match.", http.StatusBadRequest)
			return
		case len(password) < 8:
			writeSetupPage(w, r, db, lanJoinURL, "Password must be at least 8 characters.", http.StatusBadRequest)
			return
		}

		hash, err := hashPassword(password)
		if err != nil {
			http.Error(w, "Could not finish setup.", http.StatusInternalServerError)
			return
		}
		sessionID, err := newSessionID()
		if err != nil {
			http.Error(w, "Could not finish setup.", http.StatusInternalServerError)
			return
		}
		settings, err := setupSettingsFromForm(r)
		if err != nil {
			writeSetupPage(w, r, db, lanJoinURL, err.Error(), http.StatusBadRequest)
			return
		}
		theme, err := readStoredTheme(r.Context(), db)
		if err != nil {
			http.Error(w, "Could not finish setup.", http.StatusInternalServerError)
			return
		}
		settings.Theme = theme
		if err := db.FinishSetupWith(r.Context(), hash, sessionID, func(tx *sql.Tx) error {
			return lobby.WriteSetupSettings(r.Context(), tx, settings)
		}); err != nil {
			if errors.Is(err, store.ErrAdminHashExists) {
				http.Error(w, "Setup is already finished.", http.StatusConflict)
				return
			}
			http.Error(w, "Could not finish setup.", http.StatusInternalServerError)
			return
		}

		http.SetCookie(w, &http.Cookie{
			Name:     adminCookieName,
			Value:    sessionID,
			Path:     "/",
			MaxAge:   30 * 24 * 60 * 60,
			HttpOnly: true,
			Secure:   secureAdminCookie(r),
			SameSite: http.SameSiteLaxMode,
		})
		if rt != nil {
			rt.log.Write("Finish")
			rt.log.SetSinks(settings.LogStdout, settings.LogFile)
		}
		http.Redirect(w, r, "/board", http.StatusSeeOther)
	}
}

func setupTheme(db *store.DB, lanJoinURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hasHash, err := db.HasAdminHash(r.Context())
		if err != nil {
			http.Error(w, "Could not read setup state.", http.StatusInternalServerError)
			return
		}
		if hasHash {
			http.Redirect(w, r, "/board", http.StatusSeeOther)
			return
		}
		current, err := readStoredTheme(r.Context(), db)
		if err != nil {
			http.Error(w, "Could not read the theme.", http.StatusInternalServerError)
			return
		}
		next := ui.ThemeNeonDark
		if current == ui.ThemeNeonDark {
			next = ui.ThemeNeonLight
		}
		if _, err := db.SQL().ExecContext(r.Context(), `UPDATE room_state SET theme = ? WHERE id = 1`, next); err != nil {
			http.Error(w, "Could not save the theme.", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
	}
}

func requireSetup(db *store.DB, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hasHash, err := db.HasAdminHash(r.Context())
		if err != nil {
			http.Error(w, "Could not read setup state.", http.StatusInternalServerError)
			return
		}
		if !hasHash {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeSetupPage(w http.ResponseWriter, r *http.Request, db *store.DB, lanJoinURL, message string, status int) {
	page, err := loadSetupPage(r.Context(), db, lanJoinURL, message)
	if err != nil {
		http.Error(w, "Could not read setup state.", http.StatusInternalServerError)
		return
	}
	renderPage(w, "setup.html", page, status)
}

type setupPage struct {
	ui.Chrome
	Error              string
	AdvertisedHostname string
	HostnameHint       string
	SeatCap            int
	CycleSeats         bool
	LogStdout          bool
	LogFile            bool
}

func loadSetupPage(ctx context.Context, db *store.DB, lanJoinURL, message string) (setupPage, error) {
	var theme, hostname string
	var seatCap, cycle, stdout, file int
	err := db.SQL().QueryRowContext(
		ctx,
		`SELECT theme, advertised_hostname, seat_cap, cycle_seats, log_stdout, log_file
		 FROM room_state WHERE id = 1`,
	).Scan(&theme, &hostname, &seatCap, &cycle, &stdout, &file)
	if err != nil {
		return setupPage{}, err
	}
	if seatCap == 0 {
		seatCap = lobby.DefaultSeatCap
	}
	return setupPage{
		Chrome:             ui.Chrome{Title: "Set up Hackbox", Theme: ui.NormalizeTheme(theme)},
		Error:              message,
		AdvertisedHostname: hostname,
		HostnameHint:       strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(lanJoinURL, "http://"), "https://"), "/"),
		SeatCap:            seatCap,
		CycleSeats:         cycle != 0,
		LogStdout:          stdout != 0,
		LogFile:            file != 0,
	}, nil
}

func setupSettingsFromForm(r *http.Request) (lobby.SetupSettings, error) {
	cap := lobby.DefaultSeatCap
	if raw := strings.TrimSpace(r.PostFormValue("seat_cap")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 64 {
			return lobby.SetupSettings{}, fmt.Errorf("Seat cap must be 1 to 64.")
		}
		cap = n
	}
	return lobby.SetupSettings{
		AdvertisedHostname: r.PostFormValue("hostname"),
		SeatCap:            cap,
		CycleSeats:         r.PostFormValue("cycle_mode") == "cycle",
		LogStdout:          r.PostFormValue("log_stdout") == "on",
		LogFile:            r.PostFormValue("log_file") == "on",
	}, nil
}

func readStoredTheme(ctx context.Context, db *store.DB) (string, error) {
	var theme string
	if err := db.SQL().QueryRowContext(ctx, `SELECT theme FROM room_state WHERE id = 1`).Scan(&theme); err != nil {
		return "", err
	}
	return ui.NormalizeTheme(theme), nil
}

func renderPage(w http.ResponseWriter, name string, data any, status int) {
	var body bytes.Buffer
	if err := pageTemplates.ExecuteTemplate(&body, name, data); err != nil {
		http.Error(w, "Could not render the page.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = body.WriteTo(w)
}

func secureAdminCookie(r *http.Request) bool {
	if requestScheme(r) == "https" {
		return true
	}
	return isLoopbackHostname(requestHostname(r))
}

func joinURLForRequest(r *http.Request, fallback string) string {
	if isLoopbackHostname(requestHostname(r)) {
		return fallback
	}
	if r.Host == "" {
		return fallback
	}
	return requestScheme(r) + "://" + r.Host + "/"
}

func requestHostname(r *http.Request) string {
	host := r.Host
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	return strings.Trim(host, "[]")
}

func isLoopbackHostname(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	proto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])
	if strings.EqualFold(proto, "https") {
		return "https"
	}
	return "http"
}
