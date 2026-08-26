---
page_title: "celerdatabyoc_cluster_maintenance_window Resource - terraform-provider-celerdatabyoc"
subcategory: ""
description: |-
  Manages a recurring security-patch maintenance window for a CelerData cluster.
---

# celerdatabyoc_cluster_maintenance_window

Manages a recurring maintenance window on a CelerData cluster. When a security
patch (an AMI or StarRocks patch release) is published for the cluster, the
scheduler picks the next occurrence of one of the cluster's enabled windows and
applies the patch inside it, instead of requiring you to coordinate an upgrade
time by hand.

A window is scoped to one cluster. A cluster can have several windows, each
managed as its own resource; the scheduler uses whichever comes first. All
windows of a cluster must share one `window_type`: once the first window is
`WEEKLY`, every further window on that cluster must be `WEEKLY` too (likewise
for `MONTHLY`). A mismatch is reported at plan time when the cluster already
exists, otherwise on apply. Deleting the resource deletes the window; pending
patches then wait until the cluster has another enabled window.

## Example Usage

```terraform
resource "celerdatabyoc_cluster_maintenance_window" "weekly" {
  cluster_id  = celerdatabyoc_elastic_cluster_v2.prod.id
  window_type = "WEEKLY"
  day_of_week = "SUNDAY"
  start_time  = "02:00"
  end_time    = "05:00"
  timezone    = "America/Los_Angeles"
}

# A second window on the same cluster must use the same window_type.
resource "celerdatabyoc_cluster_maintenance_window" "midweek" {
  cluster_id  = celerdatabyoc_elastic_cluster_v2.prod.id
  window_type = "WEEKLY"
  day_of_week = "WEDNESDAY"
  start_time  = "01:00"
  end_time    = "04:00"
  timezone    = "America/Los_Angeles"
  enabled     = true
}
```

## Argument Reference

**Required**

- `cluster_id`: (String, ForceNew) ID of the cluster the window applies to.
  Changing it recreates the window.
- `window_type`: (String) Recurrence of the window. One of `WEEKLY`, `MONTHLY`.
  Must match the type of every other window on the cluster. A cluster's only
  window can be changed to the other type in place; to switch a cluster with
  several windows, remove the others first.
- `start_time`: (String) Window start as `HH:mm` (zero-padded, 24-hour) in `timezone`.
- `end_time`: (String) Window end as `HH:mm` in `timezone`. Must be at least
  120 minutes after `start_time` on the same day.

**Optional**

- `day_of_week`: (String) Weekday the window recurs on, one of `MONDAY`,
  `TUESDAY`, `WEDNESDAY`, `THURSDAY`, `FRIDAY`, `SATURDAY`, `SUNDAY`.
  Required when `window_type` is `WEEKLY`; must not be set otherwise.
- `day_of_month`: (Integer) Day of the month, `1` through `31`. Required when
  `window_type` is `MONTHLY`; must not be set otherwise. In months shorter
  than that day (e.g. `31` in February), the window falls on the last day of
  the month instead of being skipped.
- `timezone`: (String) IANA time zone name the times are interpreted in.
  Defaults to `UTC`.
- `enabled`: (Boolean) Whether the scheduler may use this window. Defaults to
  `true`. A disabled window is kept but never selected.

## Attribute Reference

In addition to the arguments above, this resource exports:

- `id`: (String) `<cluster_id>:<window_id>`.
- `window_id`: (String) The window ID assigned by CelerData.

## Import

Maintenance windows can be imported with `<cluster_id>:<window_id>`. Window IDs
can be listed with `GET /api/1.0/clusters/<cluster_id>/maintenance-windows`.

```shell
terraform import celerdatabyoc_cluster_maintenance_window.weekly <cluster_id>:<window_id>
```
