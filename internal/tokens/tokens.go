package tokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

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

	// Historical Argon2id parameters. They remain fixed so legacy hashes can be
	// verified without accepting database-controlled resource costs.
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16

	sha256HashPrefix                 = "sha256:"
	maxConcurrentLegacyVerifications = 2
	legacyVerificationWait           = time.Second
)

// legacyFixedSalt is the salt used by an older HashToken that produced bare
// hex output. A later legacy format embedded a random salt in a PHC string;
// VerifyToken falls back to this value only for the original bare-hex format
// so existing tokens keep working without a forced migration.
var legacyFixedSalt = []byte("hatchway-token-hash")

// legacyVerifySlots caps the memory and CPU consumed by compatibility checks.
// New tokens use constant-cost SHA-256 verification; only old database rows
// can enter this path.
var legacyVerifySlots = make(chan struct{}, maxConcurrentLegacyVerifications)

// ErrLegacyVerificationBusy means bounded legacy Argon2 capacity was not
// available before the wait limit elapsed.
var ErrLegacyVerificationBusy = errors.New("legacy token verification capacity exhausted")

// Kind identifies the purpose of a bearer token.
type Kind int

const (
	// KindUnknown is returned for strings without a recognized token prefix.
	KindUnknown Kind = iota
	// KindAPI authenticates requests to the public API.
	KindAPI
	// KindRuntime authenticates frpc to the internal frps plugin.
	KindRuntime
)

// Token contains a newly minted bearer token and its storage metadata.
type Token struct {
	Kind   Kind
	Raw    string // the full token string, shown to user exactly once
	Prefix string // first 12 chars of the full token, for DB lookup
	Hash   string // versioned digest for DB storage; verifier also accepts legacy Argon2id hashes
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

// HashToken returns a versioned SHA-256 digest of a high-entropy bearer token.
// These tokens contain roughly 143 random bits, so a deliberately slow
// password KDF adds denial-of-service cost without meaningfully improving
// offline resistance. VerifyToken retains bounded Argon2id compatibility for
// tokens minted by older releases.
func HashToken(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return sha256HashPrefix + base64.RawURLEncoding.EncodeToString(digest[:])
}

// UsesCurrentHash reports whether storedHash uses the current constant-cost
// token digest format.
func UsesCurrentHash(storedHash string) bool {
	return strings.HasPrefix(storedHash, sha256HashPrefix)
}

// VerifyToken checks that the raw token matches the stored hash. It accepts
// the current SHA-256 format plus the two legacy Argon2id formats. Server
// request paths should use VerifyTokenContext so temporary verifier saturation
// can be distinguished from invalid credentials.
func VerifyToken(raw, storedHash string) bool {
	ok, _ := VerifyTokenContext(context.Background(), raw, storedHash)
	return ok
}

// VerifyTokenContext checks a token while bounding both concurrent legacy
// Argon2 work and the time spent waiting for capacity. Once started, Argon2
// itself is not cancelable; callers may return on context cancellation while
// the bounded worker finishes and retains its slot.
func VerifyTokenContext(ctx context.Context, raw, storedHash string) (bool, error) {
	if UsesCurrentHash(storedHash) {
		return verifySHA256(raw, storedHash), nil
	}
	if strings.HasPrefix(storedHash, "$argon2id$") {
		return verifyPHC(ctx, raw, storedHash)
	}
	return verifyLegacy(ctx, raw, storedHash)
}

func verifySHA256(raw, stored string) bool {
	encoded := strings.TrimPrefix(stored, sha256HashPrefix)
	expected, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(expected) != sha256.Size {
		return false
	}
	actual := sha256.Sum256([]byte(raw))
	return subtle.ConstantTimeCompare(actual[:], expected) == 1
}

func verifyPHC(ctx context.Context, raw, stored string) (bool, error) {
	salt, hash, mem, t, p, err := parsePHC(stored)
	if err != nil {
		return false, nil
	}
	return withLegacyVerifySlot(ctx, func() bool {
		computed := argon2.IDKey([]byte(raw), salt, t, mem, p, argonKeyLen)
		return subtle.ConstantTimeCompare(computed, hash) == 1
	})
}

func verifyLegacy(ctx context.Context, raw, stored string) (bool, error) {
	expected, err := hex.DecodeString(stored)
	if err != nil || len(expected) != argonKeyLen {
		return false, nil
	}
	return withLegacyVerifySlot(ctx, func() bool {
		computed := argon2.IDKey([]byte(raw), legacyFixedSalt, argonTime, argonMemory, argonThreads, argonKeyLen)
		return subtle.ConstantTimeCompare(computed, expected) == 1
	})
}

func withLegacyVerifySlot(ctx context.Context, verify func() bool) (bool, error) {
	timer := time.NewTimer(legacyVerificationWait)
	defer timer.Stop()

	select {
	case legacyVerifySlots <- struct{}{}:
		result := make(chan bool, 1)
		go func() {
			defer func() { <-legacyVerifySlots }()
			result <- verify()
		}()
		select {
		case matches := <-result:
			return matches, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	case <-ctx.Done():
		return false, ctx.Err()
	case <-timer.C:
		return false, ErrLegacyVerificationBusy
	}
}

func parsePHC(s string) (salt, hash []byte, mem, t uint32, threads uint8, err error) {
	parts := strings.Split(s, "$")
	// Expect: ["", "argon2id", "v=19", "m=...,t=...,p=...", "<salt>", "<hash>"]
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		err = errors.New("tokens: malformed argon2id PHC string")
		return
	}
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		err = errors.New("tokens: malformed argon2id parameters")
		return
	}
	mem64, memErr := strconv.ParseUint(strings.TrimPrefix(params[0], "m="), 10, 32)
	time64, timeErr := strconv.ParseUint(strings.TrimPrefix(params[1], "t="), 10, 32)
	threads64, threadsErr := strconv.ParseUint(strings.TrimPrefix(params[2], "p="), 10, 8)
	if !strings.HasPrefix(params[0], "m=") || !strings.HasPrefix(params[1], "t=") || !strings.HasPrefix(params[2], "p=") ||
		memErr != nil || timeErr != nil || threadsErr != nil {
		err = errors.New("tokens: malformed argon2id parameters")
		return
	}
	mem, t, threads = uint32(mem64), uint32(time64), uint8(threads64)
	// Older Hatchway releases emitted exactly these costs and lengths. Reject
	// everything else before calling Argon2 so corrupted database values cannot
	// request unbounded memory or trigger zero-parameter panics.
	if mem != argonMemory || t != argonTime || threads != argonThreads {
		err = errors.New("tokens: unsupported argon2id parameters")
		return
	}
	salt, err = base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return
	}
	if len(salt) != argonSaltLen {
		err = errors.New("tokens: invalid argon2id salt length")
		return
	}
	hash, err = base64.RawStdEncoding.DecodeString(parts[5])
	if err == nil && len(hash) != argonKeyLen {
		err = errors.New("tokens: invalid argon2id hash length")
	}
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
	if strings.HasPrefix(raw, rtTokenPrefix) {
		return KindRuntime
	}
	return KindUnknown
}
