package scaleway

import (
	"strings"
	"testing"
	"time"

	"tsb-service/pkg/email/smtptest"

	"github.com/stretchr/testify/require"

	orderDomain "tsb-service/internal/modules/order/domain"
	userDomain "tsb-service/internal/modules/user/domain"
)

var allLangs = []string{"fr", "en", "nl", "zh"}

// greeting is the salutation every customer-facing template opens with.
var greeting = map[string]string{"fr": "Bonjour Jeanne Dupont", "en": "Hello Jeanne Dupont", "nl": "Hallo Jeanne Dupont", "zh": "您好"}

type sendCase struct {
	name     string
	send     func(lang string) error
	subjects map[string]string // expected subject per language
	threaded bool              // carries the order-thread Message-ID/References headers
	want     []string          // language-independent values that must reach both bodies
}

func sendCases() []sendCase {
	user := sampleUser()
	reason := orderDomain.OrderCancellationReasonOutOfStock
	link := "https://shop.test/me?followOrder=" + testOrderID.String()
	return []sendCase{
		{
			name: "verification",
			send: func(l string) error { return SendVerificationEmail(user, l, "https://shop.test/verify?token=abc") },
			subjects: map[string]string{"fr": "Veuillez vérifier votre adresse e-mail", "en": "Please verify your email",
				"nl": "Bevestig uw e-mailadres", "zh": "验证您的邮箱"},
			want: []string{"Jeanne Dupont", "https://shop.test/verify?token=abc"},
		},
		{
			name: "welcome",
			send: func(l string) error { return SendWelcomeEmail(user, l, "https://shop.test/menu") },
			subjects: map[string]string{"fr": "Bienvenue chez Tokyo Sushi Bar", "en": "Welcome to Tokyo Sushi Bar",
				"nl": "Welkom bij Tokyo Sushi Bar", "zh": "欢迎光临 Tokyo Sushi Bar"},
			want: []string{"Jeanne Dupont", "https://shop.test/menu", "Tokyo Sushi Bar"},
		},
		{
			name: "login otp",
			send: func(l string) error { return SendLoginOtpEmail(user, l, "483920") },
			subjects: map[string]string{"fr": "Votre code de connexion : 483920", "en": "Your sign-in code: 483920",
				"nl": "Uw inlogcode: 483920", "zh": "您的登录验证码：483920"},
			want: []string{"Jeanne Dupont", "483920"},
		},
		{
			name: "order pending",
			send: func(l string) error { return SendOrderPendingEmail(user, l, deliveryOrder(), sampleItems()) },
			subjects: map[string]string{"fr": "Commande en attente de validation", "en": "Order pending validation",
				"nl": "Bestelling wacht op bevestiging", "zh": "订单待验证"},
			threaded: true,
			want:     []string{"Jeanne Dupont", "Sushi - Saumon", "Boissons - Thé vert", "25,00", "3,00", "28,00", "2,50", "2,80", "WELCOME10", "27,70"},
		},
		{
			name: "order confirmed",
			send: func(l string) error {
				return SendOrderConfirmedEmail(user, l, deliveryOrder(), sampleItems(), sampleAddress())
			},
			subjects: map[string]string{"fr": "Commande confirmée", "en": "Order confirmed",
				"nl": "Bestelling bevestigd", "zh": "订单已确认"},
			threaded: true,
			want: []string{"Jeanne Dupont", "Sushi\u00a0–\u00a0Saumon", "Boissons\u00a0–\u00a0Thé vert", "25,00", "28,00", "2,50", "2,80", "WELCOME10", "27,70",
				"Rue Saint-Gilles 12", "3B", "Liège", "4000", link},
		},
		{
			name: "order canceled",
			send: func(l string) error { return SendOrderCanceledEmail(user, l, testOrderID.String(), &reason) },
			subjects: map[string]string{"fr": "Commande annulée", "en": "Order canceled",
				"nl": "Bestelling geannuleerd", "zh": "订单已取消"},
			threaded: true,
			want:     []string{"Jeanne Dupont"},
		},
		{
			name: "order ready (delivery)",
			send: func(l string) error { return SendOrderReadyEmail(user, l, deliveryOrder()) },
			subjects: map[string]string{"fr": "Votre commande est en route !", "en": "Your order is on its way!",
				"nl": "Uw bestelling is onderweg!", "zh": "您的订单正在配送中！"},
			threaded: true,
			want:     []string{"Jeanne Dupont", link},
		},
		{
			name: "order ready (pickup)",
			send: func(l string) error { return SendOrderReadyEmail(user, l, pickupOrder()) },
			subjects: map[string]string{"fr": "Votre commande est prête !", "en": "Your order is ready!",
				"nl": "Uw bestelling is klaar!", "zh": "您的订单已准备好！"},
			threaded: true,
			want:     []string{"Jeanne Dupont", link},
		},
		{
			name: "order completed",
			send: func(l string) error { return SendOrderCompletedEmail(user, l) },
			subjects: map[string]string{"fr": "Merci pour votre commande !", "en": "Thank you for your order!",
				"nl": "Bedankt voor uw bestelling!", "zh": "感谢您的订单！"},
			want: []string{"Jeanne Dupont", "https://shop.test"},
		},
		{
			name: "refund issued",
			send: func(l string) error { return SendRefundIssuedEmail(user, l, testOrderID.String(), "12,50") },
			subjects: map[string]string{"fr": "Votre remboursement a été effectué", "en": "Your refund has been issued",
				"nl": "Uw terugbetaling is uitgevoerd", "zh": "您的退款已处理"},
			threaded: true,
			want:     []string{"Jeanne Dupont", "12,50"},
		},
		{
			name: "account linked",
			send: func(l string) error { return SendAccountLinkedEmail(user, l) },
			subjects: map[string]string{"fr": "Compte Google associé", "en": "Google account linked",
				"nl": "Google-account gekoppeld", "zh": "Google 帐户已关联"},
			want: []string{"Jeanne Dupont"},
		},
		{
			name: "ready time updated",
			send: func(l string) error { return SendReadyTimeUpdatedEmail(user, l, deliveryOrder()) },
			subjects: map[string]string{"fr": "Horaire estimé modifié", "en": "Updated estimated time",
				"nl": "Geschatte tijd bijgewerkt", "zh": "预计时间已更新"},
			threaded: true,
			want:     []string{"Jeanne Dupont", link},
		},
		{
			name: "reengagement",
			send: func(l string) error { return SendReengagementEmail(user, l) },
			subjects: map[string]string{"fr": "Vous nous manquez chez Tokyo Sushi Bar !", "en": "We miss you at Tokyo Sushi Bar!",
				"nl": "Wij missen u bij Tokyo Sushi Bar!", "zh": "Tokyo Sushi Bar 想念您！"},
			want: []string{"Jeanne Dupont", "https://shop.test"},
		},
	}
}

