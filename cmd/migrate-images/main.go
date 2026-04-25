// migrate-images renames existing S3 product images from {slug}.{ext} to {id}.{ext}.
// Run once after deploying the UUID-based image key change.
// Safe to re-run: products with no uploaded image (404 from file service) are skipped.
//
// Usage: go run cmd/migrate-images/main.go
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"tsb-service/pkg/utils"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

type productRow struct {
	ID   string
	Slug string
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, reading env from environment")
	}

	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		os.Getenv("DB_HOST"), os.Getenv("DB_PORT"),
		os.Getenv("DB_USERNAME"), os.Getenv("DB_PASSWORD"),
		os.Getenv("DB_DATABASE"), os.Getenv("DB_SSL_MODE"))

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.Query(`SELECT id::text, slug FROM products WHERE slug IS NOT NULL AND slug != '' ORDER BY created_at`)
	if err != nil {
		log.Fatalf("query products: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var products []productRow
	for rows.Next() {
		var p productRow
		if err := rows.Scan(&p.ID, &p.Slug); err != nil {
			log.Fatalf("scan row: %v", err)
		}
		products = append(products, p)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("rows error: %v", err)
	}

	log.Printf("Found %d products to migrate", len(products))

	ctx := context.Background()
	ok, skipped, failed := 0, 0, 0

	for _, p := range products {
		if p.Slug == p.ID {
			log.Printf("  SKIP %s — already using UUID as key", p.ID)
			skipped++
			continue
		}

		err := utils.RenameProductImage(ctx, p.Slug, p.ID)
		if err != nil {
			// 404 means no image was ever uploaded for this product.
			if strings.Contains(err.Error(), "status 404") {
				log.Printf("  SKIP %s (slug=%s) — no image on file service", p.ID, p.Slug)
				skipped++
			} else {
				log.Printf("  FAIL %s (slug=%s) — %v", p.ID, p.Slug, err)
				failed++
			}
			continue
		}

		log.Printf("  OK   %s  %s → %s", p.ID, p.Slug, p.ID)
		ok++
	}

	log.Printf("Done: %d renamed, %d skipped, %d failed", ok, skipped, failed)
}
