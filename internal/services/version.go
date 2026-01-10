package services

import (
	"context"
	"fmt"
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
	opts := apiclient.ClientOptions{ServerAddr: cluster.Server, Insecure: cluster.Insecure, AuthToken: cluster.AuthToken, GRPCWeb: cluster.GRPCWeb, GRPCWebRootPath: cluster.GRPCWebRootPath}

	ac, err := apiclient.NewClient(&opts)
	if err != nil {
		return ServerVersion{}, fmt.Errorf("%s: create argocd client: %w", cluster.ContextName, err)
	}

	closer, c, err := ac.NewVersionClient()
	if err != nil {
		return ServerVersion{}, fmt.Errorf("%s: create version client: %w", cluster.ContextName, err)
	}
	defer func() { _ = closer.Close() }()

	resp, err := c.Version(ctx, &emptypb.Empty{})
	if err != nil {
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
