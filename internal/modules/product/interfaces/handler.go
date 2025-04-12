package interfaces

import (
	"bytes"
	"encoding/json"
	"github.com/google/uuid"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"tsb-service/internal/modules/product/domain"

	"tsb-service/internal/modules/product/application"

	"github.com/gin-gonic/gin"
)

// ProductHandler handles HTTP requests for product operations.
type ProductHandler struct {
	service application.ProductService
}

// NewProductHandler creates a new ProductHandler with the given service.
func NewProductHandler(service application.ProductService) *ProductHandler {
	return &ProductHandler{service: service}
}

// CreateProductHandler handles the HTTP POST request for creating a product.
func (h *ProductHandler) CreateProductHandler(c *gin.Context) {
	// Parse the multipart form (limit to 10 MB in memory).
	if err := c.Request.ParseMultipartForm(10 << 20); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to parse multipart form: " + err.Error()})
		return
	}

	// Decode the JSON payload from the "data" field.
	data := c.PostForm("data")
	var req CreateProductRequest
	if err := json.Unmarshal([]byte(data), &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request payload: " + err.Error()})
		return
	}

	// Create the product.
	product, err := h.service.CreateProduct(
		c.Request.Context(),
		req.CategoryID,
		req.Price,
		req.Code,
		req.IsVisible,
		req.IsAvailable,
		req.IsHalal,
		req.IsVegan,
		req.Translations,
	)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create product: " + err.Error()})
		return
	}

	// If an image file is provided, handle the file upload.
	file, fileHeader, err := c.Request.FormFile("image")
	if err == nil { // file was provided
		defer func(file multipart.File) {
			err := file.Close()
			if err != nil {
				log.Printf("failed to close file: %v", err)
			}
		}(file)

		// Prepare a new multipart form request for the file service.
		fileServiceUrl := os.Getenv("FILE_SERVICE_URL")
		var b bytes.Buffer
		writer := multipart.NewWriter(&b)

		// Add the file field.
		part, err := writer.CreateFormFile("image", fileHeader.Filename)
		if err != nil {
			log.Printf("failed to create form file part: %v", err)
		} else {
			if _, err := io.Copy(part, file); err != nil {
				log.Printf("failed to copy file content: %v", err)
			}
		}

		if product.Slug != nil {
			// Add the product_slug field.
			if err := writer.WriteField("product_slug", *product.Slug); err != nil {
				log.Printf("failed to write product_slug field: %v", err)
			}
			err := writer.Close()
			if err != nil {
				return
			}

			// Build and send the POST request to the file service.
			uploadReq, err := http.NewRequest("POST", fileServiceUrl+"/upload", &b)
			if err != nil {
				log.Printf("failed to create file upload request: %v", err)
			} else {
				uploadReq.Header.Set("Content-Type", writer.FormDataContentType())
				client := &http.Client{}
				resp, err := client.Do(uploadReq)
				if err != nil {
					log.Printf("failed to upload file: %v", err)
				} else {
					defer func(Body io.ReadCloser) {
						err := Body.Close()
						if err != nil {
							log.Printf("failed to close response body: %v", err)
						}
					}(resp.Body)
					// Optionally, check the response status code or response body here.
					if resp.StatusCode != http.StatusOK {
						log.Printf("file service responded with status: %s", resp.Status)
					}
				}
			}
		}
	} else {
		// No file provided; log the information.
		log.Println("no image file provided in the request")
	}

	// Return the newly created product.
	res := NewAdminProductResponse(product)
	c.JSON(http.StatusOK, res)
}

func (h *ProductHandler) GetProductHandler(c *gin.Context) {
	// Retrieve product by ID (omitting error handling for brevity)
	product, _ := h.service.GetProduct(c.Request.Context(), c.Param("id"))

	userLocale := c.GetString("lang")
	if userLocale == "" {
		userLocale = "fr"
	}

	// Build your response DTO including only the chosen translation.
	res := NewPublicProductResponse(product, userLocale)

	c.JSON(http.StatusOK, res)
}

func (h *ProductHandler) GetProductsHandler(c *gin.Context) {
	// Retrieve all products (omitting error handling for brevity)
	products, err := h.service.GetProducts(c.Request.Context())

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Assume you extract the user's preferred locale from the request header.
	userLocale := c.GetHeader("Accept-Language")

	// Build your response DTOs including only the chosen translations.
	var res []PublicProductResponse
	for _, p := range products {
		if p.IsVisible == true {
			res = append(res, *NewPublicProductResponse(p, userLocale))
		}
	}

	c.JSON(http.StatusOK, res)
}

func (h *ProductHandler) GetAdminProductsHandler(c *gin.Context) {
	products, err := h.service.GetProducts(c.Request.Context())

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, products)
}

// GetCategoriesHandler retrieves a list of categories.
func (h *ProductHandler) GetCategoriesHandler(c *gin.Context) {
	// Retrieve all categories (omitting error handling for brevity)
	categories, err := h.service.GetCategories(c.Request.Context())

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Assume you extract the user's preferred locale from the request header.
	userLocale := c.GetHeader("Accept-Language")

	// Build your response DTOs including only the chosen translations.
	var res []PublicCategoryResponse
	for _, c := range categories {
		res = append(res, *NewPublicCategoryResponse(c, userLocale))
	}

	c.JSON(http.StatusOK, res)
}

