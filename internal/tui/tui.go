package tui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/iamhalje/argo-sync/internal/argocd"
	"github.com/iamhalje/argo-sync/internal/models"
	"github.com/iamhalje/argo-sync/internal/services"
	"golang.org/x/sync/errgroup"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type Options struct {
	ConfigPath   string
	Contexts     []string
	Parallel     int
	WaitTimeout  time.Duration
	PollInterval time.Duration
	NoWait       bool
}

func Run(ctx context.Context, logger *slog.Logger, opts Options) error {
	api := argocd.NewGRPCAPI()
	m := newModel(ctx, logger, api, opts)

	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

type step int

const (
	stepLoading step = iota
	stepSelectApps
	stepDiffSelectCluster
	stepDiffLoading
	stepDiffResources
	stepDiffDetail
	stepSelectClusters
	stepSelectResources
	stepSelectAction
	stepConfirm
	stepRunning
	stepDone
	stepError
)

// all tui.
type model struct {
	rootCtx context.Context
	cancel  context.CancelFunc
	logger  *slog.Logger

	api       argocd.API
	discover  *services.DiscoveryService
	bulk      *services.BulkService
	clusters  []models.Cluster
	clusterBy map[string]models.Cluster
	inv       models.Inventory
	loadErrs  map[string]error

	step step
	err  error

	cursor int
	width  int
	height int

	appOffset int
	resOffset int
	clOffset  int

	appKeysAll    []models.AppKey
	appKeys       []models.AppKey
	appSelected   map[models.AppKey]bool
	resApps       []models.AppKey
	resAppIdx     int
	resList       []models.SyncResource
	resSelectedBy map[models.AppKey]map[models.SyncResource]bool
	clusterNames  []string
	clSelected    map[string]bool

	filtering  bool
	filter     textinput.Model
	refreshing bool

	actionIdx int
	action    models.Action
	runOpts   models.RunOptions

	eventsCh chan models.ProgressEvent
	doneCh   chan runDoneMsg
	results  []models.Result
	statuses map[models.Target]models.TaskStatus
	messages map[models.Target]string
	errors   map[models.Target]error

	runTargets    []models.Target
	runStartedAt  time.Time
	runFinishedAt time.Time

	beforeState map[models.Target]models.Application
	afterState  map[models.Target]models.Application

	diffApp        models.AppKey
	diffClusters   []string
	diffClCursor   int
	diffClOffset   int
	diffItems      []models.ResourceDiff
	diffResCursor  int
	diffResOffset  int
	diffDetailText []string
	diffDetailOff  int
	diffErr        error

	cli Options
}

func newModel(ctx context.Context, logger *slog.Logger, api argocd.API, opts Options) *model {
	runCtx, cancel := context.WithCancel(ctx)
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "type to filter apps (Esc clears)"
	ti.CharLimit = 64
	ti.Width = 40
	ti.Blur()
	return &model{
		rootCtx:       runCtx,
		cancel:        cancel,
		logger:        logger,
		api:           api,
		discover:      services.NewDiscoveryService(api),
		bulk:          services.NewBulkService(api, opts.Parallel),
		step:          stepLoading,
		appSelected:   map[models.AppKey]bool{},
		resSelectedBy: map[models.AppKey]map[models.SyncResource]bool{},
		clSelected:    map[string]bool{},
		filter:        ti,
		statuses:      map[models.Target]models.TaskStatus{},
		messages:      map[models.Target]string{},
		errors:        map[models.Target]error{},
		beforeState:   map[models.Target]models.Application{},
		afterState:    map[models.Target]models.Application{},
		action:        models.ActionSync,
		runOpts: models.RunOptions{
			Wait:         !opts.NoWait,
			WaitHealthy:  !opts.NoWait,
			WaitTimeout:  opts.WaitTimeout,
			PollInterval: opts.PollInterval,
		},
		cli: opts,
	}
}

type loadDoneMsg struct {
	clusters []models.Cluster
	inv      models.Inventory
	errs     map[string]error
	err      error
}

type refreshDoneMsg struct {
	clusters []models.Cluster
	inv      models.Inventory
	errs     map[string]error
	err      error
}

type progressMsg models.ProgressEvent
type eventsClosedMsg struct{}
type runDoneMsg struct {
	results []models.Result
	err     error
}

type diffLoadedMsg struct {
	app     models.AppKey
	cluster string
	items   []models.ResourceDiff
	err     error
}

func (m *model) Init() tea.Cmd {
	return m.loadCmd()
}

func (m *model) loadCmd() tea.Cmd {
	return func() tea.Msg {
		m.logger.Debug("loading clusters", slog.String("config", m.cli.ConfigPath), slog.Any("contexts", m.cli.Contexts))
		clusters, err := argocd.LoadClustersFromFile(m.cli.ConfigPath)
		if err != nil {
			m.logger.Debug("failed to load clusters", slog.Any("err", err))
			return loadDoneMsg{err: err}
		}
		clusters = filterClusters(clusters, m.cli.Contexts)
		if len(clusters) == 0 {
			return loadDoneMsg{err: fmt.Errorf("no contexts selected")}
		}
		m.logger.Debug("clusters loaded", slog.Int("count", len(clusters)))
		{
			g, vctx := errgroup.WithContext(m.rootCtx)
			g.SetLimit(10)
			for i := range clusters {
				i := i
				g.Go(func() error {
					ctx, cancel := context.WithTimeout(vctx, 5*time.Second)
					defer cancel()
					ver, err := argocd.DetectServerVersion(ctx, clusters[i])
					if err != nil {
						m.logger.Debug("version detection failed", slog.String("context", clusters[i].ContextName), slog.Any("err", err))
						return nil
					}
					clusters[i].ServerVersion = ver.Raw
					clusters[i].ServerMajor = ver.Major
					m.logger.Debug("server version detected", slog.String("context", clusters[i].ContextName), slog.String("version", ver.Raw), slog.Int("major", ver.Major))
					return nil
				})
			}
			_ = g.Wait()
		}
		start := time.Now()
		inv, errs, err := m.discover.DiscoverInventory(m.rootCtx, clusters)
		if err != nil {
			m.logger.Debug("inventory discovery failed", slog.Any("err", err), slog.Int("clusters", len(clusters)), slog.Int("cluster_errors", len(errs)))
			return loadDoneMsg{err: err, errs: errs}
		}
		m.logger.Debug("inventory discovered", slog.Duration("took", time.Since(start)), slog.Int("clusters", len(clusters)), slog.Int("apps", len(inv)), slog.Int("cluster_errors", len(errs)))
		if len(errs) > 0 {
			for k, e := range errs {
				m.logger.Debug("cluster discovery error", slog.String("context", k), slog.Any("err", e))
			}
		}
		return loadDoneMsg{clusters: clusters, inv: inv, errs: errs}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// keep offsets after resize
		m.appOffset = clamp(m.appOffset, 0, max(0, len(m.appKeys)-1))
		m.clOffset = clamp(m.clOffset, 0, max(0, len(m.clusterNames)-1))
		m.ensureVisible()
		return m, nil
	case tea.KeyMsg:
		return m.onKey(msg)
	case loadDoneMsg:
		if msg.err != nil {
			m.step = stepError
			m.err = msg.err
			return m, nil
		}
		m.clusters = msg.clusters
		m.clusterBy = map[string]models.Cluster{}
		for _, c := range m.clusters {
			m.clusterBy[c.ContextName] = c
		}
		m.inv = msg.inv
		m.loadErrs = msg.errs
		m.appKeysAll = services.InventoryKeys(m.inv)
		m.appKeys = m.appKeysAll
		m.step = stepSelectApps
		m.cursor = 0
		m.appOffset = 0
		return m, nil
	case refreshDoneMsg:
		m.refreshing = false
		if msg.err != nil {
			m.loadErrs = msg.errs
			m.err = msg.err
			return m, nil
		}

		var prev models.AppKey
		hasPrev := false
		if m.cursor >= 0 && m.cursor < len(m.appKeys) {
			prev = m.appKeys[m.cursor]
			hasPrev = true
		}

		m.clusters = msg.clusters
		m.clusterBy = map[string]models.Cluster{}
		for _, c := range m.clusters {
			m.clusterBy[c.ContextName] = c
		}
		m.inv = msg.inv
		m.loadErrs = msg.errs
		m.appKeysAll = services.InventoryKeys(m.inv)
		m.applyAppFilter()
		// drop sections for app that no longer exists
		if len(m.appSelected) > 0 {
			exists := map[models.AppKey]struct{}{}
			for _, k := range m.appKeysAll {
				exists[k] = struct{}{}
			}
			for k := range m.appSelected {
				if _, ok := exists[k]; !ok {
					delete(m.appSelected, k)
				}
			}
		}
		if hasPrev {
			if idx := indexOfAppKey(m.appKeys, prev); idx >= 0 {
				m.cursor = idx
			} else {
				m.cursor = clamp(m.cursor, 0, max(0, len(m.appKeys)-1))
			}
		} else {
			m.cursor = clamp(m.cursor, 0, max(0, len(m.appKeys)-1))
		}
		m.ensureVisible()
		return m, nil
	case progressMsg:
		ev := models.ProgressEvent(msg)
		m.statuses[ev.Target] = ev.Phase
		if ev.Message != "" {
			m.messages[ev.Target] = ev.Message
			if op, sync, health, ok := parseSyncWaitMessage(ev.Message); ok {
				m.afterState[ev.Target] = models.Application{OperationPhase: op, SyncStatus: sync, HealthStatus: health}
			}
		}
		if ev.Err != nil {
			m.errors[ev.Target] = ev.Err
		}
		m.logger.Debug("progress", slog.String("context", ev.Target.ClusterContext), slog.String("app", ev.Target.App.Name), slog.String("action", string(ev.Action)), slog.String("phase", string(ev.Phase)), slog.String("message", ev.Message), slog.Any("err", ev.Err))
		return m, waitForEvent(m.eventsCh)
	case eventsClosedMsg:
		return m, nil
	case runDoneMsg:
		m.results = msg.results
		m.runFinishedAt = time.Now()
		if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
			m.step = stepError
			m.err = msg.err
			return m, nil
		}
		m.step = stepDone
		return m, nil
	case diffLoadedMsg:
		if msg.err != nil {
			m.diffErr = msg.err
			m.step = stepDiffResources
			m.diffItems = nil
			m.diffResCursor = 0
			m.diffResOffset = 0
			return m, nil
		}
		m.diffErr = nil
		m.diffItems = msg.items
		m.diffResCursor = 0
		m.diffResOffset = 0
		m.step = stepDiffResources
		return m, nil
	default:
		return m, nil
	}
}

