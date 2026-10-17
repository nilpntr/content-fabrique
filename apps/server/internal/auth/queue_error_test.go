package auth

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nilpntr/content-fabrique/internal/store"
)

// A new workspace that cannot be created because River's tables are missing is a 503 with its own code, not an opaque 500.
func TestMissingQueueTablesAre503WithACode(t *testing.T) {
	err := fmt.Errorf("sync workspace: enqueue workspace_welcome: %w", store.ErrQueueNotMigrated)
	rec := httptest.NewRecorder()
	writeResolveError(rec, httptest.NewRequest(http.MethodGet, "/v1/me", nil), slog.New(slog.NewTextHandler(io.Discard, nil)), err)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), string(CodeQueueNotMigrated)) {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
}
