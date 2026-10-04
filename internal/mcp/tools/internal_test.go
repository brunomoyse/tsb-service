package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"tsb-service/internal/mcp/actions"
	"tsb-service/internal/mcp/upstream"
)

var brussels, _ = time.LoadLocation("Europe/Brussels")

func testDeps() (*Deps, *bytes.Buffer) {
	var buf bytes.Buffer
	return &Deps{Loc: brussels, Log: slog.New(slog.NewTextHandler(&buf, nil)), Now: func() time.Time { return time.Date(2026, 10, 3, 13, 0, 0, 0, brussels) },
		ImageClient: http.DefaultClient, AllowHTTPImages: true}, &buf
}

func TestSafeErrNeverLeaksInternals(t *testing.T) {
	d, logs := testDeps()
	tests := []struct {
		name string
		err  error
		want string
		logs bool
	}{
		{"user error", actions.Userf("No product with id %q.", "x"), `No product with id "x".`, false},
		{"wrapped user error", fmt.Errorf("ctx: %w", actions.Userf("bad price")), "bad price", false},
		{"conflict", &actions.ConflictError{Fields: []string{"price_cents"}}, "The item was changed elsewhere since this was proposed (price_cents). Please propose the change again.", false},
		{"upstream", &upstream.Error{Kind: upstream.KindUnavailable, Message: "The restaurant system is not reachable right now."}, "The restaurant system is not reachable right now.", false},
		{"canceled", context.Canceled, "The request took too long. Please try again.", false},
		{"deadline", fmt.Errorf("call: %w", context.DeadlineExceeded), "The request took too long. Please try again.", false},
		{"anything else", errors.New("pq: password authentication failed for user tsb at 10.0.0.5"), genericError, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs.Reset()
			got := d.safeErr("some_tool", tt.err)
			if got.Error() != tt.want {
				t.Errorf("message = %q, want %q", got, tt.want)
			}
			if strings.Contains(got.Error(), "password") || strings.Contains(got.Error(), "10.0.0.5") {
				t.Errorf("leaked detail: %q", got)
			}
			if tt.logs != strings.Contains(logs.String(), "tool failed") || (tt.logs && !strings.Contains(logs.String(), "password authentication")) {
				t.Errorf("logging: %q", logs.String())
			}
		})
	}
}

func TestParseTime(t *testing.T) {
	d, _ := testDeps()
	tests := []struct {
		in   string
		want string // RFC3339 in Brussels
	}{
		{"2026-10-03T18:00:00+02:00", "2026-10-03T18:00:00+02:00"},
		{"2026-10-03T16:00:00Z", "2026-10-03T18:00:00+02:00"},
		{"2026-10-03T18:00", "2026-10-03T18:00:00+02:00"},
		{" 2026-10-03T18:00:30 ", "2026-10-03T18:00:30+02:00"},
		{"2026-10-03 18:00", "2026-10-03T18:00:00+02:00"},
		{"2026-10-03 18:00:30", "2026-10-03T18:00:30+02:00"},
		{"2026-12-24T18:00", "2026-12-24T18:00:00+01:00"}, // winter time
	}
	for _, tt := range tests {
		got, err := d.parseTime("from", tt.in)
		if err != nil || got.In(brussels).Format(time.RFC3339) != tt.want {
			t.Errorf("parseTime(%q) = %v, %v; want %s", tt.in, got, err, tt.want)
		}
	}
	for _, in := range []string{"", "tomorrow", "2026-10-03", "18:00", "2026-13-01T10:00"} {
		_, err := d.parseTime("reopen_at", in)
		ue, ok := errors.AsType[*actions.UserError](err)
		if !ok || !strings.HasPrefix(ue.Msg, "reopen_at: invalid time") {
			t.Errorf("parseTime(%q): %v", in, err)
		}
	}
}

