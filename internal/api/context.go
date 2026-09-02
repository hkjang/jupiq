package api

import (
	"context"
	"net/http"
	"time"

	"github.com/hkjang/jupiq/internal/version"
)

func contextWithTimeout(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), timeout)
}

func contextWithoutCancel(r *http.Request) context.Context { return context.WithoutCancel(r.Context()) }

func (s *Server) versionInfo() version.Info { return version.Get() }
