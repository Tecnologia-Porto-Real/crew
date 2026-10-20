package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Room holds the keys derived from the clone. K is shared by everyone who can read the repo.
type Room struct {
	K   []byte
	Tag string // hex, 16 bytes; the only thing about the room a relay sees
}

func hk(secret []byte, salt, info string, n int) []byte {
	out, err := hkdf.Key(sha256.New, secret, []byte(salt), info, n)
	if err != nil {
		panic(err)
	}
	return out
}

// deriveRoom: K = HKDF(root ‖ path ‖ extra), extra = committed .claude/crew.key and/or a local passphrase key.
func deriveRoom(r *Repo, passKey []byte) Room {
	var ikm bytes.Buffer
	ikm.WriteString(r.Root)
	ikm.WriteByte(0)
	ikm.WriteString(r.Path)
	ikm.WriteByte(0)
	if b, err := os.ReadFile(filepath.Join(r.Top, ".claude", "crew.key")); err == nil {
		ikm.Write(bytes.TrimSpace(b))
	}
	ikm.WriteByte(0)
	ikm.Write(passKey)
	k := hk(ikm.Bytes(), "crew/v1", "room-key", 32)
	return Room{K: k, Tag: hex.EncodeToString(hk(k, "crew/v1", "room-tag", 16))}
}

// passphraseKey stretches a team passphrase (only used for public repos, opt-in).
func passphraseKey(pass, path string) []byte {
	k, err := pbkdf2.Key(sha256.New, pass, []byte("crew/v1/"+path), 600000, 32)
	if err != nil {
		panic(err)
	}
	return k
}

func isoWeek(t time.Time) string {
	y, w := t.UTC().ISOWeek()
	return fmt.Sprintf("%dW%02d", y, w)
}

func (rm Room) weekKey(week string) []byte { return hk(rm.K, "crew/v1", "epoch:"+week, 32) }

// seal → "v1:<week>:<base64(nonce‖ciphertext)>". Plaintext is padded to a power of two (≥256 bytes).
func (rm Room) seal(plain []byte, kind int, pubkey string) (string, error) {
	week := isoWeek(time.Now())
	aead, err := newAEAD(rm.weekKey(week))
	if err != nil {
		return "", err
	}
	padded := pad(plain)
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := aead.Seal(nil, nonce, padded, rm.aad(kind, pubkey))
	return "v1:" + week + ":" + base64.StdEncoding.EncodeToString(append(nonce, ct...)), nil
}

func (rm Room) open(content string, kind int, pubkey string) ([]byte, error) {
	parts := strings.SplitN(content, ":", 3)
	if len(parts) != 3 || parts[0] != "v1" {
		return nil, errors.New("not a crew envelope")
	}
	// accept only the current and previous few weeks; avoids decrypting stale or forged epochs forever
	now := time.Now()
	ok := false
	for d := 0; d <= 21; d += 7 {
		if isoWeek(now.AddDate(0, 0, -d)) == parts[1] {
			ok = true
		}
	}
	if !ok {
		return nil, errors.New("epoch out of range")
	}
	raw, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(rm.weekKey(parts[1]))
	if err != nil {
		return nil, err
	}
	if len(raw) < aead.NonceSize() {
		return nil, errors.New("short")
	}
	pt, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], rm.aad(kind, pubkey))
	if err != nil {
		return nil, err
	}
	return unpad(pt)
}

func (rm Room) aad(kind int, pubkey string) []byte {
	return []byte(fmt.Sprintf("%s|%d|%s", rm.Tag, kind, pubkey))
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

func pad(p []byte) []byte {
	n := 256
	for n < len(p)+4 {
		n *= 2
	}
	out := make([]byte, n)
	binary.BigEndian.PutUint32(out, uint32(len(p)))
	copy(out[4:], p)
	return out
}

func unpad(p []byte) ([]byte, error) {
	if len(p) < 4 {
		return nil, errors.New("bad padding")
	}
	n := binary.BigEndian.Uint32(p)
	if int(n) > len(p)-4 {
		return nil, errors.New("bad padding")
	}
	return p[4 : 4+n], nil
}

// deviceSecret is created once per machine and never leaves it.
func deviceSecret(dataDir string) ([]byte, error) {
	p := filepath.Join(dataDir, "device.key")
	if b, err := os.ReadFile(p); err == nil && len(b) >= 64 {
		return hex.DecodeString(strings.TrimSpace(string(b))[:64])
	}
	s := make([]byte, 32)
	if _, err := rand.Read(s); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, []byte(hex.EncodeToString(s)), 0o600); err != nil {
		return nil, err
	}
	return s, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
