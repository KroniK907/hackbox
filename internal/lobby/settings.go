package lobby

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/KroniK907/hackbox/internal/ui"
)

var (
	errSeatCapTooLow     = errors.New("lobby: seat cap is below the seated count")
	errMakeHostForbidden = errors.New("lobby: make host is not allowed while the host phone is live")
	errTakeHostDenied    = errors.New("lobby: take host is not pending on this phone")
	errBumpRequired      = errors.New("lobby: pick a seated player to bump")
	errHostBusy          = errors.New("lobby: host sit or stand is queued until the game ends")
)

// SetupSettings are the /setup form knobs written in the Finish transaction.
type SetupSettings struct {
	Theme              string
	AdvertisedHostname string
	SeatCap            int
	CycleSeats         bool
	LogStdout          bool
	LogFile            bool
}

// WriteSetupSettings stores Finish knobs on room_state inside an open tx.
func WriteSetupSettings(ctx context.Context, tx *sql.Tx, settings SetupSettings) error {
	cap := settings.SeatCap
	if cap == 0 {
		cap = DefaultSeatCap
	}
	if cap < minSeatCap || cap > maxSeatCap {
		return fmt.Errorf("lobby: seat cap must be %d to %d", minSeatCap, maxSeatCap)
	}
	_, err := tx.ExecContext(
		ctx,
		`UPDATE room_state SET
			theme = ?,
			advertised_hostname = ?,
			seat_cap = ?,
			cycle_seats = ?,
			log_stdout = ?,
			log_file = ?
		 WHERE id = 1`,
		ui.NormalizeTheme(settings.Theme),
		strings.TrimSpace(settings.AdvertisedHostname),
		cap,
		boolToInt(settings.CycleSeats),
		boolToInt(settings.LogStdout),
		boolToInt(settings.LogFile),
	)
	if err != nil {
		return fmt.Errorf("lobby: write setup settings: %w", err)
	}
	return nil
}

func (l *Lobby) phoneView(r *http.Request, player Player) (roomView, error) {
	chrome, err := l.chrome(r.Context(), "Hackbox room")
	if err != nil {
		return roomView{}, err
	}
	view := roomView{Chrome: chrome, Player: player, TakeHost: player.PendingDesignation && !player.ClaimedHost}
	if l.phoneExtras != nil {
		extra := l.phoneExtras(r, player)
		view.ShowStart = extra.ShowStart
		view.ShowReady = extra.LoadedGameID != "" && !extra.Started && player.Seated
		view.Started = extra.Started
		view.AutoStart = extra.AutoStart
		view.GameIDs = extra.GameIDs
		view.LoadedGameID = extra.LoadedGameID
	}
	if !player.ClaimedHost || !l.hasAdminCookie(r) {
		return view, nil
	}
	view.HostPanel = true
	count, err := seatedCountDB(r.Context(), l.sql)
	if err != nil {
		return roomView{}, err
	}
	cap, err := effectiveSeatCap(r.Context(), l.sql)
	if err != nil {
		return roomView{}, err
	}
	view.TableFull = count >= cap && !player.Seated
	if view.TableFull {
		seated, err := l.listPlayers(r.Context(), `seated = 1 ORDER BY rowid`)
		if err != nil {
			return roomView{}, err
		}
		for _, p := range seated {
			if p.ID != player.ID {
				view.BumpCandidates = append(view.BumpCandidates, p)
			}
		}
	}
	players, err := l.listPlayers(r.Context(), `1 = 1 ORDER BY rowid`)
	if err != nil {
		return roomView{}, err
	}
	view.Players = players
	return view, nil
}

// WritePlayPhone wraps a running game body with Lobby gear and the host drawer.
func (l *Lobby) WritePlayPhone(w http.ResponseWriter, r *http.Request, body template.HTML) {
	player, ok, err := l.PlayerFromRequest(r)
	if err != nil || !ok {
		http.Error(w, "Could not read the room.", http.StatusInternalServerError)
		return
	}
	view, err := l.phoneView(r, player)
	if err != nil {
		http.Error(w, "Could not read the room.", http.StatusInternalServerError)
		return
	}
	view.GameBody = body
	l.render(w, "play-phone.html", view, http.StatusOK)
}