func TestParseDateAndBounds(t *testing.T) {
	d, _ := testDeps()
	if got, err := parseDate("date", " 2026-10-03 "); err != nil || got != "2026-10-03" {
		t.Errorf("parseDate: %q %v", got, err)
	}
	for _, in := range []string{"", "03/10/2026", "2026-10-3x", "2026-02-30"} {
		_, err := parseDate("date", in)
		if ue, ok := errors.AsType[*actions.UserError](err); !ok || !strings.Contains(ue.Msg, "date: invalid date") {
			t.Errorf("parseDate(%q): %v", in, err)
		}
	}
	start, next := d.dayBounds("2026-10-25") // 25 hours: clocks go back that night
	if !start.Equal(time.Date(2026, 10, 25, 0, 0, 0, 0, brussels)) || next.Sub(start) != 25*time.Hour {
		t.Errorf("dayBounds across the DST change: %v %v", start, next)
	}
	if d.today() != "2026-10-03" {
		t.Errorf("today = %s", d.today())
	}
	// A late evening UTC moment is already the next day in Brussels.
	d.Now = func() time.Time { return time.Date(2026, 10, 3, 22, 30, 0, 0, time.UTC) }
	if d.today() != "2026-10-04" {
		t.Errorf("today at 22:30 UTC = %s", d.today())
	}

	// boundary: a date is a whole day, a timestamp is itself.
	b, err := d.boundary("from", "2026-10-03", false)
	if err != nil || !b.Equal(time.Date(2026, 10, 3, 0, 0, 0, 0, brussels)) {
		t.Errorf("start boundary %v %v", b, err)
	}
	b, err = d.boundary("to", "2026-10-03", true)
	if err != nil || !b.Equal(time.Date(2026, 10, 4, 0, 0, 0, 0, brussels).Add(-time.Millisecond)) {
		t.Errorf("end boundary %v %v", b, err)
	}
	b, err = d.boundary("to", "2026-10-03T15:30", true)
	if err != nil || !b.Equal(time.Date(2026, 10, 3, 15, 30, 0, 0, brussels)) {
		t.Errorf("timestamp boundary %v %v", b, err)
	}
	if _, err := d.boundary("to", "soon", true); err == nil {
		t.Error("garbage boundary")
	}
}

func TestSmallHelpers(t *testing.T) {
	d, _ := testDeps()
	tm := time.Date(2026, 10, 3, 16, 0, 0, 0, time.UTC)
	if d.fmtTimePtr(nil) != "" || d.fmtTimePtr(&tm) != "2026-10-03T18:00:00+02:00" {
		t.Error("fmtTimePtr")
	}
	if toAny(nil) != nil || toAny(json.RawMessage("")) != nil || toAny(json.RawMessage("{not json")) != nil {
		t.Error("toAny of nothing or garbage must be nil")
	}
	if m, ok := toAny(json.RawMessage(`{"a":1}`)).(map[string]any); !ok || m["a"] != float64(1) {
		t.Error("toAny of an object")
	}
	if limitOr(0, 20, 100) != 20 || limitOr(-3, 20, 100) != 20 || limitOr(5, 20, 100) != 5 || limitOr(500, 20, 100) != 100 {
		t.Error("limitOr")
	}
	if valueOr(nil, 3) != 3 || valueOr(new(5), 3) != 5 {
		t.Error("valueOr")
	}
	if got := uniq([]string{"a", "b", "a", "c", "b"}); strings.Join(got, "") != "abc" {
		t.Errorf("uniq = %v", got)
	}
	if itoa(42) != "42" {
		t.Error("itoa")
	}
	if describe("text", "tool({})") != "text\n\nExample: tool({})" {
		t.Error("describe")
	}
}

func TestDayHours(t *testing.T) {
	if got := dayHoursOut(nil); !got.Closed {
		t.Error("nil is closed")
	}
	if got := dayHoursOut(&upstream.DaySchedule{Open: "11:30", Close: "14:30", DinnerOpen: "18:00", DinnerClose: "22:00"}); got != (DayHours{Open: "11:30", Close: "14:30", DinnerOpen: "18:00", DinnerClose: "22:00"}) {
		t.Errorf("dayHoursOut = %+v", got)
	}
	if (DayHours{Closed: true, Open: "11:00"}).schedule() != nil {
		t.Error("closed wins over open")
	}
	if got := (DayHours{Open: " 11:00 ", Close: "14:00 ", DinnerOpen: " 18:00", DinnerClose: "22:00"}).schedule(); *got != (upstream.DaySchedule{Open: "11:00", Close: "14:00", DinnerOpen: "18:00", DinnerClose: "22:00"}) {
		t.Errorf("schedule trims: %+v", got)
	}
	w := weekOut(upstream.Week{"monday": {Open: "11:00", Close: "14:00"}})
	if len(w) != 7 || w["monday"].Open != "11:00" || !w["tuesday"].Closed || !w["sunday"].Closed {
		t.Errorf("weekOut: %+v", w)
	}
	o := overrideOut(upstream.ScheduleOverride{Date: time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC), Closed: true, Schedule: &upstream.DaySchedule{Open: "10:00", Close: "11:00"}, Note: new("Noël")})
	if o.Date != "2026-12-25" || o.Weekday != "friday" || !o.Hours.Closed || o.Hours.Open != "" || o.Note != "Noël" {
		t.Errorf("closed override: %+v", o)
	}
	o = overrideOut(upstream.ScheduleOverride{Date: time.Date(2026, 12, 24, 0, 0, 0, 0, time.UTC), Schedule: &upstream.DaySchedule{Open: "10:00", Close: "16:00"}})
	if o.Hours.Closed || o.Hours.Open != "10:00" || o.Note != "" || o.Weekday != "thursday" {
		t.Errorf("special hours override: %+v", o)
	}
}

