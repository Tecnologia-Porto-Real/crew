package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	kindMsg     = 4848  // regular: contract notices, questions, notes (expire in 7 days)
	kindState   = 34848 // addressable: one current state per device per room (expires in 24 h)
	roomTagName = "y"
)

// Free public relays validated with `crew probe` (accept and return crew's encrypted events).
var defaultRelays = []string{
	"wss://nos.lol",
	"wss://relay.primal.net",
	"wss://nostr.mom",
	"wss://relay.nostr.net",
	"wss://nostr.oxtr.dev",
	"wss://nostr.bitcoiner.social",
	"wss://nostr-pub.wellorder.net",
	"wss://purplerelay.com",
	"wss://offchain.pub",
}

// candidates tried by `crew probe` when refreshing the list above
var candidateRelays = append(append([]string{}, defaultRelays...),
	"wss://relay.damus.io", "wss://relay.nostr.bg", "wss://nostr.fmt.wiz.biz",
	"wss://relay.snort.social", "wss://relay.nostr.band", "wss://nostr.wine",
)

// roomRelays: rendezvous hashing — every teammate picks the same 3 relays without talking.
// A team can pin its own list in .claude/crew.json: {"relays": ["wss://..."]}.
func roomRelays(top, tag string) ([]string, bool) {
	if b, err := readSmall(filepath.Join(top, ".claude", "crew.json"), 64<<10); err == nil {
		var cfg struct {
			Relays []string `json:"relays"`
		}
		if json.Unmarshal(b, &cfg) == nil {
			var ok []string
			for _, u := range cfg.Relays {
				if validRelay(u) && len(ok) < 5 {
					ok = append(ok, u)
				}
			}
			if len(ok) > 0 {
				return ok, true
			}
		}
	}
	type sc struct {
		url   string
		score string
	}
	var l []sc
	for _, u := range defaultRelays {
		h := sha256.Sum256([]byte(tag + "|" + u))
		l = append(l, sc{u, fmt.Sprintf("%x", h)})
	}
	sort.Slice(l, func(i, j int) bool { return l[i].score > l[j].score })
	out := []string{}
	for i := 0; i < 3 && i < len(l); i++ {
		out = append(out, l[i].url)
	}
	return out, false
}

// validRelay: TLS websockets only (plain ws:// just for a relay on this machine).
func validRelay(u string) bool {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" || p.User != nil {
		return false
	}
	switch p.Scheme {
	case "wss":
		return true
	case "ws":
		h := p.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	}
	return false
}

// probe publishes one encrypted, expiring test event to each candidate relay and reads it back.
func cmdProbe() int {
	seed := hk([]byte(randomHex(32)), "crew/v1", "probe", 32)
	keys := keysFromSeed(seed)
	rm := Room{K: hk(seed, "crew/v1", "k", 32), Tag: randomHex(16)}
	now := time.Now()
	content, _ := rm.seal([]byte(`{"v":2,"type":"probe"}`), kindMsg, keys.Pub, now.Unix())
	ev := Event{CreatedAt: now.Unix(), Kind: kindMsg, Content: content,
		Tags: [][]string{{roomTagName, rm.Tag}, {"expiration", fmt.Sprint(time.Now().Add(10 * time.Minute).Unix())}}}
	keys.sign(&ev)
	for _, url := range candidateRelays {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		t0 := time.Now()
		r := publish(ctx, []string{url}, ev)[0]
		pubMs := time.Since(t0).Milliseconds()
		got := false
		if r.OK {
			for _, e := range query(ctx, []string{url}, Filter{"kinds": []int{kindMsg}, "#" + roomTagName: []string{rm.Tag}}) {
				if e.ID == ev.ID {
					got = true
				}
			}
		}
		cancel()
		status := "FAIL"
		if r.OK && got {
			status = "OK  "
		} else if r.OK {
			status = "SAVE?"
		}
		fmt.Printf("%s %-34s publish=%v (%d ms) readback=%v %s\n", status, url, r.OK, pubMs, got, strings.TrimSpace(r.Msg))
	}
	return 0
}
