package application_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise/application"
	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise/hubrisetest"
)

// ---- the HTTP source ----

func TestTheBucketSourceReadsTheThumbnailOfTheProduct(t *testing.T) {
	id := uuid.New()
	var gotPath, gotMatch string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMatch = r.URL.Path, r.Header.Get("If-None-Match")
		switch {
		case r.URL.Query().Get("x") == "304" || gotMatch == `"v1"`:
			w.WriteHeader(http.StatusNotModified)
		default:
			w.Header().Set("ETag", `"v1"`)
			_, _ = w.Write([]byte("png-bytes"))
		}
	}))
	defer srv.Close()
	src := application.HTTPProductImages{BaseURL: srv.URL + "/"}

	img, err := src.ProductImage(t.Context(), id, "")
	require.NoError(t, err)
	assert.Equal(t, "/images/thumbnails/"+id.String()+".png", gotPath)
	assert.Equal(t, []byte("png-bytes"), img.Data)
	assert.Equal(t, `"v1"`, img.Version)
	assert.False(t, img.Unchanged)
	assert.Empty(t, gotMatch, "no condition without a known version")

	img, err = src.ProductImage(t.Context(), id, `"v1"`)
	require.NoError(t, err)
	assert.Equal(t, `"v1"`, gotMatch)
	assert.True(t, img.Unchanged, "304 costs no transfer")
	assert.Empty(t, img.Data)
	assert.Equal(t, `"v1"`, img.Version)
}

func TestTheBucketSourceFallsBackToLastModifiedAndSaysWhenThereIsNoImage(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status == http.StatusOK {
			w.Header().Set("Last-Modified", "Mon, 26 Oct 2026 10:00:00 GMT")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()
	src := application.HTTPProductImages{BaseURL: srv.URL, Client: srv.Client()}
	img, err := src.ProductImage(t.Context(), uuid.New(), "")
	require.NoError(t, err)
	assert.Equal(t, "Mon, 26 Oct 2026 10:00:00 GMT", img.Version)

	for _, s := range []int{http.StatusNotFound, http.StatusForbidden} {
		status = s
		img, err = src.ProductImage(t.Context(), uuid.New(), "")
		require.NoError(t, err)
		assert.Nil(t, img, "a product without image (%d)", s)
	}
	status = http.StatusInternalServerError
	_, err = src.ProductImage(t.Context(), uuid.New(), "")
	assert.ErrorContains(t, err, "answered 500")
}

func TestTheBucketSourceRefusesWhatItCannotRead(t *testing.T) {
	big := bytes.Repeat([]byte{1}, 20<<20+10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(big) }))
	_, err := application.HTTPProductImages{BaseURL: srv.URL}.ProductImage(t.Context(), uuid.New(), "")
	assert.ErrorContains(t, err, "larger than 20 MB")
	srv.Close()

	_, err = application.HTTPProductImages{BaseURL: srv.URL}.ProductImage(t.Context(), uuid.New(), "")
	assert.ErrorContains(t, err, "read the product image", "a bucket that is down")
	_, err = application.HTTPProductImages{BaseURL: "http://bad host"}.ProductImage(t.Context(), uuid.New(), "")
	assert.Error(t, err, "a base URL that is not one")

	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("short"))
	}))
	defer cut.Close()
	_, err = application.HTTPProductImages{BaseURL: cut.URL}.ProductImage(t.Context(), uuid.New(), "")
	assert.ErrorContains(t, err, "read the product image", "a body that ends early")
}

// ---- the publisher ----

// fakeImages is the product bucket.
type fakeImages struct {
	mu     sync.Mutex
	images map[uuid.UUID]*application.SourceImage
	errs   map[uuid.UUID]error
	calls  []string // product:have
	// errOnFull makes the read without a version fail.
	errOnFull map[uuid.UUID]error
}

func newFakeImages() *fakeImages {
	return &fakeImages{images: map[uuid.UUID]*application.SourceImage{}, errs: map[uuid.UUID]error{}, errOnFull: map[uuid.UUID]error{}}
}

func (f *fakeImages) put(id uuid.UUID, data []byte, version string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images[id] = &application.SourceImage{Data: data, Version: version}
}

func (f *fakeImages) ProductImage(_ context.Context, id uuid.UUID, have string) (*application.SourceImage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, id.String()[:8]+":"+have)
	if err := f.errs[id]; err != nil {
		return nil, err
	}
	if err := f.errOnFull[id]; err != nil && have == "" {
		return nil, err
	}
	img := f.images[id]
	if img == nil {
		return nil, nil
	}
	if have != "" && have == img.Version {
		return &application.SourceImage{Version: have, Unchanged: true}, nil
	}
	return &application.SourceImage{Data: img.Data, Version: img.Version}, nil
}