func (m *model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.cancel()
		return m, tea.Quit
	}

	switch m.step {
	case stepLoading:
		if msg.String() == "q" || msg.String() == "esc" {
			m.cancel()
			return m, tea.Quit
		}
		return m, nil

	case stepError:
		if msg.String() == "q" || msg.String() == "esc" || msg.String() == "enter" {
			m.cancel()
			return m, tea.Quit
		}
		return m, nil

	case stepSelectApps:
		return m.onKeySelectApps(msg)
	case stepDiffSelectCluster:
		return m.onKeyDiffSelectCluster(msg)
	case stepDiffLoading:
		if msg.String() == "q" || msg.String() == "esc" {
			m.step = stepDiffSelectCluster
			return m, nil
		}
		return m, nil
	case stepDiffResources:
		return m.onKeyDiffResources(msg)
	case stepDiffDetail:
		return m.onKeyDiffDetail(msg)
	case stepSelectClusters:
		return m.onKeySelectClusters(msg)
	case stepSelectResources:
		return m.onKeySelectResources(msg)
	case stepSelectAction:
		return m.onKeySelectAction(msg)
	case stepConfirm:
		return m.onKeyConfirm(msg)
	case stepRunning:
		// allow cancel in running
		if msg.String() == "q" || msg.String() == "esc" {
			m.cancel()
			return m, nil
		}
		return m, nil
	case stepDone:
		switch msg.String() {
		case "q", "esc":
			return m, tea.Quit
		case "r":
			m.resetToStart()
			return m, nil
		case "enter":
			return m, tea.Quit
		default:
			return m, nil
		}
	default:
		return m, nil
	}
}

func (m *model) onKeySelectApps(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// filters
	if m.filtering {
		switch msg.String() {
		case "esc":
			m.filtering = false
			m.filter.Blur()
			m.filter.SetValue("")
			m.applyAppFilter()
			return m, nil
		case "enter":
			m.filtering = false
			m.filter.Blur()
			m.applyAppFilter()
			return m, nil
		default:
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			m.applyAppFilter()
			return m, cmd
		}
	}

	switch msg.String() {
	case "q", "esc":
		// if a filter is active, esc clears it else otherwise quit.
		if m.filter.Value() != "" {
			m.filter.SetValue("")
			m.applyAppFilter()
			return m, nil
		}
		return m, tea.Quit
	case "/", "ctrl+f":
		m.filtering = true
		m.filter.Focus()
		return m, nil
	case "R", "ctrl+r":
		if !m.refreshing {
			m.refreshing = true
			return m, m.refreshCmd()
		}
		return m, nil
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
		m.ensureVisible()
		return m, nil
	case "down":
		if m.cursor < len(m.appKeys)-1 {
			m.cursor++
		}
		m.ensureVisible()
		return m, nil
	case "pgup":
		h := m.appsListHeight()
		if h <= 0 {
			h = 10
		}
		m.cursor -= h
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.ensureVisible()
		return m, nil
	case "pgdown":
		h := m.appsListHeight()
		if h <= 0 {
			h = 10
		}
		m.cursor += h
		if m.cursor > len(m.appKeys)-1 {
			m.cursor = len(m.appKeys) - 1
		}
		m.ensureVisible()
		return m, nil
	case " ":
		if len(m.appKeys) == 0 {
			return m, nil
		}
		k := m.appKeys[m.cursor]
		m.appSelected[k] = !m.appSelected[k]
		return m, nil
	case "a":
		all := true
		for _, k := range m.appKeys {
			if !m.appSelected[k] {
				all = false
				break
			}
		}
		for _, k := range m.appKeys {
			m.appSelected[k] = !all
		}
		return m, nil
	case "enter":
		apps := m.selectedApps()
		if len(apps) == 0 {
			return m, nil
		}
		cl, err := services.ClustersForApps(m.inv, apps)
		m.resApps = apps
		m.resAppIdx = 0
		if err != nil {
			m.step = stepError
			m.err = err
			return m, nil
		}
		m.clusterNames = cl
		m.clSelected = map[string]bool{}
		// default: all
		for _, c := range cl {
			m.clSelected[c] = true
		}
		m.cursor = 0
		m.clOffset = 0
		m.step = stepSelectClusters
		return m, nil
	case "d":
		if len(m.appKeys) == 0 || m.cursor < 0 || m.cursor >= len(m.appKeys) {
			return m, nil
		}
		k := m.appKeys[m.cursor]
		clusters := make([]string, 0, len(m.inv[k]))
		for c := range m.inv[k] {
			clusters = append(clusters, c)
		}
		sort.Strings(clusters)
		if len(clusters) == 0 {
			return m, nil
		}
		m.diffApp = k
		m.diffClusters = clusters
		m.diffClCursor = 0
		m.diffClOffset = 0
		m.diffItems = nil
		m.diffErr = nil
		m.step = stepDiffSelectCluster
		return m, nil
	default:
		return m, nil
	}
}

