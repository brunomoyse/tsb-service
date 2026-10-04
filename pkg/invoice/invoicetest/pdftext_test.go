package invoicetest

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// utf16Literal is how fpdf shows a UTF-8 string: UTF-16BE bytes in a parenthesised literal.
func utf16Literal(s string) string {
	var b strings.Builder
	for _, u := range utf16.Encode([]rune(s)) {
		b.WriteByte(byte(u >> 8))
		b.WriteByte(byte(u))
	}
	return "(" + b.String() + ")Tj"
}

// pdfWith builds the smallest document the extractor accepts: one Flate content stream.
func pdfWith(t *testing.T, content string) (pdf []byte, compressed []byte) {
	t.Helper()
	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	_, err := w.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	pdf = []byte("%PDF-1.3\n1 0 obj\n<< /Length 0 >>\nstream\n" + z.String() + "\nendstream\nendobj\n%%EOF\n")
	return pdf, z.Bytes()
}

func TestPDFTextLinesReadsTheShownStrings(t *testing.T) {
	pdf, _ := pdfWith(t, "BT "+utf16Literal("Total")+" "+utf16Literal("21,00 €")+" ET")
	assert.Equal(t, []string{"Total", "21,00 €"}, PDFTextLines(t, pdf))
}

// A compressed stream that ends in 0x0D must still be read: the byte before "\nendstream" belongs
// to the data. Pad the content until the deflate output ends that way.
func TestPDFTextLinesKeepsAStreamThatEndsInCarriageReturn(t *testing.T) {
	for pad := 0; pad < 5000; pad++ {
		content := "BT " + utf16Literal("Total") + strings.Repeat(" ", pad) + fmt.Sprintf("%% %d", pad) + " ET"
		pdf, z := pdfWith(t, content)
		if z[len(z)-1] != '\r' {
			continue
		}
		assert.Equal(t, []string{"Total"}, PDFTextLines(t, pdf), "pad=%d", pad)
		return
	}
	t.Fatal("no padding produced a stream ending in a carriage return")
}
