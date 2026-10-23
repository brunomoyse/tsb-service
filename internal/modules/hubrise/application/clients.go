package application

import (
	"context"
	"net/http"
	"sync"

	"tsb-service/internal/modules/hubrise/domain"
	"tsb-service/pkg/hubrise"
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