// TestEveryCustomerEmailInEveryLanguage drives each Send* function end to end
// (template render, MIME build, SMTP delivery) in all four languages and checks
// the message the customer would receive.
func TestEveryCustomerEmailInEveryLanguage(t *testing.T) {
	for _, tc := range sendCases() {
		for _, lang := range allLangs {
			t.Run(tc.name+"/"+lang, func(t *testing.T) {
				srv := smtptest.Start(t)
				useSMTP(t, srv)

				require.NoError(t, tc.send(lang))
				m := srv.Only(t)

				require.Equal(t, tc.subjects[lang], m.Subject)
				require.Equal(t, `"Tokyo Sushi Bar" <noreply@tsb.test>`, m.From)
				require.Equal(t, "jeanne@example.com", m.To)
				require.Equal(t, []string{"jeanne@example.com"}, srv.Messages()[0].To, "envelope recipient")

				require.True(t, strings.HasPrefix(m.Text, greeting[lang]), "text opens with the %s greeting, got %.40q", lang, m.Text)
				require.Contains(t, m.HTML, greeting[lang])
				require.Contains(t, m.HTML, "<html", "HTML part must be a full document")
				require.Contains(t, m.HTML, "https://shop.test/images/tsb-black-font-100.png", "logo URL is APP_BASE_URL + brand logo path")

				for _, w := range tc.want {
					require.Contains(t, m.Text, w, "text body")
					require.Contains(t, m.HTML, w, "html body")
				}
				// Unrendered template actions would show as stray braces.
				require.NotContains(t, m.Text, "{{")
				require.NotContains(t, m.HTML, "{{")

				if tc.threaded {
					domain := brandDomain()
					root := "<order-thread-" + testOrderID.String() + "@" + domain + ">"
					require.Equal(t, root, m.Header.Get("In-Reply-To"))
					require.Equal(t, root, m.Header.Get("References"))
					require.Regexp(t, `^<email-`+testOrderID.String()+`-\d+@`+domain+`>$`, m.Header.Get("Message-ID"))
				} else {
					require.Empty(t, m.Header.Get("Message-ID"))
					require.Empty(t, m.Header.Get("References"))
				}
			})
		}
	}
}

