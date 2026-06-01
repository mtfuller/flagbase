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
	"github.com/mtfuller/flagbase/internal/frontend"
	"github.com/mtfuller/flagbase/internal/function"
	"github.com/mtfuller/flagbase/internal/gateway"
	"github.com/mtfuller/flagbase/internal/iam"
	"github.com/mtfuller/flagbase/internal/storage"
	"github.com/mtfuller/flagbase/internal/table"
	"github.com/mtfuller/flagbase/internal/tracing"
	"github.com/mtfuller/flagbase/internal/trigger"
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
	frontendSvc *frontend.Service,
	tableEng *table.Engine,
	triggerEng *trigger.Engine,
	tracer *tracing.Recorder,
) *Server {
	gw := gateway.NewProxyHandler(featureEng)
	h := &Handlers{IAM: iamSvc, Feature: featureEng, Metrics: metrics, Gateway: gw}
	ah := &AdminHandlers{IAM: iamSvc, Setup: setupMgr, Store: store, DB: db}
	fh := &FunctionHandlers{Functions: fnStore}
	ffh := &FrontendHandlers{Frontends: frontendSvc}
	th := &TableHandlers{Tables: tableEng}
	trh := &TriggerHandlers{Triggers: triggerEng}
	mh := &MonitoringHandlers{DB: db}
	_ = tracer

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
	r.Use(TraceMiddleware)
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
		r.Put("/flags/{key}/status", h.TransitionFlagStatus)

		// Named variants (A/B testing buckets)
		r.Get("/flags/{key}/variants", h.ListFlagVariants)
		r.Post("/flags/{key}/variants", h.CreateFlagVariant)
		r.Delete("/flags/{key}/variants/{variantKey}", h.DeleteFlagVariant)

		// Per-user overrides (dev testing in production)
		r.Get("/flags/{key}/overrides", h.ListFlagOverrides)
		r.Post("/flags/{key}/overrides", h.CreateFlagOverride)
		r.Delete("/flags/{key}/overrides/{userId}", h.DeleteFlagOverride)

		r.Post("/metrics", h.RecordMetric)
		r.Get("/gateway/routes", h.ListGatewayRoutes)
		r.With(RequireRole("admin")).Post("/gateway/routes", h.RegisterGatewayRoute)
		r.With(RequireRole("admin")).Delete("/gateway/routes/{id}", h.DeleteGatewayRoute)
		// HTTP trigger invocation (auth required; POST body forwarded as event data)
		r.Get("/triggers/{id}/invoke", trh.InvokeHTTPTrigger)
		r.Post("/triggers/{id}/invoke", trh.InvokeHTTPTrigger)
	})

	// Admin API (must be registered before the /admin/* SPA catch-all)
	r.Route("/admin/api", func(r chi.Router) {
		r.Use(RequireRole("admin"))
		r.Get("/users", ah.AdminListUsers)
		r.Delete("/users/{id}", ah.AdminDeleteUser)
		r.Put("/users/{id}/role", ah.AdminUpdateUserRole)
		r.Get("/groups", ah.AdminListGroups)
		r.Post("/groups", ah.AdminCreateGroup)
		r.Delete("/groups/{id}", ah.AdminDeleteGroup)
		r.Get("/groups/{id}/members", ah.AdminListGroupMembers)
		r.Post("/groups/{id}/members", ah.AdminAddGroupMember)
		r.Delete("/groups/{id}/members/{userId}", ah.AdminRemoveGroupMember)
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
		r.Get("/functions/{id}/invoke/stream", fh.InvokeFunctionStream)
		r.Get("/functions/{id}/versions", fh.ListFunctionVersions)
		r.Get("/functions/{id}/invocations", fh.ListFunctionInvocations)
		r.Post("/functions/{id}/versions", fh.DeployFunctionVersion)
		r.Get("/functions/{id}/scaffold", fh.GetFunctionScaffold)
		r.Get("/functions/{id}/triggers", trh.ListFunctionTriggers)
		r.Get("/triggers", trh.ListTriggers)
		r.Post("/triggers", trh.CreateTrigger)
		r.Get("/triggers/{id}", trh.GetTrigger)
		r.Put("/triggers/{id}", trh.UpdateTrigger)
		r.Delete("/triggers/{id}", trh.DeleteTrigger)

		// Monitoring — traces, anomalies, custom metrics
		r.Get("/monitoring/summary", mh.GetMonitoringSummary)
		r.Get("/traces", mh.ListTraces)
		r.Get("/traces/{id}", mh.GetTrace)
		r.Get("/anomalies", mh.ListAnomalies)
		r.Post("/anomalies/{id}/resolve", mh.ResolveAnomaly)
		r.Get("/functions/{id}/metrics", mh.GetFunctionMetrics)
		r.Get("/functions/{id}/invocations/{invId}", mh.GetInvocationDetail)
	})

	// Admin console SPA (served for all /admin/* paths not matched above)
	r.Get("/admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
	})
	r.Get("/admin/", serveAdmin)
	r.Get("/admin/*", serveAdmin)

	// Frontends — management API (authenticated) + public file serving
	r.Route("/api/v1/frontends", func(r chi.Router) {
		r.Use(RequireRole("user"))
		r.Get("/", ffh.ListFrontends)
		r.Post("/", ffh.CreateFrontend)
		r.Get("/{id}", ffh.GetFrontend)
		r.Put("/{id}", ffh.UpdateFrontend)
		r.Delete("/{id}", ffh.DeleteFrontend)
		r.Get("/{id}/versions", ffh.ListVersions)
		r.Post("/{id}/versions", ffh.CreateVersion)
		r.Get("/{id}/versions/{versionId}", ffh.GetVersion)
		r.Delete("/{id}/versions/{versionId}", ffh.DeleteVersion)
		r.Put("/{id}/versions/{versionId}/activate", ffh.ActivateVersion)
	})
	r.Get("/frontends/{slug}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
	})
	r.Get("/frontends/{slug}/*", ffh.ServeFrontend)

	// Tables — NoSQL CRUD with schema registry
	r.Route("/api/v1/tables", func(r chi.Router) {
		r.Use(RequireRole("user"))
		r.Get("/", th.ListTables)
		r.Post("/", th.CreateTable)
		r.Get("/{key}", th.GetTable)
		r.Post("/{key}/columns", th.AddColumns)
		r.Delete("/{key}", th.DeleteTable)
		r.Get("/{key}/records", th.ListRecords)
		r.Post("/{key}/records", th.CreateRecord)
		r.Get("/{key}/records/{id}", th.GetRecord)
		r.Put("/{key}/records/{id}", th.UpdateRecord)
		r.Delete("/{key}/records/{id}", th.DeleteRecord)
		// Flag-context lifecycle operations
		r.Delete("/{key}/rollback", th.RollbackRecords)
		r.Post("/{key}/promote", th.PromoteRecords)
	})

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
