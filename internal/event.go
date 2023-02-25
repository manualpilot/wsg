package internal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/redis/go-redis/v9"
	"golang.org/x/exp/slog"
)

func DropHandler[T any](state *State, rdb *redis.Client, verifier func(r *http.Request) (string, *T)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := verifier(r)
		if id == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		rid := fmt.Sprintf("ws:%v", id)
		ctx := r.Context()

		state.Lock.RLock()
		defer state.Lock.RUnlock()

		connection, ok := state.Connections[id]
		if !ok {
			instanceID, err := rdb.HGet(ctx, rid, "inst").Result()
			if err != nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}

			event := Event{
				Type: EventTypeDrop,
				ID:   id,
			}

			bEvent, err := json.Marshal(event)
			if err != nil {
			}

			if err := rdb.Publish(ctx, instanceID, string(bEvent)).Err(); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			w.WriteHeader(http.StatusCreated)
			return
		}

		connection <- Message{
			Drop: true,
		}
	}
}

func WriteHandler[T any](
	state *State,
	rdb *redis.Client,
	verifier func(r *http.Request) (string, *T),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := verifier(r)
		if id == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		rid := fmt.Sprintf("ws:%v", id)
		ctx := r.Context()
		isBinary := r.Header.Get("Content-Type") == "application/octet-stream"

		b, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		state.Lock.RLock()
		defer state.Lock.RUnlock()

		connection, ok := state.Connections[id]
		if !ok {
			instanceID, err := rdb.Get(ctx, rid).Result()
			if err != nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}

			event := Event{
				Type:    EventTypeWrite,
				ID:      id,
				Binary:  isBinary,
				Payload: base64.RawURLEncoding.EncodeToString(b),
			}

			bEvent, err := json.Marshal(event)
			if err != nil {
			}

			if err := rdb.Publish(ctx, instanceID, string(bEvent)).Err(); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			w.WriteHeader(http.StatusCreated)
			return
		}

		connection <- Message{
			Binary: isBinary,
			Buffer: b,
		}

		w.WriteHeader(http.StatusOK)
		return
	}
}

func SubscribeEvents(ctx context.Context, logger *slog.Logger, state *State, rdb *redis.Client, instanceID string) {
	sub := rdb.Subscribe(ctx, instanceID)
	ch := sub.Channel()

	for {
		select {
		case <-ctx.Done():
			_ = sub.Close()
			return
		case msg := <-ch:
			b, err := base64.RawURLEncoding.DecodeString(msg.Payload)
			if err != nil {
				logger.Error("failed to decode cluster event", err)
				continue
			}

			event := Event{}
			if err := json.Unmarshal(b, &event); err != nil {
				logger.Error("failed to unmarshal cluster event", err)
				continue
			}

			switch event.Type {
			case EventTypeWrite:
				func() {
					b, err := base64.RawURLEncoding.DecodeString(event.Payload)
					if err != nil {
						slog.Warn("failed to decode payload", slog.String("connection", event.ID))
						return
					}

					state.Lock.RLock()
					defer state.Lock.RUnlock()

					connection, ok := state.Connections[event.ID]
					if !ok {
						slog.Warn("no such connection", slog.String("connection", event.ID))
						return
					}

					connection <- Message{
						Binary: event.Binary,
						Buffer: b,
					}
				}()
			case EventTypeDrop:
				func() {
					state.Lock.RLock()
					defer state.Lock.RUnlock()

					connection, ok := state.Connections[event.ID]
					if !ok {
						slog.Warn("no such connection", slog.String("connection", event.ID))
						return
					}

					connection <- Message{
						Drop: true,
					}
				}()
			default:
				logger.Warn("unknown event type", slog.String("event", string(event.Type)))
				continue
			}
		}
	}
}
