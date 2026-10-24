package scaleway

import (
	"fmt"
	"html"
	"strings"

	temv1alpha1 "github.com/scaleway/scaleway-sdk-go/api/tem/v1alpha1"
	"github.com/scaleway/scaleway-sdk-go/logger"
)

// SendOpsAlertEmail tells the owner about a problem that needs a human, such as an order the EPOS
// never accepted. It is an operations message, not a customer one: plain text and a minimal HTML
// copy of it, in the language the caller wrote, with no template.
func SendOpsAlertEmail(to, subject, body string) error {
	newReq, err := copyBaseReq()
	if err != nil {
		return err
	}
	name := brandCfg().Name
	newReq.To = []*temv1alpha1.CreateEmailRequestAddress{{Email: to, Name: &name}}
	newReq.Subject = subject
	newReq.Text = body
	newReq.HTML = "<p>" + strings.ReplaceAll(html.EscapeString(body), "\n", "<br>") + "</p>"
	if err := dispatch(&newReq); err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}
	logger.Debugf("Ops alert email sent")
	return nil
}
