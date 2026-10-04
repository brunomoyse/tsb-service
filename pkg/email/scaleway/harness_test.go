package scaleway

import (
	"bufio"
	"bytes"
	"embed"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	temv1alpha1 "github.com/scaleway/scaleway-sdk-go/api/tem/v1alpha1"
	"github.com/scaleway/scaleway-sdk-go/scw"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	addressDomain "tsb-service/internal/modules/address/domain"
	orderDomain "tsb-service/internal/modules/order/domain"
	userDomain "tsb-service/internal/modules/user/domain"
	"tsb-service/pkg/brand"
)

// stubFS holds a template pair that does not match any real template path, so
// swapping it in for htmlEmailFS / textEmailFS makes one of the two renders fail.
//
//go:embed testdata/stubfs/templates/stub/*
var stubFS embed.FS

// ──────────────────────────── global state ────────────────────────────

// isolateGlobals snapshots the package-level email backend state and restores
// it when the test ends, so tests can freely swap backends.
func isolateGlobals(t *testing.T) {
	t.Helper()
	prevTem, prevBase := temClient, baseReq
	prevHost, prevPort, prevUser, prevPass := smtpHost, smtpPort, smtpUser, smtpPassword
	prevStore := suppressionStore
	prevHTML, prevText := htmlEmailFS, textEmailFS
	t.Cleanup(func() {
		temClient, baseReq = prevTem, prevBase
		smtpHost, smtpPort, smtpUser, smtpPassword = prevHost, prevPort, prevUser, prevPass
		suppressionStore = prevStore
		htmlEmailFS, textEmailFS = prevHTML, prevText
	})
	temClient, suppressionStore = nil, nil
	smtpHost, smtpPort, smtpUser, smtpPassword = "", "", "", ""
	t.Setenv("APP_BASE_URL", "https://shop.test")
}

func testBaseReq() *temv1alpha1.CreateEmailRequest {
	name := "Tokyo Sushi Bar"
	return &temv1alpha1.CreateEmailRequest{
		Region:    scw.Region("fr-par"),
		From:      &temv1alpha1.CreateEmailRequestAddress{Email: "noreply@tsb.test", Name: &name},
		ProjectID: "11111111-2222-4333-8444-555555555555",
	}
}

// ──────────────────────────── fake SMTP server ────────────────────────────

type smtpMessage struct {
	From string
	To   []string
	Data []byte
}

// fakeSMTP is a minimal in-process SMTP server recording what it receives.
type fakeSMTP struct {
	ln net.Listener

	mu       sync.Mutex
	msgs     []smtpMessage
	authLine []string

	advertiseAuth bool
	rejectRcpt    bool
}

func startFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &fakeSMTP{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeSMTP) setRejectRcpt(v bool) {
	s.mu.Lock()
	s.rejectRcpt = v
	s.mu.Unlock()
}

func (s *fakeSMTP) setAdvertiseAuth(v bool) {
	s.mu.Lock()
	s.advertiseAuth = v
	s.mu.Unlock()
}

func (s *fakeSMTP) host() string { h, _, _ := net.SplitHostPort(s.ln.Addr().String()); return h }
func (s *fakeSMTP) port() string { _, p, _ := net.SplitHostPort(s.ln.Addr().String()); return p }

func (s *fakeSMTP) handle(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	write := func(l string) { _, _ = io.WriteString(c, l+"\r\n") }
	write("220 fake ESMTP")
	var cur smtpMessage
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		up := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(up, "EHLO"), strings.HasPrefix(up, "HELO"):
			write("250-fake")
			s.mu.Lock()
			auth := s.advertiseAuth
			s.mu.Unlock()
			if auth {
				write("250-AUTH PLAIN")
			}
			write("250 8BITMIME")
		case strings.HasPrefix(up, "AUTH PLAIN"):
			s.mu.Lock()
			s.authLine = append(s.authLine, strings.TrimSpace(line[len("AUTH PLAIN"):]))
			s.mu.Unlock()
			write("235 2.7.0 ok")
		case strings.HasPrefix(up, "MAIL FROM:"):
			cur = smtpMessage{From: angle(line)}
			write("250 ok")
		case strings.HasPrefix(up, "RCPT TO:"):
			s.mu.Lock()
			rej := s.rejectRcpt
			s.mu.Unlock()
			if rej {
				write("550 5.1.1 no such user")
				continue
			}
			cur.To = append(cur.To, angle(line))
			write("250 ok")
		case up == "DATA":
			write("354 go ahead")
			var data bytes.Buffer
			for {
				l, err := br.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				l = strings.TrimPrefix(l, ".") // undo dot-stuffing
				data.WriteString(l)
			}
			cur.Data = data.Bytes()
			s.mu.Lock()
			s.msgs = append(s.msgs, cur)
			s.mu.Unlock()
			write("250 queued")
		case up == "QUIT":
			write("221 bye")
			return
		default:
			write("250 ok")
		}
	}
}

func angle(s string) string {
	i, j := strings.Index(s, "<"), strings.Index(s, ">")
	if i < 0 || j < i {
		return ""
	}
	return s[i+1 : j]
}

func (s *fakeSMTP) messages() []smtpMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]smtpMessage(nil), s.msgs...)
}

// useSMTP points the package at the fake server (what InitService does when
// SMTP_HOST is set) and installs a baseReq.
func useSMTP(t *testing.T, s *fakeSMTP) {
	t.Helper()
	isolateGlobals(t)
	smtpHost, smtpPort = s.host(), s.port()
	baseReq = testBaseReq()
}

