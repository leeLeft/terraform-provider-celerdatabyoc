package celerdatabyoc

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"terraform-provider-celerdatabyoc/celerdata-sdk/client"
	"terraform-provider-celerdatabyoc/celerdata-sdk/service/cluster"
	"terraform-provider-celerdatabyoc/common"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// State id is "<cluster_id>:<window_id>" because every window endpoint is
// addressed under its cluster; Read needs both halves to build the path.
const maintenanceWindowIDSep = ":"

// The backend rejects windows shorter than this.
const minMaintenanceWindowMinutes = 120

func resourceClusterMaintenanceWindow() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceClusterMaintenanceWindowCreate,
		ReadContext:   resourceClusterMaintenanceWindowRead,
		UpdateContext: resourceClusterMaintenanceWindowUpdate,
		DeleteContext: resourceClusterMaintenanceWindowDelete,
		CustomizeDiff: resourceClusterMaintenanceWindowCustomizeDiff,
		Schema: map[string]*schema.Schema{
			"cluster_id": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.StringIsNotWhiteSpace,
				Description:  "ID of the cluster the window applies to. Changing it recreates the window.",
			},
			"window_type": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.StringInSlice(cluster.MaintenanceWindowTypes, false),
				Description:  "Recurrence of the window: `WEEKLY` or `MONTHLY`.",
			},
			"day_of_week": {
				Type:         schema.TypeString,
				Optional:     true,
				ValidateFunc: validation.StringInSlice(cluster.WeekDays, false),
				Description:  "Weekday the window recurs on, `MONDAY` through `SUNDAY`. Required when `window_type` is `WEEKLY`.",
			},
			"day_of_month": {
				Type:         schema.TypeInt,
				Optional:     true,
				ValidateFunc: validation.IntBetween(1, 31),
				Description:  "Day of the month (1-31). Required when `window_type` is `MONTHLY`.",
			},
			"start_time": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validateMaintenanceWindowTime,
				Description:  "Window start, `HH:mm` in `timezone`.",
			},
			"end_time": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validateMaintenanceWindowTime,
				Description:  "Window end, `HH:mm` in `timezone`. Must be at least 120 minutes after `start_time`.",
			},
			"timezone": {
				Type:         schema.TypeString,
				Optional:     true,
				Default:      "UTC",
				ValidateFunc: common.ValidateSchedulingPolicyTimeZone,
				Description:  "IANA time zone the times are interpreted in, e.g. `America/Los_Angeles`.",
			},
			"enabled": {
				Type:        schema.TypeBool,
				Optional:    true,
				Default:     true,
				Description: "Whether the scheduler may use this window.",
			},
			"window_id": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Backend id of the window.",
			},
		},
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},
	}
}

// maintenanceWindowConfig is the plan-time view of the schema, kept as a
// plain struct so the cross-field rules can be unit tested without a
// ResourceDiff.
type maintenanceWindowConfig struct {
	WindowType string
	DayOfWeek  string
	DayOfMonth int
	StartTime  string
	EndTime    string
}

func resourceClusterMaintenanceWindowCustomizeDiff(ctx context.Context, d *schema.ResourceDiff, m interface{}) error {
	windowType := d.Get("window_type").(string)
	if err := validateMaintenanceWindowConfig(maintenanceWindowConfig{
		WindowType: windowType,
		DayOfWeek:  d.Get("day_of_week").(string),
		DayOfMonth: d.Get("day_of_month").(int),
		StartTime:  d.Get("start_time").(string),
		EndTime:    d.Get("end_time").(string),
	}); err != nil {
		return err
	}

	// cluster_id is unknown at plan time when the cluster is created in the
	// same apply; nothing to compare against yet, the backend still enforces.
	clusterID := d.Get("cluster_id").(string)
	if clusterID == "" || !d.NewValueKnown("window_type") {
		return nil
	}
	api := cluster.NewClustersAPI(m.(*client.CelerdataClient))
	resp, err := api.ListMaintenanceWindows(ctx, clusterID)
	if err != nil {
		if isNotFoundError(err) {
			return nil
		}
		return fmt.Errorf("list maintenance windows of cluster %s: %w", clusterID, err)
	}
	return checkMaintenanceWindowTypeConsistent(resp.Windows, d.Get("window_id").(string), windowType)
}

// checkMaintenanceWindowTypeConsistent mirrors the backend rule that every
// window of a cluster shares one window_type, so a mismatch fails at plan
// time. selfID excludes this resource's own window on update, letting a
// cluster's only window switch type. Windows created in the same apply are not
// visible here — the backend still rejects those.
func checkMaintenanceWindowTypeConsistent(existing []*cluster.MaintenanceWindow, selfID, windowType string) error {
	for _, w := range existing {
		if w == nil || (selfID != "" && w.WindowId == selfID) {
			continue
		}
		if w.WindowType != windowType {
			return fmt.Errorf("window_type %q conflicts with existing window %s of type %q on the same cluster: all maintenance windows of a cluster must share one window type",
				windowType, w.WindowId, w.WindowType)
		}
	}
	return nil
}