func TestCustomerName(t *testing.T) {
	if customerName(&upstream.Order{}) != "guest" {
		t.Error("no customer is a guest")
	}
	var o upstream.Order
	if err := json.Unmarshal([]byte(`{"customer":{"firstName":"  ","lastName":"  "}}`), &o); err != nil {
		t.Fatal(err)
	}
	if customerName(&o) != "guest" {
		t.Errorf("blank names: %q", customerName(&o))
	}
	if err := json.Unmarshal([]byte(`{"customer":{"firstName":"Marie","lastName":"dupont"}}`), &o); err != nil {
		t.Fatal(err)
	}
	if customerName(&o) != "Marie D." {
		t.Errorf("name = %q", customerName(&o))
	}
}

func TestCheckDialAddress(t *testing.T) {
	blocked := []string{"127.0.0.1:443", "[::1]:443", "10.0.0.5:80", "192.168.1.10:80", "172.16.0.1:80", "169.254.169.254:80", "0.0.0.0:80", "[::]:80", "224.0.0.1:80", "[fe80::1]:80", "[ff02::1]:80", "[fd00::1]:80", "localhost:80", "no-port",
		// IPv4-mapped IPv6 literals are judged by their embedded IPv4 address.
		"[::ffff:127.0.0.1]:80", "[::ffff:10.0.0.1]:80", "[::ffff:192.168.1.1]:80", "[::ffff:169.254.169.254]:80", "[::ffff:0.0.0.0]:80"}
	for _, a := range blocked {
		if err := checkDialAddress(a); err == nil {
			t.Errorf("%s must be refused", a)
		}
	}
	for _, a := range []string{"93.184.216.34:443", "[2606:2800:220:1:248:1893:25c8:1946]:443", "8.8.8.8:80", "[::ffff:8.8.8.8]:80"} {
		if err := checkDialAddress(a); err != nil {
			t.Errorf("%s must be allowed: %v", a, err)
		}
	}
	err := checkDialAddress("169.254.169.254:80")
	if err == nil || !strings.Contains(err.Error(), "address 169.254.169.254 is not allowed") {
		t.Errorf("message: %v", err)
	}
}

func TestSafeHTTPClientRefusesLocalServers(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits++ }))
	defer srv.Close()
	c := safeHTTPClient()
	if c.Timeout != 30*time.Second {
		t.Errorf("timeout = %v", c.Timeout)
	}
	// No proxy, whatever HTTPS_PROXY says: the guard only sees the address that is dialled, so a
	// proxy from the environment would be connected to instead of the (checked) image host.
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:3128")
	t.Setenv("HTTP_PROXY", "http://proxy.invalid:3128")
	tr, ok := safeHTTPClient().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T", c.Transport)
	}
	if tr.Proxy != nil {
		t.Error("the image client must not use a proxy from the environment")
	}
	if _, err := c.Get(srv.URL); err == nil || !strings.Contains(err.Error(), "is not allowed") {
		t.Fatalf("a loopback server must not be reachable: %v", err)
	}
	if hits != 0 {
		t.Error("the request reached the loopback server")
	}
	// A literal private address is refused before any connection.
	if _, err := c.Get("http://10.255.255.1/photo.png"); err == nil || !strings.Contains(err.Error(), "is not allowed") {
		t.Errorf("private address: %v", err)
	}
}

func imageServer(handler http.HandlerFunc) (*httptest.Server, *Deps) {
	srv := httptest.NewServer(handler)
	d, _ := testDeps()
	return srv, d
}

