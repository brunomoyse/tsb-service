package application

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"golang.org/x/image/bmp"

	"tsb-service/internal/modules/hubrise/domain"
)

var errLinkBoom = errors.New("link boom")

// recordingLinks records the link writes of the publisher and fails the one it is told to.
type recordingLinks struct {
	domain.CatalogLinkRepository
	calls []string
	fail  string
}

func (r *recordingLinks) note(call string) error {
	r.calls = append(r.calls, call)
	if r.fail == call {
		return errLinkBoom
	}
	return nil
}

func (r *recordingLinks) SetRetired(_ context.Context, _ int64, at *time.Time) error {
	if at != nil {
		return errors.New("a revival must clear the retirement")
	}
	return r.note("unretire")
}

func (r *recordingLinks) Rekey(context.Context, int64, string, string) error { return r.note("rekey") }

func (r *recordingLinks) Upsert(_ context.Context, l *domain.CatalogLink) error {
	if l.Origin != domain.OriginPublished {
		return errors.New("a generated link is a published link")
	}
	return r.note("create")
}

type recordingTx struct {
	domain.CatalogTx
	links *recordingLinks
}

func (t recordingTx) Links() domain.CatalogLinkRepository { return t.links }

type recordingLocal struct {
	domain.LocalCatalogRepository
	links *recordingLinks
	batch int
}

func (l *recordingLocal) InBatch(ctx context.Context, fn func(context.Context, domain.CatalogTx) error) error {
	l.batch++
	return fn(ctx, recordingTx{links: l.links})
}

// The links of a plan are written revivals first, then rekeys, then new links, in one batch, and the
// first failure stops the batch (the transaction rolls back) before anything reaches HubRise.
func TestTheLinksOfAPlanAreWrittenInOrderAndTheFirstFailureStops(t *testing.T) {
	plan := &PublishPlan{
		Unretire: []int64{1},
		Rekeys:   []PlannedRekey{{LinkID: 2, NewKey: "k", NewRef: "r"}},
		NewLinks: []PlannedLink{{Entity: domain.EntityProduct, Key: "P", Ref: "P"}},
	}
	for _, tc := range []struct {
		fail string
		want []string
	}{
		{"", []string{"unretire", "rekey", "create"}},
		{"unretire", []string{"unretire"}},
		{"rekey", []string{"unretire", "rekey"}},
		{"create", []string{"unretire", "rekey", "create"}},
	} {
		links := &recordingLinks{fail: tc.fail}
		local := &recordingLocal{links: links}
		p := &CatalogPublisher{Local: local}
		err := p.writeLinks(t.Context(), plan)
		if tc.fail == "" {
			require.NoError(t, err)
		} else {
			assert.ErrorIs(t, err, errLinkBoom, tc.fail)
		}
		assert.Equal(t, tc.want, links.calls, "fail on %q", tc.fail)
		assert.Equal(t, 1, local.batch)
	}

	t.Run("nothing to write is no transaction", func(t *testing.T) {
		local := &recordingLocal{links: &recordingLinks{}}
		p := &CatalogPublisher{Local: local}
		require.NoError(t, p.writeLinks(t.Context(), &PublishPlan{}))
		assert.Zero(t, local.batch)
	})
}

func TestTheImageRefNamesTheProductAndTheContent(t *testing.T) {
	id := uuid.MustParse("6f1c2a9e-1b2c-4d5e-8f90-1234567890ab")
	ref := imagePrivateRef(id, "0123456789abcdef0123456789abcdef")
	assert.Equal(t, "tsb-product-6f1c2a9e-1b2c-4d5e-8f90-1234567890ab-0123456789ab", ref)
	owner, ok := imageOwner(ref)
	assert.True(t, ok)
	assert.Equal(t, id, owner)

	for _, foreign := range []string{"", "epos-1", "tsb-product-", "tsb-product-not-a-uuid-at-all-not-a-uuid-at-all-xx", "tsb-product-short"} {
		_, ok := imageOwner(foreign)
		assert.False(t, ok, foreign)
	}
}

