package celerdatabyoc

import (
	"context"
	"strings"
	"testing"
	"time"

	"terraform-provider-celerdatabyoc/celerdata-sdk/service/cluster"
)

// fakeStateAPI serves a scripted sequence of cluster states from GetState and
// records every request it receives. The embedded interface covers the rest of
// IClusterAPI and panics if ever reached.
type fakeStateAPI struct {
	cluster.IClusterAPI
	states []string
	reqs   []*cluster.GetStateReq
}

func (f *fakeStateAPI) GetState(_ context.Context, req *cluster.GetStateReq) (*cluster.GetStateResp, error) {
	f.reqs = append(f.reqs, req)
	idx := len(f.reqs) - 1
	if idx >= len(f.states) {
		idx = len(f.states) - 1
	}
	resp := &cluster.GetStateResp{ClusterState: f.states[idx]}
	if resp.ClusterState == string(cluster.ClusterStateAbnormal) {
		resp.AbnormalReason = "status reported by agent on fe node is unknown"
	}
	return resp, nil
}

// Right after a deploy order completes, the backend's real (action + agent
// heartbeat based) state may still read Abnormal/Deploying for a few seconds.
// The wait must poll by cluster id (no action id) and ride through that window.
func TestWaitClusterRunningByClusterID_RidesThroughTransientAbnormal(t *testing.T) {
	api := &fakeStateAPI{states: []string{
		string(cluster.ClusterStateAbnormal),
		string(cluster.ClusterStateRunning),
	}}

	resp, err := waitClusterRunningByClusterID(context.Background(), api, "cluster-1", time.Minute)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if resp == nil || resp.ClusterState != string(cluster.ClusterStateRunning) {
		t.Fatalf("expected Running response, got %+v", resp)
	}
	if len(api.reqs) < 2 {
		t.Fatalf("expected at least 2 polls (Abnormal then Running), got %d", len(api.reqs))
	}
	for i, r := range api.reqs {
		if r.ActionID != "" {
			t.Fatalf("poll %d must not carry an action id (order-based state), got %q", i, r.ActionID)
		}
		if r.ClusterID != "cluster-1" {
			t.Fatalf("poll %d expected cluster id cluster-1, got %q", i, r.ClusterID)
		}
	}
}

// A cluster that never leaves Abnormal must not be waited on forever: the
// bounded wait returns an error that names the state it got stuck in.
func TestWaitClusterRunningByClusterID_TimesOutWhenStuckAbnormal(t *testing.T) {
	api := &fakeStateAPI{states: []string{string(cluster.ClusterStateAbnormal)}}

	_, err := waitClusterRunningByClusterID(context.Background(), api, "cluster-1", time.Second)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "cluster-1") || !strings.Contains(err.Error(), string(cluster.ClusterStateAbnormal)) {
		t.Fatalf("error should mention the cluster id and the last state, got: %v", err)
	}
}
