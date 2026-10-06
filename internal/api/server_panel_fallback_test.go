package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The panel fallback follows the new-api NoRoute pattern: explicit API prefixes
// keep 404 semantics, while unmatched non-API paths render the control panel.
func TestPanelFallbackServesControlPanelForUnmatchedNonAPIPaths(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	staticDir := t.TempDir()
	t.Setenv("MANAGEMENT_STATIC_PATH", staticDir)
	if err := os.WriteFile(filepath.Join(staticDir, "management.html"), []byte("<html>management app</html>"), 0o600); err != nil {
		t.Fatalf("failed to write management asset: %v", err)
	}

	server := newTestServer(t)

	t.Run("unmatched root path serves panel", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "management app") {
			t.Fatalf("expected panel HTML, got %s", rr.Body.String())
		}
	})

	t.Run("unmatched nested path serves panel", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/some/random/spa/route", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "management app") {
			t.Fatalf("expected panel HTML, got %s", rr.Body.String())
		}
	})

	t.Run("api prefix keeps 404", func(t *testing.T) {
		for _, path := range []string{"/v1/unknown", "/v0/unknown", "/api/unknown", "/v1beta/unknown", "/backend-api/unknown", "/mirasim/unknown"} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("path %s status = %d, want %d body=%s", path, rr.Code, http.StatusNotFound, rr.Body.String())
			}
		}
	})

	t.Run("oauth callback suffix keeps its handler", func(t *testing.T) {
		// /devin/callback is a registered route; with no code/error it returns 400,
		// not the panel fallback. The callback path must never be swallowed.
		req := httptest.NewRequest(http.MethodGet, "/devin/callback", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusBadRequest, rr.Body.String())
		}
	})

	t.Run("non GET/HEAD keeps 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/some/random/spa/route", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusNotFound, rr.Body.String())
		}
	})
}

func TestPanelFallbackDisabledPanelStill404(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	staticDir := t.TempDir()
	t.Setenv("MANAGEMENT_STATIC_PATH", staticDir)
	if err := os.WriteFile(filepath.Join(staticDir, "management.html"), []byte("<html>management app</html>"), 0o600); err != nil {
		t.Fatalf("failed to write management asset: %v", err)
	}

	server := newTestServer(t)
	server.cfg.RemoteManagement.DisableControlPanel = true

	req := httptest.NewRequest(http.MethodGet, "/some/random/route", nil)
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusNotFound, rr.Body.String())
	}
}

func TestPanelFallbackHomeModeStill404(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	staticDir := t.TempDir()
	t.Setenv("MANAGEMENT_STATIC_PATH", staticDir)
	if err := os.WriteFile(filepath.Join(staticDir, "management.html"), []byte("<html>management app</html>"), 0o600); err != nil {
		t.Fatalf("failed to write management asset: %v", err)
	}

	server := newTestServer(t)
	server.cfg.Home.Enabled = true

	// In Home mode the heartbeat gate blocks every non-management path with 503
	// before routing, so the panel fallback never engages there.
	req := httptest.NewRequest(http.MethodGet, "/some/random/route", nil)
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
	}
}
