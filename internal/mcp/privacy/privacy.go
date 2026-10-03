// Package privacy keeps customers' contact details away from the assistant.
// tsb-mcp never requests last names, phone numbers or emails from
// tsb-service; free text written by customers (order notes, address
// details) can still contain them, so Scrub masks them there. The owner
// finds contact details in the dashboard.
package privacy

import (
	"regexp"
	"strings"
)

var (
	email = regexp.MustCompile(`[\p{L}\p{N}._%+-]+@[\p{L}\p{N}.-]+\.\p{L}{2,}`)
	// phone: 8 or more digits, optionally with +, spaces, dots, dashes,
	// slashes and parentheses between them.
	phone = regexp.MustCompile(`\+?\(?\d(?:[\s./()-]*\d){7,}`)
	// date: yyyy-mm-dd, dd/mm/yyyy, dd.mm.yyyy have eight digits too.
	date = regexp.MustCompile(`^(\d{4}[-/.]\d{1,2}[-/.]\d{1,2}|\d{1,2}[-/.]\d{1,2}[-/.]\d{4})$`)
)

// Hidden replaces a masked phone number or email.
const Hidden = "[hidden: see the dashboard]"

// Scrub masks emails and phone numbers in customer free text.
func Scrub(s string) string {
	if s == "" {
		return s
	}
	s = email.ReplaceAllString(s, Hidden)
	return phone.ReplaceAllStringFunc(s, func(m string) string {
		if digits(m) < 8 || date.MatchString(strings.TrimSpace(m)) {
			return m
		}
		return Hidden
	})
}

func digits(s string) int {
	return strings.Count(s, "0") + strings.Count(s, "1") + strings.Count(s, "2") + strings.Count(s, "3") + strings.Count(s, "4") +
		strings.Count(s, "5") + strings.Count(s, "6") + strings.Count(s, "7") + strings.Count(s, "8") + strings.Count(s, "9")
}
