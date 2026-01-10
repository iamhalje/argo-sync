package services

import "github.com/iamhalje/argo-sync/internal/models"

func TargetsForSelection(inv models.Inventory, selectedApps []models.AppKey, selectedClusters []string) []models.Target {
	targets := make([]models.Target, 0, len(selectedApps)*len(selectedClusters))
	for _, c := range selectedClusters {
		for _, a := range selectedApps {
			if _, ok := inv[a][c]; !ok {
				continue
			}
			targets = append(targets, models.Target{ClusterContext: c, App: a})
		}
	}
	return targets
}