// validateMaintenanceWindowConfig mirrors the backend's window rules so they
// fail at plan time instead of as an opaque apply error.
func validateMaintenanceWindowConfig(c maintenanceWindowConfig) error {
	start, err := parseMaintenanceWindowTime(c.StartTime)
	if err != nil {
		return fmt.Errorf("start_time: %w", err)
	}
	end, err := parseMaintenanceWindowTime(c.EndTime)
	if err != nil {
		return fmt.Errorf("end_time: %w", err)
	}
	if end-start < minMaintenanceWindowMinutes {
		return fmt.Errorf("window must last at least %d minutes: end_time %q is %d minutes after start_time %q",
			minMaintenanceWindowMinutes, c.EndTime, end-start, c.StartTime)
	}

	switch c.WindowType {
	case cluster.MaintenanceWindowTypeWeekly:
		if c.DayOfWeek == "" {
			return fmt.Errorf("day_of_week is required when window_type is %q", c.WindowType)
		}
		if c.DayOfMonth != 0 {
			return fmt.Errorf("day_of_month can only be set when window_type is %q", cluster.MaintenanceWindowTypeMonthly)
		}
	case cluster.MaintenanceWindowTypeMonthly:
		if c.DayOfMonth == 0 {
			return fmt.Errorf("day_of_month is required when window_type is %q", c.WindowType)
		}
		if c.DayOfWeek != "" {
			return fmt.Errorf("day_of_week can only be set when window_type is %q", cluster.MaintenanceWindowTypeWeekly)
		}
	}
	return nil
}

func validateMaintenanceWindowTime(i interface{}, k string) ([]string, []error) {
	v, ok := i.(string)
	if !ok {
		return nil, []error{fmt.Errorf("expected type of %s to be string", k)}
	}
	if _, err := parseMaintenanceWindowTime(v); err != nil {
		return nil, []error{fmt.Errorf("for param `%s`, %s", k, err.Error())}
	}
	return nil, nil
}

// parseMaintenanceWindowTime turns zero-padded "HH:mm" into minutes since
// midnight. The zero padding is required because the backend always reads
// back "09:00"; accepting "9:00" would leave a permanent diff.
func parseMaintenanceWindowTime(v string) (int, error) {
	invalid := fmt.Errorf("invalid time %q, expected \"HH:mm\"", v)
	if len(v) != len("15:04") {
		return 0, invalid
	}
	t, err := time.Parse("15:04", v)
	if err != nil {
		return 0, invalid
	}
	return t.Hour()*60 + t.Minute(), nil
}

func encodeMaintenanceWindowID(clusterID, windowID string) string {
	return clusterID + maintenanceWindowIDSep + windowID
}

func decodeMaintenanceWindowID(id string) (string, string, error) {
	parts := strings.SplitN(id, maintenanceWindowIDSep, 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid maintenance window id %q: expected <cluster_id>:<window_id>", id)
	}
	return parts[0], parts[1], nil
}

func buildMaintenanceWindowReq(d *schema.ResourceData) *cluster.UpsertMaintenanceWindowReq {
	return &cluster.UpsertMaintenanceWindowReq{
		WindowType: d.Get("window_type").(string),
		DayOfWeek:  d.Get("day_of_week").(string),
		DayOfMonth: int32(d.Get("day_of_month").(int)),
		StartTime:  d.Get("start_time").(string),
		EndTime:    d.Get("end_time").(string),
		Timezone:   d.Get("timezone").(string),
		Enabled:    d.Get("enabled").(bool),
	}
}

func resourceClusterMaintenanceWindowCreate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	c := m.(*client.CelerdataClient)
	api := cluster.NewClustersAPI(c)

	clusterID := d.Get("cluster_id").(string)
	resp, err := api.CreateMaintenanceWindow(ctx, clusterID, buildMaintenanceWindowReq(d))
	if err != nil {
		return diag.FromErr(err)
	}

	log.Printf("[DEBUG] create maintenance window succeeded, cluster[%s] window[%s]", clusterID, resp.WindowId)
	d.SetId(encodeMaintenanceWindowID(clusterID, resp.WindowId))
	return resourceClusterMaintenanceWindowRead(ctx, d, m)
}

func resourceClusterMaintenanceWindowRead(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	c := m.(*client.CelerdataClient)
	api := cluster.NewClustersAPI(c)

	clusterID, windowID, err := decodeMaintenanceWindowID(d.Id())
	if err != nil {
		return diag.FromErr(err)
	}

	resp, err := api.GetMaintenanceWindow(ctx, clusterID, windowID)
	if err != nil {
		if isNotFoundError(err) {
			d.SetId("")
			return nil
		}
		return diag.FromErr(err)
	}
	if resp.Window == nil {
		d.SetId("")
		return nil
	}

	w := resp.Window
	d.Set("cluster_id", clusterID)
	d.Set("window_id", w.WindowId)
	d.Set("window_type", w.WindowType)
	d.Set("day_of_week", w.DayOfWeek)
	d.Set("day_of_month", int(w.DayOfMonth))
	d.Set("start_time", w.StartTime)
	d.Set("end_time", w.EndTime)
	d.Set("timezone", w.Timezone)
	d.Set("enabled", w.Enabled)
	return nil
}

func resourceClusterMaintenanceWindowUpdate(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	c := m.(*client.CelerdataClient)
	api := cluster.NewClustersAPI(c)

	clusterID, windowID, err := decodeMaintenanceWindowID(d.Id())
	if err != nil {
		return diag.FromErr(err)
	}

	if err := api.UpdateMaintenanceWindow(ctx, clusterID, windowID, buildMaintenanceWindowReq(d)); err != nil {
		return diag.FromErr(err)
	}
	return resourceClusterMaintenanceWindowRead(ctx, d, m)
}

func resourceClusterMaintenanceWindowDelete(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	c := m.(*client.CelerdataClient)
	api := cluster.NewClustersAPI(c)

	clusterID, windowID, err := decodeMaintenanceWindowID(d.Id())
	if err != nil {
		return diag.FromErr(err)
	}

	if err := api.DeleteMaintenanceWindow(ctx, clusterID, windowID); err != nil {
		if isNotFoundError(err) {
			d.SetId("")
			return nil
		}
		return diag.FromErr(err)
	}
	d.SetId("")
	return nil
}
