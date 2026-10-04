// Package invoicetest is test support for code that renders invoices: it reads back the text of a
// generated PDF so tests can assert on what a customer would actually see.
package invoicetest

import (
	"bytes"
	"compress/zlib"
	"io"
	"regexp"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/require"
)

var streamRe = regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)

// PDFTextLines extracts, in drawing order, every string shown by the PDF's
// text operators. fpdf writes UTF-8 (DejaVu) text as UTF-16BE inside a
// parenthesised literal followed by Tj (no space), in a Flate-compressed page stream, so
// inflating the streams and decoding those literals yields exactly what the
// reader sees, one entry per cell.
func PDFTextLines(t *testing.T, pdf []byte) []string {
	t.Helper()
	require.True(t, bytes.HasPrefix(pdf, []byte("%PDF-")), "not a PDF")
	require.True(t, bytes.HasSuffix(bytes.TrimSpace(pdf), []byte("%%EOF")), "PDF is truncated")

	var out []string
	for _, m := range streamRe.FindAllSubmatch(pdf, -1) {
		zr, err := zlib.NewReader(bytes.NewReader(m[1]))
		if err != nil {
			continue // not a Flate stream (e.g. an embedded image)
		}
		content, err := io.ReadAll(zr)
		if err != nil || !bytes.Contains(content, []byte(")Tj")) {
			continue
		}
		out = append(out, shownStrings(content)...)
	}
	require.NotEmpty(t, out, "no text found in the PDF: did the content stream encoding change?")
	return out
}

// shownStrings scans a content stream for "(literal)Tj" operations.
func shownStrings(content []byte) []string {
	var res []string
	for i := 0; i < len(content); i++ {
		if content[i] != '(' {
			continue
		}
		var lit []byte
		j := i + 1
		for ; j < len(content) && content[j] != ')'; j++ {
			if content[j] == '\\' && j+1 < len(content) {
				j++
				switch content[j] {
				case 'n':
					lit = append(lit, '\n')
				case 'r':
					lit = append(lit, '\r')
				default:
					lit = append(lit, content[j])
				}
				continue
			}
			lit = append(lit, content[j])
		}
		if !bytes.HasPrefix(content[j+1:], []byte("Tj")) {
			i = j
			continue
		}
		u := make([]uint16, 0, len(lit)/2)
		for k := 0; k+1 < len(lit); k += 2 {
			u = append(u, uint16(lit[k])<<8|uint16(lit[k+1]))
		}
		res = append(res, string(utf16.Decode(u)))
		i = j
	}
	return res
}
