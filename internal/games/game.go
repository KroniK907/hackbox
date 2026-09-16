// Package games is the compile-time game loader and the host helper contract.
package games

import (
	"net/http"
	"time"
)

// Player is a Lobby row as a game may read it. Games never write these fields.
type Player struct {
	ID               string
	DisplayName      string
	AvatarSeed       string
	Seated           bool
	Waiting          bool
	Audience         bool
	ClaimedHost      bool
	Connected        bool
	LastHeartbeatRTT time.Duration
}

// Helper is the host-owned API a game uses after Load. Games do not parse
// cookies, open host.sqlite, or write the log file.
type Helper interface {
	Seated() []Player
	Waiting() []Player
	Audience() []Player
	Player(id string) (Player, bool)
	PlayerFromRequest(r *http.Request) (Player, bool, error)
	DataDir() string
	KVGet(key string) ([]byte, bool, error)
	KVSet(key string, value []byte) error
	Finish()
	Pause()
	Resume()
	Publish(name string)
	Log(line string)
}

// Game is a compiled-in package host can Load, Start, Stop, and Shutdown.
// MinPlayers and MaxPlayers are 0 when the game does not declare a pair.
type Game interface {
	ID() string
	MinPlayers() int
	MaxPlayers() int
	Load(h Helper) error
	Settings() http.Handler
	Start(h Helper) error
	Board(w http.ResponseWriter, r *http.Request)
	Phone(w http.ResponseWriter, r *http.Request)
	Play() http.Handler
	Pause() error
	Resume() error
	Stop() error
	Shutdown() error
}

// Factory constructs one Game value. Catalog is compile-time only.
type Factory struct {
	ID  string
	New func() Game
}

// Catalog is the compile-time list of game packages. Empty until a child
// package such as Testing is imported from this package.
func Catalog() []Factory {
	return nil
}
