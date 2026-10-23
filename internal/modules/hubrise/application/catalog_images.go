package application

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // HubRise images carry an MD5 checksum, it is not used for security
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"tsb-service/pkg/utils"
)

// UtilsImageUploader stores product images through the file service (utils.UploadProductImage),
// keyed by the immutable product UUID like every other product image.
type UtilsImageUploader struct{}

// UploadProductImage sends the image to the file service.
func (UtilsImageUploader) UploadProductImage(ctx context.Context, productID uuid.UUID, filename string, data []byte) error {
	return utils.UploadProductImage(ctx, bytes.NewReader(data), filename, productID.String(), false)
}

// imageExtensions maps the media types HubRise accepts to a file extension.
var imageExtensions = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif", "image/webp": ".webp", "image/bmp": ".bmp",
}

// imageFilename names the upload after the product and the image's media type.
func imageFilename(productID uuid.UUID, contentType string) string {
	ext, ok := imageExtensions[strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))]
	if !ok {
		ext = ".jpg"
	}
	return productID.String() + ext
}

// md5Hex is the checksum HubRise reports for an image.
func md5Hex(data []byte) string {
	sum := md5.Sum(data) //nolint:gosec // see import
	return hex.EncodeToString(sum[:])
}

// errImageChecksum is returned when a downloaded image does not match the checksum HubRise reported.
func errImageChecksum(want, got string) error {
	return fmt.Errorf("image checksum mismatch: HubRise says %s, downloaded %s", want, got)
}
