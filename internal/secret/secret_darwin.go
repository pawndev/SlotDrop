//go:build darwin

// Package secret protects the saved RomM login. On macOS it's encrypted
// with AES-256-GCM under a random key kept in your login Keychain, so only
// your account on this Mac can unlock it; copying the settings file to
// another Mac or user gets nothing usable.
package secret

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <stdlib.h>
#include <string.h>
#include <Security/Security.h>

static CFMutableDictionaryRef sd_query(const char *service, const char *account) {
	CFMutableDictionaryRef q = CFDictionaryCreateMutable(NULL, 0, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CFStringRef s = CFStringCreateWithCString(NULL, service, kCFStringEncodingUTF8);
	CFStringRef a = CFStringCreateWithCString(NULL, account, kCFStringEncodingUTF8);
	CFDictionarySetValue(q, kSecClass, kSecClassGenericPassword);
	CFDictionarySetValue(q, kSecAttrService, s);
	CFDictionarySetValue(q, kSecAttrAccount, a);
	CFRelease(s);
	CFRelease(a);
	return q;
}

// sd_get copies the item's data into *out, which the caller frees.
static OSStatus sd_get(const char *service, const char *account, void **out, long *len) {
	CFMutableDictionaryRef q = sd_query(service, account);
	CFDictionarySetValue(q, kSecReturnData, kCFBooleanTrue);
	CFDictionarySetValue(q, kSecMatchLimit, kSecMatchLimitOne);
	CFTypeRef res = NULL;
	OSStatus st = SecItemCopyMatching(q, &res);
	CFRelease(q);
	if (st != errSecSuccess) {
		return st;
	}
	CFDataRef d = (CFDataRef)res;
	*len = CFDataGetLength(d);
	*out = malloc(*len > 0 ? *len : 1);
	memcpy(*out, CFDataGetBytePtr(d), *len);
	CFRelease(res);
	return st;
}

static OSStatus sd_add(const char *service, const char *account, const char *label, const void *data, long len) {
	CFMutableDictionaryRef q = sd_query(service, account);
	CFStringRef l = CFStringCreateWithCString(NULL, label, kCFStringEncodingUTF8);
	CFDataRef d = CFDataCreate(NULL, data, len);
	CFDictionarySetValue(q, kSecAttrLabel, l);
	CFDictionarySetValue(q, kSecValueData, d);
	OSStatus st = SecItemAdd(q, NULL);
	CFRelease(l);
	CFRelease(d);
	CFRelease(q);
	return st;
}

static OSStatus sd_delete(const char *service, const char *account) {
	CFMutableDictionaryRef q = sd_query(service, account);
	OSStatus st = SecItemDelete(q);
	CFRelease(q);
	return st;
}
*/
import "C"

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"unsafe"
)

var entropy = []byte("slot-label-maker/romm-login/v1")

// The Keychain item holding the key. A variable so tests can use their own.
var keyService = "SlotDrop"

const (
	keyAccount = "login-key"
	keyLabel   = "SlotDrop saved login key"

	errSecSuccess        = 0
	errSecUserCanceled   = -128
	errSecDuplicateItem  = -25299
	errSecItemNotFound   = -25300
	errSecAuthFailed     = -25293
	errSecInteractionNot = -25308
)

func keychainGet() ([]byte, int) {
	s, a := C.CString(keyService), C.CString(keyAccount)
	defer C.free(unsafe.Pointer(s))
	defer C.free(unsafe.Pointer(a))
	var out unsafe.Pointer
	var n C.long
	st := int(C.sd_get(s, a, &out, &n))
	if st != errSecSuccess {
		return nil, st
	}
	defer C.free(out)
	return C.GoBytes(out, C.int(n)), st
}

func keychainAdd(key []byte) int {
	s, a, l := C.CString(keyService), C.CString(keyAccount), C.CString(keyLabel)
	defer C.free(unsafe.Pointer(s))
	defer C.free(unsafe.Pointer(a))
	defer C.free(unsafe.Pointer(l))
	d := C.CBytes(key)
	defer C.free(d)
	return int(C.sd_add(s, a, l, d, C.long(len(key))))
}

func keychainDelete() int {
	s, a := C.CString(keyService), C.CString(keyAccount)
	defer C.free(unsafe.Pointer(s))
	defer C.free(unsafe.Pointer(a))
	return int(C.sd_delete(s, a))
}

func keychainError(st int) error {
	switch st {
	case errSecUserCanceled, errSecAuthFailed, errSecInteractionNot:
		return errors.New("SlotDrop wasn't allowed to use its key in the Keychain")
	case errSecItemNotFound:
		return errors.New("SlotDrop's key isn't in the Keychain any more")
	}
	return errors.New("the Keychain couldn't be used")
}

// key returns SlotDrop's key from the Keychain, making one if asked to.
func key(create bool) ([]byte, error) {
	k, st := keychainGet()
	if st == errSecItemNotFound && create {
		k = make([]byte, 32)
		if _, err := rand.Read(k); err != nil {
			return nil, err
		}
		if st = keychainAdd(k); st == errSecDuplicateItem { // made at the same moment elsewhere
			k, st = keychainGet()
		}
	}
	if st != errSecSuccess {
		return nil, keychainError(st)
	}
	if len(k) != 32 {
		return nil, errors.New("SlotDrop's key in the Keychain isn't usable")
	}
	return k, nil
}

func seal(key, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 1+gcm.NonceSize(), 1+gcm.NonceSize()+len(plain)+gcm.Overhead())
	out[0] = 1 // format version
	if _, err := rand.Read(out[1:]); err != nil {
		return nil, err
	}
	return gcm.Seal(out, out[1:], plain, entropy), nil
}

func open(key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(data) < 1+gcm.NonceSize() || data[0] != 1 {
		return nil, errors.New("the saved login isn't in a format this version understands")
	}
	nonce := data[1 : 1+gcm.NonceSize()]
	return gcm.Open(nil, nonce, data[1+gcm.NonceSize():], entropy)
}

// Available reports whether logins can be saved securely here.
func Available() bool { return true }

// Protect encrypts data with SlotDrop's Keychain key, making the key the
// first time.
func Protect(data []byte) ([]byte, error) {
	k, err := key(true)
	if err != nil {
		return nil, err
	}
	return seal(k, data)
}

// Unprotect decrypts what Protect made.
func Unprotect(data []byte) ([]byte, error) {
	k, err := key(false)
	if err != nil {
		return nil, err
	}
	return open(k, data)
}
