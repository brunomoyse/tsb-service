package application

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"sync"

	"go.uber.org/zap"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
	"tsb-service/pkg/logging"
)

// ClientProvider builds the HubRise API client of a connection. The workers ask for it per batch, so
// a reconnect with a new token is picked up on the next one.
type ClientProvider interface {
	Client(ctx context.Context, conn *domain.Connection) (*hubrise.Client, error)
}

// PooledClients is the production ClientProvider: it opens the stored token, builds a client against
// the configured API and keeps it until the token changes. All its clients share one Limiter, which
// is what keeps the process under the per connection caps (see hubrise.Limits.Split for replicas).
type PooledClients struct {
	codec   domain.TokenCodec
	baseURL string
	http    *http.Client
	limiter *hubrise.Limiter

	mu    sync.Mutex
	token string // the sealed token the cached client was built from
	c     *hubrise.Client
}

var _ ClientProvider = (*PooledClients)(nil)

// NewPooledClients returns a provider for the API at baseURL. httpClient and limiter may be nil (a
// default client, and a limiter with the default limits).
func NewPooledClients(codec domain.TokenCodec, baseURL string, httpClient *http.Client, limiter *hubrise.Limiter) *PooledClients {
	if limiter == nil {
		limiter = hubrise.NewLimiter(hubrise.DefaultLimits())
	}
	return &PooledClients{codec: codec, baseURL: baseURL, http: httpClient, limiter: limiter}
}

// Client returns the client of the connection; domain.ErrNotConnected when it holds no token.
func (p *PooledClients) Client(_ context.Context, conn *domain.Connection) (*hubrise.Client, error) {
	token, err := conn.AccessToken(p.codec)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.c != nil && p.token == string(conn.AccessTokenEnc) {
		return p.c, nil
	}
	opts := []hubrise.ClientOption{hubrise.WithLimiter(p.limiter)}
	if p.http != nil {
		opts = append(opts, hubrise.WithHTTPClient(p.http))
	}
	c, err := hubrise.New(p.baseURL, token.Reveal(), opts...)
	if err != nil {
		return nil, err
	}
	p.c, p.token = c, string(conn.AccessTokenEnc)
	return c, nil
}

// UnreadableTokenMessage is what the dashboard shows (HubriseStatus.lastError) when the stored token
// cannot be decrypted. It names the cause and the fix, never the token.
const UnreadableTokenMessage = "stored HubRise token cannot be decrypted (HUBRISE_TOKEN_KEY changed or lost): reconnect to HubRise"

// authRecordingClients makes a token that cannot be decrypted visible. Without it only the outbox
// would say so, while the status kept showing a healthy connection.
type authRecordingClients struct {
	inner ClientProvider
	conns domain.ConnectionRepository
	clock domain.Clock
}

// WithAuthRecording wraps a provider: the first time the stored token cannot be decrypted it stamps
// the connection's authorization error (auth_error_at and last_error), exactly as a 401 does. The
// connection is not Active from then on, so every worker pauses without burning an attempt, and a
// reconnect (which stores a new token and clears the error) resumes it.
func WithAuthRecording(inner ClientProvider, conns domain.ConnectionRepository, clock domain.Clock) ClientProvider {
	return &authRecordingClients{inner: inner, conns: conns, clock: clock}
}

func (a *authRecordingClients) Client(ctx context.Context, conn *domain.Connection) (*hubrise.Client, error) {
	c, err := a.inner.Client(ctx, conn)
	if err != nil && errors.Is(err, domain.ErrTokenUnreadable) {
		a.record(ctx, conn)
	}
	return c, err
}

// record stamps the error once. The row is read again so that a second worker, or a token replaced by
// a reconnect since conn was loaded, never overwrites what is already (or no longer) true.
func (a *authRecordingClients) record(ctx context.Context, conn *domain.Connection) {
	log := logging.FromContext(ctx)
	fresh, err := a.conns.Get(ctx)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return
	case err != nil:
		log.Error("hubrise: cannot read the connection to record the unreadable token", zap.Error(err))
		return
	}
	if fresh.AuthErrorAt != nil || !fresh.Connected() || !bytes.Equal(fresh.AccessTokenEnc, conn.AccessTokenEnc) {
		return
	}
	log.Error("hubrise: the stored access token cannot be decrypted, pipeline paused until reconnect")
	if err := a.conns.RecordAuthError(context.WithoutCancel(ctx), a.clock.Now(), UnreadableTokenMessage); err != nil {
		log.Error("hubrise: cannot record the unreadable token", zap.Error(err))
	}
}
