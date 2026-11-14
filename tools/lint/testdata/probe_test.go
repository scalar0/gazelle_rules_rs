package probe

import (
	"fmt"
	"testing"
)

// TestProbe protects test-source checks with the repository lint configuration.
// The invalid format must fail lint even when the library passes.
// The Go package tests do not check the configured lint aspect.
func TestProbe(t *testing.T) {
	if Value() != 1 {
		t.Fatal("The probe returned the wrong value.")
	}
	fmt.Printf("%d", "invalid format")
}
