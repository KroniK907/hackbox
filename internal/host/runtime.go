package host

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/KroniK907/hackbox/internal/games"
	"github.com/KroniK907/hackbox/internal/lobby"
	"github.com/KroniK907/hackbox/internal/platform/applog"
	"github.com/KroniK907/hackbox/internal/platform/hub"
	"github.com/KroniK907/hackbox/internal/store"
)

var (
	errNotLoaded    = errors.New("host: no game loaded")
	errStartRefused = errors.New("host: start refused")
	errLoadRefused  = errors.New("host: load refused")
)

type runtime struct {
	db      *store.DB
	room    *lobby.Lobby
	events  *hub.Hub
	log     *applog.Logger
	catalog map[string]games.Factory

	mu       sync.Mutex
	game     games.Game
	loadedID string
	started  bool
}

func newRuntime(db *store.DB, events *hub.Hub, catalog []games.Factory) *runtime {
	index := make(map[string]games.Factory, len(catalog))
	for _, f := range catalog {
		if f.ID != "" && f.New != nil {
			index[f.ID] = f
		}
	}
	return &runtime{
		db:      db,
		events:  events,
		log:     applog.New(events, filepath.Join(db.Dir(), "host.log")),
		catalog: index,
	}
}

func (rt *runtime) gameIDs() []string {
	ids := make([]string, 0, len(rt.catalog))
	for id := range rt.catalog {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (rt *runtime) extras(ctx context.Context) lobby.SettingsExtras {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	auto, _ := rt.room.AutoPause(ctx)
	extra := lobby.SettingsExtras{
		GameIDs:      rt.gameIDs(),
		LoadedGameID: rt.loadedID,
		AutoPause:    auto,
		LogLines:     rt.log.Lines(),
	}
	if rt.game != nil && rt.game.Settings() != nil {
		var buf bytes.Buffer
		rec := &capture{buf: &buf, header: make(http.Header)}
		req := httptestGET("/settings/game/")
		rt.game.Settings().ServeHTTP(rec, req)
		if rec.status == 0 || rec.status == http.StatusOK {
			extra.GameSettings = template.HTML(buf.String())
		}
	}
	return extra
}

func (rt *runtime) phoneExtras(_ *http.Request, _ lobby.Player) lobby.PhoneExtras {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	showStart := rt.loadedID != "" && !rt.started
	if showStart {
		n, err := rt.room.SeatedCount(context.Background())
		if err != nil || n == 0 {
			showStart = false
		}
	}
	auto, _ := rt.room.AutoStart(context.Background())
	return lobby.PhoneExtras{
		ShowStart:    showStart,
		Started:      rt.started,
		AutoStart:    auto,
		GameIDs:      rt.gameIDs(),
		LoadedGameID: rt.loadedID,
	}
}

func (rt *runtime) load(ctx context.Context, id string) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.started {
		return errLoadRefused
	}
	factory, ok := rt.catalog[id]
	if !ok {
		return errLoadRefused
	}
	game := factory.New()
	if max := game.MaxPlayers(); max > 0 {
		n, err := rt.room.SeatedCount(ctx)
		if err != nil {
			return err
		}
		if n > max {
			return errLoadRefused
		}
	}
	if rt.game != nil {
		_ = rt.game.Shutdown()
	}
	prevID := rt.loadedID
	rt.loadedID = id
	rt.game = game
	if err := os.MkdirAll(filepath.Join(rt.db.Dir(), "games", id), 0o700); err != nil {
		rt.loadedID = prevID
		rt.game = nil
		return err
	}
	if err := game.Load(rt.helperLocked()); err != nil {
		rt.loadedID = prevID
		rt.game = nil
		return err
	}
	max := game.MaxPlayers()
	if err := rt.room.SetGamePlayerMax(ctx, max); err != nil {
		return err
	}
	if err := rt.room.SetSelectedGameID(ctx, id); err != nil {
		return err
	}
	if prevID != "" && prevID != id {
		_ = rt.room.ApplyReadyReset(ctx, lobby.ReadyResetSwitch)
	}
	rt.log.Write("Load " + id)
	return nil
}

func (rt *runtime) start(ctx context.Context) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.started {
		return nil
	}
	if rt.game == nil {
		return errNotLoaded
	}
	n, err := rt.room.SeatedCount(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return errStartRefused
	}
	if min := rt.game.MinPlayers(); min > 0 && n < min {
		return errStartRefused
	}
	if err := rt.game.Start(rt.helperLocked()); err != nil {
		return err
	}
	if err := rt.room.SetRoundActive(ctx, true); err != nil {
		return err
	}
	rt.started = true
	rt.log.Write("Start " + rt.loadedID)
	rt.events.Publish("roster")
	return nil
}