func (m *model) onKeySelectResources(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		return m, tea.Quit
	case "backspace":
		m.cursor = 0
		m.clOffset = 0
		m.step = stepSelectClusters
		return m, nil
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
		m.ensureVisible()
		return m, nil
	case "down":
		if m.cursor < len(m.resList)-1 {
			m.cursor++
		}
		m.ensureVisible()
		return m, nil
	case "pgup":
		h := m.resourcesListHeight()
		if h <= 0 {
			h = 10
		}
		m.cursor -= h
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.ensureVisible()
		return m, nil
	case "pgdown":
		h := m.resourcesListHeight()
		if h <= 0 {
			h = 10
		}
		m.cursor += h
		if m.cursor > len(m.resList)-1 {
			m.cursor = len(m.resList) - 1
		}
		m.ensureVisible()
		return m, nil
	case " ":
		if len(m.resList) == 0 {
			return m, nil
		}
		r := m.resList[m.cursor]
		app := m.resApps[m.resAppIdx]
		if m.resSelectedBy[app] == nil {
			m.resSelectedBy[app] = map[models.SyncResource]bool{}
		}
		m.resSelectedBy[app][r] = !m.resSelectedBy[app][r]
		return m, nil
	case "a":
		all := true
		app := m.resApps[m.resAppIdx]
		for _, r := range m.resList {
			if m.resSelectedBy[app] == nil || !m.resSelectedBy[app][r] {
				all = false
				break
			}
		}
		if m.resSelectedBy[app] == nil {
			m.resSelectedBy[app] = map[models.SyncResource]bool{}
		}
		for _, r := range m.resList {
			m.resSelectedBy[app][r] = !all
		}
		return m, nil
	case "enter":
		app := m.resApps[m.resAppIdx]
		if len(m.selectedResourcesFor(app)) == 0 {
			// resources are required for each selected app
			return m, nil
		}
		// advance to next app, or proceed to cluster selection
		if m.resAppIdx < len(m.resApps)-1 {
			m.resAppIdx++
			m.resList = resourcesForAppInClusters(m.inv, m.resApps[m.resAppIdx], m.selectedClusters())
			m.cursor = 0
			m.resOffset = 0
			m.ensureVisible()
			return m, nil
		}
		m.cursor = 0
		m.step = stepSelectAction
		return m, nil
	default:
		return m, nil
	}
}

func (m *model) onKeySelectClusters(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		return m, tea.Quit
	case "backspace":
		m.cursor = 0
		m.appOffset = 0
		m.step = stepSelectApps
		return m, nil
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
		m.ensureVisible()
		return m, nil
	case "down":
		if m.cursor < len(m.clusterNames)-1 {
			m.cursor++
		}
		m.ensureVisible()
		return m, nil
	case "pgup":
		h := m.clustersListHeight()
		if h <= 0 {
			h = 10
		}
		m.cursor -= h
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.ensureVisible()
		return m, nil
	case "pgdown":
		h := m.clustersListHeight()
		if h <= 0 {
			h = 10
		}
		m.cursor += h
		if m.cursor > len(m.clusterNames)-1 {
			m.cursor = len(m.clusterNames) - 1
		}
		m.ensureVisible()
		return m, nil
	case " ":
		if len(m.clusterNames) == 0 {
			return m, nil
		}
		c := m.clusterNames[m.cursor]
		m.clSelected[c] = !m.clSelected[c]
		return m, nil
	case "a":
		all := true
		for _, c := range m.clusterNames {
			if !m.clSelected[c] {
				all = false
				break
			}
		}
		for _, c := range m.clusterNames {
			m.clSelected[c] = !all
		}
		return m, nil
	case "enter":
		clusters := m.selectedClusters()
		if len(clusters) == 0 {
			return m, nil
		}

		// Step 3: resources (required per app) based on selected clusters.
		apps := m.selectedApps()
		if len(apps) == 0 {
			return m, nil
		}
		m.resApps = apps
		m.resAppIdx = 0
		m.resList = resourcesForAppInClusters(m.inv, apps[0], clusters)
		m.resSelectedBy = map[models.AppKey]map[models.SyncResource]bool{}
		m.cursor = 0
		m.resOffset = 0
		m.step = stepSelectResources
		return m, nil
	default:
		return m, nil
	}
}

func (m *model) onKeySelectAction(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	actions := []models.Action{models.ActionSync, models.ActionRefresh, models.ActionHardRefresh}
	switch msg.String() {
	case "q", "esc":
		return m, tea.Quit
	case "backspace":
		m.cursor = 0
		m.step = stepSelectResources
		return m, nil
	case "up":
		if m.actionIdx > 0 {
			m.actionIdx--
		}
		return m, nil
	case "down":
		if m.actionIdx < len(actions)-1 {
			m.actionIdx++
		}
		return m, nil
	case "r":
		// pre-refresh is only applicable to Sync
		if m.actionIdx == 0 {
			m.runOpts.PreRefresh = !m.runOpts.PreRefresh
			if m.runOpts.PreRefresh {
				m.runOpts.PreHardRefresh = false
			}
		}
		return m, nil
	case "h":
		// pre-hard-refresh is only applicable to Sync
		if m.actionIdx == 0 {
			m.runOpts.PreHardRefresh = !m.runOpts.PreHardRefresh
			if m.runOpts.PreHardRefresh {
				m.runOpts.PreRefresh = false
			}
		}
		return m, nil
	case "p":
		// prune is only applicable to Sync
		if m.actionIdx == 0 {
			m.runOpts.Prune = !m.runOpts.Prune
		}
		return m, nil
	case "d":
		// dry-run is only applicable to Sync
		if m.actionIdx == 0 {
			m.runOpts.DryRun = !m.runOpts.DryRun
		}
		return m, nil
	case "o":
		// apply-only is only applicable to Sync
		if m.actionIdx == 0 {
			m.runOpts.ApplyOnly = !m.runOpts.ApplyOnly
		}
		return m, nil
	case "f":
		// force is only applicable to Sync
		if m.actionIdx == 0 {
			m.runOpts.Force = !m.runOpts.Force
		}
		return m, nil
	case "enter":
		m.action = actions[m.actionIdx]
		// flags require confirmation
		if m.action == models.ActionSync && (m.runOpts.Prune || m.runOpts.Force) {
			m.step = stepConfirm
			return m, nil
		}
		return m.startRun()
	default:
		return m, nil
	}
}

func (m *model) onKeyConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch strings.ToLower(msg.String()) {
	case "y":
		return m.startRun()
	case "n", "esc", "q":
		m.step = stepSelectAction
		return m, nil
	default:
		return m, nil
	}
}

func (m *model) onKeyDiffSelectCluster(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		m.step = stepSelectApps
		return m, nil
	case "backspace":
		m.step = stepSelectApps
		return m, nil
	case "up":
		if m.diffClCursor > 0 {
			m.diffClCursor--
		}
		m.diffEnsureVisible()
		return m, nil
	case "down":
		if m.diffClCursor < len(m.diffClusters)-1 {
			m.diffClCursor++
		}
		m.diffEnsureVisible()
		return m, nil
	case "pgup":
		h := 10
		m.diffClCursor -= h
		if m.diffClCursor < 0 {
			m.diffClCursor = 0
		}
		m.diffEnsureVisible()
		return m, nil
	case "pgdown":
		h := 10
		m.diffClCursor += h
		if m.diffClCursor > len(m.diffClusters)-1 {
			m.diffClCursor = len(m.diffClusters) - 1
		}
		m.diffEnsureVisible()
		return m, nil
	case "enter":
		if len(m.diffClusters) == 0 || m.diffClCursor < 0 || m.diffClCursor >= len(m.diffClusters) {
			return m, nil
		}
		cluster := m.diffClusters[m.diffClCursor]
		return m, m.loadDiffCmd(m.diffApp, cluster)
	default:
		return m, nil
	}
}

func (m *model) onKeyDiffResources(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ecs":
		m.step = stepSelectApps
		return m, nil
	case "backspace":
		m.step = stepDiffSelectCluster
		return m, nil
	case "up":
		if m.diffResCursor > 0 {
			m.diffResCursor--
		}
		m.diffResEnsureVisible()
		return m, nil
	case "down":
		if m.diffResCursor < len(m.diffItems)-1 {
			m.diffResCursor++
		}
		m.diffResEnsureVisible()
		return m, nil
	case "pgup":
		h := 10
		m.diffResCursor -= h
		if m.diffResCursor < 0 {
			m.diffResCursor = 0
		}
		m.diffResEnsureVisible()
		return m, nil
	case "pgdown":
		h := 10
		m.diffResCursor += h
		if m.diffResCursor > len(m.diffItems)-1 {
			m.diffResCursor = len(m.diffItems) - 1
		}
		m.diffResEnsureVisible()
		return m, nil
	case "enter":
		if len(m.diffItems) == 0 || m.diffResCursor < 0 || m.diffResCursor >= len(m.diffItems) {
			return m, nil
		}
		it := m.diffItems[m.diffResCursor]
		txt := it.Diff
		if strings.TrimSpace(txt) == "" {
			// fallback
			if strings.TrimSpace(it.PredictedLiveState) != "" || strings.TrimSpace(it.NormalizedLiveState) != "" {
				txt = "PredictedLiveState:\n" + it.PredictedLiveState + "\n\nNormalizedLiveState\n" + it.NormalizedLiveState
			} else {
				txt = "(no diff payload returned by argocd)"
			}
		}
		m.diffDetailText = strings.Split(txt, "\n")
		m.diffDetailOff = 0
		m.step = stepDiffDetail
		return m, nil
	default:
		return m, nil
	}
}

