package signature

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// StripeVerifier implements Stripe's non-standard signature verification.
//
// Stripe's Stripe-Signature header has the format:
//
//	t=1614556828,v1=abc123signaturehex,v1=optionalsecondsig
//
// The signed payload is "{timestamp}.{raw_body}". The timestamp must be
// within Tolerance of the current time to prevent replay attacks.
type StripeVerifier struct {
	Tolerance time.Duration

	// Now returns the current time. If nil, time.Now is used.
	// Exposed for testing.
	Now func() time.Time
}

func (v *StripeVerifier) Verify(header string, secret string, body []byte) error {
	if header == "" {
		return fmt.Errorf("signature header is empty")
	}

	parts := strings.Split(header, ",")

	var timestamp string
	var signatures []string

	for _, p := range parts {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			timestamp = kv[1]
		case "v1":
			signatures = append(signatures, kv[1])
		}
	}

	if timestamp == "" {
		return fmt.Errorf("stripe signature: missing t= timestamp")
	}
	if len(signatures) == 0 {
		return fmt.Errorf("stripe signature: missing v1= signature")
	}

	// Check tolerance.
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("stripe signature: invalid timestamp: %w", err)
	}

	now := time.Now
	if v.Now != nil {
		now = v.Now
	}

	age := now().Sub(time.Unix(ts, 0))
	if age < 0 {
		age = -age
	}
	if v.Tolerance > 0 && age > v.Tolerance {
		return fmt.Errorf("stripe signature: timestamp too old (%s > %s tolerance)", age.Truncate(time.Second), v.Tolerance)
	}

	// Compute expected signature: HMAC-SHA256("{timestamp}.{body}")
	signed := fmt.Sprintf("%s.%s", timestamp, string(body))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signed))
	expected := mac.Sum(nil)

	// Any v1 signature matching is sufficient.
	for _, sig := range signatures {
		sigBytes, err := hex.DecodeString(sig)
		if err != nil {
			continue
		}
		if hmac.Equal(sigBytes, expected) {
			return nil
		}
	}

	return fmt.Errorf("stripe signature: no valid v1 signature found")
}