func TestDownloadImage(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 64))
	jpg := append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF"), make([]byte, 64)...)
	webp := append([]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), make([]byte, 64)...)

	// Some WebP files are not recognised by content sniffing: RIFF....WEBP is enough.
	webpOther := append([]byte("RIFF\x24\x00\x00\x00WEBPXXXX"), make([]byte, 64)...)
	routes := map[string]func(w http.ResponseWriter){
		"/other.webp":   func(w http.ResponseWriter) { _, _ = w.Write(webpOther) },
		"/box.png":      func(w http.ResponseWriter) { _, _ = w.Write(png) },
		"/shot.jpeg":    func(w http.ResponseWriter) { _, _ = w.Write(jpg) },
		"/drink.webp":   func(w http.ResponseWriter) { _, _ = w.Write(webp) },
		"/":             func(w http.ResponseWriter) { _, _ = w.Write(png) },
		"/noext":        func(w http.ResponseWriter) { _, _ = w.Write(png) },
		"/notfound.png": func(w http.ResponseWriter) { http.Error(w, "nope", http.StatusNotFound) },
		"/text.png":     func(w http.ResponseWriter) { _, _ = w.Write([]byte("<html>not an image</html>")) },
		"/huge.png":     func(w http.ResponseWriter) { _, _ = w.Write(append(png, make([]byte, maxImageBytes)...)) },
		"/exact.png":    func(w http.ResponseWriter) { _, _ = w.Write(append(png, make([]byte, maxImageBytes-len(png))...)) },
		"/cut.png": func(w http.ResponseWriter) {
			w.Header().Set("Content-Length", "1000")
			_, _ = w.Write(png)
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler) // drop the connection mid-body
		},
	}
	srv, d := imageServer(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := routes[r.URL.Path]; ok {
			h(w)
			return
		}
		http.NotFound(w, r)
	})
	defer srv.Close()

	ok := []struct {
		path, wantType, wantName string
		wantLen                  int
	}{
		{"/box.png", "image/png", "box.png", len(png)},
		{"/shot.jpeg", "image/jpeg", "shot.jpg", len(jpg)},
		{"/drink.webp", "image/webp", "drink.webp", len(webp)},
		{"/other.webp", "image/webp", "other.webp", len(webpOther)},
		{"/", "image/png", "photo.png", len(png)},
		{"/noext", "image/png", "noext.png", len(png)},
		{"/exact.png", "image/png", "exact.png", maxImageBytes},
	}
	for _, tt := range ok {
		data, ct, name, err := d.downloadImage(t.Context(), srv.URL+tt.path+"?v=1")
		if err != nil || ct != tt.wantType || name != tt.wantName || len(data) != tt.wantLen {
			t.Errorf("%s: type %q name %q len %d err %v", tt.path, ct, name, len(data), err)
		}
	}

	bad := []struct {
		name, url, want string
	}{
		{"empty", "", "must be an https link"},
		{"not a url", "::::", "must be an https link"},
		{"no host", "https:///x.png", "must be an https link"},
		{"ftp", "ftp://example.com/x.png", "must be an https link"},
		{"file", "file:///etc/passwd", "must be an https link"},
		{"missing", srv.URL + "/notfound.png", "(HTTP 404)"},
		{"unknown path", srv.URL + "/other.png", "(HTTP 404)"},
		{"not an image", srv.URL + "/text.png", "must be a JPEG, PNG or WebP"},
		{"too big", srv.URL + "/huge.png", "larger than 5 MB"},
		{"cut body", srv.URL + "/cut.png", "could not be downloaded"},
		{"unreachable", "http://127.0.0.1:1/x.png", "could not be downloaded"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := d.downloadImage(t.Context(), tt.url)
			ue, isUser := errors.AsType[*actions.UserError](err)
			if !isUser || !strings.Contains(ue.Msg, tt.want) {
				t.Errorf("error = %v, want user error containing %q", err, tt.want)
			}
		})
	}

	// Plain http is only accepted when explicitly allowed (tests).
	d.AllowHTTPImages = false
	_, _, _, err := d.downloadImage(t.Context(), srv.URL+"/box.png")
	if ue, ok := errors.AsType[*actions.UserError](err); !ok || !strings.Contains(ue.Msg, "https") {
		t.Errorf("http image without AllowHTTPImages: %v", err)
	}
	d.AllowHTTPImages = true
	// A cancelled request is a plain download failure.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, _, err := d.downloadImage(ctx, srv.URL+"/box.png"); err == nil {
		t.Error("cancelled context")
	}
}

func TestRegisterFillsDefaults(t *testing.T) {
	d := &Deps{Loc: brussels, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	Register(srv, d)
	if d.Now == nil || d.ImageClient == nil {
		t.Fatal("Register must default the clock and the image client")
	}
	if time.Since(d.Now()) > time.Minute {
		t.Error("default clock is time.Now")
	}
	// The default image client is the guarded one: it refuses local addresses.
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer local.Close()
	if _, err := d.ImageClient.Get(local.URL); err == nil || !strings.Contains(err.Error(), "is not allowed") {
		t.Errorf("default image client reached a loopback server: %v", err)
	}
}
