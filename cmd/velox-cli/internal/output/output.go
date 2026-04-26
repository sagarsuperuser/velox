// Package output provides minimal --output text|json formatting for the
// CLI. Hand-rolled aligned columns are fine for v1 — the alternative
// (tablewriter etc) adds a transitive-dependency surface for one
// shipped feature.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Format is the --output value. "text" is the human default; "json"
// emits one canonical JSON document so the operator can pipe to jq.
type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

// ParseFormat normalizes the --output flag value. Empty defaults to
// text. Unknown values are an error so a typo doesn't silently fall
// through to the wrong shape.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "text":
		return FormatText, nil
	case "json":
		return FormatJSON, nil
	default:
		return "", fmt.Errorf("invalid --output %q (want text or json)", s)
	}
}

// JSON marshals v with two-space indent and writes it followed by a
// trailing newline, matching what `jq .` produces.
func JSON(w io.Writer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	_, err = w.Write([]byte("\n"))
	return err
}

// Table renders a header row + data rows with two-space-padded columns.
// It is intentionally not a general-purpose tablewriter — empty rows
// print just the header so the operator sees the expected schema even
// when nothing matched.
func Table(w io.Writer, header []string, rows [][]string) error {
	cols := len(header)
	widths := make([]int, cols)
	for i, h := range header {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i := 0; i < cols && i < len(row); i++ {
			if l := len(row[i]); l > widths[i] {
				widths[i] = l
			}
		}
	}

	// Header
	if err := writeRow(w, header, widths); err != nil {
		return err
	}
	for _, row := range rows {
		if err := writeRow(w, row, widths); err != nil {
			return err
		}
	}
	return nil
}

func writeRow(w io.Writer, cells []string, widths []int) error {
	var b strings.Builder
	for i, width := range widths {
		var cell string
		if i < len(cells) {
			cell = cells[i]
		}
		if i == len(widths)-1 {
			// Last column: don't pad trailing whitespace.
			b.WriteString(cell)
		} else {
			pad := width - len(cell)
			if pad < 0 {
				pad = 0
			}
			b.WriteString(cell)
			b.WriteString(strings.Repeat(" ", pad+2))
		}
	}
	b.WriteString("\n")
	_, err := io.WriteString(w, b.String())
	return err
}
