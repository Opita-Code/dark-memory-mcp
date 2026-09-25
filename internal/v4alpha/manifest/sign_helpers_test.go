package manifest

import (
	"crypto/ed25519"
	"testing"
	"time"
)

// mustKeyPair returns a fresh Ed25519 keypair or fails the test.
func mustKeyPair(t *testing.T) (ed25519.PrivateKey, ed25519.PublicKey) {
	t.Helper()
	priv, pub, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return priv, pub
}

// mustUnixTime returns a UTC time at the given wall-clock fields.
func mustUnixTime(t *testing.T, y int, mo time.Month, d, h, mi, s int) time.Time {
	t.Helper()
	return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
}

// flipOneHexChar returns s with the first hex char replaced by a
// different one. Used by security tests that want to simulate a single
// byte of tamper. If s is empty or all '0', it flips the first char
// to '1'. Otherwise flips the first char to its successor (wrapping
// from 'f' to '0').
func flipOneHexChar(s string) string {
	if s == "" {
		return "1"
	}
	first := s[0]
	var next byte = '1'
	switch first {
	case '0':
		next = '1'
	case '1', '2', '3', '4', '5', '6', '7', '8', '9':
		next = first + 1
	case 'a':
		next = 'b'
	case 'b':
		next = 'c'
	case 'c':
		next = 'd'
	case 'd':
		next = 'e'
	case 'e':
		next = 'f'
	case 'f':
		next = '0'
	}
	return string(next) + s[1:]
}
