package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
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
func roomRelays(top, tag string) []string {
	if b, err := os.ReadFile(filepath.Join(top, ".claude", "crew.json")); err == nil {
		var cfg struct {
			Relays []string `json:"relays"`
		}
		if json.Unmarshal(b, &cfg) == nil && len(cfg.Relays) > 0 {
			return cfg.Relays
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
	return out
}

// probe publishes one encrypted, expiring test event to each candidate relay and reads it back.
func cmdProbe() int {
	seed := hk([]byte(randomHex(32)), "crew/v1", "probe", 32)
	keys := keysFromSeed(seed)
	rm := Room{K: hk(seed, "crew/v1", "k", 32), Tag: randomHex(16)}
	content, _ := rm.seal([]byte(`{"v":1,"tipo":"probe"}`), kindMsg, keys.Pub)
	ev := Event{CreatedAt: time.Now().Unix(), Kind: kindMsg, Content: content,
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
