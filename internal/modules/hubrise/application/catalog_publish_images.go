package application

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	xdraw "golang.org/x/image/draw"

	// The decoders of the images the shop and HubRise exchange (JPEG and PNG are decoded through
	// image.Decode; GIF, BMP and WEBP are registered here).
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"

	_ "image/gif"
	_ "image/png"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
)

// Product images travel with the published catalog when publish_images is on (amendment A15.8):
// a derivative of at most 1 MB is uploaded to the catalog under a private_ref that names the product
// and the content, so an unchanged image is never uploaded twice, and its id goes into the
// image_ids of the HubRise product next to the ids the shop does not own.
const (
	// maxPublishImageBytes is the largest image HubRise accepts.
	maxPublishImageBytes = 1 << 20
	// maxPublishImageSide is the long side, in pixels, of a derivative that had to be shrunk.
	maxPublishImageSide = 1200
	// maxImageUploadsPerRun bounds the uploads of one run; the rest is done by the next run.
	maxImageUploadsPerRun = 50
	// maxSourceImageBytes bounds what is read from the product bucket.
	maxSourceImageBytes = 20 << 20
	// imageRefPrefix starts the private_ref of every image the shop uploads.
	imageRefPrefix = "tsb-product-"
	// pendingImagePrefix marks the id of an image that will be uploaded after the confirmation.
	pendingImagePrefix = "pending:"
)

// SourceImage is the image of a product as the product bucket serves it.
type SourceImage struct {
	Data []byte
	// Version identifies the stored object (its ETag): a later call that passes it back gets
	// Unchanged instead of the bytes.
	Version string
	// Unchanged says the image still has the version the caller holds; Data is empty then.
	Unchanged bool
}

// ProductImageSource reads the image of a shop product.
type ProductImageSource interface {
	// ProductImage returns nil when the product has no image. have is the Version of an earlier call
	// ("" for none).
	ProductImage(ctx context.Context, id uuid.UUID, have string) (*SourceImage, error)
}

// HTTPProductImages reads the product images from the public product bucket, where the shop front
// serves them from (BaseURL/images/thumbnails/<product id>.png).
type HTTPProductImages struct {
	BaseURL string
	Client  *http.Client
}

var _ ProductImageSource = HTTPProductImages{}

// ProductImage fetches the image with a conditional request, so an unchanged image costs no transfer.
func (h HTTPProductImages) ProductImage(ctx context.Context, id uuid.UUID, have string) (*SourceImage, error) {
	url := strings.TrimRight(h.BaseURL, "/") + "/images/thumbnails/" + id.String() + ".png"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if have != "" {
		req.Header.Set("If-None-Match", have)
	}
	client := h.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read the product image: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxSourceImageBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read the product image: %w", err)
		}
		if len(data) > maxSourceImageBytes {
			return nil, fmt.Errorf("the product image is larger than %d MB", maxSourceImageBytes>>20)
		}
		version := resp.Header.Get("ETag")
		if version == "" {
			version = resp.Header.Get("Last-Modified")
		}
		return &SourceImage{Data: data, Version: version}, nil
	case http.StatusNotModified:
		return &SourceImage{Version: have, Unchanged: true}, nil
	case http.StatusNotFound, http.StatusForbidden:
		return nil, nil // a bucket answers 403 for a key it does not have
	}
	return nil, fmt.Errorf("read the product image: the product bucket answered %d", resp.StatusCode)
}

// derivative is the image as it is uploaded to HubRise.
type derivative struct {
	data        []byte
	contentType string
	md5         string
}

// uploadableTypes are the image types HubRise accepts.
var uploadableTypes = []string{"image/jpeg", "image/png", "image/gif", "image/webp", "image/bmp"}

