// import-legacy-users imports users from the legacy site's CSV export into Zitadel.
//
// Users are created with email pre-verified, so returning customers can use
// "forgot password" on the new site to claim their account in one step.
//
// The script is idempotent: results are appended to a checkpoint CSV, and
// re-runs skip emails already imported successfully.
//
// It also writes a separate marketing-list CSV (newsletter opt-ins only) that
// can be uploaded directly to Scaleway / any ESP.
//
// Usage:
//
//	go run cmd/import-legacy-users/main.go --csv ./legacy_users.csv [flags]
//
// Recommended first run (dry-run on a 3-row test CSV):
//
//	go run cmd/import-legacy-users/main.go --csv ./test_users.csv --dry-run
//
// Then run live on the same test CSV (no --dry-run) to verify Zitadel connection:
//
//	go run cmd/import-legacy-users/main.go --csv ./test_users.csv --limit 3
package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type legacyUser struct {
	Email        string
	FirstName    string
	LastName     string
	Phone        string // E.164 normalized, "" if invalid/missing
	State        string
	Newsletter   bool
	LastActivity time.Time
}

type config struct {
	csvPath            string
	checkpointPath     string
	marketingPath      string
	dryRun             bool
	limit              int
	minYear            int
	includeUnconfirmed bool
	throttleMS         int
	zitadelURL         string
	pat                string
}

var nonPhoneCharRE = regexp.MustCompile(`[^\d]`)

func main() {
	cfg := parseFlags()

	if !cfg.dryRun {
		if cfg.zitadelURL == "" || cfg.pat == "" {
			log.Fatal("ZITADEL_ISSUER and ZITADEL_SERVICE_PAT are required (or pass --dry-run)")
		}
	}

	already, err := loadCheckpoint(cfg.checkpointPath)
	if err != nil {
		log.Fatalf("Failed to load checkpoint: %v", err)
	}
	if len(already) > 0 {
		log.Printf("Resuming: %d emails already imported per %s", len(already), cfg.checkpointPath)
	}

	users, err := loadCSV(cfg)
	if err != nil {
		log.Fatalf("Failed to load CSV: %v", err)
	}
	log.Printf("Loaded %d users from %s after filtering (state, year)", len(users), cfg.csvPath)

	if err := writeMarketingCSV(cfg.marketingPath, users); err != nil {
		log.Fatalf("Failed to write marketing CSV: %v", err)
	}

	cp, err := openCheckpoint(cfg.checkpointPath)
	if err != nil {
		log.Fatalf("Failed to open checkpoint: %v", err)
	}
	defer func() { _ = cp.Close() }()

	var processed, ok, skipped, failed int
	for _, u := range users {
		if cfg.limit > 0 && processed >= cfg.limit {
			break
		}
		processed++

		if _, done := already[strings.ToLower(u.Email)]; done {
			skipped++
			continue
		}

		log.Printf("[%d] %s — %s %s", processed, u.Email, u.FirstName, u.LastName)

		if cfg.dryRun {
			log.Printf("    DRY-RUN phone=%q newsletter=%v lastActivity=%s",
				u.Phone, u.Newsletter, u.LastActivity.Format("2006-01-02"))
			ok++
			_ = writeCheckpoint(cp, u.Email, "DRY-RUN", "")
			continue
		}

		zid, err := createZitadelUser(cfg, u)
		if err != nil {
			log.Printf("    ERROR: %v", err)
			failed++
			_ = writeCheckpoint(cp, u.Email, "", err.Error())
			continue
		}
		log.Printf("    OK → %s", zid)
		ok++
		_ = writeCheckpoint(cp, u.Email, zid, "")

		if cfg.throttleMS > 0 {
			time.Sleep(time.Duration(cfg.throttleMS) * time.Millisecond)
		}
	}

	log.Printf("Done: processed=%d ok=%d skipped=%d failed=%d", processed, ok, skipped, failed)
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.csvPath, "csv", "legacy_users.csv", "Path to the legacy CSV")
	flag.StringVar(&cfg.checkpointPath, "checkpoint", "import-checkpoint.csv", "Path to checkpoint CSV (append-only)")
	flag.StringVar(&cfg.marketingPath, "marketing-out", "marketing-list.csv", "Output CSV: newsletter opt-ins only")
	flag.BoolVar(&cfg.dryRun, "dry-run", false, "Parse + write outputs; do not call Zitadel")
	flag.IntVar(&cfg.limit, "limit", 0, "Process only the first N users (0 = no limit)")
	flag.IntVar(&cfg.minYear, "min-year", 2024, "Filter: lastActivity year >= this (0 = no filter)")
	flag.BoolVar(&cfg.includeUnconfirmed, "include-unconfirmed", false, "Include users in 'confirmRequired' state")
	flag.IntVar(&cfg.throttleMS, "throttle-ms", 100, "Sleep N ms between Zitadel calls")
	flag.Parse()

	_ = godotenv.Load()
	cfg.zitadelURL = strings.TrimRight(os.Getenv("ZITADEL_ISSUER"), "/")
	cfg.pat = os.Getenv("ZITADEL_SERVICE_PAT")
	return cfg
}

