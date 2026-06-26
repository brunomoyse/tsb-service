package resolver

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/99designs/gqlgen/graphql/handler/transport"
	coderws "github.com/coder/websocket"
	"go.uber.org/zap"
)

// originCheckedWebsocket wraps gqlgen's default Coder websocket implementation
// with our Origin allowlist. gqlgen v0.17.92 removed the built-in Gorilla
// Upgrader (and its CheckOrigin hook), so we enforce the same policy here:
//   - native apps (Android/iOS) send no Origin header → allowed
//   - browser origins must match the configured allowlist
//   - anything else is rejected and logged
//
// Because we vet the Origin ourselves, Coder is told to skip its own check
// (InsecureSkipVerify). Returning an error makes gqlgen respond with a 400 and
// never upgrade the connection.
type originCheckedWebsocket struct {
	allowedOrigins []string
}

func (o originCheckedWebsocket) Accept(w http.ResponseWriter, r *http.Request, options transport.WebsocketAcceptOptions) (transport.WebsocketConn, error) {
	origin := strings.TrimRight(strings.ToLower(r.Header.Get("Origin")), "/")
	if origin != "" {
		allowed := false
		for _, a := range o.allowedOrigins {
			if origin == strings.TrimRight(strings.ToLower(a), "/") {
				allowed = true
				break
			}
		}
		if !allowed {
			zap.L().Warn("WebSocket origin rejected",
				zap.String("origin", origin),
				zap.Strings("allowed", o.allowedOrigins),
			)
			return nil, fmt.Errorf("websocket origin %q not allowed", origin)
		}
	}

	return transport.CoderWebsocketImplementation{
		AcceptOptions: coderws.AcceptOptions{InsecureSkipVerify: true},
	}.Accept(w, r, options)
}
