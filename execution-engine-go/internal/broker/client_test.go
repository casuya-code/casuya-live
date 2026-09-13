package broker

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestSignVerifyRoundtrip(t *testing.T) {
	body := []byte(`{"ts":1786600000000,"key":"k"}`)
	sig := sign(body, "secret")
	if err := verifyHMAC(body, sig, "secret"); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if err := verifyHMAC(body, sig, "wrong-secret"); err == nil {
		t.Fatalf("signature accepted with wrong secret")
	}
}

func TestVerifyHMACRejectsTamperedBody(t *testing.T) {
	body := []byte("payload")
	sig := sign(body, "secret")
	tampered := []byte("payload!")
	if err := verifyHMAC(tampered, sig, "secret"); err == nil {
		t.Fatalf("tampered body accepted")
	}
}

func TestVerifyHMACRejectsMalformedSig(t *testing.T) {
	if err := verifyHMAC([]byte("x"), "%%%not-base64", "secret"); err == nil {
		t.Fatalf("malformed signature accepted")
	}
}

func TestDecodeEnvelopeRoundtrip(t *testing.T) {
	// Build exactly like analytics-engine-py does: canonical JSON of a known
	// payload, base64url WITHOUT padding for both body and signature.
	payload := map[string]any{
		"match_id":     "epl-2026-0042",
		"market_id":    "2H_1X2",
		"side":         "away",
		"odds":         5.25,
		"true_prob":    0.29,
		"implied_prob": 0.19,
		"amount":       25.0,
		"ts":           1786600700000,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	sig := sign(body, "secret")
	raw := base64.RawURLEncoding.EncodeToString(body) + "." + sig

	got, err := decodeEnvelope(raw, "secret")
	if err != nil {
		t.Fatalf("valid envelope rejected: %v", err)
	}
	if got.MatchID != "epl-2026-0042" || got.Side != "away" || got.Amount != 25.0 {
		t.Fatalf("decoded payload mismatch: %+v", got)
	}
}

func TestDecodeEnvelopeRejectsWrongSecret(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"match_id": "m"})
	sig := sign(body, "secret")
	raw := base64.RawURLEncoding.EncodeToString(body) + "." + sig
	if _, err := decodeEnvelope(raw, "nope"); err == nil {
		t.Fatalf("envelope verified with wrong secret")
	}
}

func TestDecodeEnvelopeRejectsMalformed(t *testing.T) {
	if _, err := decodeEnvelope("no-dot-separator", "secret"); err == nil {
		t.Fatalf("malformed envelope accepted")
	}
	if _, err := decodeEnvelope(".", "secret"); err == nil {
		t.Fatalf("empty envelope accepted")
	}
}

func TestDecodeEnvelopeRejectsPadding(t *testing.T) {
	// Go's RawURLEncoding rejects padded input; the Python side must strip '='.
	body, _ := json.Marshal(map[string]any{"match_id": "m"})
	sig := sign(body, "secret")
	padded := strings.TrimRight(base64.RawURLEncoding.EncodeToString(body), "=") + "==." + sig
	if _, err := decodeEnvelope(padded, "secret"); err == nil {
		t.Fatalf("padded envelope was accepted")
	}
}