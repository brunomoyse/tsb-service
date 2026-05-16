// Command zitadel-lowercase-emails scans every human user in the configured
// Zitadel instance and rewrites any non-canonical email address to its
// lowercase, trimmed form. Pre-existing mixed-case rows can otherwise break
// findZitadelUserByEmail (TEXT_QUERY_METHOD_EQUALS is case-sensitive) and
// cause drift versus the app's users table.
//
// Run with --dry-run first to see what would change.
package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"go.uber.org/zap"

	"tsb-service/pkg/logging"
)

const userPageSize = 100

type userRecord struct {
	UserID   string `json:"userId"`
	Username string `json:"username"`
	Human    struct {
		Email struct {
			Email      string `json:"email"`
			IsVerified bool   `json:"isVerified"`
		} `json:"email"`
	} `json:"human"`
}

func main() {
	dryRun := flag.Bool("dry-run", false, "List candidates without writing changes")
	flag.Parse()

	if err := godotenv.Load(); err != nil {
		zap.L().Warn("no .env file found, using environment variables")
	}

	logLevel := cmp.Or(os.Getenv("LOG_LEVEL"), "info")
	logFormat := cmp.Or(os.Getenv("LOG_FORMAT"), "text")
	logging.Setup(logLevel, logFormat)
	defer logging.Sync()

	issuer := strings.TrimRight(os.Getenv("ZITADEL_ISSUER"), "/")
	internal := strings.TrimRight(os.Getenv("ZITADEL_INTERNAL_URL"), "/")
	pat := cmp.Or(os.Getenv("ZITADEL_ADMIN_PAT"), os.Getenv("ZITADEL_SERVICE_PAT"))
	if issuer == "" || pat == "" {
		zap.L().Fatal("ZITADEL_ISSUER and ZITADEL_ADMIN_PAT (or ZITADEL_SERVICE_PAT) are required")
	}

	baseURL := issuer
	externalHost := ""
	if internal != "" {
		baseURL = internal
		externalHost = strings.TrimPrefix(strings.TrimPrefix(issuer, "https://"), "http://")
	}

	httpClient := &http.Client{Timeout: 15 * time.Second}
	call := func(method, path string, body any) ([]byte, int, error) {
		var reader io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				return nil, 0, err
			}
			reader = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, baseURL+path, reader)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+pat)
		if externalHost != "" {
			req.Host = externalHost
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer func() { _ = resp.Body.Close() }()
		respBody, err := io.ReadAll(resp.Body)
		return respBody, resp.StatusCode, err
	}

	log := zap.L()
	log.Info("scanning Zitadel users for non-canonical emails",
		zap.Bool("dry_run", *dryRun),
		zap.String("base_url", baseURL),
	)

	var (
		scanned int
		drift   int
		updated int
		failed  int
		offset  int
	)

	for {
		searchBody := map[string]any{
			"query": map[string]any{
				"offset": offset,
				"limit":  userPageSize,
				"asc":    true,
			},
		}
		respBody, status, err := call("POST", "/v2/users", searchBody)
		if err != nil {
			log.Fatal("user search failed", zap.Error(err))
		}
		if status != http.StatusOK {
			log.Fatal("user search returned non-200",
				zap.Int("status", status),
				zap.ByteString("body", respBody),
			)
		}

		var page struct {
			Result []userRecord `json:"result"`
		}
		if err := json.Unmarshal(respBody, &page); err != nil {
			log.Fatal("decode user page failed", zap.Error(err))
		}
		if len(page.Result) == 0 {
			break
		}
		scanned += len(page.Result)

		for _, u := range page.Result {
			original := u.Human.Email.Email
			canonical := strings.ToLower(strings.TrimSpace(original))
			if original == "" || original == canonical {
				continue
			}
			drift++
			// If username == old email (placeholder users created by
			// createPlaceholderZitadelUser fit this pattern), the username
			// must be rewritten too — Zitadel's SetEmail endpoint touches the
			// email field only, leaving username/loginNames stale.
			usernameNeedsUpdate := u.Username == original
			log.Info("drift detected",
				zap.String("user_id", u.UserID),
				zap.String("from", original),
				zap.String("to", canonical),
				zap.Bool("verified", u.Human.Email.IsVerified),
				zap.Bool("rewrite_username", usernameNeedsUpdate),
			)
			if *dryRun {
				continue
			}

			// SetEmail body: oneof verification at top level — `isVerified: true`
			// preserves verified status for casing-only changes. Omitting the
			// flag leaves the email unverified, which is the right default for
			// rows that were unverified to begin with.
			updateBody := map[string]any{
				"email": canonical,
			}
			if u.Human.Email.IsVerified {
				updateBody["isVerified"] = true
			}
			updateResp, updateStatus, err := call("POST", "/v2/users/"+u.UserID+"/email", updateBody)
			if err != nil {
				failed++
				log.Error("update email failed",
					zap.String("user_id", u.UserID),
					zap.Error(err),
				)
				continue
			}
			if updateStatus != http.StatusOK && updateStatus != http.StatusCreated {
				failed++
				log.Error("update email returned non-2xx",
					zap.String("user_id", u.UserID),
					zap.Int("status", updateStatus),
					zap.ByteString("body", updateResp),
				)
				continue
			}

			if usernameNeedsUpdate {
				// The v2 API has no username endpoint; fall back to the
				// management v1 surface which exposes it as a PUT.
				unameResp, unameStatus, err := call(
					"PUT",
					"/management/v1/users/"+u.UserID+"/username",
					map[string]any{"userName": canonical},
				)
				if err != nil {
					failed++
					log.Error("update username failed",
						zap.String("user_id", u.UserID),
						zap.Error(err),
					)
					continue
				}
				if unameStatus != http.StatusOK {
					failed++
					log.Error("update username returned non-200",
						zap.String("user_id", u.UserID),
						zap.Int("status", unameStatus),
						zap.ByteString("body", unameResp),
					)
					continue
				}
			}

			updated++
		}

		if len(page.Result) < userPageSize {
			break
		}
		offset += userPageSize
	}

	log.Info("scan complete",
		zap.Int("scanned", scanned),
		zap.Int("drift", drift),
		zap.Int("updated", updated),
		zap.Int("failed", failed),
		zap.Bool("dry_run", *dryRun),
	)
	if failed > 0 {
		fmt.Fprintf(os.Stderr, "%d updates failed — see logs above\n", failed)
		os.Exit(1)
	}
}