// languageVariants maps each raw language value a caller may hand to a Send*
// function to the language the customer must actually receive. French is the
// fallback for everything unsupported (owner decision).
var languageVariants = []struct{ in, want string }{
	{"fr", "fr"}, {"en", "en"}, {"nl", "nl"}, {"zh", "zh"},
	// case-insensitive, whitespace tolerant
	{"EN", "en"}, {"Fr", "fr"}, {" nl ", "nl"}, {"ZH", "zh"},
	// region / script tags reduce to the base language
	{"fr-BE", "fr"}, {"fr_FR", "fr"}, {"en-GB", "en"}, {"EN-us", "en"},
	{"nl-BE", "nl"}, {"zh-CN", "zh"}, {"zh-Hans", "zh"}, {"zh_TW", "zh"},
	// unsupported, empty or garbage -> French
	{"de", "fr"}, {"de-DE", "fr"}, {"", "fr"}, {"   ", "fr"}, {"xx", "fr"}, {"english", "fr"}, {"-", "fr"}, {"%%", "fr"},
}

// TestEveryEmailNormalisesItsLanguage drives every Send* function with each raw
// language value and requires the delivered email (template, subject and every
// per-language string such as the ETA wording) to equal the one produced for the
// expected base language. Unsupported languages must never fail the send.
func TestEveryEmailNormalisesItsLanguage(t *testing.T) {
	deliver := func(t *testing.T, tc sendCase, lang string) smtptest.View {
		srv := smtptest.Start(t)
		useSMTP(t, srv)
		require.NoError(t, tc.send(lang), "language %q must never fail the send", lang)
		require.Len(t, srv.Messages(), 1)
		// The canonical sends run on the parent test: do not keep 50+ listeners open until it ends.
		defer srv.Close()
		return srv.Only(t)
	}
	for _, tc := range sendCases() {
		canonical := map[string]smtptest.View{}
		for _, lang := range allLangs {
			canonical[lang] = deliver(t, tc, lang)
		}
		for _, v := range languageVariants {
			t.Run(tc.name+"/"+v.in, func(t *testing.T) {
				got := deliver(t, tc, v.in)
				want := canonical[v.want]
				require.Equal(t, tc.subjects[v.want], got.Subject)
				require.True(t, strings.HasPrefix(got.Text, greeting[v.want]), "text opens with the %s greeting, got %.40q", v.want, got.Text)
				require.Equal(t, want.Text, got.Text)
				require.Equal(t, want.HTML, got.HTML)
			})
		}
	}
}

