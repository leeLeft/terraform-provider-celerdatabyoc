package celerdatabyoc

import (
	"context"
	"fmt"
	"time"

	"terraform-provider-celerdatabyoc/celerdata-sdk/service/cluster"
)

// waitClusterRunningByClusterID polls the cluster state by cluster id only (no
// action id) until the backend reports Running, or the timeout elapses.
//
// Polling with an action id returns a state derived from the order that action
// belongs to, so it reads Running as soon as the deploy order is marked created.
// The state the backend actually validates operations against (CreateWarehouse
// etc.) is derived from the cluster's infra actions plus agent heartbeats, and
// can lag behind as Deploying/Abnormal for a short window. Abnormal is treated
// as pending here for that reason; a cluster that stays Abnormal surfaces as a
// timeout error rather than an immediate failure.
func waitClusterRunningByClusterID(ctx context.Context, clusterAPI cluster.IClusterAPI, clusterID string, timeout time.Duration) (*cluster.GetStateResp, error) {
	resp, err := WaitClusterStateChangeComplete(ctx, &waitStateReq{
		clusterAPI: clusterAPI,
		clusterID:  clusterID,
		timeout:    timeout,
		pendingStates: []string{
			string(cluster.ClusterStateDeploying),
			string(cluster.ClusterStateScaling),
			string(cluster.ClusterStateResuming),
			string(cluster.ClusterStateSuspending),
			string(cluster.ClusterStateReleasing),
			string(cluster.ClusterStateUpdating),
			string(cluster.ClusterStateAbnormal),
		},
		targetStates: []string{
			string(cluster.ClusterStateRunning),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("waiting for cluster (%s) to become %s: %w", clusterID, cluster.ClusterStateRunning, err)
	}
	return resp, nil
}
