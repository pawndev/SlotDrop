//go:build darwin

package secret

import (
	"bytes"
	"os"
	"testing"
)

func TestSealOpen(t *testing.T) {
	k := bytes.Repeat([]byte{7}, 32)
	enc, err := seal(k, []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(enc, []byte("hunter2")) {
		t.Fatal("the login is readable in the saved bytes")
	}
	if plain, err := open(k, enc); err != nil || string(plain) != "hunter2" {
		t.Fatalf("got %q, %v", plain, err)
	}
	if _, err := open(bytes.Repeat([]byte{8}, 32), enc); err == nil {
		t.Fatal("another key opened it")
	}
	enc[len(enc)-1] ^= 1
	if _, err := open(k, enc); err == nil {
		t.Fatal("a changed blob opened")
	}
}

// Uses the real login Keychain, under a test-only name, so it only runs
// when asked: SLOTDROP_KEYCHAIN_TEST=1 go test ./internal/secret
func TestKeychain(t *testing.T) {
	if os.Getenv("SLOTDROP_KEYCHAIN_TEST") == "" {
		t.Skip("set SLOTDROP_KEYCHAIN_TEST=1 to use the real Keychain")
	}
	keyService = "SlotDrop-test"
	keychainDelete()
	t.Cleanup(func() { keychainDelete() })

	if _, err := Unprotect([]byte{1}); err == nil {
		t.Fatal("unlocked with no key in the Keychain")
	}
	enc, err := Protect([]byte("token-123"))
	if err != nil {
		t.Fatal(err)
	}
	enc2, err := Protect([]byte("token-123"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range [][]byte{enc, enc2} {
		if plain, err := Unprotect(e); err != nil || string(plain) != "token-123" {
			t.Fatalf("got %q, %v", plain, err)
		}
	}
	keychainDelete()
	if _, err := Unprotect(enc); err == nil {
		t.Fatal("unlocked after the key was deleted")
	}
}