// TestEveryEmailFailsWhenATemplateHalfIsMissing swaps one of the embedded
// template trees for a stub so a single half (HTML or text) fails to render.
func TestEveryEmailFailsWhenATemplateHalfIsMissing(t *testing.T) {
	extra := []sendCase{
		{name: "feedback", send: func(string) error { return SendFeedbackEmail("A", "a@x.test", "svc", "typ", "msg", "en") }},
		{name: "assistant disconnected", send: func(string) error {
			return SendAssistantDisconnectedEmail("owner@x.test", "https://dash.test", time.Now())
		}},
	}
	for _, tc := range append(sendCases(), extra...) {
		for _, half := range []string{"html", "text"} {
			t.Run(tc.name+"/"+half, func(t *testing.T) {
				srv := smtptest.Start(t)
				useSMTP(t, srv)
				t.Setenv("FEEDBACK_RECIPIENT_EMAIL", "admin@x.test")
				if half == "html" {
					htmlEmailFS = stubFS
				} else {
					textEmailFS = stubFS
				}
				err := tc.send("en")
				require.ErrorContains(t, err, "failed to render email template")
				require.Empty(t, srv.Messages())
			})
		}
	}
}

// TestEveryEmailSurfacesDeliveryFailure: a rejected send is wrapped, not swallowed.
func TestEveryEmailSurfacesDeliveryFailure(t *testing.T) {
	extra := []sendCase{
		{name: "feedback", send: func(string) error { return SendFeedbackEmail("A", "a@x.test", "svc", "typ", "msg", "en") }},
		{name: "assistant disconnected", send: func(string) error {
			return SendAssistantDisconnectedEmail("owner@x.test", "https://dash.test", time.Now())
		}},
	}
	for _, tc := range append(sendCases(), extra...) {
		t.Run(tc.name, func(t *testing.T) {
			srv := smtptest.Start(t)
			srv.SetRejectAll(true)
			useSMTP(t, srv)
			t.Setenv("FEEDBACK_RECIPIENT_EMAIL", "admin@x.test")
			err := tc.send("en")
			require.ErrorContains(t, err, "failed to send email")
			require.ErrorContains(t, err, "550")
		})
	}
}

