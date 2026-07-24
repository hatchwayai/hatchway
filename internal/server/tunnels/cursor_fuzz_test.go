package tunnels

import (
	"testing"
	"time"
)

// FuzzDecodeCursor exercises the actual parser for the opaque, untrusted
// pagination cursor accepted by GET /v1/tunnels.
func FuzzDecodeCursor(f *testing.F) {
	f.Add("")
	f.Add("not-base64")
	f.Add(encodeCursor(listCursor{
		CreatedAt: time.Unix(1_700_000_000, 123).UTC(),
		TunnelID:  "t-abcdefghjkmnpqrs",
	}))

	f.Fuzz(func(t *testing.T, encoded string) {
		cursor, err := decodeCursor(encoded)
		if err != nil || encoded == "" {
			return
		}
		if cursor.CreatedAt.IsZero() || cursor.TunnelID == "" {
			t.Fatalf("successful decode returned an incomplete cursor: %+v", cursor)
		}
	})
}
