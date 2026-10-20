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
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const proto = "crew/v2"

// Room holds the keys derived from the clone. K is shared by everyone who can read the repo.
type Room struct {
	K   []byte
	Tag string // internal room id (never published); relays only see the weekly tag
}

func hk(secret []byte, salt, info string, n int) []byte {
	out, err := hkdf.Key(sha256.New, secret, []byte(salt), info, n)
	if err != nil {
		panic(err)
	}
	return out
}

// readSmall reads a regular file from the repository, refusing symlinks and anything over max bytes:
// a malicious repo could point .claude files at /dev/zero or at the user's own files.
func readSmall(p string, max int64) ([]byte, error) {
	st, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max))
}

// deriveRoom: K = HKDF(root ‖ path ‖ extra), extra = committed .claude/crew.key and/or a local passphrase key.
func deriveRoom(r *Repo, passKey []byte) Room {
	var ikm bytes.Buffer
	ikm.WriteString(r.Root)
	ikm.WriteByte(0)
	ikm.WriteString(r.Path)
	ikm.WriteByte(0)
	if b, err := readSmall(filepath.Join(r.Top, ".claude", "crew.key"), 4096); err == nil {
		ikm.Write(bytes.TrimSpace(b))
	}
	ikm.WriteByte(0)
	ikm.Write(passKey)
	k := hk(ikm.Bytes(), proto, "room-key", 32)
	return Room{K: k, Tag: hex.EncodeToString(hk(k, proto, "room-id", 16))}
}

func hasRoomKeyFile(r *Repo) bool {
	b, err := readSmall(filepath.Join(r.Top, ".claude", "crew.key"), 4096)
	return err == nil && len(bytes.TrimSpace(b)) >= 32
}

// passphraseKey stretches a team passphrase (only used for public repos, opt-in).
func passphraseKey(pass, path string) []byte {
	k, err := pbkdf2.Key(sha256.New, pass, []byte(proto+"/"+path), 600000, 32)
	if err != nil {
		panic(err)
	}
	return k
}

func isoWeek(t time.Time) string {
	y, w := t.UTC().ISOWeek()
	return fmt.Sprintf("%dW%02d", y, w)
}

// tagFor: the public room tag rotates every ISO week, so relays and observers can't follow a room for long.
func (rm Room) tagFor(t time.Time) string {
	return hex.EncodeToString(hk(rm.K, proto, "room-tag:"+isoWeek(t), 16))
}

// liveTags: tags under which messages of the last 7 days may have been published.
func (rm Room) liveTags() []string {
	now := time.Now()
	a, b := rm.tagFor(now), rm.tagFor(now.Add(-msgTTL))
	if a == b {
		return []string{a}
	}
	return []string{a, b}
}

func (rm Room) weekKey(t time.Time) []byte { return hk(rm.K, proto, "epoch:"+isoWeek(t), 32) }

const maxContent = 32 << 10 // crew events are a few KB; anything bigger is junk

// seal → base64(nonce ‖ ciphertext), keyed by the event's own week. Plaintext is padded to a power of two.
func (rm Room) seal(plain []byte, kind int, pubkey string, createdAt int64) (string, error) {
	aead, err := newAEAD(rm.weekKey(time.Unix(createdAt, 0)))
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := aead.Seal(nil, nonce, pad(plain), rm.aad(kind, pubkey, createdAt))
	return base64.StdEncoding.EncodeToString(append(nonce, ct...)), nil
}

func (rm Room) open(content string, kind int, pubkey string, createdAt int64) ([]byte, error) {
	if len(content) > maxContent {
		return nil, errors.New("too large")
	}
	raw, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(rm.weekKey(time.Unix(createdAt, 0)))
	if err != nil {
		return nil, err
	}
	if len(raw) < aead.NonceSize()+aead.Overhead() {
		return nil, errors.New("short")
	}
	pt, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], rm.aad(kind, pubkey, createdAt))
	if err != nil {
		return nil, err
	}
	return unpad(pt)
}

func (rm Room) aad(kind int, pubkey string, createdAt int64) []byte {
	return []byte(fmt.Sprintf("%s|%s|%d|%s|%d", proto, rm.Tag, kind, pubkey, createdAt))
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
	rand.Read(out[4+len(p):])
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

// deviceSecret is created once per machine and never leaves it. Creation is exclusive so two hooks
// starting together can't give one device two identities.
func deviceSecret(dataDir string) ([]byte, error) {
	p := filepath.Join(dataDir, "device.key")
	read := func() ([]byte, error) {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		s := strings.TrimSpace(string(b))
		if len(s) != 64 {
			return nil, errors.New("device key incomplete")
		}
		return hex.DecodeString(s)
	}
	if k, err := read(); err == nil {
		return k, nil
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	s := make([]byte, 32)
	if _, err := rand.Read(s); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		_, werr := f.WriteString(hex.EncodeToString(s))
		f.Close()
		if werr != nil {
			return nil, werr
		}
		return s, nil
	}
	// someone else created it at the same moment: wait for their write to land
	for i := 0; i < 40; i++ {
		if k, err := read(); err == nil {
			return k, nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return nil, errors.New("device key unreadable")
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
