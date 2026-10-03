//go:build tools

// Package tools pins build-tool dependencies so `go mod tidy` keeps them
// and their versions stay reproducible.
package tools

import (
	_ "entgo.io/ent/cmd/ent"
)
