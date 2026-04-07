// Package errs provides structured domain errors with machine-readable codes.
// The API layer translates these into HTTP responses by matching on error
// type or code, not by parsing error message strings.
package errs
