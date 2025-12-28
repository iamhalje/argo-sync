package argocd

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamhalje/argo-sync/internal/models"

	"sigs.k8s.io/yaml"
)

const DefaultConfigRelativePath = ".config/argocd/config"

// LoadClusters loads Argo CD contexts.
func LoadClusters() ([]models.Cluster, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}
	path := filepath.Join(home, DefaultConfigRelativePath)
	return LoadClustersFromFile(path)
}

func LoadClustersFromFile(path string) ([]models.Cluster, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read argocd config %q: %w", path, err)
	}

	var cfg localConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse argocd config %q: %w", path, err)
	}

	if len(cfg.Contexts) == 0 {
		return nil, errors.New("argocd config: no contexts found")
	}

	users := make(map[string]localUser, len(cfg.Users))
	for _, u := range cfg.Users {
		users[u.Name] = u
	}

	grpcWebRootByServer := map[string]string{}
	for _, s := range cfg.Servers {
		key := normalizeServer(s.Server)
		if key == "" {
			continue
		}
		if strings.TrimSpace(s.GRPCWebRootPath) == "" {
			continue
		}
		grpcWebRootByServer[key] = strings.TrimSpace(s.GRPCWebRootPath)
	}

	clusters := make([]models.Cluster, 0, len(cfg.Contexts))
	for _, c := range cfg.Contexts {
		if strings.TrimSpace(c.Name) == "" {
			continue
		}

		u := users[c.User]
		server := normalizeServer(c.Server)
		grpcWebRoot := grpcWebRootByServer[server]
		clusters = append(clusters, models.Cluster{
			ContextName: c.Name,
			Server:      server,
			Insecure:    c.Insecure,
			AuthToken:   u.AuthToken,
			GRPCWeb:     grpcWebRoot != "",
			// most setups use "/" for grpc-web root.
			GRPCWebRootPath: grpcWebRoot,
		})
	}

	if len(clusters) == 0 {
		return nil, errors.New("argocd config: contexts parsed but none usable")
	}
	return clusters, nil
}

// normalizeServer turns "https://argocd:443" or "http://argocd:443" into "argocd:443".
func normalizeServer(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			return u.Host
		}
	}
	return raw
}

type localConfig struct {
	CurrentContext string         `json:"current-context"`
	Contexts       []localContext `json:"contexts"`
	Servers        []localServer  `json:"servers"`
	Users          []localUser    `json:"users"`
}

type localContext struct {
	Name     string `json:"name"`
	Server   string `json:"server"`
	User     string `json:"user"`
	Insecure bool   `json:"insecure"`
}

type localUser struct {
	Name      string `json:"name"`
	AuthToken string `json:"auth-token"`
}

type localServer struct {
	Server          string `json:"server"`
	GRPCWebRootPath string `json:"grpc-web-root-path"`
}
