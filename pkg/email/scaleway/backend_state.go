package scaleway

// SaveBackend snapshots the process-wide e-mail backend (Scaleway client or SMTP settings, sender,
// suppression store) and returns a function that puts it back. It exists so tests of other
// packages, which point the backend at a fake with InitService, can leave no trace for the tests
// that run after them (see scalewaytest). It changes nothing at run time.
func SaveBackend() (restore func()) {
	prevTem, prevBase := temClient, baseReq
	prevHost, prevPort, prevUser, prevPass := smtpHost, smtpPort, smtpUser, smtpPassword
	prevStore := suppressionStore
	return func() {
		temClient, baseReq = prevTem, prevBase
		smtpHost, smtpPort, smtpUser, smtpPassword = prevHost, prevPort, prevUser, prevPass
		suppressionStore = prevStore
	}
}
