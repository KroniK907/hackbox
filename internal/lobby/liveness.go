package lobby

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

const (
	// HeartbeatPeriod is how often roster phones POST /lobby/heartbeat.
	HeartbeatPeriod = 2 * time.Second

	// StartupDebounce is how long after process start seated kick clocks wait.
	StartupDebounce = 5 * time.Second

	// ResetReadyEvery clears checks on Stop, Shutdown, and game switch.
	ResetReadyEvery = "every"
	// ResetReadySwitch clears checks on Shutdown or picking another game.
	ResetReadySwitch = "switch"
	// ResetReadyNever leaves checks until Unready.
	ResetReadyNever = "never"

	// ReadyResetStop is Stop (or Finish).
	ReadyResetStop = "stop"
	// ReadyResetSwitch is Shutdown or Load of another game.
	ReadyResetSwitch = "switch"
)

type liveMem struct {
	mu        sync.Mutex
	ready     map[string]bool
	lastBeat  map[string]time.Time
	dimmedAt  map[string]time.Time
	rtt       map[string]time.Duration
	startedAt time.Time
	clock     func() time.Time
	liveStop  chan struct{}
}

func newLiveMem(clock func() time.Time) *liveMem {
	if clock == nil {
		clock = time.Now
	}
	return &liveMem{
		ready:     map[string]bool{},
		lastBeat:  map[string]time.Time{},
		dimmedAt:  map[string]time.Time{},
		rtt:       map[string]time.Duration{},
		startedAt: clock(),
		clock:     clock,
	}
}

func (m *liveMem) now() time.Time {
	return m.clock()
}

// LastHeartbeatRTT is the last sampled interval for a player. Zero until a
// second successful heartbeat exists.
func (l *Lobby) LastHeartbeatRTT(playerID string) time.Duration {
	l.live.mu.Lock()
	defer l.live.mu.Unlock()
	return l.live.rtt[playerID]
}

// StartLiveness runs disconnect and auto-kick checks once a second.
func (l *Lobby) StartLiveness() {
	l.live.mu.Lock()
	if l.live.liveStop != nil {
		l.live.mu.Unlock()
		return
	}
	l.live.liveStop = make(chan struct{})
	stop := l.live.liveStop
	l.live.mu.Unlock()
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-tick.C:
				_ = l.TickLiveness(context.Background(), now)
			}
		}
	}()
}

// TickLiveness applies disconnect debounce and lobby-only auto-kick at now.
func (l *Lobby) TickLiveness(ctx context.Context, now time.Time) error {
	settings, err := l.livenessSettings(ctx)
	if err != nil {
		return err
	}
	players, err := l.listPlayers(ctx, `1 = 1 ORDER BY rowid`)
	if err != nil {
		return err
	}
	round, err := l.roundActive(ctx)
	if err != nil {
		return err
	}
	kickOK := now.Sub(l.live.startedAt) >= StartupDebounce
	for _, p := range players {
		if err := l.tickPlayer(ctx, p, now, settings, round, kickOK); err != nil {
			return err
		}
	}
	return nil
}

func (l *Lobby) tickPlayer(ctx context.Context, p Player, now time.Time, settings livenessSettings, round, kickOK bool) error {
	stale := heartbeatStale(l.lastBeatTime(p.ID), now, settings.disconnectAfter)
	if stale && !p.Disconnected {
		if err := l.SetConnected(ctx, p.ID, false); err != nil {
			return err
		}
		l.markDimmed(p.ID, now)
		p.Disconnected = true
		l.events.Publish("roster")
		if l.afterDisconnect != nil {
			l.afterDisconnect(ctx, p)
		}
	}
	if p.Disconnected {
		l.ensureDimmed(p.ID, l.live.startedAt)
	}
	if round || !kickOK || !p.Seated || p.Disconnected == false {
		return nil
	}
	if settings.kickTimeout <= 0 {
		return nil
	}
	if settings.protectHost && p.ClaimedHost {
		return nil
	}
	clockStart := l.dimmedTime(p.ID)
	if clockStart.Before(l.live.startedAt.Add(StartupDebounce)) {
		clockStart = l.live.startedAt.Add(StartupDebounce)
	}
	if now.Sub(clockStart) < time.Duration(settings.kickTimeout)*time.Second {
		return nil
	}
	if err := l.removePlayer(ctx, p.ID); err != nil {
		return err
	}
	l.forgetLive(p.ID)
	l.emit("kick")
	l.events.Publish("roster")
	return nil
}

func heartbeatStale(last time.Time, now time.Time, disconnectAfter int) bool {
	if last.IsZero() {
		return false
	}
	limit := time.Duration(disconnectAfter) * time.Second
	if disconnectAfter <= 0 {
		limit = HeartbeatPeriod
	}
	return now.Sub(last) >= limit
}

