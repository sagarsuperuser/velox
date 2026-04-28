// Package tools tracks dev-only build dependencies in go.mod.
//
// The `tools` build tag keeps these imports out of every regular build
// (the file is selected only when `-tags tools` is passed) while still
// letting `go mod tidy` see them as real imports — the standard Go
// recipe for pinning CLI tooling versions in the same go.mod that ships
// the application code.
//
// Add a new tool: import its `cmd/...` path, run `go mod tidy`, and
// invoke via `go run <path> ...` from the Makefile. Pin upgrades the
// same way you bump any other module dependency.
//
//go:build tools

package tools

import (
	_ "github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen"
)
