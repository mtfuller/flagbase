package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/mtfuller/flagbase/internal/admin"
	"github.com/mtfuller/flagbase/internal/config"
	"github.com/mtfuller/flagbase/internal/event"
	"github.com/mtfuller/flagbase/internal/feature"
	"github.com/mtfuller/flagbase/internal/function"
	"github.com/mtfuller/flagbase/internal/gateway"
	"github.com/mtfuller/flagbase/internal/iam"
	"github.com/mtfuller/flagbase/internal/storage"
	"github.com/mtfuller/flagbase/web"
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
	db *sql.DB,
	iamSvc *iam.Service,
	featureEng *feature.Engine,
	store *storage.LocalAdapter,
	bus *event.Bus,
	metrics MetricRecorder,
	setupMgr *admin.SetupManager,
	fnStore *function.Store,
) *Server {
	gw := gateway.NewProxyHandler(featureEng)
	h := &Handlers{IAM: iamSvc, Feature: featureEng, Metrics: metrics, Gateway: gw}
	ah := &AdminHandlers{IAM: iamSvc, Setup: setupMgr, Store: store, DB: db}
	fh := &FunctionHandlers{Functions: fnStore}

	adminHTML, _ := web.FS.ReadFile("index.html")
	serveAdmin := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(adminHTML) //nolint:errcheck
	}

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

	// First-time setup (public — no admin exists yet)
	r.Get("/setup/status", ah.SetupStatus)
	r.Post("/setup", ah.CompleteSetup)

	// Feature flag REST API
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(RequireRole("user"))
		r.Get("/flags", h.ListFlags)
		r.Post("/flags", h.CreateFlag)
		r.Get("/flags/{key}", h.GetFlag)
		r.Put("/flags/{key}", h.UpdateFlag)
		r.Delete("/flags/{key}", h.DeleteFlag)
		r.Get("/flags/{key}/evaluate", h.EvaluateFlag)
		r.Post("/metrics", h.RecordMetric)
		r.Get("/gateway/routes", h.ListGatewayRoutes)
		r.With(RequireRole("admin")).Post("/gateway/routes", h.RegisterGatewayRoute)
		r.With(RequireRole("admin")).Delete("/gateway/routes/{id}", h.DeleteGatewayRoute)
	})

	// Admin API (must be registered before the /admin/* SPA catch-all)
	r.Route("/admin/api", func(r chi.Router) {
		r.Use(RequireRole("admin"))
		r.Get("/users", ah.AdminListUsers)
		r.Delete("/users/{id}", ah.AdminDeleteUser)
		r.Get("/metrics/summary", ah.AdminMetricsSummary)
		r.Get("/storage/buckets", ah.AdminListBuckets)
		r.Post("/storage/buckets", ah.AdminCreateBucket)
		r.Delete("/storage/buckets/{bucket}", ah.AdminDeleteBucket)
		r.Get("/storage/buckets/{bucket}/objects", ah.AdminListObjects)
		r.Post("/storage/buckets/{bucket}/objects", ah.AdminUploadObject)
		r.Get("/storage/buckets/{bucket}/objects/{object}", ah.AdminGetObject)
		r.Delete("/storage/buckets/{bucket}/objects/{object}", ah.AdminDeleteObject)
		r.Get("/functions", fh.ListFunctions)
		r.Post("/functions", fh.CreateFunction)
		r.Get("/functions/{id}", fh.GetFunction)
		r.Delete("/functions/{id}", fh.DeleteFunction)
		r.Post("/functions/{id}/invoke", fh.InvokeFunction)
		r.Get("/functions/{id}/scaffold", fh.GetFunctionScaffold)
	})

	// Admin console SPA (served for all /admin/* paths not matched above)
	r.Get("/admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
	})
	r.Get("/admin/", serveAdmin)
	r.Get("/admin/*", serveAdmin)

	// Gateway — dynamic reverse proxy
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