func (l *Lobby) heartbeat(w http.ResponseWriter, r *http.Request) {
	start := l.live.now()
	player, ok, err := l.PlayerFromRequest(r)
	if err != nil {
		http.Error(w, "Could not read the roster.", http.StatusInternalServerError)
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	prev := l.lastBeatTime(player.ID)
	l.noteBeat(player.ID, start, prev)
	if player.Disconnected {
		if err := l.SetConnected(r.Context(), player.ID, true); err != nil {
			http.Error(w, "Could not record the heartbeat.", http.StatusInternalServerError)
			return
		}
		l.clearDimmed(player.ID)
		l.events.Publish("roster")
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *Lobby) readyToggle(w http.ResponseWriter, r *http.Request) {
	player, ok, err := l.PlayerFromRequest(r)
	if err != nil {
		http.Error(w, "Could not read the roster.", http.StatusInternalServerError)
		return
	}
	if !ok || !l.readyAllowed(r, player) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	next := !l.isReady(player.ID)
	l.setReady(player.ID, next)
	l.events.Publish("roster")
	if next {
		l.maybeAutoStart(r.Context())
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (l *Lobby) readyAllowed(r *http.Request, player Player) bool {
	if !player.Seated || l.phoneExtras == nil {
		return false
	}
	extra := l.phoneExtras(r, player)
	return extra.LoadedGameID != "" && !extra.Started
}

func (l *Lobby) maybeAutoStart(ctx context.Context) {
	if l.startRound == nil {
		return
	}
	on, err := l.AutoStart(ctx)
	if err != nil || !on {
		return
	}
	players, err := l.listPlayers(ctx, `seated = 1 ORDER BY rowid`)
	if err != nil || len(players) == 0 {
		return
	}
	for _, p := range players {
		if p.Disconnected || !l.isReady(p.ID) {
			return
		}
	}
	_ = l.startRound(ctx)
}

// ApplyReadyReset clears in-memory Ready flags for Stop or a game switch.
func (l *Lobby) ApplyReadyReset(ctx context.Context, reason string) error {
	mode, err := l.ResetReadyWhen(ctx)
	if err != nil {
		return err
	}
	switch mode {
	case ResetReadyNever:
		return nil
	case ResetReadySwitch:
		if reason != ReadyResetSwitch {
			return nil
		}
	case ResetReadyEvery:
		if reason != ReadyResetStop && reason != ReadyResetSwitch {
			return nil
		}
	default:
		if reason != ReadyResetSwitch {
			return nil
		}
	}
	if !l.clearAllReady() {
		return nil
	}
	l.events.Publish("roster")
	return nil
}

// ResetReadyWhen returns the persisted Ready-clear policy.
func (l *Lobby) ResetReadyWhen(ctx context.Context) (string, error) {
	var mode string
	if err := l.sql.QueryRowContext(ctx, `SELECT reset_ready FROM room_state WHERE id = 1`).Scan(&mode); err != nil {
		return "", fmt.Errorf("lobby: read reset ready: %w", err)
	}
	switch mode {
	case ResetReadyEvery, ResetReadySwitch, ResetReadyNever:
		return mode, nil
	default:
		return ResetReadySwitch, nil
	}
}

func (l *Lobby) roundActive(ctx context.Context) (bool, error) {
	var on int
	if err := l.sql.QueryRowContext(ctx, `SELECT round_active FROM room_state WHERE id = 1`).Scan(&on); err != nil {
		return false, fmt.Errorf("lobby: read round: %w", err)
	}
	return on != 0, nil
}

type livenessSettings struct {
	disconnectAfter int
	kickTimeout     int
	protectHost     bool
}

func (l *Lobby) livenessSettings(ctx context.Context) (livenessSettings, error) {
	var s livenessSettings
	var protect int
	err := l.sql.QueryRowContext(
		ctx,
		`SELECT disconnect_after, kick_timeout, protect_host FROM room_state WHERE id = 1`,
	).Scan(&s.disconnectAfter, &s.kickTimeout, &protect)
	if err != nil {
		return livenessSettings{}, fmt.Errorf("lobby: read liveness settings: %w", err)
	}
	s.protectHost = protect != 0
	return s, nil
}

func (l *Lobby) noteBeat(id string, now, prev time.Time) {
	l.live.mu.Lock()
	defer l.live.mu.Unlock()
	l.live.lastBeat[id] = now
	if !prev.IsZero() {
		l.live.rtt[id] = now.Sub(prev)
	}
}

func (l *Lobby) lastBeatTime(id string) time.Time {
	l.live.mu.Lock()
	defer l.live.mu.Unlock()
	return l.live.lastBeat[id]
}

func (l *Lobby) isReady(id string) bool {
	l.live.mu.Lock()
	defer l.live.mu.Unlock()
	return l.live.ready[id]
}

func (l *Lobby) setReady(id string, on bool) {
	l.live.mu.Lock()
	defer l.live.mu.Unlock()
	if on {
		l.live.ready[id] = true
		return
	}
	delete(l.live.ready, id)
}

func (l *Lobby) clearAllReady() bool {
	l.live.mu.Lock()
	defer l.live.mu.Unlock()
	if len(l.live.ready) == 0 {
		return false
	}
	l.live.ready = map[string]bool{}
	return true
}

func (l *Lobby) forgetReady(id string) {
	l.live.mu.Lock()
	delete(l.live.ready, id)
	l.live.mu.Unlock()
}

func (l *Lobby) forgetLive(id string) {
	l.live.mu.Lock()
	delete(l.live.ready, id)
	delete(l.live.lastBeat, id)
	delete(l.live.dimmedAt, id)
	delete(l.live.rtt, id)
	l.live.mu.Unlock()
}

func (l *Lobby) markDimmed(id string, at time.Time) {
	l.live.mu.Lock()
	l.live.dimmedAt[id] = at
	l.live.mu.Unlock()
}

func (l *Lobby) ensureDimmed(id string, at time.Time) {
	l.live.mu.Lock()
	if _, ok := l.live.dimmedAt[id]; !ok {
		l.live.dimmedAt[id] = at
	}
	l.live.mu.Unlock()
}

func (l *Lobby) clearDimmed(id string) {
	l.live.mu.Lock()
	delete(l.live.dimmedAt, id)
	l.live.mu.Unlock()
}

func (l *Lobby) dimmedTime(id string) time.Time {
	l.live.mu.Lock()
	defer l.live.mu.Unlock()
	return l.live.dimmedAt[id]
}

func (l *Lobby) attachLive(p *Player) {
	l.live.mu.Lock()
	defer l.live.mu.Unlock()
	if p.Seated {
		p.Ready = l.live.ready[p.ID]
		return
	}
	delete(l.live.ready, p.ID)
}