func TestOrderEmailAmountsAndLines(t *testing.T) {
	text := func(t *testing.T, lang string, o orderDomain.Order, items []orderDomain.OrderProduct, addr bool, confirmed bool) (string, string) {
		t.Helper()
		srv := smtptest.Start(t)
		useSMTP(t, srv)
		user := sampleUser()
		var err error
		switch {
		case confirmed && addr:
			err = SendOrderConfirmedEmail(user, lang, o, items, sampleAddress())
		case confirmed:
			err = SendOrderConfirmedEmail(user, lang, o, items, nil)
		default:
			err = SendOrderPendingEmail(user, lang, o, items)
		}
		require.NoError(t, err)
		m := srv.Only(t)
		return m.Text, m.HTML
	}

	for _, confirmed := range []bool{false, true} {
		kind := map[bool]string{false: "pending", true: "confirmed"}[confirmed]
		for _, lang := range allLangs {
			t.Run(kind+"/"+lang+"/delivery shows fee and coupon but no takeaway discount", func(t *testing.T) {
				txt, html := text(t, lang, deliveryOrder(), sampleItems(), true, confirmed)
				for _, body := range []string{txt, html} {
					require.Contains(t, body, "2,50", "delivery fee")
					require.Contains(t, body, "2,80", "coupon discount")
					require.Contains(t, body, "WELCOME10")
					require.Contains(t, body, "27,70")
				}
			})
			t.Run(kind+"/"+lang+"/pickup shows takeaway discount and hides the delivery fee", func(t *testing.T) {
				txt, html := text(t, lang, pickupOrder(), sampleItems(), false, confirmed)
				for _, body := range []string{txt, html} {
					require.Contains(t, body, "2,80", "takeaway discount")
					require.Contains(t, body, "25,20", "total after discount")
					require.NotContains(t, body, "2,50", "delivery fee must not appear on a pickup order")
					require.NotContains(t, body, "WELCOME10")
				}
			})
		}
	}

	t.Run("coupon without a code omits the parenthesised code", func(t *testing.T) {
		o := deliveryOrder()
		o.CouponCode = nil
		txt, _ := text(t, "en", o, sampleItems(), true, true)
		require.Contains(t, txt, "Coupon")
		require.NotContains(t, txt, "Coupon (")
	})

	t.Run("no discount lines when the order has none", func(t *testing.T) {
		o := deliveryOrder()
		o.CouponDiscount = dec("0")
		o.CouponCode = nil
		o.DeliveryFee = nil
		o.TotalPrice = dec("28.00")
		txt, _ := text(t, "en", o, sampleItems(), true, true)
		require.NotContains(t, txt, "Coupon")
		require.Contains(t, txt, "28,00")
		require.Contains(t, txt, "0,00", "missing delivery fee renders as 0,00 on a delivery order")
	})

	t.Run("amounts above 999 use a non-breaking thousands separator", func(t *testing.T) {
		items := []orderDomain.OrderProduct{{Product: orderDomain.Product{CategoryName: "Traiteur", Name: "Plateau"}, Quantity: 10, UnitPrice: dec("123.45"), TotalPrice: dec("1234.50")}}
		o := pickupOrder()
		o.TakeawayDiscount = dec("0")
		o.TotalPrice = dec("1234.50")
		txt, _ := text(t, "fr", o, items, false, true)
		require.Contains(t, txt, "1 234,50 €")
	})

	t.Run("address without a box number", func(t *testing.T) {
		srv := smtptest.Start(t)
		useSMTP(t, srv)
		a := sampleAddress()
		a.BoxNumber = nil
		require.NoError(t, SendOrderConfirmedEmail(sampleUser(), "en", deliveryOrder(), sampleItems(), a))
		txt := srv.Only(t).Text
		require.Contains(t, txt, "Rue Saint-Gilles 12\r\nLiège, 4000")
		require.NotContains(t, txt, "Box")
	})

	t.Run("pickup confirmation has no delivery address block", func(t *testing.T) {
		txt, _ := text(t, "en", pickupOrder(), sampleItems(), false, true)
		require.NotContains(t, txt, "Delivery address")
		require.NotContains(t, txt, "Saint-Gilles")
	})
}

func TestOrderConfirmedEstimatedTimePerLanguage(t *testing.T) {
	// 17:30 UTC on Wed 1 July 2026 -> 19:30 Brussels.
	want := map[string]string{
		"fr": "mercredi 1 juillet 2026 à 19:30",
		"en": "Wednesday, July 1, 2026 at 7:30 PM",
		"nl": "woensdag 1 juli 2026 om 19:30",
		"zh": "2026年7月1日 星期三 19:30",
	}
	for _, lang := range allLangs {
		for _, o := range []orderDomain.Order{deliveryOrder(), pickupOrder()} {
			t.Run(lang+"/"+string(o.OrderType), func(t *testing.T) {
				srv := smtptest.Start(t)
				useSMTP(t, srv)
				require.NoError(t, SendOrderConfirmedEmail(sampleUser(), lang, o, sampleItems(), nil))
				m := srv.Only(t)
				require.Contains(t, m.Text, want[lang])
				require.Contains(t, m.HTML, want[lang])

				srv2 := smtptest.Start(t)
				useSMTP(t, srv2)
				require.NoError(t, SendReadyTimeUpdatedEmail(sampleUser(), lang, o))
				m = srv2.Only(t)
				require.Contains(t, m.Text, want[lang])
				require.Contains(t, m.HTML, want[lang])
			})
		}
	}

	t.Run("no estimate yet tells the customer they will be informed", func(t *testing.T) {
		srv := smtptest.Start(t)
		useSMTP(t, srv)
		o := deliveryOrder()
		o.EstimatedReadyTime = nil
		require.NoError(t, SendOrderConfirmedEmail(sampleUser(), "fr", o, sampleItems(), nil))
		m := srv.Only(t)
		require.Contains(t, m.Text, "Nous vous informerons dès que l'heure estimée sera définie.")
		require.NotContains(t, m.Text, "heure estimée).")
	})
}

