package signature

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func computeHMACSHA256(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestHMACVerifier_ValidSignature(t *testing.T) {
	body := []byte(`{"event":"push"}`)
	secret := "mysecret"
	sig := computeHMACSHA256(secret, string(body))

	v := &HMACVerifier{Encoding: "hex"}
	if err := v.Verify(sig, secret, body); err != nil {
		t.Fatalf("expected valid signature: %v", err)
	}
}

func TestHMACVerifier_WithPrefix(t *testing.T) {
	body := []byte(`{"event":"push"}`)
	secret := "mysecret"
	sig := "sha256=" + computeHMACSHA256(secret, string(body))

	v := &HMACVerifier{Prefix: "sha256=", Encoding: "hex"}
	if err := v.Verify(sig, secret, body); err != nil {
		t.Fatalf("expected valid signature: %v", err)
	}
}

func TestHMACVerifier_InvalidSignature(t *testing.T) {
	body := []byte(`{"event":"push"}`)
	v := &HMACVerifier{Encoding: "hex"}
	// Valid hex but wrong signature.
	if err := v.Verify("abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", "secret", body); err == nil {
		t.Fatal("expected error for invalid signature")
	}
}

func TestHMACVerifier_NearMissSignature(t *testing.T) {
	body := []byte(`{"event":"push"}`)
	secret := "mysecret"
	sig := computeHMACSHA256(secret, string(body))

	// Flip the last character to create a near-miss.
	sigBytes := []byte(sig)
	if sigBytes[len(sigBytes)-1] == 'a' {
		sigBytes[len(sigBytes)-1] = 'b'
	} else {
		sigBytes[len(sigBytes)-1] = 'a'
	}

	v := &HMACVerifier{Encoding: "hex"}
	if err := v.Verify(string(sigBytes), secret, body); err == nil {
		t.Fatal("expected error for near-miss signature")
	}
}

func TestHMACVerifier_EmptyHeader(t *testing.T) {
	v := &HMACVerifier{Encoding: "hex"}
	if err := v.Verify("", "secret", []byte("body")); err == nil {
		t.Fatal("expected error for empty header")
	}
}

func TestHMACVerifier_EmptyBody(t *testing.T) {
	secret := "mysecret"
	body := []byte{}
	sig := computeHMACSHA256(secret, "")

	v := &HMACVerifier{Encoding: "hex"}
	if err := v.Verify(sig, secret, body); err != nil {
		t.Fatalf("expected valid signature for empty body: %v", err)
	}
}

func TestHMACVerifier_MissingPrefix(t *testing.T) {
	v := &HMACVerifier{Prefix: "sha256=", Encoding: "hex"}
	if err := v.Verify("noprefixhere", "secret", []byte("body")); err == nil {
		t.Fatal("expected error when prefix is missing from header")
	}
}

func TestHMACVerifier_InvalidHex(t *testing.T) {
	v := &HMACVerifier{Encoding: "hex"}
	if err := v.Verify("not-valid-hex!!!", "secret", []byte("body")); err == nil {
		t.Fatal("expected error for invalid hex")
	}
}

func TestHMACVerifier_UnsupportedEncoding(t *testing.T) {
	v := &HMACVerifier{Encoding: "base64"}
	if err := v.Verify("dGVzdA==", "secret", []byte("body")); err == nil {
		t.Fatal("expected error for unsupported encoding")
	}
}
