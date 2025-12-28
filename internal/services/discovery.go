package services

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/iamhalje/argo-sync/internal/argocd"
	"github.com/iamhalje/argo-sync/internal/models"

	"golang.org/x/sync/errgroup"
)

type DiscoveryService struct {
	api argocd.API
}

func NewDiscoveryService(api argocd.API) *DiscoveryService {
	return &DiscoveryService{api: api}
}

// DiscoverInventory lists apps from all clusters in parallel and groups them by AppKey.
func (s *DiscoveryService) DiscoverInventory(ctx context.Context, clusters []models.Cluster) (models.Inventory, map[string]error, error) {
	inv := models.Inventory{}
	clusterErrors := map[string]error{}
	var mu sync.Mutex

	type clusterApps struct {
		cluster string
		apps    []models.Application
	}

	results := make(chan clusterApps, len(clusters))

	g, ctx := errgroup.WithContext(ctx)
	for _, cl := range clusters {
		cl := cl
		g.Go(func() error {
			apps, err := s.api.ListApplications(ctx, cl)
			if err != nil {
				// record per-cluster error.
				mu.Lock()
				clusterErrors[cl.ContextName] = err
				mu.Unlock()
				return nil
			}
			results <- clusterApps{cluster: cl.ContextName, apps: apps}
			return nil
		})
	}

	_ = g.Wait()
	close(results)

	for r := range results {
		for _, app := range r.apps {
			if _, ok := inv[app.Key]; !ok {
				inv[app.Key] = map[string]models.Application{}
			}
			inv[app.Key][r.cluster] = app
		}
	}

	if len(inv) == 0 && len(clusterErrors) > 0 {
		// all clusters failed.
		keys := make([]string, 0, len(clusterErrors))
		for k := range clusterErrors {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b []error
		for _, k := range keys {
			b = append(b, fmt.Errorf("%s: %v", k, clusterErrors[k]))
		}
		return nil, clusterErrors, errors.Join(b...)
	}

	return inv, clusterErrors, nil
}

func InventoryKeys(inv models.Inventory) []models.AppKey {
	keys := make([]models.AppKey, 0, len(inv))
	for k := range inv {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Name < keys[j].Name })
	return keys
}

func ClustersForApps(inv models.Inventory, apps []models.AppKey) ([]string, error) {
	set := map[string]struct{}{}
	for _, a := range apps {
		m, ok := inv[a]
		if !ok {
			return nil, fmt.Errorf("inventory does not contain app %q", a.Name)
		}
		for cluster := range m {
			set[cluster] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out, nil
}
