package internal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"github.com/segmentio/ksuid"

	"nhooyr.io/websocket"
)

const defaultWaitTime = 100 * time.Millisecond

func TestE2E(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	signer := NewRequestSigner(privateKey)
	verifier := NewRequestVerifier(publicKey)

	kInstanceID, err := ksuid.NewRandom()
	if err != nil {
		t.Fatal(err)
	}

	instanceID := kInstanceID.String()
	connectionID := ""

	wroteTextMessage := false
	wroteBinaryMessage := false
	deleted := false

	dr := chi.NewRouter()
	dr.Get("/.well-known/public.txt", PublicKeyRoute(privateKey))
	dr.Get("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("param") == "" {
			t.Fatal("query params not forwarded")
		}

		if r.Header.Get("Test") == "" {
			t.Fatal("additional header no forwarded")
		}

		authToken := r.Header.Get("WebSocket-Gateway-Auth")
		if authToken == "" {
			t.Fatal("no auth token")
		}

		connectionID = verifier(r)
		if connectionID == "" {
			t.Fatal("no connection id")
		}

		w.WriteHeader(http.StatusOK)
		return
	})

	dr.Post("/", func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}

		if connectionID != verifier(r) {
			t.Fatal("no connection id")
		}

		ct := r.Header.Get("Content-Type")

		switch ct {
		case "text/plain":
			wroteTextMessage = true
		case "application/octet-stream":
			wroteBinaryMessage = true
		}

		w.WriteHeader(http.StatusOK)
		return
	})

	dr.Delete("/", func(w http.ResponseWriter, r *http.Request) {
		if connectionID != verifier(r) {
			t.Fatal("no connection id")
		}

		deleted = true

		w.WriteHeader(http.StatusOK)
		return
	})

	downstream := httptest.NewServer(dr)

	router, err := Main(ctx, instanceID, fmt.Sprintf("redis://%v", redisAddr), privateKey, downstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(router)
	defer server.Close()

	c := server.Client()
	u := server.URL + "?param=test#hash"

	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Test", "Test")

	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	//goland:noinspection GoUnhandledErrorResult
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatal("plain request did not require upgrade")
	}

	opts := &websocket.DialOptions{
		HTTPHeader: map[string][]string{
			"Test": {"Test"},
		},
	}

	// websocket -> downstream events

	conn, resp, err := websocket.Dial(ctx, u, opts)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(defaultWaitTime)
	res, err := rdb.Get(ctx, connectionID).Result()
	if err != nil {
		t.Fatal(err)
	}

	if res != instanceID {
		t.Error("connection not associated with instance")
	}

	if err := conn.Write(ctx, websocket.MessageText, []byte("a text message")); err != nil {
		t.Fatal("failed to write to socket")
	}

	time.Sleep(defaultWaitTime)
	if !wroteTextMessage {
		t.Error("did not send text message type correctly")
	}

	if err := conn.Write(ctx, websocket.MessageBinary, []byte("a binary message")); err != nil {
		t.Fatal("failed to write to socket")
	}

	time.Sleep(defaultWaitTime)
	if !wroteBinaryMessage {
		t.Error("did not send text message type correctly")
	}

	if err := conn.Close(websocket.StatusNormalClosure, "bye"); err != nil {
		t.Fatal(err)
	}

	time.Sleep(defaultWaitTime)
	if !deleted {
		t.Error("did not handle drop correctly")
	}

	if redis.Nil != rdb.Get(ctx, connectionID).Err() {
		t.Error("did not clean up connection")
	}

	// downstream -> websocket events

	conn, resp, err = websocket.Dial(ctx, u, opts)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(defaultWaitTime)

	req, err = http.NewRequest(http.MethodPost, u, bytes.NewReader([]byte("hi")))
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Content-Type", "text/plain")
	if err := signer(req, connectionID); err != nil {
		t.Fatal(err)
	}

	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Error("unexpected status code")
	}

	typ, b, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if typ != websocket.MessageText {
		t.Error("incorrect message type")
	}

	if !bytes.Equal(b, []byte("hi")) {
		t.Error("incorrect message")
	}

	req, err = http.NewRequest(http.MethodDelete, u, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := signer(req, connectionID); err != nil {
		t.Fatal(err)
	}

	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Error("unexpected status code")
	}

	_, _, err = conn.Read(ctx)
	if err == nil {
		t.Error("reading closed connection succeeded")
	}

	if !strings.Contains(err.Error(), "EOF") {
		t.Error("reading closed connection should have yielded EOF error")
	}
}
