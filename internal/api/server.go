package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/mtfuller/flagbase/internal/config"
	"github.com/mtfuller/flagbase/internal/event"
	"github.com/mtfuller/flagbase/internal/feature"
	"github.com/mtfuller/flagbase/internal/gateway"
	"github.com/mtfuller/flagbase/internal/iam"
	"github.com/mtfuller/flagbase/internal/storage"
)

// Server wraps the HTTP server and all platform services.
type Server struct {
	cfg     *config.Config
	httpSrv *http.Server
	store   *storage.LocalAdapter
	bus     *event.Bus
}

// NewServer constructs and wires the HTTP server with all routes.
func NewServer(
	cfg *config.Config,
	_ *sql.DB,
	iamSvc *iam.Service,
	featureEng *feature.Engine,
	store *storage.LocalAdapter,
	bus *event.Bus,
	metrics MetricRecorder,
) *Server {
	gw := gateway.NewProxyHandler(featureEng)
	h := &Handlers{IAM: iamSvc, Feature: featureEng, Metrics: metrics, Gateway: gw}

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	r.Use(CORS)
	r.Use(IAMContextMiddleware(iamSvc))

	// Public
	r.Get("/health", h.Health)
	r.Post("/auth/register", h.Register)
	r.Post("/auth/login", h.Login)

	// Feature flag REST API (requires authentication)
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(RequireRole("user"))
		r.Get("/flags", h.ListFlags)
		r.Post("/flags", h.CreateFlag)
		r.Get("/flags/{key}", h.GetFlag)
		r.Put("/flags/{key}", h.UpdateFlag)
		r.Delete("/flags/{key}", h.DeleteFlag)
		r.Get("/flags/{key}/evaluate", h.EvaluateFlag)

		// Metrics — SDK clients post events here
		r.Post("/metrics", h.RecordMetric)

		// Gateway route management (register/delete require admin role)
		r.Get("/gateway/routes", h.ListGatewayRoutes)
		r.With(RequireRole("admin")).Post("/gateway/routes", h.RegisterGatewayRoute)
		r.With(RequireRole("admin")).Delete("/gateway/routes/{id}", h.DeleteGatewayRoute)
	})

	// Gateway — dynamic reverse proxy driven by feature flags
	r.Handle("/gateway/*", gw)

	srv := &Server{
		cfg:   cfg,
		store: store,
		bus:   bus,
		httpSrv: &http.Server{
			Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
			Handler:      r,
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 15 * time.Second,
			IdleTimeout:  60 * time.Second,
		},
	}
	return srv
}

// Start begins serving HTTP traffic. It blocks until the server is stopped.
func (s *Server) Start() error {
	return s.httpSrv.ListenAndServe()
}

// Stop gracefully drains in-flight requests and shuts down the server.
func (s *Server) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.httpSrv.Shutdown(ctx)
}
