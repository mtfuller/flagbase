package gateway

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	"github.com/mtfuller/flagbase/internal/feature"
	"github.com/mtfuller/flagbase/internal/iam"
)

// Route maps an incoming path prefix to a backend URL, optionally gated by a feature flag.
type Route struct {
	ID         string `json:"id"`
	Pattern    string `json:"pattern"`
	BackendURL string `json:"backend_url"`
	FlagKey    string `json:"flag_key,omitempty"` // optional: must evaluate true to route
}

// ProxyHandler is a context-aware reverse proxy.
// It resolves which backend to use by evaluating feature flags against the caller's JWT identity.
//
// Flow (from design doc):
//  1. Extract Claims injected by IAMContextMiddleware
//  2. Build evaluation context from claims
//  3. Evaluate flag → select backend
//  4. Proxy to backend via httputil.ReverseProxy
//
// Routes are registered via RegisterRoute and managed through the REST API at
// /api/v1/gateway/routes. The handler is mounted at /gateway/*, so patterns
// should NOT include the /gateway prefix (e.g. register "/v2/checkout" to
// handle requests arriving at "/gateway/v2/checkout").
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

// RegisterRoute adds or replaces a route entry keyed by its Pattern.
func (p *ProxyHandler) RegisterRoute(route *Route) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.routes[route.Pattern] = route
}

// RemoveRoute deletes the route with the given ID. Returns true if found.
func (p *ProxyHandler) RemoveRoute(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for pattern, r := range p.routes {
		if r.ID == id {
			delete(p.routes, pattern)
			return true
		}
	}
	return false
}

// ListRoutes returns a snapshot of all registered routes.
func (p *ProxyHandler) ListRoutes() []*Route {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*Route, 0, len(p.routes))
	for _, r := range p.routes {
		out = append(out, r)
	}
	return out
}

// ServeHTTP implements http.Handler.
// The handler strips the /gateway prefix before matching routes so that a route
// registered as "/v2/checkout" handles requests to "/gateway/v2/checkout".
func (p *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	evalCtx := map[string]interface{}{
		"userId": "anonymous",
		"role":   "guest",
	}
	if claims, ok := r.Context().Value(iam.UserContextKey).(*iam.Claims); ok {
		evalCtx["userId"] = claims.UserID
		evalCtx["role"] = claims.Role
	}

	// Strip the /gateway mount prefix so stored patterns match cleanly.
	path := strings.TrimPrefix(r.URL.Path, "/gateway")
	if path == "" {
		path = "/"
	}

	p.mu.RLock()
	route, found := p.routes[path]
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