func (m *model) onKeyDiffDetail(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		m.step = stepDiffResources
		return m, nil
	case "backspace":
		m.step = stepDiffResources
		return m, nil
	case "up":
		if m.diffDetailOff > 0 {
			m.diffDetailOff--
		}
		return m, nil
	case "down":
		if m.diffDetailOff < max(0, len(m.diffDetailText)-1) {
			m.diffDetailOff++
		}
		return m, nil
	case "pgup":
		m.diffDetailOff -= 10
		if m.diffDetailOff < 0 {
			m.diffDetailOff = 0
		}
		return m, nil
	case "pgdown":
		m.diffDetailOff += 10
		if m.diffDetailOff > max(0, len(m.diffDetailText)-1) {
			m.diffDetailOff = max(0, len(m.diffDetailText)-1)
		}
		return m, nil
	default:
		return m, nil
	}
}

func (m *model) startRun() (tea.Model, tea.Cmd) {
	m.step = stepRunning
	m.statuses = map[models.Target]models.TaskStatus{}
	m.messages = map[models.Target]string{}
	m.errors = map[models.Target]error{}
	m.beforeState = map[models.Target]models.Application{}
	m.afterState = map[models.Target]models.Application{}
	m.results = nil
	m.eventsCh = make(chan models.ProgressEvent, 256)
	m.doneCh = make(chan runDoneMsg, 1)

	action := m.action
	opts := m.runOpts

	apps := m.selectedApps()
	clusters := m.selectedClusters()
	resourcesByApp := m.selectedResourcesByApp()

	m.runTargets = services.TargetsForSelection(m.inv, apps, clusters)
	m.runStartedAt = time.Now()
	m.runFinishedAt = time.Time{}

	for _, t := range m.runTargets {
		m.statuses[t] = models.TaskPending
		if a, ok := m.inv[t.App][t.ClusterContext]; ok {
			m.beforeState[t] = a
		}
	}

	m.logger.Debug("starting bulk run", slog.String("action", string(action)), slog.Int("selected_apps", len(apps)), slog.Int("selected_clusters", len(clusters)), slog.Bool("wait", opts.Wait), slog.Bool("wait_healthy", opts.WaitHealthy), slog.Duration("wait_timeout", opts.WaitTimeout), slog.Duration("poll_interval", opts.PollInterval), slog.Bool("prune", opts.Prune), slog.Bool("dry_run", opts.DryRun), slog.Bool("apply_only", opts.ApplyOnly), slog.Bool("force", opts.Force), slog.Int("parallel", m.cli.Parallel))

	go func() {
		start := time.Now()
		results, err := m.bulk.Run(m.rootCtx, m.inv, m.clusterBy, apps, clusters, resourcesByApp, action, opts, m.eventsCh)
		close(m.eventsCh)
		if err != nil {
			m.logger.Error("bulk run failed", slog.Any("err", err))
		}
		success := 0
		failed := 0
		for _, r := range results {
			switch r.Status {
			case models.TaskSuccess:
				success++
			case models.TaskFailed:
				failed++
			}
		}
		m.logger.Debug("bulk run finished", slog.Duration("took", time.Since(start)), slog.Int("results", len(results)), slog.Int("success", success), slog.Int("failed", failed), slog.Any("err", err))
		m.doneCh <- runDoneMsg{results: results, err: err}
		close(m.doneCh)
	}()

	return m, tea.Batch(
		waitForEvent(m.eventsCh),
		waitForDone(m.doneCh),
	)
}

func waitForEvent(ch <-chan models.ProgressEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return eventsClosedMsg{}
		}
		return progressMsg(ev)
	}
}

func waitForDone(ch <-chan runDoneMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return runDoneMsg{}
		}
		return msg
	}
}

func (m *model) View() string {
	theme := styles()
	switch m.step {
	case stepLoading:
		return theme.header.Render("argo-sync") + "\n\n" + "Loading Argo CD contexts & applications…\n\n" + theme.hint.Render("Ctrl+C to exit")
	case stepError:
		return theme.header.Render("argo-sync") + "\n\n" + theme.error.Render(fmt.Sprintf("Error: %v", m.err)) + "\n\n" + theme.hint.Render("Enter/Esc to exit")
	case stepSelectApps:
		return m.viewSelectApps(theme)
	case stepDiffSelectCluster:
		return m.viewDiffSelectCluster(theme)
	case stepDiffResources:
		return m.viewDiffResources(theme)
	case stepDiffDetail:
		return m.viewDiffDetail(theme)
	case stepSelectClusters:
		return m.viewSelectClusters(theme)
	case stepSelectResources:
		return m.viewSelectResources(theme)
	case stepSelectAction:
		return m.viewSelectAction(theme)
	case stepConfirm:
		return m.viewConfirm(theme)
	case stepRunning:
		return m.viewRunning(theme)
	case stepDone:
		return m.viewDone(theme)
	case stepDiffLoading:
		return theme.header.Render("Diff") + "\n\n" + "Loading diff...\n\n" + theme.hint.Render("Esc to cancel")
	default:
		return "unknown state"
	}
}

type uiStyles struct {
	header lipgloss.Style
	hint   lipgloss.Style
	error  lipgloss.Style
	ok     lipgloss.Style
	warn   lipgloss.Style
	dim    lipgloss.Style
}

func styles() uiStyles {
	// https://github.com/charmbracelet/lipgloss?tab=readme-ov-file#borders
	return uiStyles{
		header: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")),
		hint:   lipgloss.NewStyle().Foreground(lipgloss.Color("241")),
		error:  lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true),
		ok:     lipgloss.NewStyle().Foreground(lipgloss.Color("42")),
		warn:   lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
		dim:    lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
	}
}

func (m *model) selectedApps() []models.AppKey {
	out := make([]models.AppKey, 0, len(m.appSelected))
	for _, k := range m.appKeysAll {
		if m.appSelected[k] {
			out = append(out, k)
		}
	}
	return out
}

func (m *model) selectedResourcesFor(app models.AppKey) []models.SyncResource {
	mm := m.resSelectedBy[app]
	if len(mm) == 0 {
		return nil
	}

	// keep stable ordering: based on current resource list for this app.
	list := resourcesForAppInClusters(m.inv, app, m.selectedClusters())
	out := make([]models.SyncResource, 0, len(mm))
	for _, r := range list {
		if mm[r] {
			out = append(out, r)
		}
	}
	return out
}

func (m *model) selectedResourcesByApp() map[models.AppKey][]models.SyncResource {
	out := map[models.AppKey][]models.SyncResource{}
	for _, app := range m.selectedApps() {
		out[app] = m.selectedResourcesFor(app)
	}
	return out
}

func (m *model) selectedClusters() []string {
	out := make([]string, 0, len(m.clSelected))
	for _, c := range m.clusterNames {
		if m.clSelected[c] {
			out = append(out, c)
		}
	}
	return out
}