func TestADerivativeKeepsAnAcceptableImageAndShrinksTheRest(t *testing.T) {
	small := pngOf(t, 40, 30)
	d, err := buildDerivative(small)
	require.NoError(t, err)
	assert.Equal(t, small, d.data, "an image HubRise accepts is sent as it is")
	assert.Equal(t, "image/png", d.contentType)
	assert.Equal(t, md5Hex(small), d.md5)

	t.Run("a large image becomes a JPEG of at most 1 MB and 1200 pixels", func(t *testing.T) {
		big := noisePNG(t, 1600, 1000)
		require.Greater(t, len(big), maxPublishImageBytes)
		d, err := buildDerivative(big)
		require.NoError(t, err)
		assert.Equal(t, "image/jpeg", d.contentType)
		assert.LessOrEqual(t, len(d.data), maxPublishImageBytes)
		cfg, _, err := image.DecodeConfig(bytes.NewReader(d.data))
		require.NoError(t, err)
		assert.Equal(t, 1200, cfg.Width, "the long side is 1200 pixels")
		assert.Equal(t, 750, cfg.Height, "and the ratio is kept")
		assert.Equal(t, md5Hex(d.data), d.md5)
	})
	t.Run("a tall image is shrunk on its height, a heavy small one only recompressed", func(t *testing.T) {
		d, err := buildDerivative(noisePNG(t, 600, 1500))
		require.NoError(t, err)
		cfg, _, _ := image.DecodeConfig(bytes.NewReader(d.data))
		assert.Equal(t, 1200, cfg.Height)
		assert.Equal(t, 480, cfg.Width)

		heavy := noisePNG(t, 700, 500) // over 1 MB of PNG, under the 1200 pixels
		require.Greater(t, len(heavy), maxPublishImageBytes)
		d, err = buildDerivative(heavy)
		require.NoError(t, err)
		cfg, _, _ = image.DecodeConfig(bytes.NewReader(d.data))
		assert.Equal(t, 700, cfg.Width, "not shrunk")
	})
	t.Run("transparency is laid on white", func(t *testing.T) {
		clear := noiseNRGBA(1300, 1300, 0) // fully transparent noise: heavy, with no colour to show
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, clear))
		require.Greater(t, buf.Len(), maxPublishImageBytes)
		d, err := buildDerivative(buf.Bytes())
		require.NoError(t, err)
		img, _, err := image.Decode(bytes.NewReader(d.data))
		require.NoError(t, err)
		r, g, b, _ := img.At(10, 10).RGBA()
		assert.Greater(t, r>>8, uint32(240))
		assert.Greater(t, g>>8, uint32(240))
		assert.Greater(t, b>>8, uint32(240))
	})
	t.Run("a BMP is decoded when it is too large", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, bmp.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 800, 800)))) // 2.5 MB of BMP
		require.Greater(t, buf.Len(), maxPublishImageBytes)
		d, err := buildDerivative(buf.Bytes())
		require.NoError(t, err)
		assert.Equal(t, "image/jpeg", d.contentType)
	})
	t.Run("what HubRise does not accept is refused", func(t *testing.T) {
		_, err := buildDerivative([]byte("<html>not an image</html>"))
		assert.ErrorContains(t, err, "not one HubRise accepts")
		_, err = buildDerivative([]byte("\x00\x00\x00\x20ftypavif\x00\x00\x00\x00avifmif1miaf"))
		assert.ErrorContains(t, err, "not one HubRise accepts", "AVIF is not uploadable")
	})
	t.Run("a large file that is not a picture cannot be decoded", func(t *testing.T) {
		broken := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x42}, maxPublishImageBytes+10)...)
		_, err := buildDerivative(broken)
		assert.ErrorContains(t, err, "cannot be decoded")
	})
	t.Run("a small WEBP or GIF is sent as it is", func(t *testing.T) {
		webp := append([]byte("RIFF\x10\x00\x00\x00WEBPVP8 "), make([]byte, 8)...)
		d, err := buildDerivative(webp)
		require.NoError(t, err)
		assert.Equal(t, "image/webp", d.contentType)
		d, err = buildDerivative([]byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"))
		require.NoError(t, err)
		assert.Equal(t, "image/gif", d.contentType)
	})
}

// pngOf is a small opaque PNG.
func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 90, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// noisePNG is a PNG that does not compress.
func noisePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, noiseNRGBA(w, h, 255)))
	return buf.Bytes()
}

func noiseNRGBA(w, h int, alpha uint8) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	seed := uint32(2463534242)
	for i := range img.Pix {
		seed ^= seed << 13
		seed ^= seed >> 17
		seed ^= seed << 5
		img.Pix[i] = uint8(seed)
		if i%4 == 3 {
			img.Pix[i] = alpha
		}
	}
	return img
}