func TestOrderConfirmedPickupVsDeliveryWording(t *testing.T) {
	srv := smtptest.Start(t)
	useSMTP(t, srv)
	require.NoError(t, SendOrderConfirmedEmail(sampleUser(), "en", deliveryOrder(), sampleItems(), sampleAddress()))
	require.Contains(t, srv.Only(t).Text, "Your order will be delivered on Wednesday, July 1, 2026 at 7:30 PM (estimated time).")

	srv = smtptest.Start(t)
	useSMTP(t, srv)
	require.NoError(t, SendOrderConfirmedEmail(sampleUser(), "en", pickupOrder(), sampleItems(), nil))
	require.Contains(t, srv.Only(t).Text, "Your order will be available for pickup on Wednesday, July 1, 2026 at 7:30 PM (estimated time).")
}

func TestOrderReadyWordingByType(t *testing.T) {
	srv := smtptest.Start(t)
	useSMTP(t, srv)
	require.NoError(t, SendOrderReadyEmail(sampleUser(), "nl", deliveryOrder()))
	require.Contains(t, srv.Only(t).Text, "Uw bestelling is onderweg")

	srv = smtptest.Start(t)
	useSMTP(t, srv)
	require.NoError(t, SendOrderReadyEmail(sampleUser(), "nl", pickupOrder()))
	require.Contains(t, srv.Only(t).Text, "klaar om afgehaald te worden")
}

func TestCanceledEmailReasonPerLanguage(t *testing.T) {
	reason := orderDomain.OrderCancellationReasonKitchenClosed
	want := map[string]string{"fr": "cuisine fermée", "en": "kitchen closed", "nl": "keuken gesloten", "zh": "厨房已关闭"}
	for _, lang := range allLangs {
		t.Run(lang, func(t *testing.T) {
			srv := smtptest.Start(t)
			useSMTP(t, srv)
			require.NoError(t, SendOrderCanceledEmail(sampleUser(), lang, testOrderID.String(), &reason))
			m := srv.Only(t)
			require.Contains(t, m.Text, want[lang])
			require.Contains(t, m.HTML, want[lang])
		})
	}

	t.Run("no reason or OTHER keeps the generic copy", func(t *testing.T) {
		other := orderDomain.OrderCancellationReasonOther
		for _, r := range []*orderDomain.OrderCancellationReason{nil, &other} {
			srv := smtptest.Start(t)
			useSMTP(t, srv)
			require.NoError(t, SendOrderCanceledEmail(sampleUser(), "en", testOrderID.String(), r))
			for _, label := range want {
				require.NotContains(t, srv.Only(t).Text, label)
			}
		}
	})
}

func TestLocalizedCancellationReason(t *testing.T) {
	r := orderDomain.OrderCancellationReasonDeliveryArea
	other := orderDomain.OrderCancellationReasonOther
	unknown := orderDomain.OrderCancellationReason("SOMETHING_NEW")

	require.Equal(t, "hors zone de livraison", LocalizedCancellationReason(&r, "fr"))
	require.Equal(t, "outside delivery area", LocalizedCancellationReason(&r, "en"))
	require.Equal(t, "buiten bezorggebied", LocalizedCancellationReason(&r, "nl"))
	require.Equal(t, "超出配送范围", LocalizedCancellationReason(&r, "zh"))
	for _, lang := range []string{"de", "", "  ", "xx-YY"} {
		require.Equal(t, "hors zone de livraison", LocalizedCancellationReason(&r, lang), "unsupported language %q falls back to French", lang)
	}
	require.Equal(t, "outside delivery area", LocalizedCancellationReason(&r, " EN-gb "), "region tags and case reduce to the base language")
	require.Equal(t, "超出配送范围", LocalizedCancellationReason(&r, "zh-Hans"))
	require.Empty(t, LocalizedCancellationReason(nil, "en"))
	require.Empty(t, LocalizedCancellationReason(&other, "en"))
	require.Empty(t, LocalizedCancellationReason(&unknown, "en"), "an unmapped reason has no label")
}

