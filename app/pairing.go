package main

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"strings"
	"sync"
	"time"
)

const (
	pairCodeTTL       = 5 * time.Minute
	pairMaxFailures   = 5
	pairRateLimit     = 10
	pairRateWindow    = time.Minute
	crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

var (
	errPairInvalid     = errors.New("pairing code invalid or expired")
	errPairRateLimited = errors.New("pairing rate limited")
)

// pairingStore keeps the single active pairing code in memory. A new code
// replaces the previous one; each code works once and survives at most
// pairMaxFailures wrong guesses.
type pairingStore struct {
	mu       sync.Mutex
	now      func() time.Time
	code     string
	expires  time.Time
	failures int
	attempts map[string][]time.Time
}

func newPairingStore() *pairingStore {
	return &pairingStore{now: time.Now, attempts: map[string][]time.Time{}}
}

func newPairCode() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	value := uint64(b[0])<<32 | uint64(b[1])<<24 | uint64(b[2])<<16 | uint64(b[3])<<8 | uint64(b[4])
	out := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		out[i] = crockfordAlphabet[value&31]
		value >>= 5
	}
	return string(out)
}

// normalizePairCode applies Crockford decoding: case, dashes and spaces are
// ignored, and the look-alike letters O, I and L read as digits.
func normalizePairCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(code) {
		switch r {
		case '-', ' ':
			continue
		case 'O':
			r = '0'
		case 'I', 'L':
			r = '1'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func formatPairCode(code string) string { return code[:4] + "-" + code[4:] }

func (p *pairingStore) issue() (string, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.code = newPairCode()
	p.expires = p.now().Add(pairCodeTTL)
	p.failures = 0
	return formatPairCode(p.code), p.expires
}

func (p *pairingStore) redeem(ip, code string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for key, times := range p.attempts {
		kept := times[:0]
		for _, at := range times {
			if now.Sub(at) < pairRateWindow {
				kept = append(kept, at)
			}
		}
		if len(kept) == 0 {
			delete(p.attempts, key)
		} else {
			p.attempts[key] = kept
		}
	}
	if len(p.attempts[ip]) >= pairRateLimit {
		return errPairRateLimited
	}
	p.attempts[ip] = append(p.attempts[ip], now)
	if p.code == "" || !now.Before(p.expires) {
		p.code = ""
		return errPairInvalid
	}
	if subtle.ConstantTimeCompare([]byte(normalizePairCode(code)), []byte(p.code)) != 1 {
		p.failures++
		if p.failures >= pairMaxFailures {
			p.code = ""
		}
		return errPairInvalid
	}
	p.code = ""
	return nil
}