func (rt *runtime) stop(ctx context.Context, graceful bool) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.game == nil || !rt.started {
		return nil
	}
	if err := rt.game.Stop(); err != nil {
		return err
	}
	rt.started = false
	cycle := false
	if graceful {
		var err error
		cycle, err = rt.room.CycleSeats(ctx)
		if err != nil {
			return err
		}
	}
	if err := rt.room.EndRound(ctx, cycle); err != nil {
		return err
	}
	if err := rt.room.ApplyReadyReset(ctx, lobby.ReadyResetStop); err != nil {
		return err
	}
	rt.log.Write("Stop " + rt.loadedID)
	rt.events.Publish("roster")
	return nil
}

func (rt *runtime) shutdown(ctx context.Context) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.started && rt.game != nil {
		_ = rt.game.Stop()
		rt.started = false
		_ = rt.room.EndRound(ctx, false)
	}
	if rt.game != nil {
		_ = rt.game.Shutdown()
	}
	rt.game = nil
	rt.loadedID = ""
	if err := rt.room.SetSelectedGameID(ctx, ""); err != nil {
		return err
	}
	if err := rt.room.SetGamePlayerMax(ctx, 0); err != nil {
		return err
	}
	if err := rt.room.ApplyReadyReset(ctx, lobby.ReadyResetSwitch); err != nil {
		return err
	}
	rt.log.Write("Shutdown")
	rt.events.Publish("roster")
	return nil
}

func (rt *runtime) restore(ctx context.Context) {
	id, err := rt.room.SelectedGameID(ctx)
	if err != nil || id == "" {
		return
	}
	_ = rt.load(ctx, id)
}

func (rt *runtime) pause() error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.game == nil || !rt.started {
		return errNotLoaded
	}
	return rt.game.Pause()
}

func (rt *runtime) resume() error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.game == nil || !rt.started {
		return errNotLoaded
	}
	return rt.game.Resume()
}

func (rt *runtime) afterDisconnect(ctx context.Context, player lobby.Player) {
	if !player.Seated {
		return
	}
	rt.mu.Lock()
	started, game := rt.started, rt.game
	rt.mu.Unlock()
	if !started || game == nil {
		return
	}
	auto, err := rt.room.AutoPause(ctx)
	if err != nil || !auto {
		return
	}
	_ = game.Pause()
}

func (rt *runtime) markDisconnected(ctx context.Context, playerID string) error {
	if err := rt.room.SetConnected(ctx, playerID, false); err != nil {
		return err
	}
	rt.mu.Lock()
	started := rt.started
	game := rt.game
	rt.mu.Unlock()
	if !started || game == nil {
		rt.events.Publish("roster")
		return nil
	}
	players, err := rt.room.Players(ctx)
	if err != nil {
		return err
	}
	var seated bool
	for _, p := range players {
		if p.ID == playerID && p.Seated {
			seated = true
			break
		}
	}
	auto, err := rt.room.AutoPause(ctx)
	if err != nil {
		return err
	}
	if seated && auto {
		_ = game.Pause()
	}
	rt.events.Publish("roster")
	return nil
}

func (rt *runtime) helperLocked() games.Helper {
	return &helper{rt: rt}
}

func httptestGET(path string) *http.Request {
	req, err := http.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		panic(err)
	}
	return req
}

type capture struct {
	buf    *bytes.Buffer
	header http.Header
	status int
}

func (c *capture) Header() http.Header { return c.header }

func (c *capture) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.buf.Write(p)
}

func (c *capture) WriteHeader(status int) { c.status = status }

func (rt *runtime) wrapPhone(w http.ResponseWriter, r *http.Request, inner []byte) {
	rt.room.WritePlayPhone(w, r, template.HTML(inner))
}
