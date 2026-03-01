package signature

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

func stripeSign(t *testing.T, secret string, body []byte, ts int64) string {
	t.Helper()
	signed := fmt.Sprintf("%d.%s", ts, string(body))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signed))
	sig := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("t=%d,v1=%s", ts, sig)
}

func TestStripeVerifier_ValidSignature(t *testing.T) {
	body := []byte(`{"id":"evt_123"}`)
	secret := "whsec_test"
	now := time.Now()
	ts := now.Unix()
	header := stripeSign(t, secret, body, ts)

	v := &StripeVerifier{
		Tolerance: 5 * time.Minute,
		Now:       func() time.Time { return now },
	}
	if err := v.Verify(header, secret, body); err != nil {
		t.Fatalf("expected valid: %v", err)
	}
}

func TestStripeVerifier_InvalidSignature(t *testing.T) {
	body := []byte(`{"id":"evt_123"}`)
	now := time.Now()
	header := fmt.Sprintf("t=%d,v1=0000000000000000000000000000000000000000000000000000000000000000", now.Unix())

	v := &StripeVerifier{
		Tolerance: 5 * time.Minute,
		Now:       func() time.Time { return now },
	}
	if err := v.Verify(header, "whsec_test", body); err == nil {
		t.Fatal("expected error for invalid signature")
	}
}

func TestStripeVerifier_ExpiredTimestamp(t *testing.T) {
	body := []byte(`{"id":"evt_123"}`)
	secret := "whsec_test"
	now := time.Now()
	// Timestamp 10 minutes in the past, tolerance is 5 minutes.
	ts := now.Add(-10 * time.Minute).Unix()
	header := stripeSign(t, secret, body, ts)

	v := &StripeVerifier{
		Tolerance: 5 * time.Minute,
		Now:       func() time.Time { return now },
	}
	if err := v.Verify(header, secret, body); err == nil {
		t.Fatal("expected error for expired timestamp")
	}
}

func TestStripeVerifier_MultipleV1_OneValid(t *testing.T) {
	body := []byte(`{"id":"evt_123"}`)
	secret := "whsec_test"
	now := time.Now()
	ts := now.Unix()

	signed := fmt.Sprintf("%d.%s", ts, string(body))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signed))
	validSig := hex.EncodeToString(mac.Sum(nil))

	// Header with an invalid v1 first, then the valid one.
	header := fmt.Sprintf("t=%d,v1=0000000000000000000000000000000000000000000000000000000000000000,v1=%s", ts, validSig)

	v := &StripeVerifier{
		Tolerance: 5 * time.Minute,
		Now:       func() time.Time { return now },
	}
	if err := v.Verify(header, secret, body); err != nil {
		t.Fatalf("expected valid when any v1 matches: %v", err)
	}
}

func TestStripeVerifier_MissingTimestamp(t *testing.T) {
	v := &StripeVerifier{Tolerance: 5 * time.Minute}
	if err := v.Verify("v1=abc123", "secret", []byte("body")); err == nil {
		t.Fatal("expected error for missing t=")
	}
}

func TestStripeVerifier_MissingV1(t *testing.T) {
	v := &StripeVerifier{Tolerance: 5 * time.Minute}
	header := fmt.Sprintf("t=%d", time.Now().Unix())
	if err := v.Verify(header, "secret", []byte("body")); err == nil {
		t.Fatal("expected error for missing v1=")
	}
}

func TestStripeVerifier_EmptyHeader(t *testing.T) {
	v := &StripeVerifier{Tolerance: 5 * time.Minute}
	if err := v.Verify("", "secret", []byte("body")); err == nil {
		t.Fatal("expected error for empty header")
	}
}

func TestStripeVerifier_ZeroTolerance(t *testing.T) {
	body := []byte(`{"id":"evt_123"}`)
	secret := "whsec_test"
	now := time.Now()
	ts := now.Unix()
	header := stripeSign(t, secret, body, ts)

	// Zero tolerance = no timestamp check.
	v := &StripeVerifier{
		Tolerance: 0,
		Now:       func() time.Time { return now },
	}
	if err := v.Verify(header, secret, body); err != nil {
		t.Fatalf("expected valid with zero tolerance: %v", err)
	}
}