// buildDerivative returns the image as is when HubRise accepts it, and otherwise a JPEG of at most
// 1 MB: the image is decoded, shrunk to 1200 pixels on its long side when it is larger, laid on a
// white background (JPEG has no transparency) and encoded with a decreasing quality until it fits.
func buildDerivative(data []byte) (*derivative, error) {
	contentType, _, _ := strings.Cut(http.DetectContentType(data), ";")
	if !slices.Contains(uploadableTypes, contentType) {
		return nil, fmt.Errorf("the image type %q is not one HubRise accepts", contentType)
	}
	if len(data) <= maxPublishImageBytes {
		return &derivative{data: data, contentType: contentType, md5: md5Hex(data)}, nil
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("the image cannot be decoded: %w", err)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if long := max(w, h); long > maxPublishImageSide {
		w, h = max(1, w*maxPublishImageSide/long), max(1, h*maxPublishImageSide/long)
	}
	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, xdraw.Src)
	xdraw.CatmullRom.Scale(canvas, canvas.Bounds(), src, b, xdraw.Over, nil)
	// At 1200 pixels a side even noise fits in 1 MB at the lowest quality, so the last try is the answer.
	qualities := []int{85, 75, 65, 55, 45, 35}
	var buf bytes.Buffer
	for i, quality := range qualities {
		buf.Reset()
		_ = jpeg.Encode(&buf, canvas, &jpeg.Options{Quality: quality}) // a bytes.Buffer does not fail
		if buf.Len() <= maxPublishImageBytes || i == len(qualities)-1 {
			break
		}
	}
	return &derivative{data: buf.Bytes(), contentType: "image/jpeg", md5: md5Hex(buf.Bytes())}, nil
}

// imagePrivateRef names an image of a product: the private_ref is unique per catalog, so the same
// content of the same product is found again instead of uploaded again.
func imagePrivateRef(product uuid.UUID, md5 string) string {
	return imageRefPrefix + product.String() + "-" + md5[:12]
}

