package controllers

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"image"
	"log"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"time"
	"tsb-service/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/disintegration/imaging"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// S3 Client
var s3Client *s3.Client

// Initialize the S3 client (done separately from DB config)
func InitS3() {
	cfg, err := awscfg.LoadDefaultConfig(context.TODO())
	if err != nil {
		log.Fatalf("unable to load SDK config for AWS, %v", err)
	}
	s3Client = s3.NewFromConfig(cfg)
}

// UploadImage uploads the product image and its thumbnails
func UploadImage(c *gin.Context) {
	// Validate the form input
	productID := c.Param("id")
	if _, err := uuid.Parse(productID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid product ID"})
		return
	}

	// Fetch the product slug from the database
	slug, err := getProductSlug(productID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch product slug"})
		return
	}

	// Validate image
	file, err := c.FormFile("image")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File is required"})
		return
	}

	if err := validateImage(file); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Open image file
	srcFile, err := file.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to open image file"})
		return
	}
	defer srcFile.Close()

	// Load the original image into memory
	img, err := imaging.Decode(srcFile)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to decode image"})
		return
	}

	// Generate image and thumbnail in different formats
	formats := []string{"png", "webp", "avif"}
	baseFileName := slug + "-" + fmt.Sprintf("%d", time.Now().Unix())

	for _, format := range formats {
		// Resize to normal size (600px width)
		normalSize := imaging.Resize(img, 600, 0, imaging.Lanczos)
		uploadToS3(normalSize, baseFileName+"."+format, format, "images/")

		// Resize to thumbnail size (350px width)
		thumbnail := imaging.Resize(img, 350, 0, imaging.Lanczos)
		uploadToS3(thumbnail, baseFileName+"."+format, format, "images/thumbnails/")
	}

	c.JSON(http.StatusOK, gin.H{"message": "Images uploaded successfully"})
}

// Fetch the product slug by product ID
func getProductSlug(productID string) (string, error) {
	var slug string
	query := "SELECT slug FROM products WHERE id = $1"
	err := config.DB.QueryRow(query, productID).Scan(&slug)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("product not found")
	}
	if err != nil {
		return "", err
	}
	return slug, nil
}

// Validate image for file type and size (max 5MB)
func validateImage(file *multipart.FileHeader) error {
	if file.Size > 5*1024*1024 { // 5MB limit
		return fmt.Errorf("file size exceeds 5MB")
	}

	ext := filepath.Ext(file.Filename)
	if ext != ".jpg" && ext != ".jpeg" && ext != ".png" && ext != ".gif" {
		return fmt.Errorf("unsupported file type")
	}

	return nil
}

// Upload an image to S3
func uploadToS3(img image.Image, fileName, format, folder string) error {
	// Encode image to buffer based on format
	var buf bytes.Buffer
	switch format {
	case "png":
		err := imaging.Encode(&buf, img, imaging.PNG)
		if err != nil {
			return fmt.Errorf("failed to encode PNG: %v", err)
		}
	case "webp":
		err := imaging.Encode(&buf, img, imaging.JPEG) // Use JPEG encoding as Go's stdlib lacks native WebP support
		if err != nil {
			return fmt.Errorf("failed to encode WebP: %v", err)
		}
	case "avif":
		// For AVIF, you'd need a separate library like libvips.
		err := imaging.Encode(&buf, img, imaging.JPEG) // Placeholder for AVIF
		if err != nil {
			return fmt.Errorf("failed to encode AVIF: %v", err)
		}
	default:
		return fmt.Errorf("unsupported format: %v", format)
	}

	// Upload to S3
	key := folder + fileName
	_, err := s3Client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: aws.String("tsb-storage"), // Update with your bucket
		Key:    aws.String(key),
		Body:   bytes.NewReader(buf.Bytes()),
	})

	if err != nil {
		return fmt.Errorf("failed to upload to S3: %v", err)
	}
	return nil
}