func TestRefundEmailShowsAmountAsGiven(t *testing.T) {
	srv := smtptest.Start(t)
	useSMTP(t, srv)
	require.NoError(t, SendRefundIssuedEmail(sampleUser(), "fr", testOrderID.String(), "1 234,56"))
	m := srv.Only(t)
	require.Contains(t, m.Text, "1 234,56")
	require.Contains(t, m.HTML, "1 234,56")
}

func TestEmailHTMLEscapesUserInputButTextDoesNot(t *testing.T) {
	user := userDomain.User{FirstName: "Zoë", LastName: `<b>O'Neil&Co</b>`, Email: "zoe@example.com"}
	srv := smtptest.Start(t)
	useSMTP(t, srv)
	require.NoError(t, SendWelcomeEmail(user, "fr", "https://shop.test"))
	m := srv.Only(t)

	require.Contains(t, m.Text, `Zoë <b>O'Neil&Co</b>`)
	require.NotContains(t, m.HTML, "<b>O'Neil", "markup in a name must not be injected into the HTML part")
	require.Contains(t, m.HTML, "Zoë &lt;b&gt;O&#39;Neil&amp;Co&lt;/b&gt;")
	// The To display name is not interpreted either (only the address is on the envelope).
	require.Equal(t, "zoe@example.com", m.To)
}

func TestSalutationFallsBackWhenNameIsMissing(t *testing.T) {
	srv := smtptest.Start(t)
	useSMTP(t, srv)
	require.NoError(t, SendWelcomeEmail(userDomain.User{Email: "social.only@example.com"}, "en", "https://shop.test"))
	require.True(t, strings.HasPrefix(srv.Only(t).Text, "Hello social.only,"), srv.Only(t).Text[:30])
}

func TestFeedbackEmail(t *testing.T) {
	srv := smtptest.Start(t)
	useSMTP(t, srv)
	t.Setenv("FEEDBACK_RECIPIENT_EMAIL", "admin@tsb.test")

	require.NoError(t, SendFeedbackEmail("Marc <script>", "marc@example.com", "delivery", "complaint", "Cold sushi & late", "nl"))
	m := srv.Only(t)

	require.Equal(t, "Customer feedback (complaint) - Marc <script>", m.Subject)
	require.Equal(t, "admin@tsb.test", m.To)
	require.Equal(t, []string{"admin@tsb.test"}, srv.Messages()[0].To)
	for _, w := range []string{"marc@example.com", "delivery", "complaint", "nl"} {
		require.Contains(t, m.Text, w)
		require.Contains(t, m.HTML, w)
	}
	require.Contains(t, m.Text, "Cold sushi & late")
	require.Contains(t, m.HTML, "Cold sushi &amp; late")
	require.NotContains(t, m.HTML, "<script>", "customer-supplied markup is escaped")
	require.Empty(t, m.Header.Get("References"), "feedback is not part of an order thread")
}

func TestAssistantDisconnectedEmail(t *testing.T) {
	srv := smtptest.Start(t)
	useSMTP(t, srv)
	// 13:05 UTC on 3 Oct 2026 is 15:05 in Brussels (CEST).
	expired := time.Date(2026, 10, 3, 13, 5, 0, 0, time.UTC)
	require.NoError(t, SendAssistantDisconnectedEmail("owner@tsb.test", "https://dash.test/assistant", expired))
	m := srv.Only(t)

	require.Equal(t, "微信助手已断开，请重新连接", m.Subject)
	require.Equal(t, "owner@tsb.test", m.To)
	for _, body := range []string{m.Text, m.HTML} {
		require.Contains(t, body, "10月3日 15:05")
		require.Contains(t, body, "https://dash.test/assistant")
	}
}
