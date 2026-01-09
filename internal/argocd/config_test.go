package argocd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"argocd:443", "argocd:443"},
		{" https://argocd.example.com:443 ", "argocd.example.com:443"},
		{"http://argo:80", "argo:80"},
		// If URL can't be parsed into host, keep original.
		{"https://", "https://"},
	}

	for _, tt := range tests {
		if got := normalizeServer(tt.in); got != tt.want {
			t.Fatalf("normalizeServer(%q)=%q want %q", tt.in, got, tt.want)
		}
	}
}

func TestLoadClustersFromFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := filepath.Join(dir, "argocd-config.yaml")

	cfg := `
current-context: ctx-a
contexts:
  - name: ctx-a
    server: https://argocd-a.example.com:443
    user: user-a
    insecure: true
  - name: ctx-b
    server: argocd-b.example.com:443
    user: user-b
servers:
  - server: https://argocd-a.example.com:443
    grpc-web-root-path: /argo
  - server: argocd-b.example.com:443
    grpc-web-root-path: /
users:
  - name: user-a
    auth-token: token-a
  - name: user-b
    auth-token: token-b
`
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	clusters, err := LoadClustersFromFile(p)
	if err != nil {
		t.Fatalf("LoadClustersFromFile: %v", err)
	}
	if len(clusters) != 2 {
		t.Fatalf("clusters len=%d want 2", len(clusters))
	}

	// ctx-a should have normalized server host and grpc-web enabled.
	if clusters[0].ContextName != "ctx-a" {
		t.Fatalf("clusters[0].ContextName=%q", clusters[0].ContextName)
	}
	if clusters[0].Server != "argocd-a.example.com:443" {
		t.Fatalf("clusters[0].Server=%q", clusters[0].Server)
	}
	if !clusters[0].Insecure {
		t.Fatalf("clusters[0].Insecure=false want true")
	}
	if clusters[0].AuthToken != "token-a" {
		t.Fatalf("clusters[0].AuthToken=%q", clusters[0].AuthToken)
	}
	if !clusters[0].GRPCWeb {
		t.Fatalf("clusters[0].GRPCWeb=false want true")
	}
	if clusters[0].GRPCWebRootPath != "/argo" {
		t.Fatalf("clusters[0].GRPCWebRootPath=%q", clusters[0].GRPCWebRootPath)
	}
}

func TestLoadClustersFromFile_NoContexts(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := filepath.Join(dir, "argocd-config.yaml")
	if err := os.WriteFile(p, []byte("contexts: []\n"), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	_, err := LoadClustersFromFile(p)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

