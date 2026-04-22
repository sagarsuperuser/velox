// Package pdffonts embeds the TTF fonts used by Velox's PDF renderers
// (invoice, credit note). Centralizing the fonts avoids duplicating the
// ~1 MB font binaries per domain package and removes the runtime.Caller
// filesystem trick the first renderer shipped with — the compiled binary
// carries the fonts and renders work identically on any machine.
//
// Noto Sans is the default family because it covers the Latin, Greek,
// Cyrillic and several CJK ranges out of the box, which matters for
// customer names, tax IDs and memos that can land in any script.
package pdffonts

import (
	_ "embed"
	"fmt"

	"github.com/signintech/gopdf"
)

//go:embed NotoSans-Regular.ttf
var regularTTF []byte

//go:embed NotoSans-Bold.ttf
var boldTTF []byte

// Family names the renderers pass to SetFont. Exported so callers don't
// duplicate the magic strings.
const (
	FamilyRegular = "noto"
	FamilyBold    = "noto-bold"
)

// RegisterNotoSans loads the embedded Regular and Bold faces into pdf
// under FamilyRegular and FamilyBold. Safe to call once per *gopdf.GoPdf
// right after Start().
func RegisterNotoSans(pdf *gopdf.GoPdf) error {
	if err := pdf.AddTTFFontData(FamilyRegular, regularTTF); err != nil {
		return fmt.Errorf("load regular font: %w", err)
	}
	if err := pdf.AddTTFFontData(FamilyBold, boldTTF); err != nil {
		return fmt.Errorf("load bold font: %w", err)
	}
	return nil
}
