package host

import (
	"net/http"

	"github.com/KroniK907/hackbox/internal/games"
)

type fakeGame struct {
	min, max                int
	loaded, started, paused bool
	stopped, shutdown       bool
	helper                  games.Helper
	settingsHits, playHits  int
}

func (f *fakeGame) ID() string      { return "fake" }
func (f *fakeGame) MinPlayers() int { return f.min }
func (f *fakeGame) MaxPlayers() int { return f.max }

func (f *fakeGame) Load(h games.Helper) error {
	f.helper = h
	f.loaded = true
	f.shutdown = false
	return nil
}

func (f *fakeGame) Settings() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.settingsHits++
		if r.Method == http.MethodPost {
			_ = f.helper.KVSet("note", []byte("saved"))
			http.Redirect(w, r, "/settings", http.StatusSeeOther)
			return
		}
		_, _ = w.Write([]byte(`<p>fake-settings</p><form method="post" action="/settings/game/save"><button>Save knob</button></form>`))
	})
}

func (f *fakeGame) Start(h games.Helper) error {
	f.helper = h
	f.started = true
	f.paused = false
	return nil
}

func (f *fakeGame) Board(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("FAKE-BOARD"))
	if f.paused {
		_, _ = w.Write([]byte(" PAUSED"))
	}
	if f.helper != nil {
		seated := f.helper.Seated()
		if len(seated) > 0 && seated[0].LastHeartbeatRTT == 0 {
			_, _ = w.Write([]byte(" rtt-zero=yes"))
		}
	}
}

func (f *fakeGame) Phone(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("FAKE-PHONE"))
}

func (f *fakeGame) Play() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.playHits++
		_, _ = w.Write([]byte("play-ok"))
	})
}

func (f *fakeGame) Pause() error {
	f.paused = true
	return nil
}

func (f *fakeGame) Resume() error {
	f.paused = false
	return nil
}

func (f *fakeGame) Stop() error {
	f.started = false
	f.stopped = true
	return nil
}

func (f *fakeGame) Shutdown() error {
	f.loaded = false
	f.shutdown = true
	return nil
}
