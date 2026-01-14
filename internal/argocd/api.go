package argocd

import (
	"context"
	"fmt"

	"github.com/iamhalje/argo-sync/internal/models"

	"github.com/argoproj/argo-cd/v2/pkg/apiclient"
	argoapp "github.com/argoproj/argo-cd/v2/pkg/apiclient/application"
	argoappv1 "github.com/argoproj/argo-cd/v2/pkg/apis/application/v1alpha1"
)

// API is wrapper over Argo CD Application Services.
type API interface {
	ListApplications(ctx context.Context, cluster models.Cluster) ([]models.Application, error)
	RefreshApplication(ctx context.Context, cluster models.Cluster, app models.AppRef, hard bool) error
	SyncApplication(ctx context.Context, cluster models.Cluster, app models.AppRef, opts models.RunOptions) error
	GetApplication(ctx context.Context, cluster models.Cluster, app models.AppRef) (models.Application, error)
}

func (a *GRPCAPI) ListApplications(ctx context.Context, cluster models.Cluster) ([]models.Application, error) {
	c, closeFn, err := a.appClient(cluster)
	if err != nil {
		return nil, err
	}
	defer closeFn()

	resp, err := c.List(ctx, &argoapp.ApplicationQuery{})
	if err != nil {
		return nil, fmt.Errorf("%s: list applications: %w", cluster.ContextName, err)
	}

	out := make([]models.Application, 0, len(resp.Items))
	for _, it := range resp.Items {
<<<<<<< HEAD
		resources := make([]models.SyncResources, 0, len(it.Status.Resources))
		for _, r := range it.Status.Resources {
			resources = append(resources, models.SyncResources{
=======
		resources := make([]models.SyncResource, 0, len(it.Status.Resources))
		for _, r := range it.Status.Resources {
			resources = append(resources, models.SyncResource{
>>>>>>> cursor/-bc-70a3bc6c-cdc6-4cc7-b3a3-eabb8562f89c-956b
				Group:     r.Group,
				Kind:      r.Kind,
				Name:      r.Name,
				Namespace: r.Namespace,
			})
		}
		out = append(out, models.Application{
			Key:          models.AppKey{Name: it.Name},
			Project:      it.Spec.Project,
			Namespace:    it.Namespace,
			SyncStatus:   string(it.Status.Sync.Status),
			HealthStatus: string(it.Status.Health.Status),
			Resources:    resources,
			OperationPhase: func() string {
				if it.Status.OperationState == nil {
					return ""
				}
				return string(it.Status.OperationState.Phase)
			}(),
			OperationMessage: func() string {
				if it.Status.OperationState == nil {
					return ""
				}
				return it.Status.OperationState.Message
			}(),
		})
	}
	return out, nil
}

func (a *GRPCAPI) RefreshApplication(ctx context.Context, cluster models.Cluster, app models.AppRef, hard bool) error {
	c, closeFn, err := a.appClient(cluster)
	if err != nil {
		return err
	}
	defer closeFn()

	refresh := "normal"
	if hard {
		refresh = "hard"
	}

	_, err = c.Get(ctx, &argoapp.ApplicationQuery{
		Name:         ptr(app.Name),
		AppNamespace: ptr(app.Namespace),
		Refresh:      ptr(refresh),
	})
	if err != nil {
		return fmt.Errorf("%s/%s: refresh (hard=%v): %w", cluster.ContextName, app.Name, hard, err)
	}
	return nil
}

func (a *GRPCAPI) SyncApplication(ctx context.Context, cluster models.Cluster, app models.AppRef, opts models.RunOptions) error {
	c, closeFn, err := a.appClient(cluster)
	if err != nil {
		return err
	}
	defer closeFn()

	req := &argoapp.ApplicationSyncRequest{
		Name:         ptr(app.Name),
		AppNamespace: ptr(app.Namespace),
		Prune:        ptr(opts.Prune),
		DryRun:       ptr(opts.DryRun),
	}

	if len(opts.Resources) > 0 {
		req.Resources = make([]*argoappv1.SyncOperationResource, 0, len(opts.Resources))
<<<<<<< HEAD

		for _, r := range opts.Resources {
			req.Resources = append(req.Resources, &argoappv1.SyncOperationResource{Group: r.Group, Kind: r.Kind, Name: r.Name, Namespace: r.Namespace})
=======
		for _, r := range opts.Resources {
			req.Resources = append(req.Resources, &argoappv1.SyncOperationResource{
				Group:     r.Group,
				Kind:      r.Kind,
				Name:      r.Name,
				Namespace: r.Namespace,
			})
>>>>>>> cursor/-bc-70a3bc6c-cdc6-4cc7-b3a3-eabb8562f89c-956b
		}
	}

	// Mirror argocd CLI:
	// https://github.com/argoproj/argo-cd/blob/master/pkg/apis/application/v1alpha1/types.go#L1581
	// - default strategy is "hook"
	// - "apply-only" selects strategy "apply"
	// - --force sets Force on selected strategy
	if opts.ApplyOnly {
		req.Strategy = &argoappv1.SyncStrategy{Apply: &argoappv1.SyncStrategyApply{}}
		req.Strategy.Apply.Force = opts.Force
	} else {
		req.Strategy = &argoappv1.SyncStrategy{Hook: &argoappv1.SyncStrategyHook{}}
		req.Strategy.Hook.Force = opts.Force
	}

	_, err = c.Sync(ctx, req)
	if err != nil {
		return fmt.Errorf("%s/%s: sync (prune=%v dryRun=%v applyOnly=%v force=%v): %w",
			cluster.ContextName, app.Name, opts.Prune, opts.DryRun, opts.ApplyOnly, opts.Force, err)
	}
	return nil
}

func (a *GRPCAPI) GetApplication(ctx context.Context, cluster models.Cluster, app models.AppRef) (models.Application, error) {
	c, closeFn, err := a.appClient(cluster)
	if err != nil {
		return models.Application{}, err
	}
	defer closeFn()

	resp, err := c.Get(ctx, &argoapp.ApplicationQuery{
		Name:         ptr(app.Name),
		AppNamespace: ptr(app.Namespace),
	})
	if err != nil {
		return models.Application{}, fmt.Errorf("%s/%s: get application: %w", cluster.ContextName, app.Name, err)
	}

	out := models.Application{
		Key:          models.AppKey{Name: resp.Name},
		Project:      resp.Spec.Project,
		Namespace:    resp.Namespace,
		SyncStatus:   string(resp.Status.Sync.Status),
		HealthStatus: string(resp.Status.Health.Status),
	}
	if len(resp.Status.Resources) > 0 {
		out.Resources = make([]models.SyncResource, 0, len(resp.Status.Resources))
		for _, r := range resp.Status.Resources {
			out.Resources = append(out.Resources, models.SyncResource{
				Group:     r.Group,
				Kind:      r.Kind,
				Name:      r.Name,
				Namespace: r.Namespace,
			})
		}
	}
	if resp.Status.OperationState != nil {
		out.OperationPhase = string(resp.Status.OperationState.Phase)
		out.OperationMessage = resp.Status.OperationState.Message
	}
	return out, nil
}

func (a *GRPCAPI) appClient(cluster models.Cluster) (argoapp.ApplicationServiceClient, func(), error) {
	opts := apiclient.ClientOptions{
		ServerAddr:      cluster.Server,
		Insecure:        cluster.Insecure,
		AuthToken:       cluster.AuthToken,
		GRPCWeb:         cluster.GRPCWeb,
		GRPCWebRootPath: cluster.GRPCWebRootPath,
	}

	ac, err := apiclient.NewClient(&opts)
	if err != nil {
		return nil, func() {}, fmt.Errorf("%s: create argocd client: %w", cluster.ContextName, err)
	}

	closer, c, err := ac.NewApplicationClient()
	if err != nil {
		return nil, func() {}, fmt.Errorf("%s: create application client: %w", cluster.ContextName, err)
	}

	closeFn := func() {
		_ = closer.Close()
	}
	return c, closeFn, nil
}

type GRPCAPI struct{}

func NewGRPCAPI() *GRPCAPI { return &GRPCAPI{} }

func ptr[T any](v T) *T { return &v }
