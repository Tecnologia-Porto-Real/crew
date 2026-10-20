package main

// Minimal Nostr client (NIP-01): signed events, publish, one-shot query and live subscription.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
	"github.com/coder/websocket"
)

type Event struct {
	ID        string     `json:"id"`
	PubKey    string     `json:"pubkey"`
	CreatedAt int64      `json:"created_at"`
	Kind      int        `json:"kind"`
	Tags      [][]string `json:"tags"`
	Content   string     `json:"content"`
	Sig       string     `json:"sig"`
}

type Keys struct {
	priv *btcec.PrivateKey
	Pub  string
}

func keysFromSeed(seed []byte) Keys {
	priv, _ := btcec.PrivKeyFromBytes(seed)
	return Keys{priv: priv, Pub: hex.EncodeToString(schnorr.SerializePubKey(priv.PubKey()))}
}

func marshalNoEscape(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return bytes.TrimRight(b.Bytes(), "\n")
}

func (e *Event) hash() [32]byte {
	return sha256.Sum256(marshalNoEscape([]any{0, e.PubKey, e.CreatedAt, e.Kind, e.Tags, e.Content}))
}

func (k Keys) sign(e *Event) error {
	e.PubKey = k.Pub
	if e.Tags == nil {
		e.Tags = [][]string{}
	}
	h := e.hash()
	e.ID = hex.EncodeToString(h[:])
	sig, err := schnorr.Sign(k.priv, h[:])
	if err != nil {
		return err
	}
	e.Sig = hex.EncodeToString(sig.Serialize())
	return nil
}

func (e *Event) verify() bool {
	h := e.hash()
	if hex.EncodeToString(h[:]) != e.ID {
		return false
	}
	pk, err := hex.DecodeString(e.PubKey)
	if err != nil {
		return false
	}
	pub, err := schnorr.ParsePubKey(pk)
	if err != nil {
		return false
	}
	sb, err := hex.DecodeString(e.Sig)
	if err != nil {
		return false
	}
	sig, err := schnorr.ParseSignature(sb)
	if err != nil {
		return false
	}
	return sig.Verify(h[:], pub)
}

func (e *Event) tag(name string) string {
	for _, t := range e.Tags {
		if len(t) >= 2 && t[0] == name {
			return t[1]
		}
	}
	return ""
}

// ---------- relay I/O ----------

func dial(ctx context.Context, url string) (*websocket.Conn, error) {
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: map[string][]string{"User-Agent": {"crew-plugin"}}})
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(1 << 20)
	return c, nil
}

func send(ctx context.Context, c *websocket.Conn, v any) error {
	return c.Write(ctx, websocket.MessageText, marshalNoEscape(v))
}

func recv(ctx context.Context, c *websocket.Conn) ([]json.RawMessage, error) {
	_, data, err := c.Read(ctx)
	if err != nil {
		return nil, err
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(data, &arr); err != nil || len(arr) == 0 {
		return nil, errors.New("bad frame")
	}
	return arr, nil
}

func str(m json.RawMessage) string {
	var s string
	json.Unmarshal(m, &s)
	return s
}

type PubResult struct {
	Relay string
	OK    bool
	Msg   string
}

// publish sends ev to every relay concurrently and waits for each OK (or the timeout).
func publish(ctx context.Context, relays []string, ev Event) []PubResult {
	res := make([]PubResult, len(relays))
	var wg sync.WaitGroup
	for i, url := range relays {
		wg.Add(1)
		go func(i int, url string) {
			defer wg.Done()
			res[i] = PubResult{Relay: url}
			c, err := dial(ctx, url)
			if err != nil {
				res[i].Msg = err.Error()
				return
			}
			defer c.Close(websocket.StatusNormalClosure, "")
			if err := send(ctx, c, []any{"EVENT", ev}); err != nil {
				res[i].Msg = err.Error()
				return
			}
			for {
				arr, err := recv(ctx, c)
				if err != nil {
					res[i].Msg = err.Error()
					return
				}
				if str(arr[0]) == "OK" && len(arr) >= 3 && str(arr[1]) == ev.ID {
					json.Unmarshal(arr[2], &res[i].OK)
					if len(arr) >= 4 {
						res[i].Msg = str(arr[3])
					}
					return
				}
			}
		}(i, url)
	}
	wg.Wait()
	return res
}

type Filter map[string]any

// query asks every relay for stored events matching f, until EOSE or timeout, and merges by id.
func query(ctx context.Context, relays []string, f Filter) []Event {
	var mu sync.Mutex
	seen := map[string]bool{}
	var out []Event
	var wg sync.WaitGroup
	for _, url := range relays {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			c, err := dial(ctx, url)
			if err != nil {
				return
			}
			defer c.Close(websocket.StatusNormalClosure, "")
			sub := "q" + randomHex(4)
			if send(ctx, c, []any{"REQ", sub, f}) != nil {
				return
			}
			for {
				arr, err := recv(ctx, c)
				if err != nil {
					return
				}
				switch str(arr[0]) {
				case "EVENT":
					var ev Event
					if len(arr) >= 3 && json.Unmarshal(arr[2], &ev) == nil && ev.verify() {
						mu.Lock()
						if !seen[ev.ID] {
							seen[ev.ID] = true
							out = append(out, ev)
						}
						mu.Unlock()
					}
				case "EOSE", "CLOSED":
					send(ctx, c, []any{"CLOSE", sub})
					return
				}
			}
		}(url)
	}
	wg.Wait()
	return out
}

// subscribe keeps a live subscription on every relay (reconnecting with backoff) until ctx ends.
// onEvent may be called concurrently from several relays; duplicates are filtered here.
func subscribe(ctx context.Context, relays []string, f func() Filter, onEvent func(Event)) {
	var mu sync.Mutex
	seen := map[string]bool{}
	var wg sync.WaitGroup
	for _, url := range relays {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			backoff := 2 * time.Second
			for ctx.Err() == nil {
				err := func() error {
					c, err := dial(ctx, url)
					if err != nil {
						return err
					}
					defer c.Close(websocket.StatusNormalClosure, "")
					sub := "s" + randomHex(4)
					if err := send(ctx, c, []any{"REQ", sub, f()}); err != nil {
						return err
					}
					backoff = 2 * time.Second
					for {
						arr, err := recv(ctx, c)
						if err != nil {
							return err
						}
						switch str(arr[0]) {
						case "EVENT":
							var ev Event
							if len(arr) >= 3 && json.Unmarshal(arr[2], &ev) == nil && ev.verify() {
								mu.Lock()
								dup := seen[ev.ID]
								seen[ev.ID] = true
								mu.Unlock()
								if !dup {
									onEvent(ev)
								}
							}
						case "CLOSED":
							msg := ""
							if len(arr) >= 3 {
								msg = str(arr[2])
							}
							return fmt.Errorf("closed: %s", msg)
						}
					}
				}()
				if ctx.Err() != nil {
					return
				}
				if err != nil && (strings.Contains(err.Error(), "blocked") || strings.Contains(err.Error(), "restricted")) {
					return // relay refuses us; stay off it for this run
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				if backoff < 2*time.Minute {
					backoff *= 2
				}
			}
		}(url)
	}
	wg.Wait()
}
