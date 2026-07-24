package tunnels

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

const crockfordAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// GenerateTunnelID returns a t-prefixed Crockford-style identifier with 16
// random body characters (~79 bits of entropy). Returns an error if the
// system entropy source fails — callers should surface this as a 500.
func GenerateTunnelID() (string, error) {
	const prefix = "t-"
	const bodyLen = 16
	base := big.NewInt(int64(len(crockfordAlphabet)))

	body := make([]byte, bodyLen)
	for i := range body {
		n, err := rand.Int(rand.Reader, base)
		if err != nil {
			return "", fmt.Errorf("crypto/rand: %w", err)
		}
		body[i] = crockfordAlphabet[n.Int64()]
	}
	return prefix + string(body), nil
}
