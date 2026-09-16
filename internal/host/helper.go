package host

import (
	"context"
	"net/http"
	"path/filepath"

	"github.com/KroniK907/hackbox/internal/games"
	"github.com/KroniK907/hackbox/internal/lobby"
)

type helper struct {
	rt *runtime
}

func (h *helper) Seated() []games.Player {
	return h.list(func(p lobby.Player) bool { return p.Seated })
}

func (h *helper) Waiting() []games.Player {
	return h.list(func(p lobby.Player) bool { return p.Waiting })
}

func (h *helper) Audience() []games.Player {
	return h.list(func(p lobby.Player) bool { return !p.Seated })
}

func (h *helper) Player(id string) (games.Player, bool) {
	for _, p := range h.list(func(lobby.Player) bool { return true }) {
		if p.ID == id {
			return p, true
		}
	}
	return games.Player{}, false
}

func (h *helper) PlayerFromRequest(r *http.Request) (games.Player, bool, error) {
	player, ok, err := h.rt.room.PlayerFromRequest(r)
	if err != nil || !ok {
		return games.Player{}, ok, err
	}
	return h.toGame(player), true, nil
}

func (h *helper) DataDir() string {
	id := h.rt.loadedID
	if id == "" {
		return filepath.Join(h.rt.db.Dir(), "games")
	}
	return filepath.Join(h.rt.db.Dir(), "games", id)
}

func (h *helper) KVGet(key string) ([]byte, bool, error) {
	return h.rt.db.KVGet(context.Background(), h.rt.loadedID, key)
}

func (h *helper) KVSet(key string, value []byte) error {
	return h.rt.db.KVSet(context.Background(), h.rt.loadedID, key, value)
}

func (h *helper) Finish() {
	_ = h.rt.stop(context.Background(), true)
}

func (h *helper) Pause() {
	_ = h.rt.pause()
}

func (h *helper) Resume() {
	_ = h.rt.resume()
}

func (h *helper) Publish(name string) {
	h.rt.events.Publish(name)
}

func (h *helper) Log(line string) {
	if h.rt.loadedID == "" {
		return
	}
	h.rt.log.Write(h.rt.loadedID + ": " + line)
}

func (h *helper) list(keep func(lobby.Player) bool) []games.Player {
	players, err := h.rt.room.Players(context.Background())
	if err != nil {
		return nil
	}
	out := make([]games.Player, 0, len(players))
	for _, p := range players {
		if keep(p) {
			out = append(out, h.toGame(p))
		}
	}
	return out
}

func (h *helper) toGame(p lobby.Player) games.Player {
	return games.Player{
		ID:               p.ID,
		DisplayName:      p.DisplayName,
		AvatarSeed:       p.AvatarSeed,
		Seated:           p.Seated,
		Waiting:          p.Waiting,
		Audience:         !p.Seated,
		ClaimedHost:      p.ClaimedHost,
		Connected:        !p.Disconnected,
		LastHeartbeatRTT: h.rt.room.LastHeartbeatRTT(p.ID),
	}
}