// ──────────────────────────── parsed mail ────────────────────────────

type mailView struct {
	Header  mail.Header
	Subject string
	From    string
	To      string
	Text    string
	HTML    string
}

func parseMail(t *testing.T, raw []byte) mailView {
	t.Helper()
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	require.NoError(t, err)
	subject, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	require.NoError(t, err)

	mt, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	require.NoError(t, err)
	require.Equal(t, "multipart/alternative", mt)

	v := mailView{Header: m.Header, Subject: subject, From: m.Header.Get("From"), To: m.Header.Get("To")}
	mr := multipart.NewReader(m.Body, params["boundary"])
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		body, err := io.ReadAll(p)
		require.NoError(t, err)
		s := strings.TrimSuffix(string(body), "\r\n")
		switch ct, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type")); ct {
		case "text/plain":
			v.Text = s
		case "text/html":
			v.HTML = s
		default:
			t.Fatalf("unexpected part type %q", ct)
		}
	}
	return v
}

// lastMail returns the single message the fake server received.
func lastMail(t *testing.T, s *fakeSMTP) mailView {
	t.Helper()
	msgs := s.messages()
	require.Len(t, msgs, 1, "expected exactly one SMTP message")
	return parseMail(t, msgs[0].Data)
}

// ──────────────────────────── fake Scaleway TEM ────────────────────────────

type temRequest struct {
	Method string
	Path   string
	Query  map[string][]string
	Token  string
	Body   []byte
}

type fakeTEM struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []temRequest
}

// startFakeTEM serves the TEM REST API through handler and wires temClient to it.
func startFakeTEM(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, n int)) *fakeTEM {
	t.Helper()
	isolateGlobals(t)
	f := &fakeTEM{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, temRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Token: r.Header.Get("X-Auth-Token"), Body: body})
		n := len(f.reqs)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		handler(w, r, n)
	}))
	t.Cleanup(f.Close)

	client, err := scw.NewClient(
		scw.WithAuth("SCWXXXXXXXXXXXXXXXXX", uuid.NewString()),
		scw.WithAPIURL(f.URL),
		scw.WithDefaultRegion(scw.RegionFrPar),
		scw.WithDefaultProjectID("11111111-2222-4333-8444-555555555555"),
	)
	require.NoError(t, err)
	temClient = temv1alpha1.NewAPI(client)
	baseReq = testBaseReq()
	return f
}

func (f *fakeTEM) requests() []temRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]temRequest(nil), f.reqs...)
}

func temOK(w http.ResponseWriter, _ *http.Request, _ int) {
	_, _ = io.WriteString(w, `{"emails":[{"id":"e1","status":"new"}]}`)
}

// ──────────────────────────── fixtures ────────────────────────────

var testOrderID = uuid.MustParse("7b0f3c1e-5d2a-4c8b-9e41-0a1b2c3d4e5f")

func sampleUser() userDomain.User {
	return userDomain.User{FirstName: "Jeanne", LastName: "Dupont", Email: "jeanne@example.com"}
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func decPtr(s string) *decimal.Decimal { d := dec(s); return &d }

func strPtr(s string) *string { return &s }

// sampleItems: 2 x 12.50 + 1 x 3.00 = 28.00.
func sampleItems() []orderDomain.OrderProduct {
	return []orderDomain.OrderProduct{
		{Product: orderDomain.Product{CategoryName: "Sushi", Name: "Saumon"}, Quantity: 2, UnitPrice: dec("12.50"), TotalPrice: dec("25.00")},
		{Product: orderDomain.Product{CategoryName: "Boissons", Name: "Thé vert"}, Quantity: 1, UnitPrice: dec("3.00"), TotalPrice: dec("3.00")},
	}
}

// 17:30 UTC on 1 July 2026 is 19:30 in Brussels (CEST), a Wednesday.
func sampleReadyTime() *time.Time {
	t := time.Date(2026, 7, 1, 17, 30, 0, 0, time.UTC)
	return &t
}

// deliveryOrder: 28.00 + 2.50 delivery - 2.80 coupon = 27.70.
func deliveryOrder() orderDomain.Order {
	return orderDomain.Order{
		ID:                 testOrderID,
		OrderType:          orderDomain.OrderTypeDelivery,
		DeliveryFee:        decPtr("2.50"),
		CouponDiscount:     dec("2.80"),
		CouponCode:         strPtr("WELCOME10"),
		TotalPrice:         dec("27.70"),
		EstimatedReadyTime: sampleReadyTime(),
	}
}

// pickupOrder: 28.00 - 2.80 takeaway discount = 25.20. A delivery fee is set on
// purpose: it must not leak into a pickup email.
func pickupOrder() orderDomain.Order {
	return orderDomain.Order{
		ID:                 testOrderID,
		OrderType:          orderDomain.OrderTypePickUp,
		DeliveryFee:        decPtr("2.50"),
		TakeawayDiscount:   dec("2.80"),
		TotalPrice:         dec("25.20"),
		EstimatedReadyTime: sampleReadyTime(),
	}
}

func sampleAddress() *addressDomain.Address {
	return &addressDomain.Address{
		StreetName: "Rue Saint-Gilles", HouseNumber: "12", BoxNumber: strPtr("3B"),
		Postcode: "4000", MunicipalityName: "Liège",
	}
}

// brandDomain is the default brand's email domain (used in Message-ID headers).
func brandDomain() string {
	brand.Load()
	return brand.Current().Domain
}
