package services

import (
	"context"
	"errors"
	"testing"

	"github.com/iamhalje/argo-sync/internal/models"
)

type fakeAPI struct {
	listFn func(ctx context.Context, cluster models.Cluster) ([]models.Application, error)

	refreshFn func(ctx context.Context, cluster models.Cluster, app models.AppRef, hard bool) error
	syncFn    func(ctx context.Context, cluster models.Cluster, app models.AppRef, opts models.RunOptions) error
	getFn     func(ctx context.Context, cluster models.Cluster, app models.AppRef) (models.Application, error)
}

func (f fakeAPI) ListApplications(ctx context.Context, cluster models.Cluster) ([]models.Application, error) {
	if f.listFn == nil {
		return nil, nil
	}
	return f.listFn(ctx, cluster)
}

func (f fakeAPI) RefreshApplication(ctx context.Context, cluster models.Cluster, app models.AppRef, hard bool) error {
	if f.refreshFn == nil {
		return nil
	}
	return f.refreshFn(ctx, cluster, app, hard)
}

func (f fakeAPI) SyncApplication(ctx context.Context, cluster models.Cluster, app models.AppRef, opts models.RunOptions) error {
	if f.syncFn == nil {
		return nil
	}
	return f.syncFn(ctx, cluster, app, opts)
}

func (f fakeAPI) GetApplication(ctx context.Context, cluster models.Cluster, app models.AppRef) (models.Application, error) {
	if f.getFn == nil {
		return models.Application{}, nil
	}
	return f.getFn(ctx, cluster, app)
}

func TestDiscoveryService_DiscoverInventory_PartialFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	clA := models.Cluster{ContextName: "a"}
	clB := models.Cluster{ContextName: "b"}

	api := fakeAPI{
		listFn: func(ctx context.Context, cluster models.Cluster) ([]models.Application, error) {
			switch cluster.ContextName {
			case "a":
				return []models.Application{
					{Key: models.AppKey{Name: "app-1"}, Namespace: "ns"},
				}, nil
			case "b":
				return nil, errors.New("boom")
			default:
				return nil, nil
			}
		},
	}

	svc := NewDiscoveryService(api)
	inv, errs, err := svc.DiscoverInventory(ctx, []models.Cluster{clA, clB})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(errs) != 1 || errs["b"] == nil {
		t.Fatalf("expected error for cluster b, got: %#v", errs)
	}
	if inv == nil || len(inv) != 1 {
		t.Fatalf("expected inventory with 1 app, got: %#v", inv)
	}
	if _, ok := inv[models.AppKey{Name: "app-1"}]["a"]; !ok {
		t.Fatalf("expected app-1 in cluster a")
	}
}

func TestDiscoveryService_DiscoverInventory_AllFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	clA := models.Cluster{ContextName: "a"}
	clB := models.Cluster{ContextName: "b"}

	api := fakeAPI{
		listFn: func(ctx context.Context, cluster models.Cluster) ([]models.Application, error) {
			return nil, errors.New("boom")
		},
	}

	svc := NewDiscoveryService(api)
	inv, errs, err := svc.DiscoverInventory(ctx, []models.Cluster{clA, clB})
	if err == nil {
		t.Fatalf("expected err, got nil")
	}
	if inv != nil {
		t.Fatalf("expected nil inventory on all-failure, got: %#v", inv)
	}
	if len(errs) != 2 {
		t.Fatalf("expected 2 cluster errors, got: %#v", errs)
	}
}

