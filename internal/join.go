package internal

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/segmentio/ksuid"
	"golang.org/x/exp/slog"

	"nhooyr.io/websocket"
	"io"
)

func JoinRoute[T any](
	state *State,
	logger *slog.Logger,
	rdb *redis.Client,
	signer func(r *http.Request, id string, meta *T) error,
	instanceID, downstream, serviceDomain string,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		now := time.Now()

		kid, err := ksuid.NewRandom()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		id := kid.String()
		rid := fmt.Sprintf("ws:%v", id)
		log := logger.With(slog.String("id", id))
		hc := http.Client{Timeout: 30 * time.Second}

		req, err := http.NewRequest(http.MethodGet, downstream, nil)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if err := signer(r, id, nil); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		for key, value := range r.Header {
			req.Header.Add(key, strings.Join(value, ","))
		}

		params := url.Values{}
		for key, value := range r.URL.Query() {
			params.Add(key, strings.Join(value, ","))
		}

		req.URL.RawQuery = params.Encode()

		resp, err := hc.Do(req)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}

		// TODO: could we close this earlier?
		//goland:noinspection GoUnhandledErrorResult
		defer resp.Body.Close()

		meta, err := io.ReadAll(resp.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
			w.WriteHeader(resp.StatusCode)
			return
		}

		overrideID := resp.Header.Get("WebSocket-Gateway-Override-ID")
		if overrideID != "" {
			id = overrideID
		}

		opts := &websocket.AcceptOptions{
			OriginPatterns: []string{serviceDomain},
		}

		conn, err := websocket.Accept(w, r, opts)
		if err != nil {
			return
		}

		msgChan := make(chan Message)

		state.Lock.Lock()
		state.Connections[id] = msgChan
		state.Lock.Unlock()

		data := map[string]string{
			"inst": instanceID,
			"join": strconv.Itoa(int(now.Unix())),
			"recv": "0",
			"sent": "0",
		}

		if err := rdb.HSet(ctx, rid, data).Err(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if err := rdb.Expire(ctx, rid, 90*time.Second).Err(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		defer func() {
			state.Lock.Lock()
			defer state.Lock.Unlock()
			delete(state.Connections, id)
			close(msgChan)
			if err := rdb.Del(context.Background(), rid).Err(); err != nil {
				log.Error("failed to cleanup", err)
			}

			req, err := http.NewRequest(http.MethodDelete, downstream, nil)
			if err != nil {
				return
			}

			if err := signer(req, id, nil); err != nil {
				return
			}

			if len(meta) > 0 {
				req.Header.Set("Websocket-Gateway-Meta", string(meta))
			}

			resp, err := hc.Do(req)
			if err != nil {
				return
			}

			//goland:noinspection GoUnhandledErrorResult
			defer resp.Body.Close()
		}()

		go func() {
			defer cancel()
			for {
				typ, b, err := conn.Read(ctx)
				if err != nil {
					return
				}

				if err := rdb.HIncrBy(ctx, rid, "recv", 1).Err(); err != nil {
					log.Error("failed to update received messages stats", err)
					return
				}

				req, err := http.NewRequest(http.MethodPost, downstream, bytes.NewReader(b))
				if err != nil {
					return
				}

				if err := signer(req, id, nil); err != nil {
					return
				}

				if len(meta) > 0 {
					req.Header.Set("Websocket-Gateway-Meta", string(meta))
				}

				if typ == websocket.MessageBinary {
					req.Header.Set("Content-Type", "application/octet-stream")
				} else {
					req.Header.Set("Content-Type", "text/plain")
				}

				resp, err := hc.Do(req)
				if err != nil {
					return
				}

				//goland:noinspection GoUnhandledErrorResult
				resp.Body.Close()
			}
		}()

		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(45 * time.Second):
					if err := conn.Ping(ctx); err != nil {
						log.Error("failed to ping", err)
						_ = conn.Close(websocket.StatusAbnormalClosure, "hello?")
						return
					}

					if err := rdb.Expire(ctx, rid, 60*time.Second).Err(); err != nil {
						log.Error("failed extend exp", err)
						_ = conn.Close(websocket.StatusAbnormalClosure, "it broke")
						return
					}
				}
			}
		}()

		for {
			select {
			case <-ctx.Done():
				log.Info("left")
				return
			case msg := <-msgChan:
				if msg.Drop {
					return
				}

				typ := websocket.MessageText
				if msg.Binary {
					typ = websocket.MessageBinary
				}

				if err := conn.Write(ctx, typ, msg.Buffer); err != nil {
					log.Error("failed to write message", err)
					return
				}

				if err := rdb.HIncrBy(ctx, rid, "sent", 1).Err(); err != nil {
					log.Error("failed to update sent messages stats", err)
					return
				}
			}
		}
	}
}
