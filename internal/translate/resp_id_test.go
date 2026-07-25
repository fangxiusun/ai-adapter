package translate

import (
	"regexp"
	"testing"
)

func TestGeneratedIDsAreUniqueAndWellFormed(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{24}$`)
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id := generateID()
		if !pattern.MatchString(id) {
			t.Fatalf("generated ID %q is not a 24-character hexadecimal ID", id)
		}
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate generated ID: %s", id)
		}
		seen[id] = struct{}{}
	}
}
