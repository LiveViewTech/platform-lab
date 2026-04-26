package provider

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LiveViewTech/platform-lab/tools/helm-version-check/pkg/config"
)

const testIndex = `
apiVersion: v1
entries:
  nginx:
    - version: "1.2.0"
      appVersion: "1.25.0"
    - version: "1.1.0"
      appVersion: "1.24.0"
    - version: "2.0.0"
      appVersion: "1.27.0"
    - version: "1.0.0"
      appVersion: "1.23.0"
  redis:
    - version: "7.0.0"
      appVersion: "7.2.0"
    - version: "6.9.0"
      appVersion: "7.1.0"
`

func testServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/index.yaml" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/yaml")
		w.Write([]byte(body))
	}))
}

func TestHTTPProvider_LatestVersion(t *testing.T) {
	srv := testServer(t, testIndex)
	defer srv.Close()

	p := NewHTTP("test", srv.URL, config.AuthConfig{})
	latest, err := p.LatestVersion("nginx")
	if err != nil {
		t.Fatal(err)
	}
	if latest != "2.0.0" {
		t.Errorf("expected 2.0.0, got %q", latest)
	}
}

func TestHTTPProvider_AllVersions(t *testing.T) {
	srv := testServer(t, testIndex)
	defer srv.Close()

	p := NewHTTP("test", srv.URL, config.AuthConfig{})
	versions, err := p.AllVersions("nginx")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 4 {
		t.Errorf("expected 4 versions, got %d: %v", len(versions), versions)
	}
}

func TestHTTPProvider_ChartNotFound(t *testing.T) {
	srv := testServer(t, testIndex)
	defer srv.Close()

	p := NewHTTP("test", srv.URL, config.AuthConfig{})
	_, err := p.LatestVersion("does-not-exist")
	if err == nil {
		t.Fatal("expected error for unknown chart")
	}
	if _, ok := err.(*ErrNotFound); !ok {
		t.Errorf("expected ErrNotFound, got %T: %v", err, err)
	}
}

func TestHTTPProvider_IndexCachedOnce(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.yaml" {
			calls++
			w.Write([]byte(testIndex))
		}
	}))
	defer srv.Close()

	p := NewHTTP("test", srv.URL, config.AuthConfig{})
	p.LatestVersion("nginx")
	p.LatestVersion("redis")
	p.AllVersions("nginx")

	if calls != 1 {
		t.Errorf("expected index.yaml to be fetched once, got %d calls", calls)
	}
}

func TestHTTPProvider_AppVersionFor(t *testing.T) {
	srv := testServer(t, testIndex)
	defer srv.Close()

	p := NewHTTP("test", srv.URL, config.AuthConfig{})
	cases := []struct {
		chart, version, want string
	}{
		{"nginx", "2.0.0", "1.27.0"},
		{"nginx", "1.0.0", "1.23.0"},
		{"redis", "7.0.0", "7.2.0"},
		{"nginx", "9.9.9", ""},      // version not in index
		{"missing", "1.0.0", ""},    // chart not in index
	}
	for _, c := range cases {
		got := p.AppVersionFor(c.chart, c.version)
		if got != c.want {
			t.Errorf("AppVersionFor(%q, %q) = %q, want %q", c.chart, c.version, got, c.want)
		}
	}
}

func TestHTTPProvider_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	p := NewHTTP("test", srv.URL, config.AuthConfig{})
	_, err := p.LatestVersion("nginx")
	if err == nil {
		t.Fatal("expected error on HTTP 404")
	}
}

func TestHTTPProvider_BasicAuth(t *testing.T) {
	const user, pass = "testuser", "testpass"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != user || p != pass {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(testIndex))
	}))
	defer srv.Close()

	// Correct credentials — should succeed.
	p := NewHTTP("test", srv.URL, config.AuthConfig{Type: "basic", Username: user, Password: pass})
	if _, err := p.LatestVersion("nginx"); err != nil {
		t.Fatalf("expected success with valid credentials: %v", err)
	}

	// No credentials — should get 401.
	p2 := NewHTTP("test", srv.URL, config.AuthConfig{})
	if _, err := p2.LatestVersion("nginx"); err == nil {
		t.Fatal("expected error without credentials")
	}
}

func TestHTTPProvider_TokenAuth(t *testing.T) {
	const token = "my-secret-token"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(testIndex))
	}))
	defer srv.Close()

	// Correct token — should succeed.
	p := NewHTTP("test", srv.URL, config.AuthConfig{Type: "token", Token: token})
	if _, err := p.LatestVersion("nginx"); err != nil {
		t.Fatalf("expected success with valid token: %v", err)
	}

	// Wrong token — should get 401.
	p2 := NewHTTP("test", srv.URL, config.AuthConfig{Type: "token", Token: "wrong"})
	if _, err := p2.LatestVersion("nginx"); err == nil {
		t.Fatal("expected error with wrong token")
	}
}
