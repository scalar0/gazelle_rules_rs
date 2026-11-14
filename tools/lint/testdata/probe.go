package probe

import (
	"lint.test/left"
	"lint.test/right"
)

// Value requires metadata from two dependencies with the same Bazel target name.
// The lint check must pass without losing either dependency.
// A fixture with one library cannot expose metadata path collisions.
func Value() int { return left.Value() + right.Value() }
