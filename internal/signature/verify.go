package signature

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Verifier validates a webhook signature against the raw request body.
type Verifier interface {
	Verify(header string, secret string, body []byte) error
}

// HMACVerifier implements generic HMAC-SHA256 verification.
// Works for GitHub, GitLab, and other providers that use a standard
// HMAC-SHA256 signature with a configurable header prefix and encoding.
type HMACVerifier struct {
	// Prefix is stripped from the header value before decoding.
	// For GitHub: "sha256=". Leave empty if the header is just the raw signature.
	Prefix string

	// Encoding of the signature in the header. Only "hex" is supported.
	Encoding string
}

func (v *HMACVerifier) Verify(header string, secret string, body []byte) error {
	if header == "" {
		return fmt.Errorf("signature header is empty")
	}

	sig := header
	if v.Prefix != "" {
		if !strings.HasPrefix(sig, v.Prefix) {
			return fmt.Errorf("signature header missing expected prefix %q", v.Prefix)
		}
		sig = strings.TrimPrefix(sig, v.Prefix)
	}

	encoding := v.Encoding
	if encoding == "" {
		encoding = "hex"
	}

	var sigBytes []byte
	switch encoding {
	case "hex":
		var err error
		sigBytes, err = hex.DecodeString(sig)
		if err != nil {
			return fmt.Errorf("decoding signature hex: %w", err)
		}
	default:
		return fmt.Errorf("unsupported signature encoding: %q", encoding)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := mac.Sum(nil)

	if !hmac.Equal(sigBytes, expected) {
		return fmt.Errorf("signature mismatch")
	}

	return nil
}