func (m *model) viewSelectApps(s uiStyles) string {
	var b strings.Builder
	b.WriteString(fitLine(m.width, s.header.Render("Step 1/4: Select Applications")))
	b.WriteString("\n")
	b.WriteString(fitLine(m.width, s.hint.Render("↑/↓ move | PgUp/PgDn jump | Space toggle | a all/none | d diff | / or Ctrl+F filter | R refresh | Enter next | q quit")))
	b.WriteString("\n")
	if m.filtering || m.filter.Value() != "" {
		if m.filtering {
			b.WriteString(fitLine(m.width, m.filter.View()))
		} else {
			b.WriteString(fitLine(m.width, s.dim.Render(fmt.Sprintf("Filter: %s (press / or Ctrl+F to edit, Esc to clear)", m.filter.Value()))))
		}
	}
	b.WriteString("\n\n")

	if m.refreshing {
		b.WriteString(fitLine(m.width, s.header.Render("Refreshing application statuses...")))
		b.WriteString("\n\n")
	}

	if len(m.loadErrs) > 0 {
		// keep it compact: show up to 3 errors + summary
		type kv struct {
			k string
			v error
		}
		items := make([]kv, 0, len(m.loadErrs))
		for k, v := range m.loadErrs {
			items = append(items, kv{k: k, v: v})
		}
		sort.Slice(items, func(i, j int) bool { return items[i].k < items[j].k })
		show := items
		if len(show) > 3 {
			show = show[:3]
		}

		b.WriteString(s.warn.Render("Some clusters failed to list applications:"))
		b.WriteString("\n")
		for _, it := range show {
			b.WriteString(s.dim.Render(fmt.Sprintf("- %s: %s", it.k, truncate(it.v.Error(), 120))))
			b.WriteString("\n")
		}
		if len(items) > len(show) {
			b.WriteString(s.dim.Render(fmt.Sprintf("…and %d more", len(items)-len(show))))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(m.appKeys) == 0 {
		b.WriteString(s.dim.Render("applications not found"))
		return b.String()
	}

	start, end := m.appsVisibleRange()
	for i := start; i < end; i++ {
		k := m.appKeys[i]
		cursor := " "
		if i == m.cursor {
			cursor = ">"
		}
		check := "[ ]"
		if m.appSelected[k] {
			check = "[x]"
		}
		clusters := make([]string, 0, len(m.inv[k]))
		for c := range m.inv[k] {
			clusters = append(clusters, c)
		}
		sort.Strings(clusters)

		// show up to 3 clusters as "ctx:healths" and "+N"
		lineClusters := clusters
		if len(lineClusters) > 3 {
			lineClusters = lineClusters[:3]
		}
		var parts []string
		for _, c := range lineClusters {
			app := m.inv[k][c]
			parts = append(parts, fmt.Sprintf("%s:%s", c, renderAppStateShort(s, app)))
		}
		if len(clusters) > len(lineClusters) {
			parts = append(parts, s.dim.Render(fmt.Sprintf("+%d", len(clusters)-len(lineClusters))))
		}
		b.WriteString(fmt.Sprintf("%s %s %-40s %s\n", cursor, check, k.Name, strings.Join(parts, " ")))
	}
	b.WriteString(s.dim.Render(fmt.Sprintf("Showing %d–%d of %d", start+1, end, len(m.appKeys))))
	b.WriteString("\n")

	if m.cursor >= 0 && m.cursor < len(m.appKeys) {
		k := m.appKeys[m.cursor]
		b.WriteString("\n")
		b.WriteString(s.dim.Render("Details: "))
		b.WriteString(k.Name)
		b.WriteString("\n")

		clusters := make([]string, 0, len(m.inv[k]))
		for c := range m.inv[k] {
			clusters = append(clusters, c)
		}
		sort.Strings(clusters)
		for _, c := range clusters {
			a := m.inv[k][c]
			b.WriteString(fmt.Sprintf("  %-24s %s  %s\n",
				c,
				renderAppHealth(s, a.HealthStatus),
				renderAppSync(s, a.SyncStatus),
			))
		}
	}
	return b.String()
}

func (m *model) viewSelectResources(s uiStyles) string {
	var b strings.Builder
	app := models.AppKey{}
	if m.resAppIdx >= 0 && m.resAppIdx < len(m.resApps) {
		app = m.resApps[m.resAppIdx]
	}
	title := "Step 3/4: Select Resources"
	if app.Name != "" {
		title = fmt.Sprintf("Step 3/4: Select Resources for %s (%d/%d)", app.Name, m.resAppIdx+1, len(m.resApps))
	}
	b.WriteString(fitLine(m.width, s.header.Render(title)))
	b.WriteString("\n")
	b.WriteString(fitLine(m.width, s.hint.Render("↑/↓ move | PgUp/PgDn jump | Space toggle | a all/none | Backspace back | Enter next app | q quit")))
	b.WriteString("\n")

	selected := 0
	if app.Name != "" {
		selected = len(m.selectedResourcesFor(app))
	}
	b.WriteString(fitLine(m.width, s.dim.Render(fmt.Sprintf("selected: %d (required)", selected))))
	b.WriteString("\n\n")

	if len(m.resList) == 0 {
		b.WriteString(s.dim.Render("resources not available"))
		return b.String()
	}

	start, end := m.resourcesVisibleRange()
	for i := start; i < end; i++ {
		r := m.resList[i]
		cursor := " "
		if i == m.cursor {
			cursor = ">"
		}
		check := "[ ]"
		if app.Name != "" && m.resSelectedBy[app] != nil && m.resSelectedBy[app][r] {
			check = "[x]"
		}
		b.WriteString(fmt.Sprintf("%s %s %s\n", cursor, check, formatSyncResource(r)))
	}
	b.WriteString(s.dim.Render(fmt.Sprintf("Showing %d–%d of %d", start+1, end, len(m.resList))))
	return b.String()
}

func (m *model) viewSelectClusters(s uiStyles) string {
	var b strings.Builder
	b.WriteString(fitLine(m.width, s.header.Render("Step 2/4: Select Clusters")))
	b.WriteString("\n")
	b.WriteString(fitLine(m.width, s.hint.Render("↑/↓ move | PgUp/PgDn jump | Space toggle | a all/none | Backspace back | Enter next | q quit")))
	b.WriteString("\n\n")

	start, end := m.clustersVisibleRange()
	for i := start; i < end; i++ {
		c := m.clusterNames[i]
		cursor := " "
		if i == m.cursor {
			cursor = ">"
		}
		check := "[ ]"
		if m.clSelected[c] {
			check = "[x]"
		}
		verLabel := "version=unknown"
		if cl, ok := m.clusterBy[c]; ok {
			if v := strings.TrimSpace(cl.ServerVersion); v != "" {
				verLabel = "version=" + v
			} else if cl.ServerMajor > 0 {
				verLabel = fmt.Sprintf("version=v%d.x", cl.ServerMajor)
			}
		}
		b.WriteString(fmt.Sprintf("%s %s %-28s %s\n", cursor, check, c, s.dim.Render(verLabel)))
	}
	if len(m.clusterNames) > 0 {
		b.WriteString(s.dim.Render(fmt.Sprintf("Showing %d–%d of %d", start+1, end, len(m.clusterNames))))
		b.WriteString("\n")
	}
	return b.String()
}

func (m *model) viewSelectAction(s uiStyles) string {
	actions := []models.Action{models.ActionSync, models.ActionRefresh, models.ActionHardRefresh}
	labels := map[models.Action]string{
		models.ActionSync:        "sync",
		models.ActionRefresh:     "refresh",
		models.ActionHardRefresh: "hard refresh",
	}

	var b strings.Builder
	b.WriteString(fitLine(m.width, s.header.Render("Step 4/4: Operation")))
	b.WriteString("\n")
	b.WriteString(fitLine(m.width, s.hint.Render("↑/↓ move | Enter run | Backspace back | q quit")))
	b.WriteString("\n\n")

	for i, a := range actions {
		cursor := " "
		if i == m.actionIdx {
			cursor = ">"
		}
		b.WriteString(fmt.Sprintf("%s  %s\n", cursor, labels[a]))
	}

	b.WriteString("\n")
	b.WriteString(s.dim.Render("Options:"))
	b.WriteString("\n")
	if m.actionIdx == 0 {
		b.WriteString(fmt.Sprintf("  [%s] pre-refresh before sync (r)", onOff(m.runOpts.PreRefresh)))
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("  [%s] pre-hard-refresh before sync (h)", onOff(m.runOpts.PreHardRefresh)))
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("  [%s] prune for sync (p)", onOff(m.runOpts.Prune)))
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("  [%s] dry-run (no changes) (d)", onOff(m.runOpts.DryRun)))
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("  [%s] apply-only strategy (o)", onOff(m.runOpts.ApplyOnly)))
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("  [%s] force apply (f)", onOff(m.runOpts.Force)))
		b.WriteString("\n")
	} else {
		b.WriteString(s.dim.Render(fmt.Sprintf("  [%s] pre-refresh before sync (r)", onOff(m.runOpts.PreRefresh))))
		b.WriteString("\n")
		b.WriteString(s.dim.Render(fmt.Sprintf("  [%s] pre-hard-refresh before sync (h)", onOff(m.runOpts.PreHardRefresh))))
		b.WriteString("\n")
		b.WriteString(s.dim.Render(fmt.Sprintf("  [%s] prune for sync (p)", onOff(m.runOpts.Prune))))
		b.WriteString("\n")
		b.WriteString(s.dim.Render(fmt.Sprintf("  [%s] dry-run (no changes) (d)", onOff(m.runOpts.DryRun))))
		b.WriteString("\n")
		b.WriteString(s.dim.Render(fmt.Sprintf("  [%s] apply-only strategy (o)", onOff(m.runOpts.ApplyOnly))))
		b.WriteString("\n")
		b.WriteString(s.dim.Render(fmt.Sprintf("  [%s] force apply (f)", onOff(m.runOpts.Force))))
		b.WriteString("\n")
	}
	return b.String()
}

