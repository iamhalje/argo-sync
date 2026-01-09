package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/iamhalje/argo-sync/internal/models"
)

func TestTargetsForSelection(t *testing.T) {
	t.Parallel()

	inv := models.Inventory{
		models.AppKey{Name: "a"}: {
			"c1": {Key: models.AppKey{Name: "a"}, Namespace: "ns"},
		},
	}

	got := TargetsForSelection(inv, []models.AppKey{{Name: "a"}, {Name: "missing"}}, []string{"c1", "c2"})
	if len(got) != 1 {
		t.Fatalf("targets len=%d want 1: %#v", len(got), got)
	}
	if got[0].ClusterContext != "c1" || got[0].App.Name != "a" {
		t.Fatalf("unexpected target: %#v", got[0])
	}
}

func TestBulkService_Run_UnknownClusterContext(t *testing.T) {
	t.Parallel()

	api := fakeAPI{}
	svc := NewBulkService(api, 5)

	inv := models.Inventory{
		models.AppKey{Name: "app"}: {
			"missing": {Key: models.AppKey{Name: "app"}, Namespace: "ns"},
		},
	}
	clustersByContext := map[string]models.Cluster{} // empty -> unknown

	results, err := svc.Run(context.Background(), inv, clustersByContext, []models.AppKey{{Name: "app"}}, []string{"missing"}, models.ActionSync, models.RunOptions{}, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results len=%d want 1: %#v", len(results), results)
	}
	if results[0].Status != models.TaskFailed {
		t.Fatalf("status=%s want failed", results[0].Status)
	}
	if results[0].Err == nil {
		t.Fatalf("expected err in result")
	}
}

func TestBulkService_Run_Cancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before starting

	api := fakeAPI{
		syncFn: func(ctx context.Context, cluster models.Cluster, app models.AppRef, opts models.RunOptions) error {
			return ctx.Err()
		},
	}
	svc := NewBulkService(api, 5)

	inv := models.Inventory{
		models.AppKey{Name: "app"}: {
			"c1": {Key: models.AppKey{Name: "app"}, Namespace: "ns"},
		},
	}
	clustersByContext := map[string]models.Cluster{
		"c1": {ContextName: "c1"},
	}

	results, err := svc.Run(ctx, inv, clustersByContext, []models.AppKey{{Name: "app"}}, []string{"c1"}, models.ActionSync, models.RunOptions{}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
	if len(results) != 1 {
		t.Fatalf("results len=%d want 1: %#v", len(results), results)
	}
	if results[0].Status != models.TaskCancelled {
		t.Fatalf("status=%s want cancelled", results[0].Status)
	}
}

func TestBulkService_Run_WaitHealthy_SuccessFast(t *testing.T) {
	t.Parallel()

	api := fakeAPI{
		syncFn: func(ctx context.Context, cluster models.Cluster, app models.AppRef, opts models.RunOptions) error {
			return nil
		},
		getFn: func(ctx context.Context, cluster models.Cluster, app models.AppRef) (models.Application, error) {
			return models.Application{
				Key:            models.AppKey{Name: app.Name},
				SyncStatus:     "Synced",
				HealthStatus:   "Healthy",
				OperationPhase: "Succeeded",
			}, nil
		},
	}
	svc := NewBulkService(api, 1)

	inv := models.Inventory{
		models.AppKey{Name: "app"}: {
			"c1": {Key: models.AppKey{Name: "app"}, Namespace: "ns"},
		},
	}
	clustersByContext := map[string]models.Cluster{
		"c1": {ContextName: "c1"},
	}

	results, err := svc.Run(context.Background(), inv, clustersByContext, []models.AppKey{{Name: "app"}}, []string{"c1"}, models.ActionSync, models.RunOptions{
		Wait:         true,
		WaitHealthy:  true,
		WaitTimeout:  50 * time.Millisecond,
		PollInterval: 1 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(results) != 1 || results[0].Status != models.TaskSuccess {
		t.Fatalf("unexpected results: %#v", results)
	}
}

