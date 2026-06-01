package api

import (
	"context"
	"net/http"

	"github.com/mtfuller/flagbase/internal/iam"
	"github.com/mtfuller/flagbase/internal/tracing"
)

// IAMContextMiddleware extracts and validates the Authorization JWT, then injects
// the parsed Claims into the request context under iam.UserContextKey.
// Requests with no token are passed through unauthenticated (not rejected).
func IAMContextMiddleware(iamService *iam.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := r.Header.Get("Authorization")
			if token == "" {
				next.ServeHTTP(w, r)
				return
			}

			claims, err := iamService.ValidateToken(token)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}

			ctx := context.WithValue(r.Context(), iam.UserContextKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireRole rejects requests whose authenticated role does not match the
// required role (admins always pass regardless of role check).
func RequireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := r.Context().Value(iam.UserContextKey).(*iam.Claims)
			if !ok || (claims.Role != role && claims.Role != "admin") {
				writeError(w, http.StatusForbidden, "forbidden")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// TraceMiddleware generates a new trace ID for every request and injects it
// into the context so downstream handlers (function invoke, gateway, etc.) can
// link their operations to the originating HTTP call.
func TraceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := r.Header.Get("X-Flagbase-Trace-Id")
		if traceID == "" {
			traceID = tracing.NewTraceID()
		}
		ctx := tracing.WithTraceID(r.Context(), traceID)
		w.Header().Set("X-Flagbase-Trace-Id", traceID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// CORS adds permissive CORS headers for local and dashboard use.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
