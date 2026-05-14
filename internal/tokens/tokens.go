package tokens

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"golang.org/x/crypto/argon2"
)

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func randomBase62(length int) (string, error) {
	base := big.NewInt(62)
	result := make([]byte, length)
	for i := range result {
		n, err := rand.Int(rand.Reader, base)
		if err != nil {
			return "", fmt.Errorf("generate random base62: %w", err)
		}
		result[i] = base62Alphabet[n.Int64()]
	}
	return string(result), nil
}

const (
	apiTokenPrefix = "sk_live_"
	rtTokenPrefix  = "rt_"
	tokenPrefixLen = 12
	apiTokenBytes  = 24
	rtTokenBytes   = 24

	// argon2id parameters: time=3, memory=64MB, threads=4, keyLen=32.
	// These match OWASP recommendations for password hashing and are
	// appropriate for API tokens where verification is infrequent and
	// not latency-sensitive (unlike per-request bcrypt on every HTTP call).
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// legacyFixedSalt is the salt used by an older HashToken that produced bare
// hex output. New hashes embed a per-token random salt in the PHC string;
// VerifyToken falls back to this value when it sees a legacy-format hash so
// existing tokens in the database keep working without a forced migration.
var legacyFixedSalt = []byte("hatchway-token-hash")

type Kind int

const (
	KindAPI Kind = iota
	KindRuntime
)

type Token struct {
	Kind   Kind
	Raw    string // the full token string, shown to user exactly once
	Prefix string // first 12 chars of the full token, for DB lookup
	Hash   string // argon2id PHC string ($argon2id$...) for DB storage; verifier also accepts legacy hex
}

// MintAPIToken generates a new API token: sk_live_<base62>.
func MintAPIToken() (*Token, error) {
	body, err := randomBase62(apiTokenBytes)
	if err != nil {
		return nil, err
	}
	raw := apiTokenPrefix + body
	return &Token{
		Kind:   KindAPI,
		Raw:    raw,
		Prefix: raw[:tokenPrefixLen],
		Hash:   HashToken(raw),
	}, nil
}

// MintRuntimeToken generates a new runtime token: rt_<base62>.
func MintRuntimeToken() (*Token, error) {
	body, err := randomBase62(rtTokenBytes)
	if err != nil {
		return nil, err
	}
	raw := rtTokenPrefix + body
	return &Token{
		Kind:   KindRuntime,
		Raw:    raw,
		Prefix: raw[:tokenPrefixLen],
		Hash:   HashToken(raw),
	}, nil
}

// HashToken returns a PHC-encoded argon2id hash with a fresh random salt.
// Output format:
//
//	$argon2id$v=19$m=<memory>,t=<time>,p=<threads>$<salt-b64>$<hash-b64>
//
// Each call returns a different string (random salt) but VerifyToken accepts
// any prior output for the same token.
func HashToken(raw string) string {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		// crypto/rand failure is exceptional — caller has nothing to do
		// other than crash. Returning a malformed string would silently
		// produce unverifiable tokens.
		panic("tokens: crypto/rand failed: " + err.Error())
	}
	h := argon2.IDKey([]byte(raw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(h),
	)
}

// VerifyToken checks that the raw token matches the stored hash.
// Accepts both PHC-encoded hashes and legacy hex-encoded hashes (fixed salt).
func VerifyToken(raw, storedHash string) bool {
	if strings.HasPrefix(storedHash, "$argon2id$") {
		return verifyPHC(raw, storedHash)
	}
	return verifyLegacy(raw, storedHash)
}

func verifyPHC(raw, stored string) bool {
	salt, hash, mem, t, p, err := parsePHC(stored)
	if err != nil {
		return false
	}
	computed := argon2.IDKey([]byte(raw), salt, t, mem, p, uint32(len(hash)))
	return subtle.ConstantTimeCompare(computed, hash) == 1
}

func verifyLegacy(raw, stored string) bool {
	computed := argon2.IDKey([]byte(raw), legacyFixedSalt, argonTime, argonMemory, argonThreads, argonKeyLen)
	hex := fmt.Sprintf("%x", computed)
	return subtle.ConstantTimeCompare([]byte(hex), []byte(stored)) == 1
}

func parsePHC(s string) (salt, hash []byte, mem, t uint32, threads uint8, err error) {
	parts := strings.Split(s, "$")
	// Expect: ["", "argon2id", "v=19", "m=...,t=...,p=...", "<salt>", "<hash>"]
	if len(parts) != 6 || parts[1] != "argon2id" {
		err = errors.New("tokens: malformed argon2id PHC string")
		return
	}
	if _, perr := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &threads); perr != nil {
		err = fmt.Errorf("tokens: bad PHC params: %w", perr)
		return
	}
	salt, err = base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return
	}
	hash, err = base64.RawStdEncoding.DecodeString(parts[5])
	return
}

// ParsePrefix extracts the first 12 characters of a full token.
func ParsePrefix(raw string) (string, error) {
	if len(raw) < tokenPrefixLen {
		return "", fmt.Errorf("token too short to extract prefix")
	}
	return raw[:tokenPrefixLen], nil
}

// KindOf returns the token kind based on its prefix.
func KindOf(raw string) Kind {
	if strings.HasPrefix(raw, apiTokenPrefix) {
		return KindAPI
	}
	return KindRuntime
}
