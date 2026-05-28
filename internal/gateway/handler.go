package gateway

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"

	"github.com/mtfuller/flagbase/internal/feature"
	"github.com/mtfuller/flagbase/internal/iam"
)

// Route maps an incoming path prefix to a backend URL, optionally gated by a feature flag.
type Route struct {
	ID         string
	Pattern    string
	BackendURL string
	FlagKey    string // optional: feature flag key that must evaluate true to route
}

// ProxyHandler is a context-aware reverse proxy.
// It resolves which backend to use by evaluating feature flags against the caller's JWT identity.
//
// Flow (from design doc):
//  1. Extract Claims injected by IAMContextMiddleware
//  2. Build evaluation context from claims
//  3. Evaluate flag → select backend
//  4. Proxy to backend via httputil.ReverseProxy
type ProxyHandler struct {
	FeatureEngine *feature.Engine
	mu            sync.RWMutex
	routes        map[string]*Route
}

// NewProxyHandler creates a ProxyHandler backed by the given feature engine.
func NewProxyHandler(fe *feature.Engine) *ProxyHandler {
	return &ProxyHandler{
		FeatureEngine: fe,
		routes:        make(map[string]*Route),
	}
}

// RegisterRoute adds or replaces a route entry.
func (p *ProxyHandler) RegisterRoute(route *Route) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.routes[route.Pattern] = route
}

// ServeHTTP implements http.Handler.
func (p *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	evalCtx := map[string]interface{}{
		"userId": "anonymous",
		"role":   "guest",
	}
	if claims, ok := r.Context().Value(iam.UserContextKey).(*iam.Claims); ok {
		evalCtx["userId"] = claims.UserID
		evalCtx["role"] = claims.Role
	}

	p.mu.RLock()
	route, found := p.routes[r.URL.Path]
	p.mu.RUnlock()

	if !found {
		writeGatewayError(w, http.StatusNotFound, "no route registered for this path")
		return
	}

	if route.FlagKey != "" && !p.FeatureEngine.EvaluateBool(route.FlagKey, evalCtx) {
		writeGatewayError(w, http.StatusServiceUnavailable, "route disabled by feature flag")
		return
	}

	target, err := url.Parse(route.BackendURL)
	if err != nil {
		writeGatewayError(w, http.StatusInternalServerError, "invalid backend URL")
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ServeHTTP(w, r)
}

func writeGatewayError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}