// loadCheckpoint returns the set of emails already imported successfully
// (i.e. rows whose zitadel_user_id column is non-empty and not an error).
func loadCheckpoint(path string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	first := true
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if first {
			first = false
			continue
		}
		if len(rec) < 2 || rec[0] == "" || rec[1] == "" {
			continue
		}
		out[strings.ToLower(rec[0])] = struct{}{}
	}
	return out, nil
}

func openCheckpoint(path string) (*os.File, error) {
	_, statErr := os.Stat(path)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	if os.IsNotExist(statErr) {
		if _, err := f.WriteString("email,zitadel_user_id,error\n"); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	return f, nil
}

func writeCheckpoint(f *os.File, email, zid, errMsg string) error {
	w := csv.NewWriter(f)
	_ = w.Write([]string{email, zid, errMsg})
	w.Flush()
	return w.Error()
}

func loadCSV(cfg config) ([]legacyUser, error) {
	f, err := os.Open(cfg.csvPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	r := csv.NewReader(f)
	r.Comma = ';'
	r.LazyQuotes = true
	r.FieldsPerRecord = -1

	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.TrimSpace(h)] = i
	}
	required := []string{"email", "firstName", "lastName", "phone", "state", "newsletter", "lastActivity"}
	for _, k := range required {
		if _, ok := col[k]; !ok {
			return nil, fmt.Errorf("missing column %q in CSV", k)
		}
	}

	var out []legacyUser
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		get := func(name string) string {
			i := col[name]
			if i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}

		u := legacyUser{
			Email:      strings.ToLower(get("email")),
			FirstName:  get("firstName"),
			LastName:   get("lastName"),
			Phone:      normalizePhoneBE(get("phone")),
			State:      get("state"),
			Newsletter: get("newsletter") == "1",
		}
		if la := get("lastActivity"); la != "" {
			if t, err := time.Parse("2006-01-02 15:04:05", la); err == nil {
				u.LastActivity = t
			}
		}
		if !filterUser(cfg, u) {
			continue
		}
		out = append(out, u)
	}
	return out, nil
}

func filterUser(cfg config, u legacyUser) bool {
	if u.Email == "" || !strings.Contains(u.Email, "@") {
		return false
	}
	if u.FirstName == "" || u.LastName == "" {
		return false
	}
	if u.State != "normal" && (!cfg.includeUnconfirmed || u.State != "confirmRequired") {
		return false
	}
	if cfg.minYear > 0 && u.LastActivity.Year() < cfg.minYear {
		return false
	}
	return true
}

// normalizePhoneBE returns a Belgian E.164 phone or "" if the input is
// unrecognizable. Handles raw national numbers (9 digits, no prefix),
// leading-zero national format, 0032 prefix, and already-E.164 input.
func normalizePhoneBE(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	hasPlus := strings.HasPrefix(s, "+")
	digits := nonPhoneCharRE.ReplaceAllString(s, "")

	switch {
	case hasPlus && strings.HasPrefix(digits, "32") && len(digits) >= 10 && len(digits) <= 12:
		return "+" + digits
	case strings.HasPrefix(digits, "0032") && len(digits) >= 12 && len(digits) <= 14:
		return "+" + digits[2:]
	case strings.HasPrefix(digits, "32") && len(digits) >= 10 && len(digits) <= 12:
		return "+" + digits
	case strings.HasPrefix(digits, "0") && len(digits) >= 9 && len(digits) <= 11:
		return "+32" + digits[1:]
	case len(digits) >= 8 && len(digits) <= 10:
		return "+32" + digits
	default:
		return ""
	}
}

func writeMarketingCSV(path string, users []legacyUser) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{"email", "first_name", "last_name", "phone", "last_activity"}); err != nil {
		return err
	}
	var n int
	for _, u := range users {
		if !u.Newsletter {
			continue
		}
		if err := w.Write([]string{u.Email, u.FirstName, u.LastName, u.Phone, u.LastActivity.Format("2006-01-02")}); err != nil {
			return err
		}
		n++
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	log.Printf("Wrote marketing list: %d newsletter opt-ins → %s", n, path)
	return nil
}

func createZitadelUser(cfg config, u legacyUser) (string, error) {
	body := map[string]any{
		"userName": u.Email,
		"profile": map[string]any{
			"givenName":  u.FirstName,
			"familyName": u.LastName,
		},
		"email": map[string]any{
			"email":      u.Email,
			"isVerified": true,
		},
	}
	if u.Phone != "" {
		body["phone"] = map[string]any{
			"phone":      u.Phone,
			"isVerified": false,
		}
	}

	jb, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequest("POST", cfg.zitadelURL+"/v2/users/human", bytes.NewReader(jb))
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.pat)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var result struct {
		UserID string `json:"userId"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	if result.UserID == "" {
		return "", fmt.Errorf("empty userId in response: %s", strings.TrimSpace(string(respBody)))
	}
	return result.UserID, nil
}
