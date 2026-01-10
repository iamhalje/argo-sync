package services

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/iamhalje/argo-sync/internal/argocd"
	"github.com/iamhalje/argo-sync/internal/models"

	"golang.org/x/sync/errgroup"
)

type BulkService struct {
	api      argocd.API
	parallel int
}

func NewBulkService(api argocd.API, parallel int) *BulkService {
	if parallel <= 0 {
		parallel = 20
	}
	return &BulkService{api: api, parallel: parallel}
}

// Run executes action for all (cluster, app) targets that exist in inventory.
func (s *BulkService) Run(ctx context.Context, inv models.Inventory, clustersByContext map[string]models.Cluster, selectedApps []models.AppKey, selectedClusters []string, action models.Action, opts models.RunOptions, events chan<- models.ProgressEvent) ([]models.Result, error) {
	targets := TargetsForSelection(inv, selectedApps, selectedClusters)
	if len(targets) == 0 {
		return nil, errors.New("no runnable targets")
	}

	emit := func(ev models.ProgressEvent) {
		if events == nil {
			return
		}
		select {
		case events <- ev:
		case <-ctx.Done():
		}
	}

	results := make([]models.Result, 0, len(targets))
	resCh := make(chan models.Result, len(targets))

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(s.parallel)

	for _, t := range targets {
		t := t
		g.Go(func() error {
			// if the run was cancelled before starting this target, repot it as cancelled
			select {
			case <-ctx.Done():
				err := ctx.Err()
				emit(models.ProgressEvent{At: time.Now(), Target: t, Phase: models.TaskCancelled, Action: action, Err: err})
				resCh <- models.Result{Target: t, Action: action, Status: models.TaskCancelled, Err: err}
				return err
			default:
			}

			cl, ok := clustersByContext[t.ClusterContext]
			if !ok {
				err := fmt.Errorf("unknown cluster context %q", t.ClusterContext)
				emit(models.ProgressEvent{At: time.Now(), Target: t, Phase: models.TaskFailed, Action: action, Err: err})
				resCh <- models.Result{Target: t, Action: action, Status: models.TaskFailed, Err: err}
				return nil
			}

			emit(models.ProgressEvent{At: time.Now(), Target: t, Phase: models.TaskRunning, Action: action})

			var err error
			switch action {
			case models.ActionRefresh:
				appMeta := inv[t.App][t.ClusterContext]
				err = s.api.RefreshApplication(ctx, cl, models.AppRef{Name: t.App.Name, Namespace: appMeta.Namespace}, false)
			case models.ActionHardRefresh:
				appMeta := inv[t.App][t.ClusterContext]
				err = s.api.RefreshApplication(ctx, cl, models.AppRef{Name: t.App.Name, Namespace: appMeta.Namespace}, true)
			case models.ActionSync:
				appMeta := inv[t.App][t.ClusterContext]
				ref := models.AppRef{Name: t.App.Name, Namespace: appMeta.Namespace}
				if opts.PreHardRefresh {
					if e := s.api.RefreshApplication(ctx, cl, ref, true); e != nil {
						err = fmt.Errorf("pre hard refresh failed: %w", e)
						break
					}
				} else if opts.PreRefresh {
					if e := s.api.RefreshApplication(ctx, cl, ref, false); e != nil {
						err = fmt.Errorf("pre refresh failed: %w", e)
						break
					}
				}
				emit(models.ProgressEvent{At: time.Now(), Target: t, Phase: models.TaskRunning, Action: action, Message: "submitted sync request"})
				err = s.api.SyncApplication(ctx, cl, ref, opts)
				if err != nil {
					// ah shit, sync already in progress
					if opts.Wait && IsOpInProgressErr(err) {
						emit(models.ProgressEvent{At: time.Now(), Target: t, Phase: models.TaskRunning, Action: action, Message: "sync already in progress"})
					} else {
						break
					}
				}
				if opts.Wait {
					if e := s.waitForHealthy(ctx, cl, t, ref, opts, emit); e != nil {
						err = e
					}
				}
			default:
				err = fmt.Errorf("unsupported action %q", action)
			}

			if err != nil {
				// treat cancellation/timeouts as cancelled
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					emit(models.ProgressEvent{At: time.Now(), Target: t, Phase: models.TaskCancelled, Action: action, Err: err})
					resCh <- models.Result{Target: t, Action: action, Status: models.TaskCancelled, Err: err}
					return nil
				}
				emit(models.ProgressEvent{At: time.Now(), Target: t, Phase: models.TaskFailed, Action: action, Err: err})
				resCh <- models.Result{Target: t, Action: action, Status: models.TaskFailed, Err: err}
				return nil
			}

			emit(models.ProgressEvent{At: time.Now(), Target: t, Phase: models.TaskSuccess, Action: action})
			resCh <- models.Result{Target: t, Action: action, Status: models.TaskSuccess}
			return nil
		})
	}

	err := g.Wait()
	close(resCh)

	for r := range resCh {
		results = append(results, r)
	}

	slices.SortFunc(results, func(a, b models.Result) int {
		if a.Target.ClusterContext != b.Target.ClusterContext {
			return cmp(a.Target.ClusterContext, b.Target.ClusterContext)
		}
		return cmp(a.Target.App.Name, b.Target.App.Name)
	})

	return results, err
}

func IsOpInProgressErr(err error) bool {
	// argocd returns:
	// "rpc error: code = failedPrecondition desc = another operation is already in progress"
	if err == nil {
		return false
	}

	s := strings.ToLower(err.Error())
	return strings.Contains(s, "another operation is already in progress") || strings.Contains("")
}

func (s *BulkService) waitForHealthy(ctx context.Context, cluster models.Cluster, target models.Target, app models.AppRef, opts models.RunOptions, emit func(models.ProgressEvent)) error {
	timeout := opts.WaitTimeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	poll := opts.PollInterval
	if poll == 0 {
		poll = 2 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		st, err := s.api.GetApplication(ctx, cluster, app)
		if err != nil {
			return err
		}

		msg := fmt.Sprintf("operation=%s sync=%s health=%s", normEmpty(st.OperationPhase, "Unknown"), normEmpty(st.SyncStatus, "Unknown"), normEmpty(st.HealthStatus, "Unknown"))
		emit(models.ProgressEvent{At: time.Now(), Target: target, Phase: models.TaskRunning, Action: models.ActionSync, Message: msg})

		if st.OperationPhase == "Failed" || st.OperationPhase == "Error" {
			m := st.OperationMessage
			if m == "" {
				m = "operation failed"
			}
			return fmt.Errorf("%s/%s: sync operation %s: %s", cluster.ContextName, app.Name, st.OperationPhase, m)
		}

		synced := st.SyncStatus == "Synced"
		healthy := st.HealthStatus == "Healthy"
		healthUnknown := st.HealthStatus == "" || st.HealthStatus == "Unknown"

		doneByOp := st.OperationPhase == "" || st.OperationPhase == "Succeeded"
		if synced && doneByOp {
			if !opts.WaitHealthy {
				return nil
			}
			if healthy || healthUnknown {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("%s/%s: wait timeout (%s): %s", cluster.ContextName, app.Name, timeout, msg)
		case <-ticker.C:
		}
	}
}

func normEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func cmp(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