// GetProductsByCategoryHandler retrieves a list of products for a given category.
func (h *ProductHandler) GetProductsByCategoryHandler(c *gin.Context) {
	// Retrieve products by category ID (omitting error handling for brevity)
	categoryID := c.Param("categoryID")
	products, err := h.service.GetProductsByCategory(c.Request.Context(), categoryID)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Assume you extract the user's preferred locale from the request header.
	userLocale := c.GetHeader("Accept-Language")

	// Build your response DTOs including only the chosen translations.
	var res []PublicProductResponse
	for _, p := range products {
		res = append(res, *NewPublicProductResponse(p, userLocale))
	}

	c.JSON(http.StatusOK, res)
}

// UpdateProductHandler handles partial product updates.
func (h *ProductHandler) UpdateProductHandler(c *gin.Context) {
	// 1. Get product id from URL parameter.
	idStr := c.Param("id")
	productID, err := uuid.Parse(idStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid product id"})
		return
	}

	// 2. Retrieve the current product.
	currentProduct, err := h.service.GetProduct(c.Request.Context(), productID.String())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve product"})
		return
	}

	// 3. Parse the multipart form (limit to 10 MB).
	if err := c.Request.ParseMultipartForm(10 << 20); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to parse multipart form: " + err.Error()})
		return
	}

	// 4. Decode the JSON payload from the "data" field.
	data := c.PostForm("data")
	var req UpdateProductRequest
	if err := json.Unmarshal([]byte(data), &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request payload: " + err.Error()})
		return
	}

	// 5. Merge update values if provided.
	// (Adjust these according to your UpdateProductRequest struct and business logic.)
	if req.Price != nil {
		currentProduct.Price = *req.Price
	}
	if req.Code != nil {
		currentProduct.Code = req.Code
	}
	if req.PieceCount != nil {
		currentProduct.PieceCount = req.PieceCount
	}
	if req.IsVisible != nil {
		currentProduct.IsVisible = *req.IsVisible
	}
	if req.IsAvailable != nil {
		currentProduct.IsAvailable = *req.IsAvailable
	}
	if req.IsHalal != nil {
		currentProduct.IsHalal = *req.IsHalal
	}
	if req.IsVegan != nil {
		currentProduct.IsVegan = *req.IsVegan
	}
	if req.Translations != nil {
		var translations []domain.Translation
		for _, t := range *req.Translations {
			translations = append(translations, domain.Translation{
				Language:    t.Language,
				Name:        t.Name,
				Description: t.Description,
			})
		}
		currentProduct.Translations = translations
	}

	// 6. If an image file is provided, handle the file upload.
	file, fileHeader, err := c.Request.FormFile("image")
	if err == nil { // image file was provided
		defer func(file multipart.File) {
			err := file.Close()
			if err != nil {
				log.Printf("failed to close file: %v", err)
			}
		}(file)

		// Prepare a new multipart form request for the file service.
		fileServiceUrl := os.Getenv("FILE_SERVICE_URL")
		var b bytes.Buffer
		writer := multipart.NewWriter(&b)

		// Add the file field.
		part, err := writer.CreateFormFile("image", fileHeader.Filename)
		if err != nil {
			log.Printf("failed to create form file part: %v", err)
		} else {
			if _, err := io.Copy(part, file); err != nil {
				log.Printf("failed to copy file content: %v", err)
			}
		}

		// If the product's slug exists, add the product_slug field.
		if currentProduct.Slug != nil {
			if err := writer.WriteField("product_slug", *currentProduct.Slug); err != nil {
				log.Printf("failed to write product_slug field: %v", err)
			}
			err := writer.Close()
			if err != nil {
				return
			}

			// Build and send the POST request to the file service.
			uploadReq, err := http.NewRequest("POST", fileServiceUrl+"/upload", &b)
			if err != nil {
				log.Printf("failed to create file upload request: %v", err)
			} else {
				uploadReq.Header.Set("Content-Type", writer.FormDataContentType())
				client := &http.Client{}
				resp, err := client.Do(uploadReq)
				if err != nil {
					log.Printf("failed to upload file: %v", err)
				} else {
					defer func(Body io.ReadCloser) {
						err := Body.Close()
						if err != nil {
							log.Printf("failed to close response body: %v", err)
						}
					}(resp.Body)
					if resp.StatusCode != http.StatusOK {
						log.Printf("file service responded with status: %s", resp.Status)
					}
				}
			}
		}
	} else {
		// No image file provided; optionally log this information.
		log.Println("no image file provided in the update request")
	}

	// 7. Call the service layer to update the product.
	if err := h.service.UpdateProduct(c.Request.Context(), currentProduct); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update product: " + err.Error()})
		return
	}

	// 8. Retrieve the updated product.
	updatedProduct, err := h.service.GetProduct(c.Request.Context(), productID.String())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to retrieve updated product"})
		return
	}

	// 9. Create a response DTO from the product domain object and return it.
	res := NewAdminProductResponse(updatedProduct)
	c.JSON(http.StatusOK, res)
}
