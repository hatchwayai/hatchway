package tokens

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

// legacyHashForTest reproduces the pre-PHC HashToken format so we can verify
// backward compatibility without exporting it from the production package.
func legacyHashForTest(raw string) string {
	h := argon2.IDKey([]byte(raw), legacyFixedSalt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("%x", h)
}

func TestMintAPIToken(t *testing.T) {
	tok, err := MintAPIToken()
	if err != nil {
		t.Fatal(err)
	}

	if tok.Kind != KindAPI {
		t.Errorf("expected KindAPI, got %v", tok.Kind)
	}
	if !strings.HasPrefix(tok.Raw, "sk_live_") {
		t.Errorf("token should start with sk_live_, got %s", tok.Raw)
	}
	if len(tok.Prefix) != 12 {
		t.Errorf("prefix should be 12 chars, got %d: %s", len(tok.Prefix), tok.Prefix)
	}
	if tok.Prefix != tok.Raw[:12] {
		t.Errorf("prefix should be first 12 chars of raw token")
	}
	if tok.Hash == "" {
		t.Error("hash should not be empty")
	}
}

func TestMintRuntimeToken(t *testing.T) {
	tok, err := MintRuntimeToken()
	if err != nil {
		t.Fatal(err)
	}

	if tok.Kind != KindRuntime {
		t.Errorf("expected KindRuntime, got %v", tok.Kind)
	}
	if !strings.HasPrefix(tok.Raw, "rt_") {
		t.Errorf("token should start with rt_, got %s", tok.Raw)
	}
	if len(tok.Prefix) != 12 {
		t.Errorf("prefix should be 12 chars, got %d: %s", len(tok.Prefix), tok.Prefix)
	}
}

func TestTokenUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok, err := MintAPIToken()
		if err != nil {
			t.Fatal(err)
		}
		if seen[tok.Raw] {
			t.Fatal("duplicate token generated")
		}
		seen[tok.Raw] = true
	}
}

func TestVerifyToken(t *testing.T) {
	tok, err := MintAPIToken()
	if err != nil {
		t.Fatal(err)
	}

	if !VerifyToken(tok.Raw, tok.Hash) {
		t.Error("token should verify against its own hash")
	}
}

func TestVerifyToken_Tampering(t *testing.T) {
	tok, err := MintAPIToken()
	if err != nil {
		t.Fatal(err)
	}

	// Tamper with one character. Pick a replacement that's guaranteed to
	// differ from the last char — base62 includes "X" itself, so a fixed "X"
	// flaked ~1/62 of the time.
	last := tok.Raw[len(tok.Raw)-1]
	replacement := byte('X')
	if last == replacement {
		replacement = 'Y'
	}
	tampered := tok.Raw[:len(tok.Raw)-1] + string(replacement)
	if VerifyToken(tampered, tok.Hash) {
		t.Error("tampered token should not verify")
	}
}

func TestVerifyToken_WrongHash(t *testing.T) {
	tok, err := MintAPIToken()
	if err != nil {
		t.Fatal(err)
	}

	if VerifyToken(tok.Raw, "00000000000000000000000000000000") {
		t.Error("token should not verify against a wrong hash")
	}
}

func TestVerifyToken_DifferentToken(t *testing.T) {
	tok1, _ := MintAPIToken()
	tok2, _ := MintAPIToken()

	if VerifyToken(tok1.Raw, tok2.Hash) {
		t.Error("token should not verify against a different token's hash")
	}
}

func TestHashUsesRandomSaltButVerifies(t *testing.T) {
	tok, _ := MintAPIToken()
	h1 := HashToken(tok.Raw)
	h2 := HashToken(tok.Raw)
	if h1 == h2 {
		t.Error("hash should be salted: two calls must not produce the same string")
	}
	if !VerifyToken(tok.Raw, h1) || !VerifyToken(tok.Raw, h2) {
		t.Error("both salted hashes must verify against the original token")
	}
}

func TestHashFormatIsPHC(t *testing.T) {
	h := HashToken("sk_live_abc")
	if !strings.HasPrefix(h, "$argon2id$") {
		t.Errorf("expected PHC string starting with $argon2id$, got %q", h)
	}
}

func TestVerifyToken_LegacyRoundTrip(t *testing.T) {
	// Build a legacy-format hash the same way old code did: argon2id with
	// the fixed salt, hex-encoded. Verifier must accept it.
	tok, _ := MintAPIToken()
	legacy := legacyHashForTest(tok.Raw)
	if !VerifyToken(tok.Raw, legacy) {
		t.Error("verifier should accept legacy fixed-salt hex hashes")
	}
}

func TestParsePrefix(t *testing.T) {
	prefix, err := ParsePrefix("sk_live_abcdef123456")
	if err != nil {
		t.Fatal(err)
	}
	if prefix != "sk_live_abcd" {
		t.Errorf("expected sk_live_abcd, got %s", prefix)
	}
}

func TestParsePrefix_TooShort(t *testing.T) {
	_, err := ParsePrefix("short")
	if err == nil {
		t.Error("expected error for short token")
	}
}

func TestKindOf(t *testing.T) {
	if KindOf("sk_live_xxx") != KindAPI {
		t.Error("sk_live_ should be KindAPI")
	}
	if KindOf("rt_xxx") != KindRuntime {
		t.Error("rt_ should be KindRuntime")
	}
}

func TestParsePHC_Malformed(t *testing.T) {
	cases := []struct {
		name string
		s    string
	}{
		{"empty", ""},
		{"missing prefix", "not a phc"},
		{"wrong algo", "$argon2i$v=19$m=65536,t=3,p=4$YWJjZGVm$YWJjZGVm"},
		{"too few fields", "$argon2id$v=19$m=65536,t=3,p=4$abc"},
		{"bad params", "$argon2id$v=19$m=bad,t=bad,p=bad$YWJjZGVm$YWJjZGVm"},
		{"bad salt b64", "$argon2id$v=19$m=65536,t=3,p=4$@@@@@@$YWJjZGVm"},
		{"bad hash b64", "$argon2id$v=19$m=65536,t=3,p=4$YWJjZGVm$@@@@@@"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if VerifyToken("sk_live_abc", tt.s) {
				t.Errorf("malformed PHC %q should not verify", tt.s)
			}
		})
	}
}

func TestParsePHC_DirectErrors(t *testing.T) {
	// Drive parsePHC directly to exercise the error returns.
	_, _, _, _, _, err := parsePHC("$bcrypt$abc")
	if err == nil {
		t.Error("non-argon2id prefix should error")
	}
	_, _, _, _, _, err = parsePHC("$argon2id$v=19$m=x,t=y,p=z$YWJj$YWJj")
	if err == nil {
		t.Error("non-numeric params should error")
	}
	_, _, _, _, _, err = parsePHC("$argon2id$v=19$m=1,t=1,p=1$!!!$YWJj")
	if err == nil {
		t.Error("malformed salt should error")
	}
}

func TestMintRuntimeToken_PrefixLength(t *testing.T) {
	tok, err := MintRuntimeToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(tok.Prefix) != 12 {
		t.Errorf("prefix len = %d, want 12", len(tok.Prefix))
	}
	if tok.Prefix != tok.Raw[:12] {
		t.Errorf("prefix should be first 12 chars of raw")
	}
}