// imageOwner reads the product out of a private_ref the shop made.
func imageOwner(privateRef string) (uuid.UUID, bool) {
	rest, ok := strings.CutPrefix(privateRef, imageRefPrefix)
	if !ok || len(rest) < 36 {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(rest[:36])
	return id, err == nil
}

// PublishImages is what the planner needs to write the images of the products.
type PublishImages struct {
	// Desired maps a product to the HubRise image id it carries: "" for a product without image.
	// A product without entry keeps whatever HubRise has.
	Desired map[uuid.UUID]string
	// Owned maps an image id of the catalog to the product whose image it is (the images the shop
	// uploaded, found by their private_ref). Other ids are never touched.
	Owned map[string]uuid.UUID
}

// pendingImage is an image the run still has to upload.
type pendingImage struct {
	product    uuid.UUID
	privateRef string
	d          *derivative
}

// imageSet is the images of one run: what is known (from the catalog's image list), what is to be
// uploaded once the publish is confirmed, and what went wrong.
type imageSet struct {
	images   PublishImages
	pending  []pendingImage
	warnings []domain.Warning
	// more says uploads were left for the next run.
	more bool
}

func (s *imageSet) warn(product uuid.UUID, msg string) {
	s.warnings = append(s.warnings, domain.Warning{Code: domain.WarnImage, Key: product.String(), Message: msg})
}

// imageStamp is what an earlier run learned of a product image, to skip the transfer of an
// unchanged one.
type imageStamp struct{ version, md5 string }

// ImageStamps is the memory of the reads of the product bucket (per process: a restart only costs a
// full read). The zero value and nil remember nothing.
type ImageStamps struct{ m *sync.Map }

// NewImageStamps returns a memory that remembers.
func NewImageStamps() *ImageStamps { return &ImageStamps{m: &sync.Map{}} }

func (s *ImageStamps) get(id uuid.UUID) imageStamp {
	if s == nil || s.m == nil {
		return imageStamp{}
	}
	v, _ := s.m.Load(id)
	st, _ := v.(imageStamp)
	return st
}

func (s *ImageStamps) set(id uuid.UUID, st imageStamp) {
	if s != nil && s.m != nil {
		s.m.Store(id, st)
	}
}

func (s *ImageStamps) forget(id uuid.UUID) {
	if s != nil && s.m != nil {
		s.m.Delete(id)
	}
}

// gatherImages reads everything the images of a publish need without writing anything to HubRise:
// the images of the catalog, the image of each visible product and its derivative. An image that
// still has to be uploaded gets a placeholder id the planner writes and uploadImages replaces.
func (p *CatalogPublisher) gatherImages(ctx context.Context, api PublishAPI, catalogID string, snap *domain.LocalSnapshot) (*imageSet, error) {
	set := &imageSet{images: PublishImages{Desired: map[uuid.UUID]string{}, Owned: map[string]uuid.UUID{}}}
	listed, err := api.ListImages(ctx, catalogID, "")
	if err != nil {
		return nil, err
	}
	byRef := make(map[string]hubrise.Image, len(listed))
	for _, img := range listed {
		byRef[img.PrivateRef] = img
		if owner, ok := imageOwner(img.PrivateRef); ok {
			set.images.Owned[img.ID] = owner
		}
	}
	if p.ImageSource == nil {
		set.warnings = append(set.warnings, domain.Warning{Code: domain.WarnImage, Message: "the product image store is not configured, the images are left as they are"})
		return set, nil
	}
	for i := range snap.Products {
		lp := &snap.Products[i]
		if !lp.Visible {
			continue
		}
		p.gatherImage(ctx, set, byRef, lp.ID)
	}
	return set, nil
}

func (p *CatalogPublisher) gatherImage(ctx context.Context, set *imageSet, byRef map[string]hubrise.Image, id uuid.UUID) {
	stamp := p.ImageStamps.get(id)
	src, err := p.ImageSource.ProductImage(ctx, id, stamp.version)
	if err != nil {
		set.warn(id, err.Error())
		return
	}
	if src == nil { // the product has no image: the one HubRise holds from us goes
		p.ImageStamps.forget(id)
		set.images.Desired[id] = ""
		return
	}
	var d *derivative
	if src.Unchanged && stamp.md5 != "" {
		if img, ok := byRef[imagePrivateRef(id, stamp.md5)]; ok {
			set.images.Desired[id] = img.ID
			return
		}
		// HubRise forgot the image (unattached images are purged): read it again.
		if src, err = p.ImageSource.ProductImage(ctx, id, ""); err != nil || src == nil || src.Unchanged {
			set.warn(id, "the product image could not be read again")
			return
		}
	}
	if d, err = buildDerivative(src.Data); err != nil {
		set.warn(id, err.Error())
		return
	}
	p.ImageStamps.set(id, imageStamp{version: src.Version, md5: d.md5})
	ref := imagePrivateRef(id, d.md5)
	if img, ok := byRef[ref]; ok {
		set.images.Desired[id] = img.ID
		return
	}
	set.images.Desired[id] = pendingImagePrefix + ref
	set.pending = append(set.pending, pendingImage{product: id, privateRef: ref, d: d})
}

// uploadImages uploads the images that are not in the catalog yet (at most maxImageUploadsPerRun),
// before the catalog is written, and replaces the placeholders with the ids HubRise gave. An upload
// that fails leaves its product as HubRise has it and is reported.
func (p *CatalogPublisher) uploadImages(ctx context.Context, api PublishAPI, catalogID string, set *imageSet) {
	for i, pend := range set.pending {
		if i >= maxImageUploadsPerRun {
			delete(set.images.Desired, pend.product)
			set.more = true
			continue
		}
		img, err := api.UploadImage(ctx, catalogID, pend.privateRef, pend.d.contentType, pend.d.data)
		if err != nil {
			delete(set.images.Desired, pend.product)
			set.warn(pend.product, "the image could not be uploaded: "+truncate(err.Error(), 200))
			continue
		}
		set.images.Desired[pend.product] = img.ID
		set.images.Owned[img.ID] = pend.product
	}
	set.pending = nil
}
