package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/AcSunday/cocogem/internal/store"
	w "github.com/AcSunday/cocogem/internal/workspace"
)

func (s *Server) events(rw http.ResponseWriter, r *http.Request) {
	afterText := r.URL.Query().Get("after")
	if header := r.Header.Get("Last-Event-ID"); header != "" {
		afterText = header
	}
	var after int64
	if afterText != "" {
		v, err := strconv.ParseInt(afterText, 10, 64)
		if err != nil || v < 0 {
			respond(rw, r, nil, errors.New("事件 cursor 无效"))
			return
		}
		after = v
	}
	if err := s.visibleRunRead(r, r.PathValue("id")); err != nil {
		respond(rw, r, nil, err)
		return
	}
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	rw.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(rw)
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		s.auth.mu.Lock()
		_, authenticated := s.auth.session(r)
		s.auth.mu.Unlock()
		if !authenticated {
			return
		}
		wake := s.store.Changed()
		var events []w.Event
		var terminal bool
		err := s.store.Read(r.Context(), func(tx *store.Tx) error {
			run, err := tx.Run(r.PathValue("id"))
			if err != nil {
				return err
			}
			terminal = w.Terminal(run.Status)
			events, err = tx.Events(run.ID, after)
			return err
		})
		if err != nil {
			return
		}
		_ = controller.SetWriteDeadline(time.Now().Add(20 * time.Second))
		for _, ev := range events {
			b, _ := json.Marshal(ev)
			if _, err := fmt.Fprintf(rw, "id: %d\ndata: %s\n\n", ev.Seq, b); err != nil {
				return
			}
			after = ev.Seq
		}
		if err := controller.Flush(); err != nil {
			return
		}
		if len(events) == 500 {
			continue
		}
		if terminal {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-wake:
		case <-heartbeat.C:
			if _, err := fmt.Fprint(rw, ": heartbeat\n\n"); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		}
	}
}

func (s *Server) eventHistory(rw http.ResponseWriter, r *http.Request) {
	after := int64(0)
	if text := r.URL.Query().Get("after"); text != "" {
		value, err := strconv.ParseInt(text, 10, 64)
		if err != nil || value < 0 {
			respond(rw, r, nil, errors.New("事件 cursor 无效"))
			return
		}
		after = value
	}
	limit, err := queryLimit(r, store.DefaultEventLimit, store.MaxEventLimit)
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	var page store.EventPage
	err = s.store.Read(r.Context(), func(tx *store.Tx) error {
		run, err := visibleRun(tx, r.PathValue("id"))
		if err != nil {
			return err
		}
		page, err = tx.EventPage(run.ID, after, limit)
		return err
	})
	respond(rw, r, page, err)
}

func (s *Server) messageContent(rw http.ResponseWriter, r *http.Request) {
	limit, err := queryLimit(r, store.DefaultContentChunks, store.MaxContentChunks)
	if err != nil {
		respond(rw, r, nil, err)
		return
	}
	var page store.MessageContentPage
	err = s.store.Read(r.Context(), func(tx *store.Tx) error {
		run, err := visibleRun(tx, r.PathValue("id"))
		if err != nil {
			return err
		}
		message, err := tx.Message(r.PathValue("messageID"))
		if err != nil {
			return err
		}
		// A message is addressable only through its own run and thread; never
		// let a cursor or an ID reach another run's body.
		if message.RunID != run.ID || message.ThreadID != run.ThreadID {
			return store.ErrNotFound
		}
		page, err = tx.MessageContent(message.ID, r.URL.Query().Get("cursor"), limit)
		return err
	})
	if errors.Is(err, store.ErrInvalidCursor) {
		writeError(rw, r, 400, "invalid_cursor", "正文 cursor 无效")
		return
	}
	respond(rw, r, page, err)
}
