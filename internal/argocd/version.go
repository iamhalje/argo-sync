package argocd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/argoproj/argo-cd/v2/pkg/apiclient"
	"github.com/iamhalje/argo-sync/internal/models"
	emptypb "google.golang.org/protobuf/types/known/emptypb"
)

type ServerVersion struct {
	Raw   string
	Major int
}

func DetectServerVersion(ctx context.Context, cluster models.Cluster) (ServerVersion, error) {
	// 1) Best-effort: gRPC Version API (fast, uses same channel as other ops).
	opts := apiclient.ClientOptions{ServerAddr: cluster.Server, Insecure: cluster.Insecure, AuthToken: cluster.AuthToken, GRPCWeb: cluster.GRPCWeb, GRPCWebRootPath: cluster.GRPCWebRootPath}

	ac, err := apiclient.NewClient(&opts)
	if err != nil {
		// If gRPC client can't be created (e.g. URL/proxy mismatch), try HTTP fallback before failing.
		if v, httpErr := detectServerVersionHTTP(ctx, cluster); httpErr == nil {
			return v, nil
		}
		return ServerVersion{}, fmt.Errorf("%s: create argocd client: %w", cluster.ContextName, err)
	}

	closer, c, err := ac.NewVersionClient()
	if err != nil {
		if v, httpErr := detectServerVersionHTTP(ctx, cluster); httpErr == nil {
			return v, nil
		}
		return ServerVersion{}, fmt.Errorf("%s: create version client: %w", cluster.ContextName, err)
	}
	defer func() { _ = closer.Close() }()

	resp, err := c.Version(ctx, &emptypb.Empty{})
	if err != nil {
		if v, httpErr := detectServerVersionHTTP(ctx, cluster); httpErr == nil {
			return v, nil
		}
		return ServerVersion{}, fmt.Errorf("%s: get server version: %w", cluster.ContextName, err)
	}

	raw := strings.TrimSpace(resp.GetVersion())
	major := parseMajor(raw)
	return ServerVersion{Raw: raw, Major: major}, nil
}

func parseMajor(v string) int {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return 0
	}
	parts := strings.Split(v, ".")
	if len(parts) == 0 {
		return 0
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil || n < 0 {
		return 0
	}
	return n
}

type versionHTTPResponse struct {
	Version string `json:"Version"`
}

func detectServerVersionHTTP(ctx context.Context, cluster models.Cluster) (ServerVersion, error) {
	path := apiPath(cluster)

	// Try https first (default for Argo CD), then http as fallback.
	schemes := []string{"https", "http"}
	var lastErr error
	for _, scheme := range schemes {
		u := scheme + "://" + strings.TrimSpace(cluster.Server) + path
		v, err := httpGetVersion(ctx, cluster, u)
		if err == nil {
			return v, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("unknown error")
	}
	return ServerVersion{}, fmt.Errorf("%s: http version detection failed: %w", cluster.ContextName, lastErr)
}

func apiPath(cluster models.Cluster) string {
	// If Argo CD is behind a subpath (grpc-web root), REST endpoints follow the same prefix.
	// Examples:
	// - root="" or "/"        => /api/version
	// - root="/argocd"        => /argocd/api/version
	// - root="argocd/"        => /argocd/api/version
	root := strings.TrimSpace(cluster.GRPCWebRootPath)
	prefix := strings.Trim(root, "/")
	if prefix == "" || prefix == "." {
		return "/api/version"
	}
	return "/" + prefix + "/api/version"
}

func httpGetVersion(ctx context.Context, cluster models.Cluster, url string) (ServerVersion, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ServerVersion{}, err
	}
	if tok := strings.TrimSpace(cluster.AuthToken); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: cluster.Insecure}, //nolint:gosec // matches argocd config "insecure"
	}
	client := &http.Client{Transport: tr}

	resp, err := client.Do(req)
	if err != nil {
		return ServerVersion{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Read a small chunk to help debugging (auth / proxy / not found).
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		msg := strings.TrimSpace(string(b))
		if msg == "" {
			msg = resp.Status
		}
		return ServerVersion{}, fmt.Errorf("GET %s: %s", url, msg)
	}

	var out versionHTTPResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ServerVersion{}, fmt.Errorf("GET %s: decode json: %w", url, err)
	}
	raw := strings.TrimSpace(out.Version)
	return ServerVersion{Raw: raw, Major: parseMajor(raw)}, nil
}
