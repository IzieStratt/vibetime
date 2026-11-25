//go:build !private

package server

import (
	"database/sql"

	"github.com/IzieStratt/vibetime/internal/verify"
)

// Hooks for server-side cross-account checks. The public build has none; they are deployed
// from a private module.
func (s *Server) extraFlags(_ user, _ SubmitRequest, _ *verify.Result) any { return nil }

func (s *Server) recordExtra(_ *sql.Tx, _, _ int64, _ any) {}