func (m *model) viewConfirm(s uiStyles) string {
	var b strings.Builder
	b.WriteString(s.header.Render("Confirm operation"))
	b.WriteString("\n\n")
	if m.runOpts.Prune {
		b.WriteString(s.warn.Render("Prune is enabled. This may delete resources managed by Argo CD."))
		b.WriteString("\n")
	}
	if m.runOpts.Force {
		b.WriteString(s.warn.Render("Force is enabled. This may replace fields or recreate resources."))
		b.WriteString("\n")
	}
	if !m.runOpts.Prune && !m.runOpts.Force {
		b.WriteString(s.warn.Render("Proceed with the selected sync options?"))
		b.WriteString("\n")
	}
	b.WriteString("\n\n")
	b.WriteString(s.hint.Render("Press y to confirm, n to go back"))
	return b.String()
}

func (m *model) viewRunning(s uiStyles) string {
	var b strings.Builder
	b.WriteString(s.header.Render("Running…"))
	b.WriteString("\n")
	b.WriteString(s.hint.Render("q/esc cancels. Ctrl+C quits immediately."))
	b.WriteString("\n\n")

	total := 0
	success := 0
	failed := 0
	cancelled := 0

	for _, t := range m.runTargets {
		total++
		st := m.statuses[t]
		switch st {
		case models.TaskSuccess:
			success++
		case models.TaskFailed:
			failed++
		case models.TaskCancelled:
			cancelled++
		}

		b.WriteString(fmt.Sprintf("%-24s %-40s %s", t.ClusterContext, t.App.Name, renderStatus(s, st)))

		if msg := m.messages[t]; msg != "" {
			b.WriteString("\n")
			b.WriteString(s.dim.Render("  ↳ " + truncate(msg, 120)))
		}

		if err := m.errors[t]; err != nil {
			b.WriteString("\n")
			b.WriteString(s.error.Render("  ↳ " + truncate(err.Error(), 120)))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(s.dim.Render(fmt.Sprintf("Total: %d  Success: %d  Failed: %d  Cancelled: %d  Updated: %s", total, success, failed, cancelled, time.Now().Format("15:04:05"))))
	return b.String()
}

func (m *model) viewDone(s uiStyles) string {
	var b strings.Builder
	b.WriteString(fitLine(m.width, s.header.Render("Done")))
	b.WriteString("\n\n")

	total := len(m.runTargets)
	success := 0
	failed := 0
	cancelled := 0
	pending := 0
	for _, t := range m.runTargets {
		switch m.statuses[t] {
		case models.TaskSuccess:
			success++
		case models.TaskFailed:
			failed++
		case models.TaskCancelled:
			cancelled++
		default:
			pending++
		}
	}

	if m.action != "" {
		b.WriteString(s.dim.Render(fmt.Sprintf("action: %s", m.action)))
		b.WriteString("\n")
	}
	if !m.runStartedAt.IsZero() {
		finish := m.runFinishedAt
		if finish.IsZero() {
			finish = time.Now()
		}
		b.WriteString(s.dim.Render(fmt.Sprintf("duration: %s", finish.Sub(m.runStartedAt).Round(time.Millisecond))))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	b.WriteString(fmt.Sprintf("%s %d\n", s.dim.Render("total:"), total))
	b.WriteString(fmt.Sprintf("%s %d\n", s.ok.Render("success:"), success))
	b.WriteString(fmt.Sprintf("%s %d\n", s.error.Render("failed:"), failed))
	b.WriteString(fmt.Sprintf("%s %d\n", s.warn.Render("cancelled:"), cancelled))
	b.WriteString(fmt.Sprintf("%s %d\n", s.dim.Render("pending:"), pending))

	changed := 0
	for _, t := range m.runTargets {
		after, ok := m.afterState[t]
		if !ok {
			continue
		}
		before := m.beforeState[t]
		if strings.TrimSpace(before.SyncStatus) != strings.TrimSpace(after.SyncStatus) || strings.TrimSpace(before.HealthStatus) != strings.TrimSpace(after.HealthStatus) {
			changed++
		}
	}

	if changed > 0 {
		b.WriteString("\n")
		b.WriteString(s.dim.Render(fmt.Sprintf("Status changes... (from wait loop): %d", changed)))
		b.WriteString("\n")
		shown := 0
		for _, t := range m.runTargets {
			if shown >= 10 {
				break
			}
			after, ok := m.afterState[t]
			if !ok {
				continue
			}
			before := m.beforeState[t]
			if strings.TrimSpace(before.SyncStatus) == strings.TrimSpace(after.SyncStatus) && strings.TrimSpace(before.HealthStatus) == strings.TrimSpace(after.HealthStatus) {
				continue
			}

			b.WriteString(s.dim.Render(fmt.Sprintf("- %-24s %-40s %s/%s -> %s/%s", t.ClusterContext, t.App.Name, shortHealth(before.HealthStatus), shortSync(before.SyncStatus), shortHealth(after.HealthStatus), shortSync(after.SyncStatus))))
			b.WriteString("\n")
			shown++
		}
		if changed > shown {
			b.WriteString(s.dim.Render(fmt.Sprintf("..and %d more", changed-shown)))
			b.WriteString("\n")
		}
	}

	if failed > 0 {
		b.WriteString("\n")
		b.WriteString(s.error.Render("Failures:"))
		b.WriteString("\n")
		shown := 0
		for _, t := range m.runTargets {
			if shown >= 5 {
				break
			}
			if m.statuses[t] != models.TaskFailed {
				continue
			}
			err := m.errors[t]
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			if msg == "" {
				msg = "unknown error"
			}
			b.WriteString(s.dim.Render(fmt.Sprintf("- %-24s %-40s %s", t.ClusterContext, t.App.Name, truncate(msg, 120))))
			b.WriteString("\n")
			shown++
		}
		if failed > shown {
			b.WriteString(s.dim.Render(fmt.Sprintf("…and %d more", failed-shown)))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(fitLine(m.width, s.hint.Render("r restart  Enter/q to exit")))

	return b.String()
}

func onOff(v bool) string {
	if v {
		return "x"
	}
	return " "
}

func renderStatus(s uiStyles, st models.TaskStatus) string {
	switch st {
	case models.TaskRunning:
		return s.warn.Render("running")
	case models.TaskSuccess:
		return s.ok.Render("success")
	case models.TaskFailed:
		return s.error.Render("failed")
	case models.TaskCancelled:
		return s.dim.Render("cancelled")
	default:
		return s.dim.Render("pending")
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func renderAppStateShort(s uiStyles, a models.Application) string {
	h := shortHealth(a.HealthStatus)
	sy := shortSync(a.SyncStatus)

	if isHealthRed(a.HealthStatus) {
		return s.error.Render(h + sy)
	}
	if isHealthYellow(a.HealthStatus) || isSyncYellow(a.SyncStatus) {
		return s.warn.Render(h + sy)
	}
	if isHealthGreen(a.HealthStatus) && isSyncGreen(a.SyncStatus) {
		return s.ok.Render(h + sy)
	}
	return s.dim.Render(h + sy)
}

func renderAppHealth(s uiStyles, health string) string {
	label := "health=" + norm(health)
	switch {
	case isHealthRed(health):
		return s.error.Render(label)
	case isHealthYellow(health):
		return s.warn.Render(label)
	case isHealthGreen(health):
		return s.ok.Render(label)
	default:
		return s.dim.Render(label)
	}
}

func renderAppSync(s uiStyles, sync string) string {
	label := "sync=" + norm(sync)
	switch {
	case isSyncGreen(sync):
		return s.ok.Render(label)
	case isSyncYellow(sync):
		return s.warn.Render(label)
	default:
		return s.dim.Render(label)
	}
}

func norm(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "Unknown"
	}
	return v
}

// https://github.com/argoproj/argo-cd/blob/master/gitops-engine/pkg/health/health.go#L14
func shortHealth(v string) string {
	switch strings.TrimSpace(v) {
	case "Healthy":
		return "H"
	case "Degraded":
		return "D"
	case "Progressing":
		return "P"
	case "Suspended":
		return "S"
	case "Missing":
		return "M"
	case "Unknown":
		return "U"
	default:
		return "?"
	}
}

func shortSync(v string) string {
	switch strings.TrimSpace(v) {
	case "Synced":
		return "S"
	case "OutOfSync":
		return "O"
	default:
		return "?"
	}
}

func isHealthGreen(v string) bool {
	return strings.TrimSpace(v) == "Healthy"
}

func isHealthYellow(v string) bool {
	switch strings.TrimSpace(v) {
	case "Progressing", "Suspended":
		return true
	default:
		return false
	}
}

func isHealthRed(v string) bool {
	switch strings.TrimSpace(v) {
	case "Degraded", "Missing":
		return true
	default:
		return false
	}
}

func isSyncGreen(v string) bool {
	return strings.TrimSpace(v) == "Synced"
}

func isSyncYellow(v string) bool {
	return strings.TrimSpace(v) == "OutOfSync"
}

func (m *model) resetToStart() {
	m.step = stepSelectApps
	m.cursor = 0
	m.appOffset = 0
	m.resOffset = 0
	m.clOffset = 0

	m.appSelected = map[models.AppKey]bool{}
	m.resSelectedBy = map[models.AppKey]map[models.SyncResource]bool{}
	m.clSelected = map[string]bool{}
	m.clusterNames = nil
	m.resApps = nil
	m.resAppIdx = 0
	m.resList = nil

	m.actionIdx = 0
	m.action = models.ActionSync
	m.runOpts = models.RunOptions{
		Wait:         !m.cli.NoWait,
		WaitHealthy:  !m.cli.NoWait,
		WaitTimeout:  m.cli.WaitTimeout,
		PollInterval: m.cli.PollInterval,
	}

	m.statuses = map[models.Target]models.TaskStatus{}
	m.errors = map[models.Target]error{}
	m.results = nil
}

func (m *model) ensureVisible() {
	switch m.step {
	case stepSelectApps:
		h := m.appsListHeight()
		m.appOffset = ensureOffset(m.appOffset, m.cursor, h, len(m.appKeys))
	case stepSelectResources:
		h := m.resourcesListHeight()
		m.resOffset = ensureOffset(m.resOffset, m.cursor, h, len(m.resList))
	case stepSelectClusters:
		h := m.clustersListHeight()
		m.clOffset = ensureOffset(m.clOffset, m.cursor, h, len(m.clusterNames))
	default:
		// nothing
	}
}

func (m *model) appsVisibleRange() (start, end int) {
	h := m.appsListHeight()
	if h <= 0 {
		h = 20
	}
	start = clamp(m.appOffset, 0, max(0, len(m.appKeys)-1))
	end = min(len(m.appKeys), start+h)
	return start, end
}

func (m *model) clustersVisibleRange() (start, end int) {
	h := m.clustersListHeight()
	if h <= 0 {
		h = 20
	}
	start = clamp(m.clOffset, 0, max(0, len(m.clusterNames)-1))
	end = min(len(m.clusterNames), start+h)
	return start, end
}

func (m *model) resourcesVisibleRange() (start, end int) {
	h := m.resourcesListHeight()
	if h <= 0 {
		h = 20
	}
	start = clamp(m.resOffset, 0, max(0, len(m.resList)-1))
	end = min(len(m.resList), start+h)
	return start, end
}

func (m *model) appsListHeight() int {
	if m.height <= 0 {
		return 20
	}
	header := 4
	if m.filtering || m.filter.Value() != "" {
		header += 1
	}
	warnings := m.warningBlockHeight()
	details := m.detailsBlockHeight()
	footer := 1
	available := m.height - header - warnings - details - footer - 1
	return max(5, available)
}

func (m *model) applyAppFilter() {
	q := strings.TrimSpace(m.filter.Value())
	if q == "" {
		m.appKeys = m.appKeysAll
		m.cursor = clamp(m.cursor, 0, max(0, len(m.appKeys)-1))
		m.ensureVisible()
		return
	}
	q = strings.ToLower(q)
	out := make([]models.AppKey, 0, len(m.appKeysAll))
	for _, k := range m.appKeysAll {
		if strings.Contains(strings.ToLower(k.Name), q) {
			out = append(out, k)
		}
	}
	m.appKeys = out
	m.cursor = 0
	m.appOffset = 0
	m.ensureVisible()
}

func (m *model) resourcesListHeight() int {
	if m.height <= 0 {
		return 20
	}
	header := 5
	footer := 1
	available := m.height - header - footer - 1
	return max(5, available)
}

func (m *model) clustersListHeight() int {
	if m.height <= 0 {
		return 20
	}
	header := 3
	footer := 1
	available := m.height - header - footer - 1
	return max(5, available)
}

func resourcesForAppInClusters(inv models.Inventory, appKey models.AppKey, clusters []string) []models.SyncResource {
	set := map[models.SyncResource]struct{}{}
	perCluster, ok := inv[appKey]
	if ok {
		allowed := map[string]struct{}{}
		for _, c := range clusters {
			allowed[c] = struct{}{}
		}
		for ctx, app := range perCluster {
			if len(allowed) > 0 {
				if _, ok := allowed[ctx]; !ok {
					continue
				}
			}
			for _, r := range app.Resources {
				if strings.TrimSpace(r.Kind) == "" || strings.TrimSpace(r.Name) == "" {
					continue
				}
				set[r] = struct{}{}
			}
		}
	}

	out := make([]models.SyncResource, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return out
}

func formatSyncResource(r models.SyncResource) string {
	g := strings.TrimSpace(r.Group)
	if g == "" {
		g = "core"
	}
	ns := strings.TrimSpace(r.Namespace)
	if ns == "" {
		ns = "-"
	}
	return fmt.Sprintf("%s/%s %s/%s", g, r.Kind, ns, r.Name)
}

func (m *model) warningBlockHeight() int {
	if len(m.loadErrs) == 0 {
		return 0
	}
	show := min(3, len(m.loadErrs))
	extra := 0
	if len(m.loadErrs) > show {
		extra = 1
	}
	return 1 + show + extra + 1
}

func (m *model) detailsBlockHeight() int {
	if m.cursor < 0 || m.cursor >= len(m.appKeys) {
		return 0
	}
	k := m.appKeys[m.cursor]
	n := len(m.inv[k])
	if n == 0 {
		return 0
	}
	// blank + "Details: " + up to 8 cluster lines
	return 1 + 1 + min(n, 8)
}

func ensureOffset(offset, cursor, height, total int) int {
	if total <= 0 {
		return 0
	}
	if height <= 0 {
		height = 1
	}
	if cursor < offset {
		return cursor
	}
	if cursor >= offset+height {
		return cursor - height + 1
	}
	return offset
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func fitLine(width int, s string) string {
	if width <= 0 {
		return s
	}
	return lipgloss.NewStyle().Width(width).Render(s)
}

func (m *model) refreshCmd() tea.Cmd {
	return func() tea.Msg {
		m.logger.Debug("refreshing inventory", slog.String("config", m.cli.ConfigPath), slog.Any("contexts", m.cli.Contexts))
		clusters, err := argocd.LoadClustersFromFile(m.cli.ConfigPath)
		if err != nil {
			return refreshDoneMsg{err: err}
		}
		clusters = filterClusters(clusters, m.cli.Contexts)
		if len(clusters) == 0 {
			return refreshDoneMsg{err: fmt.Errorf("no contexts selected")}
		}
		// detection argocd version to enable version specific (server side diff)
		{
			g, vctx := errgroup.WithContext(m.rootCtx)
			g.SetLimit(10)
			for i := range clusters {
				i := i
				g.Go(func() error {
					ctx, cancel := context.WithTimeout(vctx, 5*time.Second)
					defer cancel()
					ver, err := argocd.DetectServerVersion(ctx, clusters[i])
					if err != nil {
						m.logger.Debug("version detection failed", slog.String("context", clusters[i].ContextName), slog.Any("err", err))
						return nil
					}
					clusters[i].ServerVersion = ver.Raw
					clusters[i].ServerMajor = ver.Major
					m.logger.Debug("server version detected", slog.String("context", clusters[i].ContextName), slog.String("version", ver.Raw), slog.Int("major", ver.Major))
					return nil
				})
			}
			_ = g.Wait()
		}
		start := time.Now()
		inv, errs, err := m.discover.DiscoverInventory(m.rootCtx, clusters)
		if err != nil {
			return refreshDoneMsg{err: err, errs: errs}
		}
		m.logger.Debug("refresh inventory done", slog.Duration("took", time.Since(start)), slog.Int("clusters", len(clusters)), slog.Int("apps", len(inv)), slog.Int("cluster_errors", len(errs)))
		return refreshDoneMsg{clusters: clusters, inv: inv, errs: errs}
	}
}

func filterClusters(clusters []models.Cluster, allow []string) []models.Cluster {
	if len(allow) == 0 {
		return clusters
	}
	allowed := map[string]struct{}{}
	for _, c := range allow {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		allowed[c] = struct{}{}
	}
	out := make([]models.Cluster, 0, len(clusters))
	for _, cl := range clusters {
		if _, ok := allowed[cl.ContextName]; ok {
			out = append(out, cl)
		}
	}
	return out
}

func indexOfAppKey(keys []models.AppKey, k models.AppKey) int {
	for i := range keys {
		if keys[i] == k {
			return i
		}
	}
	return -1
}

func parseSyncWaitMessage(msg string) (operation, sync, health string, ok bool) {
	// expected format for services/bulk.go:
	// "operation=<op> sync=<sync> health=<health>"

	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "", "", "", false
	}
	parts := strings.Fields(msg)
	if len(parts) < 3 {
		return "", "", "", false
	}

	var opOK, syncOK, healthOK bool
	for _, p := range parts {
		if strings.HasPrefix(p, "operation=") {
			operation = strings.TrimPrefix(p, "operation=")
			opOK = true
		} else if strings.HasPrefix(p, "sync=") {
			sync = strings.TrimPrefix(p, "sync=")
			syncOK = true
		} else if strings.HasPrefix(p, "health=") {
			health = strings.TrimPrefix(p, "health=")
			healthOK = true
		}
	}
	return operation, sync, health, opOK && syncOK && healthOK
}

func (m *model) loadDiffCmd(app models.AppKey, clusterCtx string) tea.Cmd {
	m.step = stepDiffLoading
	return func() tea.Msg {
		cl, ok := m.clusterBy[clusterCtx]
		if !ok {
			return diffLoadedMsg{app: app, cluster: clusterCtx, err: fmt.Errorf("unknown cluster context %q", clusterCtx)}
		}
		meta, ok := m.inv[app][clusterCtx]
		if !ok {
			return diffLoadedMsg{app: app, cluster: clusterCtx, err: fmt.Errorf("app %q not found in context %q", app.Name, clusterCtx)}
		}
		ctx, cancel := context.WithTimeout(m.rootCtx, 30*time.Second)
		defer cancel()

		items, err := m.api.ManagedResourceDiffs(ctx, cl, models.AppRef{Name: app.Name, Namespace: meta.Namespace}, meta.Project)
		return diffLoadedMsg{app: app, cluster: clusterCtx, items: items, err: err}
	}
}

func (m *model) viewDiffSelectCluster(s uiStyles) string {
	var b strings.Builder
	b.WriteString(fitLine(m.width, s.header.Render("Diff: Select Cluster")))
	b.WriteString("\n")
	b.WriteString(fitLine(m.width, s.hint.Render("↑/↓ move | Enter load | Backspace/Esc back")))
	b.WriteString("\n\n")
	b.WriteString(s.dim.Render(fmt.Sprintf("Application: %s", m.diffApp.Name)))
	b.WriteString("\n\n")

	start, end := m.diffClustersVisibleRange()
	for i := start; i < end; i++ {
		c := m.diffClusters[i]
		cursor := " "
		if i == m.diffClCursor {
			cursor = ">"
		}
		b.WriteString(fmt.Sprintf("%s %s\n", cursor, c))
	}
	if len(m.diffClusters) > 0 {
		b.WriteString(s.dim.Render(fmt.Sprintf("Showing %d-%d of %d", start+1, end, len(m.diffClusters))))
		b.WriteString("\n")
	}
	return b.String()
}

func (m *model) viewDiffResources(s uiStyles) string {
	var b strings.Builder
	b.WriteString(fitLine(m.width, s.header.Render("Diff: Resources")))
	b.WriteString("\n")
	b.WriteString(fitLine(m.width, s.hint.Render("↑/↓ move | Enter details | Backspace back | Esc apps")))
	b.WriteString("\n\n")

	if m.diffErr != nil {
		b.WriteString(s.error.Render("Error: " + m.diffErr.Error()))
		b.WriteString("\n\n")
	}

	if len(m.diffItems) == 0 {
		b.WriteString(s.dim.Render("no diff items returned"))
		b.WriteString("\n")
		return b.String()
	}

	start, end := m.diffResourcesVisibleRange()

	for i := start; i < end; i++ {
		it := m.diffItems[i]
		cursor := " "
		if i == m.diffResCursor {
			cursor = ">"
		}

		mod := " "
		if it.Modified {
			mod = "*"
		}

		ns := it.Namespace
		if strings.TrimSpace(ns) == "" {
			ns = "-"
		}

		b.WriteString(fmt.Sprintf("%s [%s] %s/%s %s/%s\n", cursor, mod, it.Group, it.Kind, ns, it.Name))
	}
	b.WriteString(s.dim.Render(fmt.Sprintf("Showing %d–%d of %d (* = modified)", start+1, end, len(m.diffItems))))
	b.WriteString("\n")
	return b.String()
}

func (m *model) viewDiffDetail(s uiStyles) string {
	var b strings.Builder
	b.WriteString(fitLine(m.width, s.header.Render("Diff: Details")))
	b.WriteString("\n")
	b.WriteString(fitLine(m.width, s.hint.Render("↑/↓ scroll | PgUp/PgDn | Backspace/Esc back")))
	b.WriteString("\n\n")

	if len(m.diffDetailText) == 0 {
		b.WriteString(s.dim.Render("(empty)"))
		return b.String()
	}

	h := m.height - 5

	if h <= 0 {
		h = 20
	}

	start := clamp(m.diffDetailOff, 0, max(0, len(m.diffDetailText)-1))
	end := min(len(m.diffDetailText), start+h)

	for i := start; i < end; i++ {
		b.WriteString(fitLine(m.width, m.diffDetailText[i]))
		b.WriteString("\n")
	}

	b.WriteString(s.dim.Render(fmt.Sprintf("Showing %d–%d of %d", start+1, end, len(m.diffDetailText))))
	return b.String()
}

func (m *model) diffEnsureVisible() {
	h := 10
	m.diffClOffset = ensureOffset(m.diffClOffset, m.diffClCursor, h, len(m.diffClusters))
}

func (m *model) diffResEnsureVisible() {
	h := 12
	m.diffResOffset = ensureOffset(m.diffResOffset, m.diffResCursor, h, len(m.diffItems))
}

func (m *model) diffClustersVisibleRange() (start, end int) {
	h := 12
	start = clamp(m.diffClOffset, 0, max(0, len(m.diffClusters)-1))
	end = min(len(m.diffClusters), start+h)
	return start, end
}

func (m *model) diffResourcesVisibleRange() (start, end int) {
	h := 12
	start = clamp(m.diffResOffset, 0, max(0, len(m.diffItems)-1))
	end = min(len(m.diffItems), start+h)
	return start, end
}