func (l *Lobby) settings(w http.ResponseWriter, r *http.Request) {
	if !l.hasAdminCookie(r) {
		chrome, err := l.chrome(r.Context(), "Hackbox settings")
		if err != nil {
			http.Error(w, "Could not read the room.", http.StatusInternalServerError)
			return
		}
		l.render(w, "settings-login.html", settingsData{Chrome: chrome}, http.StatusOK)
		return
	}
	data, err := l.settingsView(r.Context(), "")
	if err != nil {
		http.Error(w, "Could not read the room.", http.StatusInternalServerError)
		return
	}
	l.render(w, "settings.html", data, http.StatusOK)
}

func (l *Lobby) settingsLog(w http.ResponseWriter, r *http.Request) {
	if !l.requireAdmin(w, r) {
		return
	}
	chrome, err := l.chrome(r.Context(), "Game log")
	if err != nil {
		http.Error(w, "Could not read the room.", http.StatusInternalServerError)
		return
	}
	page := logPage{Chrome: chrome}
	if l.settingsExtras != nil {
		page.Lines = l.settingsExtras(r.Context()).LogLines
	}
	l.render(w, "settings-log.html", page, http.StatusOK)
}

func (l *Lobby) settingsLogTail(w http.ResponseWriter, r *http.Request) {
	if !l.requireAdmin(w, r) {
		return
	}
	var lines []string
	if l.settingsExtras != nil {
		lines = l.settingsExtras(r.Context()).LogLines
	}
	l.render(w, "log-lines", logPage{Lines: lines}, http.StatusOK)
}

type logPage struct {
	ui.Chrome
	Lines []string
}

