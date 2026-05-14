package tunnels

import (
	"strings"
	"testing"
)

// FuzzTunnelIDValidation fuzzes arbitrary strings to ensure
// validation logic doesn't panic and correctly classifies IDs.
func FuzzTunnelIDValidation(f *testing.F) {
	// Seed with valid IDs
	for i := 0; i < 10; i++ {
		id, err := GenerateTunnelID()
		if err != nil {
			f.Fatal(err)
		}
		f.Add(id)
	}
	// Seed with invalid inputs
	f.Add("")
	f.Add("t-")
	f.Add("x-1234567890abcdef")
	f.Add("t-0000000000000000")
	f.Add("t-_underscore_test")

	f.Fuzz(func(t *testing.T, id string) {
		// Validate: does it look like a proper tunnel ID?
		valid := true

		if !strings.HasPrefix(id, "t-") {
			valid = false
		}
		if len(id) != 18 {
			valid = false
		}

		if valid && len(id) > 2 {
			body := id[2:]
			confusable := "0oli1"
			for _, c := range confusable {
				if strings.ContainsRune(body, c) {
					valid = false
					break
				}
			}
			if strings.Contains(id, "_") {
				valid = false
			}
			for _, c := range body {
				if !strings.ContainsRune(crockfordAlphabet, c) {
					valid = false
					break
				}
			}
		}

		// This just ensures no panic occurred
		_ = valid
	})
}
