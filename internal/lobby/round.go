package lobby

import (
	"context"
	"fmt"
)

// Players returns the live roster in row order.
func (l *Lobby) Players(ctx context.Context) ([]Player, error) {
	return l.listPlayers(ctx, `1 = 1 ORDER BY rowid`)
}

// SeatedCount is the number of seated roster rows.
func (l *Lobby) SeatedCount(ctx context.Context) (int, error) {
	return seatedCountDB(ctx, l.sql)
}

// CycleSeats reports whether graceful Stop should rotate the table.
func (l *Lobby) CycleSeats(ctx context.Context) (bool, error) {
	row, err := readRoomSettings(ctx, l.sql)
	if err != nil {
		return false, err
	}
	return row.cycleSeats, nil
}

// SetConnected writes the disconnected flag. connected false is a dimmed seat.
func (l *Lobby) SetConnected(ctx context.Context, playerID string, connected bool) error {
	res, err := l.sql.ExecContext(
		ctx,
		`UPDATE roster SET disconnected = ? WHERE player_id = ?`,
		boolToInt(!connected),
		playerID,
	)
	if err != nil {
		return fmt.Errorf("lobby: set connected: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("lobby: set connected rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("lobby: unknown player")
	}
	return nil
}

// SetRoundActive marks a running game so sit/stand queues and fill can wait.
func (l *Lobby) SetRoundActive(ctx context.Context, active bool) error {
	if _, err := l.sql.ExecContext(ctx, `UPDATE room_state SET round_active = ? WHERE id = 1`, boolToInt(active)); err != nil {
		return fmt.Errorf("lobby: set round: %w", err)
	}
	return nil
}

// SetGamePlayerMax stores the Loaded game max. 0 restores host-cap-only fill.
func (l *Lobby) SetGamePlayerMax(ctx context.Context, max int) error {
	if max < 0 {
		max = 0
	}
	if _, err := l.sql.ExecContext(ctx, `UPDATE room_state SET game_max_players = ? WHERE id = 1`, max); err != nil {
		return fmt.Errorf("lobby: set game max: %w", err)
	}
	return nil
}

// SetSelectedGameID persists the Loaded package id. Empty means none.
func (l *Lobby) SetSelectedGameID(ctx context.Context, id string) error {
	if _, err := l.sql.ExecContext(ctx, `UPDATE room_state SET selected_game_id = ? WHERE id = 1`, id); err != nil {
		return fmt.Errorf("lobby: set selected game: %w", err)
	}
	return nil
}

// SelectedGameID returns the persisted Loaded package id.
func (l *Lobby) SelectedGameID(ctx context.Context) (string, error) {
	var id string
	if err := l.sql.QueryRowContext(ctx, `SELECT selected_game_id FROM room_state WHERE id = 1`).Scan(&id); err != nil {
		return "", fmt.Errorf("lobby: read selected game: %w", err)
	}
	return id, nil
}

// SetAutoPause stores the seated-disconnect auto-pause bit. Default off.
func (l *Lobby) SetAutoPause(ctx context.Context, on bool) error {
	if _, err := l.sql.ExecContext(ctx, `UPDATE room_state SET auto_pause = ? WHERE id = 1`, boolToInt(on)); err != nil {
		return fmt.Errorf("lobby: set auto-pause: %w", err)
	}
	return nil
}

// SetAutoStart stores the claim-host auto-start switch. Default off.
func (l *Lobby) SetAutoStart(ctx context.Context, on bool) error {
	if _, err := l.sql.ExecContext(ctx, `UPDATE room_state SET auto_start = ? WHERE id = 1`, boolToInt(on)); err != nil {
		return fmt.Errorf("lobby: set auto-start: %w", err)
	}
	return nil
}

// AutoStart reports the claim-host auto-start switch.
func (l *Lobby) AutoStart(ctx context.Context) (bool, error) {
	var on int
	if err := l.sql.QueryRowContext(ctx, `SELECT auto_start FROM room_state WHERE id = 1`).Scan(&on); err != nil {
		return false, fmt.Errorf("lobby: read auto-start: %w", err)
	}
	return on != 0, nil
}

// AutoPause reports the seated-disconnect auto-pause bit.
func (l *Lobby) AutoPause(ctx context.Context) (bool, error) {
	var on int
	if err := l.sql.QueryRowContext(ctx, `SELECT auto_pause FROM room_state WHERE id = 1`).Scan(&on); err != nil {
		return false, fmt.Errorf("lobby: read auto-pause: %w", err)
	}
	return on != 0, nil
}

// EndRound returns Lobby after Stop. cycle rotates seats on a graceful finish.
// Disconnected seated rows are dropped. A queued host sit or stand is applied.
func (l *Lobby) EndRound(ctx context.Context, cycle bool) error {
	if err := l.SetRoundActive(ctx, false); err != nil {
		return err
	}
	if err := l.dropDisconnectedSeated(ctx); err != nil {
		return err
	}
	if cycle {
		if err := l.rotateSeated(ctx); err != nil {
			return err
		}
	}
	return l.applyHostQueue(ctx)
}

func (l *Lobby) dropDisconnectedSeated(ctx context.Context) error {
	players, err := l.listPlayers(ctx, `seated = 1 AND disconnected = 1`)
	if err != nil {
		return err
	}
	for _, p := range players {
		if err := l.removePlayer(ctx, p.ID); err != nil {
			return err
		}
	}
	return nil
}

func (l *Lobby) applyHostQueue(ctx context.Context) error {
	var intent string
	if err := l.sql.QueryRowContext(ctx, `SELECT host_queue FROM room_state WHERE id = 1`).Scan(&intent); err != nil {
		return fmt.Errorf("lobby: read host queue: %w", err)
	}
	if intent == "" {
		return nil
	}
	players, err := l.listPlayers(ctx, `claimed_host = 1`)
	if err != nil {
		return err
	}
	if len(players) == 0 {
		_, err := l.sql.ExecContext(ctx, `UPDATE room_state SET host_queue = '' WHERE id = 1`)
		return err
	}
	if err := l.changeHostSeat(ctx, players[0], intent, ""); err != nil {
		if err == errBumpRequired {
			return nil
		}
		return err
	}
	return nil
}

func (l *Lobby) emit(line string) {
	if l.notice != nil && line != "" {
		l.notice(line)
	}
}