func (l *Lobby) login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		l.writeLogin(w, r, "Could not read the form.", http.StatusBadRequest)
		return
	}
	password := r.PostFormValue("password")
	hash, err := l.adminHash(r.Context())
	if err != nil {
		http.Error(w, "Could not read the password.", http.StatusInternalServerError)
		return
	}
	if !l.passwordMatches(hash, password) {
		l.emit("failed admin login")
		l.writeLogin(w, r, "Admin password is incorrect.", http.StatusUnauthorized)
		return
	}
	sessionID, err := newRandomValue(32)
	if err != nil {
		http.Error(w, "Could not log in.", http.StatusInternalServerError)
		return
	}
	if _, err := l.sql.ExecContext(
		r.Context(),
		`INSERT INTO admin_session (id, kind) VALUES (?, 'operator')`,
		sessionID,
	); err != nil {
		http.Error(w, "Could not log in.", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, l.cookie(r, l.adminCookieName, sessionID))
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (l *Lobby) logout(w http.ResponseWriter, r *http.Request) {
	if !l.requireAdmin(w, r) {
		return
	}
	cookie, _ := r.Cookie(l.adminCookieName)
	if _, err := l.sql.ExecContext(r.Context(), `DELETE FROM admin_session WHERE id = ?`, cookie.Value); err != nil {
		http.Error(w, "Could not log out.", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     l.adminCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   l.secureCookie(r),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (l *Lobby) setSeatCap(w http.ResponseWriter, r *http.Request) {
	if !l.requireAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read the form.", http.StatusBadRequest)
		return
	}
	cap, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("seat_cap")))
	if err != nil || cap < minSeatCap || cap > maxSeatCap {
		data, viewErr := l.settingsView(r.Context(), "Seat cap must be 1 to 64.")
		if viewErr != nil {
			http.Error(w, "Could not read the room.", http.StatusInternalServerError)
			return
		}
		l.render(w, "settings.html", data, http.StatusBadRequest)
		return
	}
	if err := l.writeSeatCap(r.Context(), cap); err != nil {
		if errors.Is(err, errSeatCapTooLow) {
			data, viewErr := l.settingsView(r.Context(), "")
			if viewErr != nil {
				http.Error(w, "Could not read the room.", http.StatusInternalServerError)
				return
			}
			seated, _ := seatedCountDB(r.Context(), l.sql)
			data.SeatCapError = fmt.Sprintf("Seat cap cannot go below the %d seated players.", seated)
			l.render(w, "settings.html", data, http.StatusBadRequest)
			return
		}
		http.Error(w, "Could not save the seat cap.", http.StatusInternalServerError)
		return
	}
	l.events.Publish("roster")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (l *Lobby) setHostname(w http.ResponseWriter, r *http.Request) {
	if !l.requireAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read the form.", http.StatusBadRequest)
		return
	}
	if err := l.writeHostname(r.Context(), r.PostFormValue("hostname")); err != nil {
		http.Error(w, "Could not save the hostname.", http.StatusInternalServerError)
		return
	}
	l.events.Publish("roster")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (l *Lobby) setCycleMode(w http.ResponseWriter, r *http.Request) {
	if !l.requireAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read the form.", http.StatusBadRequest)
		return
	}
	cycle := r.PostFormValue("cycle_mode") == "cycle"
	if _, err := l.sql.ExecContext(r.Context(), `UPDATE room_state SET cycle_seats = ? WHERE id = 1`, boolToInt(cycle)); err != nil {
		http.Error(w, "Could not save keep vs cycle.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (l *Lobby) cycleSeated(w http.ResponseWriter, r *http.Request) {
	if !l.requireAdmin(w, r) {
		return
	}
	if err := l.rotateSeated(r.Context()); err != nil {
		http.Error(w, "Could not cycle seated players.", http.StatusInternalServerError)
		return
	}
	l.events.Publish("roster")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (l *Lobby) setFillEmpty(w http.ResponseWriter, r *http.Request) {
	if !l.requireAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read the form.", http.StatusBadRequest)
		return
	}
	on := r.PostFormValue("fill_empty") == "1" || r.PostFormValue("fill_empty") == "on"
	if _, err := l.sql.ExecContext(r.Context(), `UPDATE room_state SET fill_empty = ? WHERE id = 1`, boolToInt(on)); err != nil {
		http.Error(w, "Could not save fill empty seats.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (l *Lobby) setLogStdout(w http.ResponseWriter, r *http.Request) {
	l.setLogFlag(w, r, "log_stdout")
}

func (l *Lobby) setLogFile(w http.ResponseWriter, r *http.Request) {
	l.setLogFlag(w, r, "log_file")
}

func (l *Lobby) setLogFlag(w http.ResponseWriter, r *http.Request, column string) {
	if !l.requireAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read the form.", http.StatusBadRequest)
		return
	}
	on := r.PostFormValue("enabled") == "1" || r.PostFormValue("enabled") == "on"
	if column != "log_stdout" && column != "log_file" {
		http.Error(w, "Unknown log flag.", http.StatusBadRequest)
		return
	}
	if _, err := l.sql.ExecContext(r.Context(), `UPDATE room_state SET `+column+` = ? WHERE id = 1`, boolToInt(on)); err != nil {
		http.Error(w, "Could not save the log flag.", http.StatusInternalServerError)
		return
	}
	if l.logSinksChanged != nil {
		row, err := readRoomSettings(r.Context(), l.sql)
		if err == nil {
			l.logSinksChanged(row.logStdout, row.logFile)
		}
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (l *Lobby) setAutoPause(w http.ResponseWriter, r *http.Request) {
	if !l.requireAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read the form.", http.StatusBadRequest)
		return
	}
	on := r.PostFormValue("enabled") == "1" || r.PostFormValue("enabled") == "on"
	if err := l.SetAutoPause(r.Context(), on); err != nil {
		http.Error(w, "Could not save auto-pause.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (l *Lobby) setAutoStart(w http.ResponseWriter, r *http.Request) {
	if !l.requireAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read the form.", http.StatusBadRequest)
		return
	}
	on := r.PostFormValue("enabled") == "1" || r.PostFormValue("enabled") == "on"
	if err := l.SetAutoStart(r.Context(), on); err != nil {
		http.Error(w, "Could not save auto-start.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, settingsReturn(r), http.StatusSeeOther)
}

func (l *Lobby) setDisconnectAfter(w http.ResponseWriter, r *http.Request) {
	l.setIntSetting(w, r, "disconnect_after", "disconnect_after", 0, 3600, "Disconnected after must be 0 to 3600.")
}

func (l *Lobby) setKickTimeout(w http.ResponseWriter, r *http.Request) {
	l.setIntSetting(w, r, "kick_timeout", "kick_timeout", 0, 86400, "Kick timeout must be 0 to 86400.")
}

func (l *Lobby) setProtectHost(w http.ResponseWriter, r *http.Request) {
	l.setBoolSetting(w, r, "protect_host", "Could not save protect-host.")
}

func (l *Lobby) setSeatDisconnectedWaiters(w http.ResponseWriter, r *http.Request) {
	l.setBoolSetting(w, r, "seat_disconnected_waiters", "Could not save seat disconnected waiters.")
}

func (l *Lobby) setResetReady(w http.ResponseWriter, r *http.Request) {
	if !l.requireAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read the form.", http.StatusBadRequest)
		return
	}
	mode := r.PostFormValue("reset_ready")
	switch mode {
	case ResetReadyEvery, ResetReadySwitch, ResetReadyNever:
	default:
		http.Error(w, "Unknown Reset Player Ready State value.", http.StatusBadRequest)
		return
	}
	if _, err := l.sql.ExecContext(r.Context(), `UPDATE room_state SET reset_ready = ? WHERE id = 1`, mode); err != nil {
		http.Error(w, "Could not save Reset Player Ready State.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (l *Lobby) setIntSetting(w http.ResponseWriter, r *http.Request, field, column string, min, max int, bad string) {
	if !l.requireAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read the form.", http.StatusBadRequest)
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue(field)))
	if err != nil || n < min || n > max {
		data, viewErr := l.settingsView(r.Context(), bad)
		if viewErr != nil {
			http.Error(w, "Could not read the room.", http.StatusInternalServerError)
			return
		}
		l.render(w, "settings.html", data, http.StatusBadRequest)
		return
	}
	if _, err := l.sql.ExecContext(r.Context(), `UPDATE room_state SET `+column+` = ? WHERE id = 1`, n); err != nil {
		http.Error(w, "Could not save the setting.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (l *Lobby) setBoolSetting(w http.ResponseWriter, r *http.Request, column, fail string) {
	if !l.requireAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read the form.", http.StatusBadRequest)
		return
	}
	on := r.PostFormValue("enabled") == "1" || r.PostFormValue("enabled") == "on"
	if _, err := l.sql.ExecContext(r.Context(), `UPDATE room_state SET `+column+` = ? WHERE id = 1`, boolToInt(on)); err != nil {
		http.Error(w, fail, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func settingsReturn(r *http.Request) string {
	if r.PostFormValue("return") == "/settings" {
		return "/settings"
	}
	return "/"
}

func (l *Lobby) hostStand(w http.ResponseWriter, r *http.Request) {
	if !l.requireAdmin(w, r) {
		return
	}
	player, ok, err := l.PlayerFromRequest(r)
	if err != nil || !ok || !player.ClaimedHost {
		http.Error(w, "Host phone required.", http.StatusForbidden)
		return
	}
	if err := l.changeHostSeat(r.Context(), player, "stand", ""); err != nil {
		if errors.Is(err, errHostBusy) {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		http.Error(w, "Could not stand.", http.StatusInternalServerError)
		return
	}
	l.events.Publish("roster")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (l *Lobby) hostSit(w http.ResponseWriter, r *http.Request) {
	if !l.requireAdmin(w, r) {
		return
	}
	player, ok, err := l.PlayerFromRequest(r)
	if err != nil || !ok || !player.ClaimedHost {
		http.Error(w, "Host phone required.", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read the form.", http.StatusBadRequest)
		return
	}
	if err := l.changeHostSeat(r.Context(), player, "sit", r.PostFormValue("bump_player_id")); err != nil {
		if errors.Is(err, errHostBusy) {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		if errors.Is(err, errBumpRequired) {
			http.Error(w, "Pick a seated player to bump.", http.StatusConflict)
			return
		}
		http.Error(w, "Could not sit.", http.StatusInternalServerError)
		return
	}
	l.events.Publish("roster")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (l *Lobby) makeHost(w http.ResponseWriter, r *http.Request) {
	if !l.requireOperator(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read the form.", http.StatusBadRequest)
		return
	}
	playerID := r.PostFormValue("player_id")
	if playerID == "" {
		http.Error(w, "Player is required.", http.StatusBadRequest)
		return
	}
	if err := l.writeMakeHost(r.Context(), playerID); err != nil {
		if errors.Is(err, errMakeHostForbidden) {
			http.Error(w, "Make host is only for a missing or disconnected host.", http.StatusConflict)
			return
		}
		http.Error(w, "Could not make host.", http.StatusInternalServerError)
		return
	}
	l.events.Publish("roster")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (l *Lobby) takeHost(w http.ResponseWriter, r *http.Request) {
	player, ok, err := l.PlayerFromRequest(r)
	if err != nil {
		http.Error(w, "Could not read the roster.", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read the form.", http.StatusBadRequest)
		return
	}
	sessionID, err := l.writeTakeHost(r.Context(), player, r.PostFormValue("password"))
	if err != nil {
		if errors.Is(err, errPasswordMismatch) {
			http.Error(w, "Admin password is incorrect.", http.StatusUnauthorized)
			return
		}
		if errors.Is(err, errTakeHostDenied) {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		http.Error(w, "Could not take host.", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, l.cookie(r, l.adminCookieName, sessionID))
	l.events.Publish("roster")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (l *Lobby) settingsView(ctx context.Context, seatErr string) (settingsData, error) {
	open, err := l.roomOpen(ctx)
	if err != nil {
		return settingsData{}, err
	}
	players, err := l.listPlayers(ctx, `1 = 1 ORDER BY rowid`)
	if err != nil {
		return settingsData{}, err
	}
	chrome, err := l.chrome(ctx, "Hackbox settings")
	if err != nil {
		return settingsData{}, err
	}
	row, err := readRoomSettings(ctx, l.sql)
	if err != nil {
		return settingsData{}, err
	}
	canMake := true
	for _, p := range players {
		if p.ClaimedHost && !p.Disconnected {
			canMake = false
			break
		}
	}
	data := settingsData{
		Chrome:             chrome,
		Open:               open,
		CycleSeats:         row.cycleSeats,
		FillEmpty:          row.fillEmpty,
		LogStdout:          row.logStdout,
		LogFile:            row.logFile,
		ProtectHost:        row.protectHost,
		SeatDisconnected:   row.seatDisconnected,
		DisconnectAfter:    row.disconnectAfter,
		KickTimeout:        row.kickTimeout,
		ResetReadyWhen:     row.resetReady,
		SeatCap:            row.seatCap,
		AdvertisedHostname: row.hostname,
		SeatCapError:       seatErr,
		CanMakeHost:        canMake,
		Players:            players,
	}
	autoStart, err := l.AutoStart(ctx)
	if err != nil {
		return settingsData{}, err
	}
	data.AutoStart = autoStart
	if l.settingsExtras != nil {
		extra := l.settingsExtras(ctx)
		data.GameIDs = extra.GameIDs
		data.LoadedGameID = extra.LoadedGameID
		data.GameSettings = extra.GameSettings
		data.AutoPause = extra.AutoPause
	} else {
		on, err := l.AutoPause(ctx)
		if err != nil {
			return settingsData{}, err
		}
		data.AutoPause = on
	}
	return data, nil
}

func (l *Lobby) writeLogin(w http.ResponseWriter, r *http.Request, message string, status int) {
	chrome, err := l.chrome(r.Context(), "Hackbox settings")
	if err != nil {
		http.Error(w, "Could not read the room.", http.StatusInternalServerError)
		return
	}
	l.render(w, "settings-login.html", settingsData{Chrome: chrome, LoginError: message}, status)
}

func (l *Lobby) hasAdminCookie(r *http.Request) bool {
	cookie, err := r.Cookie(l.adminCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	var n int
	if err := l.sql.QueryRowContext(
		r.Context(),
		`SELECT COUNT(*) FROM admin_session WHERE id = ?`,
		cookie.Value,
	).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

func (l *Lobby) requireOperator(w http.ResponseWriter, r *http.Request) bool {
	cookie, err := r.Cookie(l.adminCookieName)
	if err != nil || cookie.Value == "" {
		http.Error(w, "Admin session required.", http.StatusUnauthorized)
		return false
	}
	var kind string
	if err := l.sql.QueryRowContext(
		r.Context(),
		`SELECT kind FROM admin_session WHERE id = ?`,
		cookie.Value,
	).Scan(&kind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Admin session required.", http.StatusUnauthorized)
			return false
		}
		http.Error(w, "Could not read the session.", http.StatusInternalServerError)
		return false
	}
	if kind != "operator" {
		http.Error(w, "Operator session required.", http.StatusForbidden)
		return false
	}
	return true
}

func (l *Lobby) adminHash(ctx context.Context) (string, error) {
	var hash string
	err := l.sql.QueryRowContext(ctx, `SELECT hash FROM admin_password WHERE id = 1`).Scan(&hash)
	if err != nil {
		return "", fmt.Errorf("lobby: read admin hash: %w", err)
	}
	return hash, nil
}

func (l *Lobby) writeSeatCap(ctx context.Context, cap int) error {
	tx, err := l.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("lobby: begin seat cap: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	count, err := seatedCount(ctx, tx)
	if err != nil {
		return err
	}
	if cap < count {
		return errSeatCapTooLow
	}
	if _, err := tx.ExecContext(ctx, `UPDATE room_state SET seat_cap = ? WHERE id = 1`, cap); err != nil {
		return fmt.Errorf("lobby: update seat cap: %w", err)
	}
	if err := l.fillWait(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("lobby: commit seat cap: %w", err)
	}
	return nil
}

func (l *Lobby) writeHostname(ctx context.Context, hostname string) error {
	_, err := l.sql.ExecContext(
		ctx,
		`UPDATE room_state SET advertised_hostname = ? WHERE id = 1`,
		strings.TrimSpace(hostname),
	)
	if err != nil {
		return fmt.Errorf("lobby: update hostname: %w", err)
	}
	return nil
}

func (l *Lobby) rotateSeated(ctx context.Context) error {
	tx, err := l.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("lobby: begin cycle: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT player_id FROM roster WHERE seated = 1 ORDER BY rowid`)
	if err != nil {
		return fmt.Errorf("lobby: list seated for cycle: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return fmt.Errorf("lobby: scan seated for cycle: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		seq, err := nextWaitSeq(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE roster SET seated = 0, waiting = 1, wait_seq = ? WHERE player_id = ?`,
			seq,
			id,
		); err != nil {
			return fmt.Errorf("lobby: cycle seated player: %w", err)
		}
	}
	if err := l.fillWait(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("lobby: commit cycle: %w", err)
	}
	return nil
}

func (l *Lobby) changeHostSeat(ctx context.Context, player Player, intent, bumpID string) error {
	tx, err := l.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("lobby: begin host seat: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var roundActive int
	if err := tx.QueryRowContext(ctx, `SELECT round_active FROM room_state WHERE id = 1`).Scan(&roundActive); err != nil {
		return fmt.Errorf("lobby: read round: %w", err)
	}
	if roundActive != 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE room_state SET host_queue = ? WHERE id = 1`, intent); err != nil {
			return fmt.Errorf("lobby: queue host seat: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("lobby: commit host queue: %w", err)
		}
		return errHostBusy
	}
	if _, err := tx.ExecContext(ctx, `UPDATE room_state SET host_queue = '' WHERE id = 1`); err != nil {
		return fmt.Errorf("lobby: clear host queue: %w", err)
	}
	if intent == "stand" {
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE roster SET seated = 0, waiting = 0, wait_seq = NULL WHERE player_id = ?`,
			player.ID,
		); err != nil {
			return fmt.Errorf("lobby: host stand: %w", err)
		}
		if err := l.fillWait(ctx, tx); err != nil {
			return err
		}
	} else {
		if err := sitHost(ctx, tx, player.ID, bumpID); err != nil {
			return err
		}
		if !player.Seated {
			l.forgetReady(player.ID)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("lobby: commit host seat: %w", err)
	}
	return nil
}

func sitHost(ctx context.Context, tx *sql.Tx, hostID, bumpID string) error {
	var seated int
	if err := tx.QueryRowContext(ctx, `SELECT seated FROM roster WHERE player_id = ?`, hostID).Scan(&seated); err != nil {
		return fmt.Errorf("lobby: read host seat: %w", err)
	}
	if seated != 0 {
		return nil
	}
	count, err := seatedCount(ctx, tx)
	if err != nil {
		return err
	}
	cap, err := seatCapTx(ctx, tx)
	if err != nil {
		return err
	}
	if count < cap {
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE roster SET seated = 1, waiting = 0, wait_seq = NULL WHERE player_id = ?`,
			hostID,
		); err != nil {
			return fmt.Errorf("lobby: host sit: %w", err)
		}
		return nil
	}
	if bumpID == "" || bumpID == hostID {
		return errBumpRequired
	}
	var bumpSeated int
	if err := tx.QueryRowContext(ctx, `SELECT seated FROM roster WHERE player_id = ?`, bumpID).Scan(&bumpSeated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errBumpRequired
		}
		return fmt.Errorf("lobby: read bump target: %w", err)
	}
	if bumpSeated == 0 {
		return errBumpRequired
	}
	front, err := frontWaitSeq(ctx, tx)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE roster SET seated = 0, waiting = 1, wait_seq = ? WHERE player_id = ?`,
		front,
		bumpID,
	); err != nil {
		return fmt.Errorf("lobby: bump player: %w", err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE roster SET seated = 1, waiting = 0, wait_seq = NULL WHERE player_id = ?`,
		hostID,
	); err != nil {
		return fmt.Errorf("lobby: host sit after bump: %w", err)
	}
	return nil
}

func frontWaitSeq(ctx context.Context, tx *sql.Tx) (int64, error) {
	var seq sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MIN(wait_seq) FROM roster WHERE waiting = 1`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("lobby: min wait seq: %w", err)
	}
	if !seq.Valid {
		return 1, nil
	}
	return seq.Int64 - 1, nil
}

func (l *Lobby) writeMakeHost(ctx context.Context, playerID string) error {
	tx, err := l.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("lobby: begin make host: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var hostID string
	var disconnected int
	err = tx.QueryRowContext(
		ctx,
		`SELECT player_id, disconnected FROM roster WHERE claimed_host = 1`,
	).Scan(&hostID, &disconnected)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("lobby: read claimed host: %w", err)
	}
	if err == nil && disconnected == 0 {
		return errMakeHostForbidden
	}
	if _, err := tx.ExecContext(ctx, `UPDATE roster SET pending_designation = 0`); err != nil {
		return fmt.Errorf("lobby: clear designation: %w", err)
	}
	res, err := tx.ExecContext(ctx, `UPDATE roster SET pending_designation = 1 WHERE player_id = ?`, playerID)
	if err != nil {
		return fmt.Errorf("lobby: set designation: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("lobby: unknown player")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("lobby: commit make host: %w", err)
	}
	return nil
}

func (l *Lobby) writeTakeHost(ctx context.Context, player Player, password string) (string, error) {
	tx, err := l.sql.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("lobby: begin take host: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT pending_designation FROM roster WHERE player_id = ?`, player.ID).Scan(&pending); err != nil {
		return "", fmt.Errorf("lobby: read designation: %w", err)
	}
	if pending == 0 {
		return "", errTakeHostDenied
	}
	var hash string
	if err := tx.QueryRowContext(ctx, `SELECT hash FROM admin_password WHERE id = 1`).Scan(&hash); err != nil {
		return "", fmt.Errorf("lobby: read admin hash: %w", err)
	}
	if !l.passwordMatches(hash, password) {
		return "", errPasswordMismatch
	}
	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM admin_session
		 WHERE id IN (SELECT session_id FROM host_phone_session)`,
	); err != nil {
		return "", fmt.Errorf("lobby: revoke host-phone sessions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE roster SET claimed_host = 0, pending_designation = 0`); err != nil {
		return "", fmt.Errorf("lobby: clear claimed host: %w", err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE roster SET claimed_host = 1, pending_designation = 0 WHERE player_id = ?`,
		player.ID,
	); err != nil {
		return "", fmt.Errorf("lobby: claim host: %w", err)
	}
	sessionID, err := newRandomValue(32)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO admin_session (id, kind) VALUES (?, 'host-phone')`, sessionID); err != nil {
		return "", fmt.Errorf("lobby: insert host-phone session: %w", err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO host_phone_session (player_id, session_id) VALUES (?, ?)`,
		player.ID,
		sessionID,
	); err != nil {
		return "", fmt.Errorf("lobby: link host-phone session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("lobby: commit take host: %w", err)
	}
	return sessionID, nil
}

type roomSettings struct {
	hostname         string
	seatCap          int
	cycleSeats       bool
	fillEmpty        bool
	logStdout        bool
	logFile          bool
	protectHost      bool
	seatDisconnected bool
	disconnectAfter  int
	kickTimeout      int
	resetReady       string
}

func readRoomSettings(ctx context.Context, q queryer) (roomSettings, error) {
	var row roomSettings
	var cycle, fill, stdout, file, protect, seatDisc int
	err := q.QueryRowContext(
		ctx,
		`SELECT advertised_hostname, seat_cap, cycle_seats, fill_empty, log_stdout, log_file,
		        protect_host, seat_disconnected_waiters, disconnect_after, kick_timeout, reset_ready
		 FROM room_state WHERE id = 1`,
	).Scan(
		&row.hostname, &row.seatCap, &cycle, &fill, &stdout, &file,
		&protect, &seatDisc, &row.disconnectAfter, &row.kickTimeout, &row.resetReady,
	)
	if err != nil {
		return roomSettings{}, fmt.Errorf("lobby: read room settings: %w", err)
	}
	if row.seatCap < minSeatCap || row.seatCap > maxSeatCap {
		row.seatCap = DefaultSeatCap
	}
	row.cycleSeats = cycle != 0
	row.fillEmpty = fill != 0
	row.logStdout = stdout != 0
	row.logFile = file != 0
	row.protectHost = protect != 0
	row.seatDisconnected = seatDisc != 0
	switch row.resetReady {
	case ResetReadyEvery, ResetReadySwitch, ResetReadyNever:
	default:
		row.resetReady = ResetReadySwitch
	}
	return row, nil
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readSeatCap(ctx context.Context, q queryer) (int, error) {
	row, err := readRoomSettings(ctx, q)
	if err != nil {
		return 0, err
	}
	return row.seatCap, nil
}

func effectiveSeatCap(ctx context.Context, q queryer) (int, error) {
	hostCap, err := readSeatCap(ctx, q)
	if err != nil {
		return 0, err
	}
	var gameMax int
	if err := q.QueryRowContext(ctx, `SELECT game_max_players FROM room_state WHERE id = 1`).Scan(&gameMax); err != nil {
		return 0, fmt.Errorf("lobby: read game max: %w", err)
	}
	if gameMax > 0 && gameMax < hostCap {
		return gameMax, nil
	}
	return hostCap, nil
}

func seatCapTx(ctx context.Context, tx *sql.Tx) (int, error) {
	return effectiveSeatCap(ctx, tx)
}

func seatedCountDB(ctx context.Context, q queryer) (int, error) {
	var count int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM roster WHERE seated = 1`).Scan(&count); err != nil {
		return 0, fmt.Errorf("lobby: count seated: %w", err)
	}
	return count, nil
}

func readAdvertisedHostname(ctx context.Context, q queryer) (string, error) {
	var hostname string
	if err := q.QueryRowContext(ctx, `SELECT advertised_hostname FROM room_state WHERE id = 1`).Scan(&hostname); err != nil {
		return "", fmt.Errorf("lobby: read hostname: %w", err)
	}
	return strings.TrimSpace(hostname), nil
}

func joinURLFromHostname(hostname, fallback string) string {
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return fallback
	}
	if strings.Contains(hostname, "://") {
		if !strings.HasSuffix(hostname, "/") {
			return hostname + "/"
		}
		return hostname
	}
	scheme := "http"
	if fallback != "" {
		if u, err := url.Parse(fallback); err == nil && u.Scheme != "" {
			scheme = u.Scheme
		}
	}
	return scheme + "://" + hostForJoinURL(hostname) + "/"
}

// hostForJoinURL keeps a typed port and omits the listen port otherwise.
func hostForJoinURL(hostname string) string {
	if _, _, err := net.SplitHostPort(hostname); err == nil {
		return hostname
	}
	if ip := net.ParseIP(hostname); ip != nil && ip.To4() == nil {
		return "[" + ip.String() + "]"
	}
	return hostname
}