func (f *fakeImages) reads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func tinyPNG(t *testing.T, shade uint8) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 4, 4))
	for i := range img.Pix {
		img.Pix[i] = shade
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// withImages turns the images on and gives the publisher a bucket.
func (e *penv) withImages() *fakeImages {
	e.t.Helper()
	e.exec(`UPDATE hubrise_connection SET publish_images = TRUE`)
	f := newFakeImages()
	e.pub.ImageSource = f
	e.pub.ImageStamps = application.NewImageStamps()
	return f
}

type catalogProduct struct {
	Ref      string   `json:"ref"`
	ImageIDs []string `json:"image_ids"`
}

// remoteProducts reads the product of the mock catalog by ref.
func (e *penv) remoteProducts() map[string]catalogProduct {
	e.t.Helper()
	var data struct {
		Products []catalogProduct `json:"products"`
	}
	require.NoError(e.t, json.Unmarshal(e.srv.CatalogData(), &data))
	out := map[string]catalogProduct{}
	for _, p := range data.Products {
		out[p.Ref] = p
	}
	return out
}

func (e *penv) productRef(id uuid.UUID) string {
	e.t.Helper()
	l, err := e.links.FindByLocal(e.t.Context(), domain.EntityProduct, id)
	require.NoError(e.t, err)
	return l.HubriseRef
}

func TestTheFirstPublishUploadsTheImageOfTheProductOnce(t *testing.T) {
	e := newPubEnv(t)
	bucket := e.withImages()
	id := e.product("M1", "Maki", "8.90")
	noImage := e.product("M2", "Nigiri", "5.00")
	hidden := e.product("M3", "Caché", "5.00")
	e.exec(`UPDATE products SET is_visible = false WHERE id = $1`, hidden)
	data := tinyPNG(t, 10)
	bucket.put(id, data, `"a"`)
	bucket.put(hidden, data, `"h"`)

	prev, err := e.pub.Preview(t.Context(), false)
	require.NoError(t, err)
	assert.Empty(t, e.srv.Images(), "a preview uploads nothing")
	assert.Zero(t, e.puts())

	out, err := e.pub.PublishNow(t.Context(), application.PublishRequest{PlanHash: prev.Plan.PlanHash})
	require.NoError(t, err, "the plan hash of the preview is accepted although the image ids were not known")
	require.True(t, out.Published)

	imgs := e.srv.Images()
	require.Len(t, imgs, 1, "one image: the visible product that has one")
	assert.Equal(t, "tsb-product-"+id.String()+"-"+imgs[0].MD5[:12], imgs[0].PrivateRef)
	stored, ok := e.srv.ImageBytes(imgs[0].ID)
	require.True(t, ok)
	assert.Equal(t, data, stored, "a small image goes up as it is")
	products := e.remoteProducts()
	assert.Equal(t, []string{imgs[0].ID}, products[e.productRef(id)].ImageIDs)
	assert.Empty(t, products[e.productRef(noImage)].ImageIDs)
	assert.Empty(t, out.Plan.Warnings)
}

func TestAnUnchangedImageIsNeitherDownloadedNorUploadedAgain(t *testing.T) {
	e := newPubEnv(t)
	bucket := e.withImages()
	id := e.product("M1", "Maki", "8.90")
	bucket.put(id, tinyPNG(t, 10), `"a"`)
	e.confirmFirst()
	require.Len(t, e.srv.Images(), 1)
	first := e.srv.Images()[0].ID

	e.edit(`UPDATE products SET price = 9 WHERE id = $1`, id)
	require.True(t, e.tick(time.Minute))
	assert.Len(t, e.srv.Images(), 1, "no second upload")
	assert.Equal(t, []string{first}, e.remoteProducts()[e.productRef(id)].ImageIDs, "the image is still attached")
	last := bucket.calls[len(bucket.calls)-1]
	assert.Equal(t, id.String()[:8]+`:"a"`, last, "the version of the bucket object was sent back")
	assert.Equal(t, 2, e.puts())
}

func TestAChangedImageIsUploadedAndTheOldOneLeavesTheProduct(t *testing.T) {
	e := newPubEnv(t)
	bucket := e.withImages()
	id := e.product("M1", "Maki", "8.90")
	bucket.put(id, tinyPNG(t, 10), `"a"`)
	e.confirmFirst()
	old := e.srv.Images()[0]

	// The menu itself does not change: the picture does.
	bucket.put(id, tinyPNG(t, 200), `"b"`)
	_, err := e.state.MarkDirty(t.Context(), e.clock.Now())
	require.NoError(t, err)
	require.True(t, e.tick(time.Minute), "an image change publishes although the local menu hash is the same")
	require.Len(t, e.srv.Images(), 2)
	var fresh string
	for _, img := range e.srv.Images() {
		if img.ID != old.ID {
			fresh = img.ID
		}
	}
	assert.Equal(t, []string{fresh}, e.remoteProducts()[e.productRef(id)].ImageIDs)
}

func TestAProductThatLostItsImageLosesItInTheCatalog(t *testing.T) {
	e := newPubEnv(t)
	bucket := e.withImages()
	id := e.product("M1", "Maki", "8.90")
	bucket.put(id, tinyPNG(t, 10), `"a"`)
	e.confirmFirst()
	require.NotEmpty(t, e.remoteProducts()[e.productRef(id)].ImageIDs)

	delete(bucket.images, id)
	_, err := e.state.MarkDirty(t.Context(), e.clock.Now())
	require.NoError(t, err)
	require.True(t, e.tick(time.Minute))
	assert.Empty(t, e.remoteProducts()[e.productRef(id)].ImageIDs)
}

func TestAnImageHubRiseDoesNotOwnIsLeftOnTheProduct(t *testing.T) {
	e := newPubEnv(t)
	bucket := e.withImages()
	id := e.product("M1", "Maki", "8.90")
	e.confirmFirst()
	foreign := e.srv.AddImage("foreign", "image/png", tinyPNG(t, 99))

	// Someone attached an image in the HubRise back office.
	var raw map[string]any
	require.NoError(t, json.Unmarshal(e.srv.CatalogData(), &raw))
	for _, p := range raw["products"].([]any) {
		p.(map[string]any)["image_ids"] = []string{foreign.ID}
	}
	body, err := json.Marshal(reupload(raw))
	require.NoError(t, err)
	require.NoError(t, e.srv.ReplaceCatalogRaw(body))

	bucket.put(id, tinyPNG(t, 10), `"a"`)
	_, err = e.state.MarkDirty(t.Context(), e.clock.Now())
	require.NoError(t, err)
	require.True(t, e.tick(time.Minute))
	ids := e.remoteProducts()[e.productRef(id)].ImageIDs
	require.Len(t, ids, 2)
	assert.Contains(t, ids, foreign.ID, "the foreign image stays")
}

func TestAnImageThatFailsIsReportedAndTheCatalogIsStillPublished(t *testing.T) {
	t.Run("the upload fails", func(t *testing.T) {
		e := newPubEnv(t)
		bucket := e.withImages()
		id := e.product("M1", "Maki", "8.90")
		bucket.put(id, tinyPNG(t, 10), `"a"`)
		e.srv.AddFault(hubrisetest.Fault{Method: "POST", Path: "/v1/catalogs/", Status: 422, Times: 1})
		out := e.confirmFirst()
		assert.True(t, out.Published)
		assert.Equal(t, 1, e.puts())
		require.Len(t, out.Plan.Warnings, 1)
		assert.Equal(t, domain.WarnImage, out.Plan.Warnings[0].Code)
		assert.Empty(t, e.remoteProducts()[e.productRef(id)].ImageIDs)
	})
	t.Run("the bucket fails or holds something else than a picture", func(t *testing.T) {
		e := newPubEnv(t)
		bucket := e.withImages()
		a := e.product("M1", "Maki", "8.90")
		b := e.product("M2", "Nigiri", "5.00")
		bucket.errs[a] = errors.New("bucket down")
		bucket.put(b, []byte("<html>"), `"x"`)
		out := e.confirmFirst()
		assert.True(t, out.Published)
		require.Len(t, out.Plan.Warnings, 2)
		for _, w := range out.Plan.Warnings {
			assert.Equal(t, domain.WarnImage, w.Code)
		}
		assert.Empty(t, e.srv.Images())
	})
	t.Run("the images of the catalog cannot be listed", func(t *testing.T) {
		e := newPubEnv(t)
		bucket := e.withImages()
		id := e.product("M1", "Maki", "8.90")
		bucket.put(id, tinyPNG(t, 10), `"a"`)
		e.srv.AddFault(hubrisetest.Fault{Method: "GET", Path: "/v1/catalogs/" + hubrisetest.CatalogID + "/images", Status: 500, Times: 10})
		_, err := e.pub.Preview(t.Context(), false)
		assert.Error(t, err)
		assert.Zero(t, e.puts())
	})
}

func TestWithoutABucketTheImagesAreLeftAsTheyAreWithAWarning(t *testing.T) {
	e := newPubEnv(t)
	e.exec(`UPDATE hubrise_connection SET publish_images = TRUE`)
	e.product("M1", "Maki", "8.90")
	out := e.confirmFirst()
	assert.True(t, out.Published)
	require.Len(t, out.Plan.Warnings, 1)
	assert.Equal(t, domain.WarnImage, out.Plan.Warnings[0].Code)
}

func TestImagesAreOffWhenThePublishImagesSettingIsOff(t *testing.T) {
	e := newPubEnv(t)
	f := newFakeImages()
	e.pub.ImageSource = f
	id := e.product("M1", "Maki", "8.90")
	f.put(id, tinyPNG(t, 10), `"a"`)
	e.confirmFirst()
	assert.Zero(t, f.reads())
	assert.Empty(t, e.srv.Images())
}

func TestAtMostFiftyImagesAreUploadedPerRunAndTheRestFollows(t *testing.T) {
	e := newPubEnv(t)
	bucket := e.withImages()
	for i := range 53 {
		id := e.product("", "P"+uuid.NewString()[:6], "5.00")
		bucket.put(id, tinyPNG(t, uint8(i)), `"v"`)
	}
	e.srv.SetLimits(hubrisetest.Limits{PerMinute: 5000, PerHour: 25000, PerDay: 100000, HeavyPerMinute: 1000})
	out := e.confirmFirst()
	assert.True(t, out.Published)
	assert.Len(t, e.srv.Images(), 50)
	assert.True(t, e.publisherState().Dirty(), "the rest is queued")

	require.True(t, e.tick(time.Minute))
	assert.Len(t, e.srv.Images(), 53)
	assert.False(t, e.publisherState().Dirty())
	withImage := 0
	for _, p := range e.remoteProducts() {
		if len(p.ImageIDs) == 1 {
			withImage++
		}
	}
	assert.Equal(t, 53, withImage)
}

func TestAnImageHubRiseForgotBeforeItWasAttachedIsFetchedAgain(t *testing.T) {
	e := newPubEnv(t)
	bucket := e.withImages()
	id := e.product("M1", "Maki", "8.90")
	bucket.put(id, tinyPNG(t, 10), `"a"`)
	e.confirmFirst()
	require.Len(t, e.srv.Images(), 1)

	// The catalog lost the image (purged) and the product with it.
	var raw map[string]any
	require.NoError(t, json.Unmarshal(e.srv.CatalogData(), &raw))
	for _, p := range raw["products"].([]any) {
		delete(p.(map[string]any), "image_ids")
	}
	body, err := json.Marshal(reupload(raw))
	require.NoError(t, err)
	require.NoError(t, e.srv.ReplaceCatalogRaw(body))
	e.srv.PurgeImages()
	require.Empty(t, e.srv.Images())

	_, err = e.state.MarkDirty(t.Context(), e.clock.Now())
	require.NoError(t, err)
	require.True(t, e.tick(time.Minute))
	require.Len(t, e.srv.Images(), 1, "uploaded again")
	assert.Equal(t, id.String()[:8]+":", bucket.calls[len(bucket.calls)-1], "read again without a version")
	assert.NotEmpty(t, e.remoteProducts()[e.productRef(id)].ImageIDs)
}

// reupload turns the catalog the mock serves (ids) back into what an EPOS uploads (refs).
func reupload(served map[string]any) map[string]any {
	refs := map[string]string{}
	for _, c := range served["categories"].([]any) {
		c := c.(map[string]any)
		refs[c["id"].(string)] = c["ref"].(string)
		delete(c, "id")
	}
	for _, p := range served["products"].([]any) {
		p := p.(map[string]any)
		delete(p, "id")
		if id, ok := p["category_id"].(string); ok {
			p["category_ref"] = refs[id]
		}
		delete(p, "category_id")
		for _, s := range p["skus"].([]any) {
			delete(s.(map[string]any), "id")
		}
	}
	return served
}

func TestTheMemoryOfTheBucketIsOptional(t *testing.T) {
	e := newPubEnv(t)
	bucket := e.withImages()
	e.pub.ImageStamps = nil // nothing is remembered: every run reads the images again
	id := e.product("M1", "Maki", "8.90")
	bucket.put(id, tinyPNG(t, 10), `"a"`)
	e.confirmFirst()
	first := e.srv.Images()
	require.Len(t, first, 1)

	e.edit(`UPDATE products SET price = 9 WHERE id = $1`, id)
	require.True(t, e.tick(time.Minute))
	assert.Equal(t, first, e.srv.Images(), "the same content is found by its private_ref, not uploaded again")
	assert.Equal(t, id.String()[:8]+":", bucket.calls[len(bucket.calls)-1])
	assert.Equal(t, []string{first[0].ID}, e.remoteProducts()[e.productRef(id)].ImageIDs)

	e.pub.ImageStamps = &application.ImageStamps{} // the zero value remembers nothing either
	e.edit(`UPDATE products SET price = 10 WHERE id = $1`, id)
	require.True(t, e.tick(time.Minute))
	assert.Equal(t, first, e.srv.Images())
}

func TestAnImageThatCannotBeReadAgainIsReported(t *testing.T) {
	e := newPubEnv(t)
	bucket := e.withImages()
	id := e.product("M1", "Maki", "8.90")
	bucket.put(id, tinyPNG(t, 10), `"a"`)
	e.confirmFirst()
	e.srv.PurgeImages()
	bucket.errOnFull[id] = errors.New("bucket down")
	prev, err := e.pub.Preview(t.Context(), false)
	require.NoError(t, err)
	require.Len(t, prev.Plan.Warnings, 1)
	assert.Equal(t, domain.WarnImage, prev.Plan.Warnings[0].Code)
	assert.Contains(t, prev.Plan.Warnings[0].Message, "could not be read again")
}

func TestWhenEveryImageFailsTheProductsKeepTheirImages(t *testing.T) {
	e := newPubEnv(t)
	bucket := e.withImages()
	id := e.product("M1", "Maki", "8.90")
	bucket.put(id, tinyPNG(t, 10), `"a"`)
	e.confirmFirst()
	attached := e.remoteProducts()[e.productRef(id)].ImageIDs
	require.Len(t, attached, 1)

	bucket.errs[id] = errors.New("bucket down")
	e.edit(`UPDATE products SET price = 9 WHERE id = $1`, id)
	require.True(t, e.tick(time.Minute))
	assert.Equal(t, attached, e.remoteProducts()[e.productRef(id)].ImageIDs, "a bucket outage detaches nothing")
}

func TestTheRunsAreStoppedByTheProblemsOfTheImagePath(t *testing.T) {
	t.Run("the catalog images cannot be listed in the background", func(t *testing.T) {
		e := newPubEnv(t)
		bucket := e.withImages()
		id := e.product("M1", "Maki", "8.90")
		e.confirmFirst()
		bucket.put(id, tinyPNG(t, 10), `"a"`)
		e.edit(`UPDATE products SET price = 9 WHERE id = $1`, id)
		e.srv.AddFault(hubrisetest.Fault{Method: "GET", Path: "/v1/catalogs/" + hubrisetest.CatalogID + "/images", Status: 500, Times: 100})
		require.True(t, e.tick(time.Minute))
		assert.Equal(t, domain.PublishUnavailable, e.publisherState().ErrorCode())
		assert.Equal(t, 1, e.puts())
	})
	t.Run("the lease is lost before the uploads", func(t *testing.T) {
		for failOn := 1; failOn <= 5; failOn++ {
			e := newPubEnv(t)
			bucket := e.withImages()
			id := e.product("M1", "Maki", "8.90")
			e.confirmFirst()
			bucket.put(id, tinyPNG(t, 10), `"a"`)
			e.edit(`UPDATE products SET price = 9 WHERE id = $1`, id)
			e.clock.Advance(time.Minute)
			p := *e.pub
			p.State = &pubFaultyState{PublishStateRepository: e.pub.State, extendFailOn: failOn}
			_, err := p.Tick(t.Context())
			if failOn <= 4 {
				assert.ErrorIs(t, err, errPubBoom, "extension %d", failOn)
				assert.Equal(t, 1, e.puts(), "no write (extension %d)", failOn)
			}
		}
	})
	t.Run("the follow-up of the remaining uploads cannot be scheduled", func(t *testing.T) {
		e := newPubEnv(t)
		bucket := e.withImages()
		for i := range 51 {
			id := e.product("", "P"+uuid.NewString()[:6], "5.00")
			bucket.put(id, tinyPNG(t, uint8(i)), `"v"`)
		}
		e.srv.SetLimits(hubrisetest.Limits{PerMinute: 5000, PerHour: 25000, PerDay: 100000, HeavyPerMinute: 1000})
		p := *e.pub
		p.State = &pubFaultyState{PublishStateRepository: e.pub.State, fail: map[string]bool{"MarkDirty": true}}
		prev, err := p.Preview(t.Context(), false)
		require.NoError(t, err)
		out, err := p.PublishNow(t.Context(), application.PublishRequest{PlanHash: prev.Plan.PlanHash})
		require.NoError(t, err, "the publish itself succeeded")
		assert.True(t, out.Published)
		assert.Len(t, e.srv.Images(), 50)
	})
}

// ---- the planner ----

func TestThePlannerKeepsForeignImagesAndReplacesTheOwnOnes(t *testing.T) {
	own := puid(2)
	local := func() *domain.LocalSnapshot {
		return &domain.LocalSnapshot{
			Categories: []domain.LocalCategory{{ID: puid(1), Slug: "own", Order: 1, Names: ptexts("Own", "")}},
			Products: []domain.LocalProduct{{ID: own, CategoryID: puid(1), Code: "OWN1", Price: pdec("1.00"), VatCategory: "food",
				Visible: true, Available: true, Texts: ptexts("Own product", "")}},
		}
	}
	var links []domain.CatalogLink
	plan := func(base string, desired map[uuid.UUID]string) *application.PublishPlan {
		t.Helper()
		p, err := application.PlanPublish(application.PublishInput{
			Local: local(), Links: links, Settings: application.PublishSettings{Language: "fr", Images: true}, Base: json.RawMessage(base),
			Images: &application.PublishImages{Desired: desired, Owned: map[string]uuid.UUID{"old": own, "gone": uuid.New()}},
		})
		require.NoError(t, err)
		return p
	}
	imageIDs := func(p *application.PublishPlan) []any {
		var data struct {
			Products []map[string]any `json:"products"`
		}
		require.NoError(t, json.Unmarshal(p.Data, &data))
		require.Len(t, data.Products, 1)
		ids, _ := data.Products[0]["image_ids"].([]any)
		return ids
	}

	fresh := plan(`{}`, map[uuid.UUID]string{own: "new"})
	assert.Equal(t, []any{"new"}, imageIDs(fresh), "a new product carries its image")

	for _, l := range fresh.NewLinks {
		links = append(links, linkOf(l.Entity, l.LocalID, l.Key, l.Ref))
	}
	// withImages is the catalog the first publish made, with image ids on its product.
	withImages := func(ids ...string) string {
		var data map[string]any
		require.NoError(t, json.Unmarshal(fresh.Data, &data))
		data["products"].([]any)[0].(map[string]any)["image_ids"] = ids
		out, err := json.Marshal(data)
		require.NoError(t, err)
		return string(out)
	}
	base := withImages("foreign", "old")
	assert.Equal(t, []any{"foreign", "new"}, imageIDs(plan(base, map[uuid.UUID]string{own: "new"})), "the own image is replaced, the foreign one stays")
	assert.Equal(t, []any{"foreign"}, imageIDs(plan(base, map[uuid.UUID]string{own: ""})), "no image: the own one goes")
	assert.Equal(t, []any{"foreign", "old"}, imageIDs(plan(base, map[uuid.UUID]string{})), "no decision: nothing is touched")
	assert.Equal(t, []any{"foreign", "old"}, imageIDs(plan(base, map[uuid.UUID]string{own: "old"})), "the same image is not doubled")

	alone := withImages("old")
	assert.Empty(t, imageIDs(plan(alone, map[uuid.UUID]string{own: ""})), "the key leaves with its last id")

	// Only the image differs: the plan says so, and the plan hash that was confirmed does not change.
	a, b := plan(base, map[uuid.UUID]string{own: "old"}), plan(base, map[uuid.UUID]string{own: "new"})
	assert.Equal(t, a.PlanHash, b.PlanHash)
	var fields [][]string
	for _, c := range b.Changes {
		if c.Action == application.ActionPublishUpdate {
			fields = append(fields, c.Fields)
		}
	}
	assert.Equal(t, [][]string{{application.FieldPubImage}}, fields)
	assert.True(t, b.NeedsPut)
	assert.False(t, a.NeedsPut)
}
