package celerdatabyoc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"terraform-provider-celerdatabyoc/celerdata-sdk/client"
	"terraform-provider-celerdatabyoc/celerdata-sdk/service/cluster"
	"terraform-provider-celerdatabyoc/celerdata-sdk/service/network"
	"terraform-provider-celerdatabyoc/common"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	DEFAULT_WAREHOUSE_NAME = "default_warehouse"
	CROSSING_AZ            = "crossing_az"
	SPECIFY_AZ             = "specify_az"
)

// V2 support multi-warehouse
func resourceElasticClusterV2() *schema.Resource {
	return &schema.Resource{
		ReadContext:   resourceElasticClusterV2Read,
		CreateContext: resourceElasticClusterV2Create,
		UpdateContext: resourceElasticClusterV2Update,
		DeleteContext: resourceElasticClusterV2Delete,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"csp": {
				Type:     schema.TypeString,
				Required: true,
			},
			"region": {
				Type:     schema.TypeString,
				Required: true,
			},
			"cluster_state": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"cluster_name": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.StringMatch(regexp.MustCompile(`^[0-9a-zA-Z_-]{1,32}$`), "The cluster name is restricted to a maximum length of 32 characters and can only consist of alphanumeric characters (a-z, A-Z, 0-9), hyphens (-), and underscores (_)."),
			},
			"coordinator_node_size": {
				Type:         schema.TypeString,
				Required:     true,
				ValidateFunc: validation.StringIsNotEmpty,
			},
			"coordinator_node_count": {
				Type:         schema.TypeInt,
				Optional:     true,
				Default:      1,
				ValidateFunc: validation.IntInSlice([]int{1, 3, 5, 7}),
			},
			"coordinator_node_volume_config": {
				Type:     schema.TypeList,
				Optional: true,
				MaxItems: 1,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"vol_size": {
							Type:             schema.TypeInt,
							Optional:         true,
							Default:          150,
							ValidateDiagFunc: common.ValidateVolumeSize(),
						},
						"iops": {
							Type:         schema.TypeInt,
							Optional:     true,
							ValidateFunc: validation.IntAtLeast(0),
						},
						"throughput": {
							Type:         schema.TypeInt,
							Optional:     true,
							ValidateFunc: validation.IntAtLeast(0),
						},
					},
				},
			},
			"custom_ami": {
				Type:     schema.TypeList,
				Optional: true,
				Computed: true,
				MaxItems: 1,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"ami": {
							Type:         schema.TypeString,
							Required:     true,
							ValidateFunc: validation.StringIsNotEmpty,
						},
						"os": {
							Type:         schema.TypeString,
							Required:     true,
							ValidateFunc: validation.StringInSlice([]string{"al2023"}, false),
						},
					},
				},
			},
			"default_warehouse": {
				Type:     schema.TypeList,
				Required: true,
				MinItems: 1,
				MaxItems: 1,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name": {
							Type:     schema.TypeString,
							Computed: true,
						},
						"compute_node_size": {
							Type:         schema.TypeString,
							Required:     true,
							ValidateFunc: validation.StringIsNotEmpty,
						},
						"compute_node_count": {
							Type:         schema.TypeInt,
							Optional:     true,
							Default:      3,
							ValidateFunc: validation.IntAtLeast(1),
						},
						"distribution_policy": {
							Type:     schema.TypeString,
							Optional: true,
							Default:  CROSSING_AZ,
							ValidateFunc: validation.StringInSlice([]string{
								SPECIFY_AZ,
								CROSSING_AZ,
							}, false),
						},
						"specify_az": {
							Type:     schema.TypeString,
							Optional: true,
						},
						"compute_node_volume_config": {
							Type:     schema.TypeList,
							Optional: true,
							MaxItems: 1,
							Elem: &schema.Resource{
								Schema: map[string]*schema.Schema{
									"vol_number": {
										Description: "Specifies the number of disk. The default value is 2.",
										Type:        schema.TypeInt,
										Optional:    true,
										Default:     2,
										ValidateFunc: func(i interface{}, k string) (warnings []string, errors []error) {
											v, ok := i.(int)
											if !ok {
												errors = append(errors, fmt.Errorf("expected type of %s to be int", k))
												return warnings, errors
											}

											if v < 1 || v > 24 {
												errors = append(errors, fmt.Errorf("%s`s value is invalid. The range of values is: [1,24]", k))
											}

											return warnings, errors
										},
									},
									"vol_size": {
										Description:      "Specifies the size of a single disk in GB. The default size for per disk is 100GB.",
										Type:             schema.TypeInt,
										Optional:         true,
										Default:          100,
										ValidateDiagFunc: common.ValidateVolumeSize(),
									},
									"iops": {
										Type:         schema.TypeInt,
										Optional:     true,
										ValidateFunc: validation.IntAtLeast(0),
									},
									"throughput": {
										Type:         schema.TypeInt,
										Optional:     true,
										ValidateFunc: validation.IntAtLeast(0),
									},
								},
							},
						},
						"auto_scaling_policy": {
							Type:     schema.TypeString,
							Optional: true,
							ValidateFunc: func(i interface{}, s string) ([]string, []error) {
								err := ValidateAutoScalingPolicyStr(i.(string))
								if err != nil {
									return nil, []error{err}
								}
								return nil, nil
							},
						},
						"compute_node_configs": {
							Type:     schema.TypeMap,
							Optional: true,
							Elem:     &schema.Schema{Type: schema.TypeString},
						},
					},
				},
			},
			"warehouse": {
				Type:     schema.TypeList,
				Optional: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"name": {
							Type:     schema.TypeString,
							Required: true,
							ValidateFunc: func(i interface{}, k string) (warnings []string, errors []error) {
								whName := i.(string)
								if len(whName) == 0 {
									errors = append(errors, fmt.Errorf("%s`s value is invalid. Warehouse name can not be empty", k))
								} else if whName == DEFAULT_WAREHOUSE_NAME {
									errors = append(errors, fmt.Errorf("%s`s value is invalid. Normal warehouses can't be named: %s", k, DEFAULT_WAREHOUSE_NAME))
								} else if strings.Contains(whName, "-") {
									errors = append(errors, fmt.Errorf("%s`s value is invalid. Warehouse name can contain '-'", k))
								}
								return warnings, errors
							},
						},
						"compute_node_size": {
							Type:         schema.TypeString,
							Required:     true,
							ValidateFunc: validation.StringIsNotEmpty,
						},
						"compute_node_count": {
							Type:         schema.TypeInt,
							Optional:     true,
							Default:      3,
							ValidateFunc: validation.IntAtLeast(1),
						},
						"distribution_policy": {
							Type:     schema.TypeString,
							Optional: true,
							Default:  CROSSING_AZ,
							ValidateFunc: validation.StringInSlice([]string{
								SPECIFY_AZ,
								CROSSING_AZ,
							}, false),
						},
						"specify_az": {
							Type:     schema.TypeString,
							Optional: true,
						},
						"compute_node_volume_config": {
							Type:     schema.TypeList,
							Optional: true,
							MaxItems: 1,
							Elem: &schema.Resource{
								Schema: map[string]*schema.Schema{
									"vol_number": {
										Description: "Specifies the number of disk. The default value is 2.",
										Type:        schema.TypeInt,
										Optional:    true,
										Default:     2,
										ValidateFunc: func(i interface{}, k string) (warnings []string, errors []error) {
											v, ok := i.(int)
											if !ok {
												errors = append(errors, fmt.Errorf("expected type of %s to be int", k))
												return warnings, errors
											}

											if v < 1 || v > 24 {
												errors = append(errors, fmt.Errorf("%s`s value is invalid. The range of values is: [1,24]", k))
											}

											return warnings, errors
										},
									},
									"vol_size": {
										Description:      "Specifies the size of a single disk in GB. The default size for per disk is 100GB.",
										Type:             schema.TypeInt,
										Optional:         true,
										ValidateDiagFunc: common.ValidateVolumeSize(),
									},
									"iops": {
										Type:         schema.TypeInt,
										Optional:     true,
										ValidateFunc: validation.IntAtLeast(0),
									},
									"throughput": {
										Type:         schema.TypeInt,
										Optional:     true,
										ValidateFunc: validation.IntAtLeast(0),
									},
								},
							},
						},
						"idle_suspend_interval": {
							Type:        schema.TypeInt,
							Description: "Specifies the amount of time (in minutes) during which a warehouse can stay idle. After the specified time period elapses, the warehouse will be automatically suspended.",
							Optional:    true,
							Default:     0,
							ValidateFunc: func(i interface{}, k string) (warnings []string, errors []error) {
								v, ok := i.(int)
								if !ok {
									errors = append(errors, fmt.Errorf("expected type of %s to be int", k))
									return warnings, errors
								}

								if v != 0 {
									if v < 15 || v > 999999 {
										errors = append(errors, fmt.Errorf("the %s range should be [15,999999]", k))
										return warnings, errors
									}
								}
								return warnings, errors
							},
						},
						"auto_scaling_policy": {
							Type:     schema.TypeString,
							Optional: true,
							ValidateFunc: func(i interface{}, s string) ([]string, []error) {
								err := ValidateAutoScalingPolicyStr(i.(string))
								if err != nil {
									return nil, []error{err}
								}
								return nil, nil
							},
						},
						"expected_state": {
							Type:         schema.TypeString,
							Optional:     true,
							Default:      string(cluster.ClusterStateRunning),
							ValidateFunc: validation.StringInSlice([]string{string(cluster.ClusterStateSuspended), string(cluster.ClusterStateRunning)}, false),
						},
						"compute_node_configs": {
							Type:     schema.TypeMap,
							Optional: true,
							Elem:     &schema.Schema{Type: schema.TypeString},
						},
					},
				},
			},
			"warehouse_external_info": {
				Type:     schema.TypeMap,
				Computed: true,
				Elem: &schema.Schema{
					Type: schema.TypeString,
				},
			},
			"resource_tags": {
				Type:        schema.TypeMap,
				Optional:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
				Description: "A map of tags to assign to the resource. For AWS, these are tags; for GCP, these are labels.",
			},
			"default_admin_password": {
				Type:             schema.TypeString,
				Required:         true,
				Sensitive:        true,
				ValidateDiagFunc: common.ValidatePassword(),
			},
			"data_credential_id": {
				Type:     schema.TypeString,
				Required: true,
			},
			"deployment_credential_id": {
				Type:     schema.TypeString,
				Required: true,
			},
			"network_id": {
				Type:     schema.TypeString,
				Required: true,
			},
			"expected_cluster_state": {
				Type:         schema.TypeString,
				Optional:     true,
				Default:      string(cluster.ClusterStateRunning),
				ValidateFunc: validation.StringInSlice([]string{string(cluster.ClusterStateSuspended), string(cluster.ClusterStateRunning)}, false),
			},
			"free_tier": {
				Type:     schema.TypeBool,
				Computed: true,
			},
			"init_scripts": {
				Type:     schema.TypeSet,
				Optional: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"script_path": {
							Type:     schema.TypeString,
							Required: true,
						},
						"logs_dir": {
							Type:     schema.TypeString,
							Required: true,
						},
					},
				},
			},
			"run_scripts_parallel": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},
			"query_port": {
				Type:     schema.TypeInt,
				Optional: true,
				Default:  9030,
				ValidateFunc: func(i interface{}, k string) (warnings []string, errors []error) {
					v, ok := i.(int)
					if !ok {
						errors = append(errors, fmt.Errorf("expected type of %s to be int", k))
						return warnings, errors
					}
					if v < 1 || v > 65535 {
						errors = append(errors, fmt.Errorf("the %s range should be 1-65535", k))
						return warnings, errors
					}
					if v == 443 {
						errors = append(errors, fmt.Errorf("%s : duplicate port 443 definitions", k))
						return warnings, errors
					}
					return warnings, errors
				},
			},
			"idle_suspend_interval": {
				Type:        schema.TypeInt,
				Description: "Specifies the amount of time (in minutes) during which a cluster can stay idle. After the specified time period elapses, the cluster will be automatically suspended.",
				Optional:    true,
				Default:     0,
				ValidateFunc: func(i interface{}, k string) (warnings []string, errors []error) {
					v, ok := i.(int)
					if !ok {
						errors = append(errors, fmt.Errorf("expected type of %s to be int", k))
						return warnings, errors
					}

					if v != 0 {
						if v < 15 || v > 999999 {
							errors = append(errors, fmt.Errorf("the %s range should be [15,999999]", k))
							return warnings, errors
						}
					}
					return warnings, errors
				},
			},
			"ldap_ssl_certs": {
				Type:     schema.TypeSet,
				Optional: true,
				Elem: &schema.Schema{
					Type: schema.TypeString,
					ValidateFunc: func(i interface{}, k string) (warnings []string, errors []error) {
						value, ok := i.(string)
						if !ok {
							errors = append(errors, fmt.Errorf("expected type of %s to be string", k))
							return warnings, errors
						}

						if len(value) > 0 {
							if !CheckS3Path(value) {
								errors = append(errors, fmt.Errorf("for %s invalid s3 path:%s", k, value))
							}
						} else {
							errors = append(errors, fmt.Errorf("%s`s value cann`t be empty", k))
						}
						return warnings, errors
					},
				},
			},
			"run_scripts_timeout": {
				Type:         schema.TypeInt,
				Optional:     true,
				Default:      3600,
				ValidateFunc: validation.IntAtMost(int(common.DeployOrScaleClusterTimeout.Seconds())),
			},
			"coordinator_node_configs": {
				Type:     schema.TypeMap,
				Optional: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
			},
			"ranger_certs_dir": {
				Type:         schema.TypeString,
				Optional:     true,
				ValidateFunc: validation.StringIsNotWhiteSpace,
			},
		},
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},
		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(common.DeployOrScaleClusterTimeout),
			Update: schema.DefaultTimeout(common.DeployOrScaleClusterTimeout),
		},
		CustomizeDiff: customizeEl2Diff,
	}
}

func customizeEl2Diff(ctx context.Context, d *schema.ResourceDiff, m interface{}) error {
	c := m.(*client.CelerdataClient)
	clusterAPI := cluster.NewClustersAPI(c)
	networkAPI := network.NewNetworkAPI(c)

	clusterId := d.Id()
	csp := d.Get("csp").(string)
	region := d.Get("region").(string)
	isNewResource := d.Id() == ""

	n := d.Get("coordinator_node_size")
	newCoordinatorVmInfoResp, err := clusterAPI.GetVmInfo(ctx, &cluster.GetVmInfoReq{
		Csp:         csp,
		Region:      region,
		ProcessType: string(cluster.ClusterModuleTypeFE),
		VmCate:      n.(string),
	})
	if err != nil {
		log.Printf("[ERROR] query vm info failed, csp:%s region:%s vmCate:%s err:%+v", csp, region, n.(string), err)
		return fmt.Errorf("query vm info failed, csp:%s region:%s vmCate:%s errMsg:%s", csp, region, n.(string), err.Error())
	}
	if newCoordinatorVmInfoResp.VmInfo == nil {
		return fmt.Errorf("vm info not exists, csp:%s region:%s vmCate:%s", csp, region, n.(string))
	}

	if len(d.Get("network_id").(string)) > 0 {
		netResp, err := networkAPI.GetNetwork(ctx, d.Get("network_id").(string))
		if err != nil {
			return err
		}

		coordinatorNodeCount := d.Get("coordinator_node_count").(int)
		if d.HasChange("coordinator_node_count") {
			_, n := d.GetChange("coordinator_node_count")
			coordinatorNodeCount = n.(int)
		}
		if netResp.Network.MultiAz && coordinatorNodeCount < 3 {
			return errors.New("in multi-AZ deployment mode, the number of coordinator nodes should be greater than or equal to 3")
		}
	}

	warehouses := make([]interface{}, 0)
	warehouses = append(warehouses, d.Get("default_warehouse").([]interface{})[0])
	warehouses = append(warehouses, d.Get("warehouse").([]interface{})...)

	for _, v := range warehouses {
		vMap := v.(map[string]interface{})
		if vMap["distribution_policy"].(string) != SPECIFY_AZ && len(vMap["specify_az"].(string)) > 0 {
			return errors.New("specify_az parameter only takes effect when the distribution_policy value is \"specify_az\"")
		}
	}

	feArch := newCoordinatorVmInfoResp.VmInfo.Arch

	if d.HasChange("coordinator_node_size") && !isNewResource {
		o, _ := d.GetChange("coordinator_node_size")
		oldVmInfoResp, err := clusterAPI.GetVmInfo(ctx, &cluster.GetVmInfoReq{
			Csp:         csp,
			Region:      region,
			ProcessType: string(cluster.ClusterModuleTypeFE),
			VmCate:      o.(string),
		})
		if err != nil {
			log.Printf("[ERROR] query vm info failed, csp:%s region:%s vmCate:%s err:%+v", csp, region, o.(string), err)
			return fmt.Errorf("query vm info failed, csp:%s region:%s vmCate:%s errMsg:%s", csp, region, o.(string), err.Error())
		}
		if oldVmInfoResp.VmInfo == nil {
			return fmt.Errorf("vm info not exists, csp:%s region:%s vmCate:%s", csp, region, o.(string))
		}
		if feArch != oldVmInfoResp.VmInfo.Arch {
			return fmt.Errorf("the vm instance architecture can not be changed, csp:%s region:%s oldVmCate:%s  newVmCate:%s", csp, region, o.(string), n.(string))
		}
	}

	if d.HasChange("coordinator_node_volume_config") && !isNewResource {
		o, n := d.GetChange("coordinator_node_volume_config")

		oldVolumeConfig := cluster.DefaultFeVolumeMap()
		newVolumeConfig := cluster.DefaultFeVolumeMap()

		if len(o.([]interface{})) > 0 {
			oldVolumeConfig = o.([]interface{})[0].(map[string]interface{})
		}
		if len(n.([]interface{})) > 0 {
			newVolumeConfig = n.([]interface{})[0].(map[string]interface{})
		}

		oldVolumeSize, newVolumeSize := oldVolumeConfig["vol_size"].(int), newVolumeConfig["vol_size"].(int)

		if newVolumeSize < oldVolumeSize {
			return fmt.Errorf("the coordinator node `vol_size` does not support decrease")
		}
	}

	if !newCoordinatorVmInfoResp.VmInfo.IsInstanceStore {
		if v, ok := d.GetOk("coordinator_node_volume_config"); ok {
			nodeType := "Coordinator node"
			volumeCate := newCoordinatorVmInfoResp.VmInfo.VmVolumeInfos[0].VolumeCate
			volumeConfig := v.([]interface{})[0].(map[string]interface{})
			err = VolumeParamVerify(ctx, &VolumeParamVerifyReq{
				ClusterAPI:   clusterAPI,
				VolumeCate:   volumeCate,
				VolumeConfig: volumeConfig,
			})
			if err != nil {
				log.Printf("[ERROR] verify %s volume params failed, volumeCate:%s volumeConfig:%+v err:%+v", nodeType, volumeCate, volumeConfig, err)
				return fmt.Errorf("verify %s volume params failed, volumeCate:%s volumeConfig:%+v err:%+v", nodeType, volumeCate, volumeConfig, err)
			}
		}
	}

	if d.HasChange("default_warehouse") {
		_, n := d.GetChange("default_warehouse")

		// Check vm arch
		whVmInfoMap := make(map[string]*cluster.VMInfo)
		for _, item := range n.([]interface{}) {
			m := item.(map[string]interface{})
			whName := strings.TrimSpace(m["name"].(string))
			vmCateName := m["compute_node_size"].(string)
			vmCateInfoResp, err := clusterAPI.GetVmInfo(ctx, &cluster.GetVmInfoReq{
				Csp:         csp,
				Region:      region,
				ProcessType: string(cluster.ClusterModuleTypeBE),
				VmCate:      vmCateName,
			})
			if err != nil {
				log.Printf("[ERROR] query vm info failed, csp:%s region:%s vmCate:%s err:%+v", csp, region, vmCateName, err)
				return fmt.Errorf("query vm info failed, csp:%s region:%s vmCate:%s errMsg:%s", csp, region, vmCateName, err.Error())
			}
			if vmCateInfoResp.VmInfo == nil {
				return fmt.Errorf("vm info not exists, csp:%s region:%s vmCate:%s", csp, region, vmCateName)
			}
			if vmCateInfoResp.VmInfo.Arch != feArch {
				return fmt.Errorf("the vm instance`s architecture of the warehouse[%s] must be the same as the coordinator node, expect:%s but found:%s", whName, feArch, vmCateInfoResp.VmInfo.Arch)
			}

			if vmCateInfoResp.VmInfo.IsInstanceStore {
				attrs := make([]string, 0)
				if v, ok := m["compute_node_volume_config"]; ok && len(v.([]interface{})) > 0 {
					attrs = append(attrs, "compute_node_volume_config")
				}
				if len(attrs) > 0 {
					return fmt.Errorf("the vm instance type[%s] of the warehouse[%s] does not support specifying the volume config of disks, field: %+v is not supported", vmCateName, whName, strings.Join(attrs, ","))
				}
			} else {
				if v, ok := m["compute_node_volume_config"]; ok && len(v.([]interface{})) > 0 {
					nodeType := "Compute node"
					volumeCate := vmCateInfoResp.VmInfo.VmVolumeInfos[0].VolumeCate
					volumeConfig := v.([]interface{})[0].(map[string]interface{})
					err = VolumeParamVerify(ctx, &VolumeParamVerifyReq{
						ClusterAPI:   clusterAPI,
						VolumeCate:   volumeCate,
						VolumeConfig: volumeConfig,
					})
					if err != nil {
						log.Printf("[ERROR] verify %s volume params failed, volumeCate:%s volumeConfig:%+v err:%+v", nodeType, volumeCate, volumeConfig, err)
						return fmt.Errorf("verify %s volume params failed, volumeCate:%s volumeConfig:%+v err:%+v", nodeType, volumeCate, volumeConfig, err)
					}
				}
			}
			whVmInfoMap[whName] = vmCateInfoResp.VmInfo
		}

		if len(clusterId) > 0 {
			// Check is instance store
			whExternalInfoMap := d.Get("warehouse_external_info").(map[string]interface{})
			for whName, whExInfo := range whExternalInfoMap {
				if v, ok := whVmInfoMap[whName]; ok {
					whExternalInfo := &cluster.WarehouseExternalInfo{}
					json.Unmarshal([]byte(whExInfo.(string)), whExternalInfo)

					expectStr := "local disk vm instance type"
					if !whExternalInfo.IsInstanceStore {
						expectStr = "nonlocal disk vm instance type"
					}
					if whExternalInfo.IsInstanceStore != v.IsInstanceStore {
						return fmt.Errorf("the disk type of the warehouse[%s] must be the same as the previous disk type, expect:%s", whName, expectStr)
					}
				}
			}
		}
	}

	if d.HasChange("warehouse") {

		_, n := d.GetChange("warehouse")
		// 1. pre check, warehosue name must be unique
		countMap := make(map[string]int, 0)
		for _, item := range n.([]interface{}) {
			m := item.(map[string]interface{})
			whName := strings.TrimSpace(m["name"].(string))
			if v, ok := countMap[whName]; ok {
				v++
				countMap[whName] = v
			} else {
				countMap[whName] = 1
			}
		}

		for k, v := range countMap {
			if v > 1 {
				return fmt.Errorf("only one warehouse with name '%s' is allowed", k)
			}
		}

		// 2. check vm arch
		whVmInfoMap := make(map[string]*cluster.VMInfo)
		for _, item := range n.([]interface{}) {
			m := item.(map[string]interface{})
			whName := strings.TrimSpace(m["name"].(string))
			vmCateName := m["compute_node_size"].(string)
			vmCateInfoResp, err := clusterAPI.GetVmInfo(ctx, &cluster.GetVmInfoReq{
				Csp:         csp,
				Region:      region,
				ProcessType: string(cluster.ClusterModuleTypeBE),
				VmCate:      vmCateName,
			})
			if err != nil {
				log.Printf("[ERROR] query vm info failed, csp:%s region:%s vmCate:%s err:%+v", csp, region, vmCateName, err)
				return fmt.Errorf("query vm info failed, csp:%s region:%s vmCate:%s errMsg:%s", csp, region, vmCateName, err.Error())
			}
			if vmCateInfoResp.VmInfo == nil {
				return fmt.Errorf("vm info not exists, csp:%s region:%s vmCate:%s", csp, region, vmCateName)
			}
			if vmCateInfoResp.VmInfo.Arch != feArch {
				return fmt.Errorf("the vm instance`s architecture of the warehouse[%s] must be the same as the coordinator node, expect:%s but found:%s", whName, feArch, vmCateInfoResp.VmInfo.Arch)
			}

			if vmCateInfoResp.VmInfo.IsInstanceStore {
				attrs := make([]string, 0)
				if v, ok := m["compute_node_volume_config"]; ok && len(v.([]interface{})) > 0 {
					attrs = append(attrs, "compute_node_volume_config")
				}
				if len(attrs) > 0 {
					return fmt.Errorf("the vm instance type[%s] of the warehouse[%s] does not support specifying the volume config of disks, field: %+v is not supported", vmCateName, whName, strings.Join(attrs, ","))
				}
			} else {
				if v, ok := m["compute_node_volume_config"]; ok && len(v.([]interface{})) > 0 {
					nodeType := "Compute node"
					volumeCate := vmCateInfoResp.VmInfo.VmVolumeInfos[0].VolumeCate
					volumeConfig := v.([]interface{})[0].(map[string]interface{})
					err = VolumeParamVerify(ctx, &VolumeParamVerifyReq{
						ClusterAPI:   clusterAPI,
						VolumeCate:   volumeCate,
						VolumeConfig: volumeConfig,
					})
					if err != nil {
						log.Printf("[ERROR] verify %s volume params failed, volumeCate:%s volumeConfig:%+v err:%+v", nodeType, volumeCate, volumeConfig, err)
						return fmt.Errorf("verify %s volume params failed, volumeCate:%s volumeConfig:%+v err:%+v", nodeType, volumeCate, volumeConfig, err)
					}
				}
			}

			whVmInfoMap[whName] = vmCateInfoResp.VmInfo
		}

		if len(clusterId) > 0 {
			// 3. check is instance store
			whExternalInfoMap := d.Get("warehouse_external_info").(map[string]interface{})
			for whName, whExInfo := range whExternalInfoMap {
				if v, ok := whVmInfoMap[whName]; ok {
					whExternalInfo := &cluster.WarehouseExternalInfo{}
					json.Unmarshal([]byte(whExInfo.(string)), whExternalInfo)

					expectStr := "local disk vm instance type"
					if !whExternalInfo.IsInstanceStore {
						expectStr = "nonlocal disk vm instance type"
					}
					if whExternalInfo.IsInstanceStore != v.IsInstanceStore {
						return fmt.Errorf("the disk type of the warehouse[%s] must be the same as the previous disk type, expect:%s", whName, expectStr)
					}
				}
			}
		}
	}
	return nil
}

func resourceElasticClusterV2Create(ctx context.Context, d *schema.ResourceData, m interface{}) (diags diag.Diagnostics) {
	c := m.(*client.CelerdataClient)

	clusterAPI := cluster.NewClustersAPI(c)
	networkAPI := network.NewNetworkAPI(c)
	clusterName := d.Get("cluster_name").(string)

	clusterConf := &cluster.ClusterConf{
		ClusterName:        clusterName,
		Csp:                d.Get("csp").(string),
		Region:             d.Get("region").(string),
		ClusterType:        cluster.ClusterTypeElasic,
		Password:           d.Get("default_admin_password").(string),
		SslConnEnable:      true,
		NetIfaceId:         d.Get("network_id").(string),
		DeployCredlId:      d.Get("deployment_credential_id").(string),
		DataCredId:         d.Get("data_credential_id").(string),
		RunScriptsParallel: d.Get("run_scripts_parallel").(bool),
		QueryPort:          int32(d.Get("query_port").(int)),
		RunScriptsTimeout:  int32(d.Get("run_scripts_timeout").(int)),
	}

	netResp, err := networkAPI.GetNetwork(ctx, d.Get("network_id").(string))
	if err != nil {
		return diag.FromErr(err)
	}

	coordinatorNodeCount := d.Get("coordinator_node_count").(int)
	if netResp.Network.MultiAz && coordinatorNodeCount < 3 {
		return diag.FromErr(errors.New("in multi-AZ deployment mode, the number of coordinator nodes should be greater than or equal to 3"))
	}

	if v, ok := d.GetOk("resource_tags"); ok {
		rTags := v.(map[string]interface{})
		tags := make([]*cluster.Kv, 0, len(rTags))
		for k, v := range rTags {
			tags = append(tags, &cluster.Kv{Key: k, Value: v.(string)})
		}
		clusterConf.Tags = tags
	}

	if v, ok := d.GetOk("init_scripts"); ok {
		vL := v.(*schema.Set).List()
		scripts := make([]*cluster.Script, 0, len(vL))
		for _, v := range vL {
			s := v.(map[string]interface{})
			scripts = append(scripts, &cluster.Script{
				ScriptPath: s["script_path"].(string),
				LogsDir:    s["logs_dir"].(string),
			})
		}

		clusterConf.Scripts = scripts
	}

	if _, ok := d.GetOk("custom_ami"); ok {
		customAmi := &cluster.CustomAmi{
			AmiID: d.Get("custom_ami.0.ami").(string),
			OS:    d.Get("custom_ami.0.os").(string),
		}

		clusterConf.CustomAmi = customAmi
	}

	coordinatorItem := &cluster.ClusterItem{
		Type:         cluster.ClusterModuleTypeFE,
		Name:         "FE",
		Num:          uint32(d.Get("coordinator_node_count").(int)),
		InstanceType: d.Get("coordinator_node_size").(string),
		DiskInfo: &cluster.DiskInfo{
			Number:  1,
			PerSize: 150,
		},
	}
	if v, ok := d.GetOk("coordinator_node_volume_config"); ok {
		volumeConfig := v.([]interface{})[0].(map[string]interface{})
		diskInfo := coordinatorItem.DiskInfo
		if v, ok := volumeConfig["vol_size"]; ok {
			diskInfo.PerSize = uint64(v.(int))
		}
		if v, ok := volumeConfig["iops"]; ok {
			diskInfo.Iops = uint64(v.(int))
		}
		if v, ok := volumeConfig["throughput"]; ok {
			diskInfo.Throughput = uint64(v.(int))
		}
	}

	clusterConf.ClusterItems = append(clusterConf.ClusterItems, coordinatorItem)

	defaultWhMap := d.Get("default_warehouse").([]interface{})[0].(map[string]interface{})
	defaultWhMap["name"] = DEFAULT_WAREHOUSE_NAME

	normalWhMaps := make([]map[string]interface{}, 0)
	for _, wh := range d.Get("warehouse").([]interface{}) {
		whMap := wh.(map[string]interface{})
		whMap["name"] = strings.TrimSpace(whMap["name"].(string))
		normalWhMaps = append(normalWhMaps, whMap)
	}

	defaultWarehouseItem := &cluster.ClusterItem{
		Type:               cluster.ClusterModuleTypeWarehouse,
		Name:               defaultWhMap["name"].(string),
		Num:                uint32(defaultWhMap["compute_node_count"].(int)),
		InstanceType:       defaultWhMap["compute_node_size"].(string),
		DistributionPolicy: defaultWhMap["distribution_policy"].(string),
		SpecifyAZ:          defaultWhMap["specify_az"].(string),
		DiskInfo: &cluster.DiskInfo{
			Number:  2,
			PerSize: 100,
		},
	}

	if len(defaultWhMap["compute_node_volume_config"].([]interface{})) > 0 {
		diskInfo := defaultWarehouseItem.DiskInfo
		volumeConfig := defaultWhMap["compute_node_volume_config"].([]interface{})[0].(map[string]interface{})
		if v, ok := volumeConfig["vol_number"]; ok {
			diskInfo.Number = uint32(v.(int))
		}
		if v, ok := volumeConfig["vol_size"]; ok {
			diskInfo.PerSize = uint64(v.(int))
		}
		if v, ok := volumeConfig["iops"]; ok {
			diskInfo.Iops = uint64(v.(int))
		}
		if v, ok := volumeConfig["throughput"]; ok {
			diskInfo.Throughput = uint64(v.(int))
		}
	}

	clusterConf.ClusterItems = append(clusterConf.ClusterItems, defaultWarehouseItem)

	resp, err := clusterAPI.Deploy(ctx, &cluster.DeployReq{
		RequestId:   uuid.NewString(),
		ClusterConf: clusterConf,
	})
	if err != nil {
		return diag.FromErr(err)
	}
	log.Printf("[DEBUG] submit deploy succeeded, action id:%s cluster id:%s]", resp.ActionID, resp.ClusterID)

	clusterId := resp.ClusterID
	defaultWarehouseId := resp.DefaultWarehouseId
	d.SetId(clusterId)
	stateResp, err := WaitClusterStateChangeComplete(ctx, &waitStateReq{
		clusterAPI: clusterAPI,
		clusterID:  resp.ClusterID,
		actionID:   resp.ActionID,
		timeout:    common.DeployOrScaleClusterTimeout,
		pendingStates: []string{
			string(cluster.ClusterStateDeploying),
			string(cluster.ClusterStateScaling),
			string(cluster.ClusterStateResuming),
			string(cluster.ClusterStateSuspending),
			string(cluster.ClusterStateReleasing),
			string(cluster.ClusterStateUpdating),
		},
		targetStates: []string{
			string(cluster.ClusterStateRunning),
			string(cluster.ClusterStateAbnormal),
		},
	})
	if err != nil {
		diags = append(diags, diag.Diagnostic{
			Severity: diag.Warning,
			Summary:  "Operation state not complete",
			Detail:   fmt.Sprintf("waiting for cluster (%s) change complete failed errMsg: %s", d.Id(), err.Error()),
		})
		return diags
	}

	if stateResp.ClusterState == string(cluster.ClusterStateAbnormal) {
		d.SetId("")
		return diag.FromErr(errors.New(stateResp.AbnormalReason))
	}
	log.Printf("[DEBUG] deploy succeeded, action id:%s cluster id:%s]", resp.ActionID, resp.ClusterID)

	if v, ok := d.GetOk("coordinator_node_configs"); ok && len(d.Get("coordinator_node_configs").(map[string]interface{})) > 0 {
		configMap := v.(map[string]interface{})
		configs := make(map[string]string, 0)
		for k, v := range configMap {
			configs[k] = v.(string)
		}
		warnDiag := UpsertClusterConfig(ctx, clusterAPI, &cluster.UpsertClusterConfigReq{
			ClusterID:  resp.ClusterID,
			ConfigType: cluster.CustomConfigTypeFE,
			Configs:    configs,
		})
		if warnDiag != nil {
			return warnDiag
		}
	}

	if v, ok := defaultWhMap["compute_node_configs"]; ok && len(defaultWhMap["compute_node_configs"].(map[string]interface{})) > 0 {
		configMap := v.(map[string]interface{})
		configs := make(map[string]string, 0)
		for k, v := range configMap {
			configs[k] = v.(string)
		}
		warnDiag := UpsertClusterConfig(ctx, clusterAPI, &cluster.UpsertClusterConfigReq{
			ClusterID:   resp.ClusterID,
			ConfigType:  cluster.CustomConfigTypeBE,
			WarehouseID: defaultWarehouseId,
			Configs:     configs,
		})
		if warnDiag != nil {
			return warnDiag
		}
	}

	if v, ok := d.GetOk("ldap_ssl_certs"); ok {

		arr := v.(*schema.Set).List()
		sslCerts := make([]string, 0)
		for _, v := range arr {
			value := v.(string)
			sslCerts = append(sslCerts, value)
		}

		if len(sslCerts) > 0 {
			warningDiag := UpsertClusterLdapSslCert(ctx, clusterAPI, d.Id(), sslCerts, false)
			if warningDiag != nil {
				return warningDiag
			}
		}
	}

	if v, ok := d.GetOk("ranger_certs_dir"); ok {
		rangerCertsDirPath := v.(string)
		warningDiag := UpsertClusterRangerCert(ctx, clusterAPI, d.Id(), rangerCertsDirPath, false)
		if warningDiag != nil {
			return warningDiag
		}
	}

	if d.Get("idle_suspend_interval").(int) > 0 {
		enable := true
		clusterId := resp.ClusterID
		intervalTimeMills := uint64(d.Get("idle_suspend_interval").(int) * 60 * 1000)
		warningDiag := UpdateClusterIdleConfig(ctx, clusterAPI, clusterId, intervalTimeMills, enable)
		if warningDiag != nil {
			return warningDiag
		}
	}

	policyJson := defaultWhMap["auto_scaling_policy"].(string)
	if len(policyJson) > 0 {
		err := setWarehouseAutoScalingPolicy(ctx, clusterAPI, clusterId, defaultWarehouseId, policyJson)
		if err != nil {
			msg := fmt.Sprintf("Add warehouse auto-scaling configuration failed, errMsg:%s", err.Error())
			log.Printf("[ERROR] %s", msg)
			return diag.Diagnostics{
				diag.Diagnostic{
					Severity: diag.Warning,
					Summary:  fmt.Sprintf("Config warehouse[%s] auto-scaling configuration failed", DEFAULT_WAREHOUSE_NAME),
					Detail:   msg,
				},
			}
		}
	}

	// create normal warehouses
	for _, v := range normalWhMaps {
		errDiag := createWarehouse(ctx, clusterAPI, clusterId, v)
		if errDiag != nil {
			return diag.Diagnostics{
				diag.Diagnostic{
					Severity: diag.Warning,
					Summary:  fmt.Sprintf("Create warehouse[%s] failed. %s", v["name"].(string), errDiag[0].Summary),
					Detail:   errDiag[0].Detail,
				},
			}
		}
	}

	if d.Get("expected_cluster_state").(string) == string(cluster.ClusterStateSuspended) {
		warningDiag := UpdateClusterState(ctx, clusterAPI, d.Get("id").(string), string(cluster.ClusterStateRunning), string(cluster.ClusterStateSuspended))
		if warningDiag != nil {
			return warningDiag
		}
	}
	return resourceElasticClusterV2Read(ctx, d, m)
}

func resourceElasticClusterV2Read(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	c := m.(*client.CelerdataClient)

	clusterId := d.Id()
	clusterAPI := cluster.NewClustersAPI(c)
	log.Printf("[DEBUG] resourceElasticClusterV2Read cluster id:%s", clusterId)
	var diags diag.Diagnostics
	stateResp, err := WaitClusterStateChangeComplete(ctx, &waitStateReq{
		clusterAPI: clusterAPI,
		clusterID:  clusterId,
		timeout:    30 * time.Minute,
		pendingStates: []string{
			string(cluster.ClusterStateDeploying),
			string(cluster.ClusterStateScaling),
			string(cluster.ClusterStateResuming),
			string(cluster.ClusterStateSuspending),
			string(cluster.ClusterStateReleasing),
			string(cluster.ClusterStateUpdating),
		},
		targetStates: []string{
			string(cluster.ClusterStateRunning),
			string(cluster.ClusterStateSuspended),
			string(cluster.ClusterStateAbnormal),
			string(cluster.ClusterStateReleased),
		},
	})
	if err != nil {
		return diag.FromErr(fmt.Errorf("waiting for cluster (%s) change complete: %s", d.Id(), err))
	}

	if stateResp.ClusterState == string(cluster.ClusterStateReleased) {
		log.Printf("[WARN] Cluster (%s) not found, removing from state", d.Id())
		d.SetId("")
		return diags
	}

	log.Printf("[DEBUG] get cluster, cluster[%s]", clusterId)
	resp, err := clusterAPI.Get(ctx, &cluster.GetReq{ClusterID: clusterId})
	if err != nil {
		if !d.IsNewResource() && status.Code(err) == codes.NotFound {
			log.Printf("[WARN] Cluster (%s) not found, removing from state", d.Id())
			d.SetId("")
			return diags
		}
		return diag.FromErr(err)
	}

	jsonBytes, err := json.Marshal(resp.Cluster)
	if err != nil {
		log.Printf("[Error] marshaling to JSON:%s [DEBUG] get cluster, resp:%+v", err.Error(), resp.Cluster)
	} else {
		log.Printf("[DEBUG] get cluster, resp:%s", string(jsonBytes))
	}

	coordinatorNodeConfigsResp, err := clusterAPI.GetCustomConfig(ctx, &cluster.ListCustomConfigReq{
		ClusterID:  clusterId,
		ConfigType: cluster.CustomConfigTypeFE,
	})
	if err != nil {
		log.Printf("[ERROR] query cluster custom config failed, err:%+v", err)
		return diag.FromErr(err)
	}

	d.Set("cluster_state", string(resp.Cluster.ClusterState))
	d.Set("expected_cluster_state", string(resp.Cluster.ClusterState))
	d.Set("cluster_name", resp.Cluster.ClusterName)
	d.Set("data_credential_id", resp.Cluster.DataCredID)
	d.Set("network_id", resp.Cluster.NetIfaceID)
	d.Set("deployment_credential_id", resp.Cluster.DeployCredID)
	d.Set("coordinator_node_size", resp.Cluster.FeModule.InstanceType)
	d.Set("coordinator_node_count", int(resp.Cluster.FeModule.Num))
	d.Set("free_tier", resp.Cluster.FreeTier)
	d.Set("query_port", resp.Cluster.QueryPort)
	d.Set("idle_suspend_interval", resp.Cluster.IdleSuspendInterval)
	if resp.Cluster.CustomAmi != nil {
		d.Set("custom_ami", []interface{}{
			map[string]interface{}{
				"ami": resp.Cluster.CustomAmi.AmiID,
				"os":  resp.Cluster.CustomAmi.OS,
			},
		})
	}

	d.Set("csp", resp.Cluster.Csp)
	d.Set("region", resp.Cluster.Region)

	csp := d.Get("csp").(string)
	tags := make(map[string]string)
	for k, v := range resp.Cluster.Tags {
		if !IsInternalTagKeys(csp, k) {
			tags[k] = v
		}
	}
	d.Set("resource_tags", tags)
	if len(resp.Cluster.LdapSslCerts) > 0 {
		d.Set("ldap_ssl_certs", resp.Cluster.LdapSslCerts)
	}
	if len(resp.Cluster.RangerCertsDirPath) > 0 {
		d.Set("ranger_certs_dir", resp.Cluster.RangerCertsDirPath)
	}

	default_warehouses := make([]map[string]interface{}, 0)
	normal_warehouses := make([]map[string]interface{}, 0)

	warehouseExternalInfo := make(map[string]interface{}, 0)

	for _, v := range resp.Cluster.Warehouses {
		if v.Deleted {
			continue
		}
		warehouseId := v.Id
		warehouseName := v.Name
		isDefaultWarehouse := v.IsDefaultWarehouse

		whMap := make(map[string]interface{}, 0)
		whMap["name"] = warehouseName
		whMap["compute_node_size"] = v.Module.InstanceType
		whMap["compute_node_count"] = v.Module.Num
		whMap["distribution_policy"] = v.DistributionPolicyStr
		whMap["specify_az"] = v.SpecifyAZ

		whModule := v.Module
		if !whModule.IsInstanceStore {
			computeNodeVolumeConfig := make(map[string]interface{}, 0)
			computeNodeVolumeConfig["vol_number"] = whModule.VmVolNum
			computeNodeVolumeConfig["vol_size"] = whModule.VmVolSizeGB
			computeNodeVolumeConfig["iops"] = whModule.Iops
			computeNodeVolumeConfig["throughput"] = whModule.Throughput
			whMap["compute_node_volume_config"] = []interface{}{computeNodeVolumeConfig}
		}

		autoScalingConfigResp, err := clusterAPI.GetWarehouseAutoScalingConfig(ctx, &cluster.GetWarehouseAutoScalingConfigReq{
			WarehouseId: warehouseId,
		})
		if err != nil {
			log.Printf("[ERROR] Query warehouse auto scaling config failed, warehouseId:%s", warehouseId)
			return diag.Diagnostics{
				diag.Diagnostic{
					Severity: diag.Warning,
					Summary:  fmt.Sprintf("Failed to get warehouse auto scaling config, warehouseId:[%s] ", warehouseId),
					Detail:   err.Error(),
				},
			}
		}

		policy := autoScalingConfigResp.Policy
		if policy != nil && policy.State {
			bytes, _ := json.Marshal(policy)
			whMap["auto_scaling_policy"] = string(bytes)
		}

		computeNodeConfigsResp, err := clusterAPI.GetCustomConfig(ctx, &cluster.ListCustomConfigReq{
			ClusterID:   clusterId,
			ConfigType:  cluster.CustomConfigTypeBE,
			WarehouseID: warehouseId,
		})
		if err != nil {
			log.Printf("[ERROR] query cluster custom config failed, err:%+v", err)
			return diag.FromErr(err)
		}
		if len(computeNodeConfigsResp.Configs) > 0 {
			whMap["compute_node_configs"] = computeNodeConfigsResp.Configs
		}

		if !isDefaultWarehouse {
			whMap["expected_state"] = v.State
			idleConfigResp, err := clusterAPI.GetWarehouseIdleConfig(ctx, &cluster.GetWarehouseIdleConfigReq{
				WarehouseId: warehouseId,
			})
			if err != nil {
				log.Printf("[ERROR] Query warehouse idle suspend config failed, warehouseId:%s", warehouseId)
				return diag.Diagnostics{
					diag.Diagnostic{
						Severity: diag.Warning,
						Summary:  fmt.Sprintf("Failed to get warehouse idle suspend config, warehouseId:[%s] ", warehouseId),
						Detail:   err.Error(),
					},
				}
			}
			idleConfig := idleConfigResp.Config
			if idleConfig != nil && idleConfig.State {
				whMap["idle_suspend_interval"] = idleConfig.IntervalMs / 1000 / 60
			} else {
				whMap["idle_suspend_interval"] = 0
			}
			normal_warehouses = append(normal_warehouses, whMap)
		} else {
			default_warehouses = append(default_warehouses, whMap)
		}

		whInfo := &cluster.WarehouseExternalInfo{
			Id:                 warehouseId,
			IsInstanceStore:    v.Module.IsInstanceStore,
			IsDefaultWarehouse: isDefaultWarehouse,
		}
		whInfoBytes, _ := json.Marshal(whInfo)
		warehouseExternalInfo[warehouseName] = string(whInfoBytes)
	}

	configuredWH := d.Get("default_warehouse").([]interface{})[0].(map[string]interface{})
	if rawVol, ok := configuredWH["compute_node_volume_config"]; !ok || len(rawVol.([]interface{})) == 0 {
		default_warehouses[0]["compute_node_volume_config"] = nil
	} else {
		rawMap := rawVol.([]interface{})[0].(map[string]interface{})
		if rawMap["iops"] == nil {
			default_warehouses[0]["compute_node_volume_config"].(map[string]interface{})["iops"] = nil
		}
		if rawMap["throughput"] == nil {
			default_warehouses[0]["compute_node_volume_config"].(map[string]interface{})["throughput"] = nil
		}
	}

	if len(normal_warehouses) > 0 {
		configuredWHs := d.Get("warehouse").([]interface{})
		configuredWHsMap := make(map[string]map[string]interface{}, 0)

		for _, c := range configuredWHs {
			cwh := c.(map[string]interface{})
			if len(cwh["compute_node_volume_config"].([]interface{})) > 0 {
				configuredWHsMap[cwh["name"].(string)] = cwh["compute_node_volume_config"].([]interface{})[0].(map[string]interface{})
			}
		}
		for _, wh := range normal_warehouses {
			whName := wh["name"].(string)
			if v, ok := configuredWHsMap[whName]; !ok || v == nil {
				wh["compute_node_volume_config"] = nil
			} else {
				if v["iops"] == nil {
					wh["compute_node_volume_config"].([]interface{})[0].(map[string]interface{})["iops"] = nil
				}
				if v["throughput"] == nil {
					wh["compute_node_volume_config"].([]interface{})[0].(map[string]interface{})["throughput"] = nil
				}
			}
		}
	}

	d.Set("default_warehouse", default_warehouses)
	d.Set("warehouse", normal_warehouses)
	d.Set("warehouse_external_info", warehouseExternalInfo)

	if len(coordinatorNodeConfigsResp.Configs) > 0 {
		d.Set("coordinator_node_configs", coordinatorNodeConfigsResp.Configs)
	}

	feModule := resp.Cluster.FeModule
	if !feModule.IsInstanceStore {
		feVolumeConfig := make(map[string]interface{}, 0)
		feVolumeConfig["vol_size"] = feModule.VmVolSizeGB
		feVolumeConfig["iops"] = feModule.Iops
		feVolumeConfig["throughput"] = feModule.Throughput
		if v, ok := d.GetOk("coordinator_node_volume_config"); ok && v != nil {
			if v.([]interface{})[0].(map[string]interface{})["iops"] == nil {
				feVolumeConfig["iops"] = nil
			}
			if v.([]interface{})[0].(map[string]interface{})["throughput"] == nil {
				feVolumeConfig["throughput"] = nil
			}
			d.Set("coordinator_node_volume_config", []interface{}{feVolumeConfig})
		}
	}

	if len(coordinatorNodeConfigsResp.Configs) > 0 {
		d.Set("coordinator_node_configs", coordinatorNodeConfigsResp.Configs)
	}

	log.Printf("[DEBUG] get cluster, warehouses:%+v", resp.Cluster.Warehouses)

	return diags
}

func resourceElasticClusterV2Delete(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	c := m.(*client.CelerdataClient)

	clusterId := d.Id()
	clusterAPI := cluster.NewClustersAPI(c)
	log.Printf("[DEBUG] resourceElasticClusterV2Delete cluster id:%s", clusterId)
	var diags diag.Diagnostics

	_, err := WaitClusterStateChangeComplete(ctx, &waitStateReq{
		clusterAPI: clusterAPI,
		clusterID:  clusterId,
		timeout:    30 * time.Minute,
		pendingStates: []string{
			string(cluster.ClusterStateDeploying),
			string(cluster.ClusterStateScaling),
			string(cluster.ClusterStateResuming),
			string(cluster.ClusterStateSuspending),
			string(cluster.ClusterStateReleasing),
			string(cluster.ClusterStateUpdating),
		},
		targetStates: []string{
			string(cluster.ClusterStateRunning),
			string(cluster.ClusterStateSuspended),
			string(cluster.ClusterStateAbnormal),
			string(cluster.ClusterStateReleased),
		},
	})
	if err != nil {
		return diag.FromErr(fmt.Errorf("waiting for Cluster (%s) delete: %s", d.Id(), err))
	}

	log.Printf("[DEBUG] release cluster, cluster id:%s", clusterId)
	resp, err := clusterAPI.Release(ctx, &cluster.ReleaseReq{ClusterID: clusterId})
	if err != nil {
		return diag.FromErr(err)
	}

	log.Printf("[DEBUG] wait release cluster, cluster id:%s action id:%s", clusterId, resp.ActionID)
	stateResp, err := WaitClusterStateChangeComplete(ctx, &waitStateReq{
		clusterAPI: clusterAPI,
		actionID:   resp.ActionID,
		clusterID:  clusterId,
		timeout:    30 * time.Minute,
		pendingStates: []string{
			string(cluster.ClusterStateReleasing),
			string(cluster.ClusterStateRunning),
			string(cluster.ClusterStateSuspended),
			string(cluster.ClusterStateAbnormal),
			string(cluster.ClusterStateUpdating),
		},
		targetStates: []string{string(cluster.ClusterStateReleased), string(cluster.ClusterStateAbnormal)},
	})
	if err != nil {
		return diag.FromErr(fmt.Errorf("waiting for Cluster (%s) delete: %s", d.Id(), err))
	}

	if stateResp.ClusterState == string(cluster.ClusterStateAbnormal) {
		d.SetId("")
		return diag.FromErr(fmt.Errorf("release cluster failed: %s, we have successfully released your cluster, but cloud resources may not be released. Please release cloud resources manually according to the email", stateResp.AbnormalReason))
	}

	// d.SetId("") is automatically called assuming delete returns no errors, but
	// it is added here for explicitness.
	d.SetId("")
	return diags
}

func elasticClusterV2NeedUnlock(d *schema.ResourceData) bool {
	result := !d.IsNewResource() && d.Get("free_tier").(bool) &&
		(d.HasChange("coordinator_node_size") || d.HasChange("coordinator_node_count"))

	if !result && (d.HasChange("warehouse") || d.HasChange("default_warehouse")) {
		result = true
	}

	return result
}

func resourceElasticClusterV2Update(ctx context.Context, d *schema.ResourceData, m interface{}) diag.Diagnostics {
	var immutableFields = []string{"csp", "region", "cluster_name", "default_admin_password", "data_credential_id", "deployment_credential_id", "network_id", "query_port"}
	for _, f := range immutableFields {
		if d.HasChange(f) && !d.IsNewResource() {
			return diag.FromErr(fmt.Errorf("the `%s` field is not allowed to be modified", f))
		}
	}

	c := m.(*client.CelerdataClient)

	// Warning or errors can be collected in a slice type
	clusterId := d.Id()
	clusterAPI := cluster.NewClustersAPI(c)
	log.Printf("[DEBUG] resourceElasticClusterV2Update cluster id:%s", clusterId)
	stateResp, err := WaitClusterStateChangeComplete(ctx, &waitStateReq{
		clusterAPI: clusterAPI,
		clusterID:  clusterId,
		timeout:    30 * time.Minute,
		pendingStates: []string{
			string(cluster.ClusterStateDeploying),
			string(cluster.ClusterStateScaling),
			string(cluster.ClusterStateResuming),
			string(cluster.ClusterStateSuspending),
			string(cluster.ClusterStateReleasing),
			string(cluster.ClusterStateUpdating),
		},
		targetStates: []string{
			string(cluster.ClusterStateRunning),
			string(cluster.ClusterStateSuspended),
			string(cluster.ClusterStateAbnormal),
			string(cluster.ClusterStateReleased),
		},
	})
	if err != nil {
		return diag.FromErr(fmt.Errorf("waiting for cluster (%s) change complete: %s", d.Id(), err))
	}

	if stateResp.ClusterState == string(cluster.ClusterStateReleased) {
		log.Printf("[WARN] cluster (%s) not found", clusterId)
		d.SetId("")
		return diag.FromErr(fmt.Errorf("cluster (%s) not found", clusterId))
	}

	if stateResp.ClusterState == string(cluster.ClusterStateAbnormal) {
		return diag.FromErr(errors.New(stateResp.AbnormalReason))
	}

	if d.HasChange("idle_suspend_interval") && !d.IsNewResource() {
		o, n := d.GetChange("idle_suspend_interval")

		v := n.(int)
		enable := n.(int) > 0
		if !enable {
			v = o.(int)
		}
		intervalTimeMills := uint64(v * 60 * 1000)
		errDiag := UpdateClusterIdleConfig(ctx, clusterAPI, clusterId, intervalTimeMills, enable)
		if errDiag != nil {
			return errDiag
		}
	}

	// Warning or errors can be collected in a slice type
	var diags diag.Diagnostics
	if needResume(d) {
		o, n := d.GetChange("expected_cluster_state")
		errDiag := UpdateClusterState(ctx, clusterAPI, d.Get("id").(string), o.(string), n.(string))
		if errDiag != nil {
			return errDiag
		}
	}

	if d.HasChange("ldap_ssl_certs") && !d.IsNewResource() {
		sslCerts := make([]string, 0)
		if v, ok := d.GetOk("ldap_ssl_certs"); ok {
			arr := v.(*schema.Set).List()
			for _, v := range arr {
				value := v.(string)
				sslCerts = append(sslCerts, value)
			}
		}
		warningDiag := UpsertClusterLdapSslCert(ctx, clusterAPI, d.Id(), sslCerts, true)
		if warningDiag != nil {
			return warningDiag
		}
	}

	if d.HasChange("resource_tags") && !d.IsNewResource() {
		_, n := d.GetChange("resource_tags")

		nTags := n.(map[string]interface{})
		tags := make(map[string]string, len(nTags))
		for k, v := range nTags {
			tags[k] = v.(string)
		}
		err := clusterAPI.UpdateResourceTags(ctx, &cluster.UpdateResourceTagsReq{
			ClusterId: clusterId,
			Tags:      tags,
		})
		if err != nil {
			return diag.FromErr(fmt.Errorf("cluster (%s) failed to update resource tags: %s", d.Id(), err.Error()))
		}
	}

	if d.HasChange("init_scripts") && !d.IsNewResource() {
		_, n := d.GetChange("init_scripts")
		vL := n.(*schema.Set).List()
		scripts := make([]*cluster.Script, 0, len(vL))
		for _, v := range vL {
			s := v.(map[string]interface{})
			scripts = append(scripts, &cluster.Script{
				ScriptPath: s["script_path"].(string),
				LogsDir:    s["logs_dir"].(string),
			})
		}
		err := clusterAPI.UpdateDeploymentScripts(ctx, &cluster.UpdateDeploymentScriptsReq{
			ClusterId: clusterId,
			Scripts:   scripts,
			Parallel:  d.Get("run_scripts_parallel").(bool),
			Timeout:   int32(d.Get("run_scripts_timeout").(int)),
		})
		if err != nil {
			return diag.FromErr(fmt.Errorf("failed to update cluster(%s) init-scripts: %s", d.Id(), err.Error()))
		}
	}

	if d.HasChange("ranger_certs_dir") && !d.IsNewResource() {
		rangerCertsDirPath := d.Get("ranger_certs_dir").(string)
		warningDiag := UpsertClusterRangerCert(ctx, clusterAPI, d.Id(), rangerCertsDirPath, true)
		if warningDiag != nil {
			return warningDiag
		}
	}

	if elasticClusterV2NeedUnlock(d) {
		err := clusterAPI.UnlockFreeTier(ctx, clusterId)
		if err != nil {
			return diag.FromErr(fmt.Errorf("cluster (%s) failed to unlock free tier: %s", d.Id(), err.Error()))
		}
	}

	if d.HasChange("coordinator_node_size") && !d.IsNewResource() {
		_, n := d.GetChange("coordinator_node_size")
		resp, err := clusterAPI.ScaleUp(ctx, &cluster.ScaleUpReq{
			RequestId:  uuid.NewString(),
			ClusterId:  clusterId,
			ModuleType: cluster.ClusterModuleTypeFE,
			VmCategory: n.(string),
		})
		if err != nil {
			return diag.FromErr(fmt.Errorf("cluster (%s) failed to scale up fe nodes: %s", d.Id(), err))
		}

		stateResp, err := WaitClusterStateChangeComplete(ctx, &waitStateReq{
			clusterAPI:    clusterAPI,
			actionID:      resp.ActionId,
			clusterID:     clusterId,
			timeout:       common.DeployOrScaleClusterTimeout,
			pendingStates: []string{string(cluster.ClusterStateScaling)},
			targetStates:  []string{string(cluster.ClusterStateRunning), string(cluster.ClusterStateAbnormal)},
		})
		if err != nil {
			return diag.FromErr(fmt.Errorf("waiting for cluster (%s) running %s", d.Id(), err))
		}

		if stateResp.ClusterState == string(cluster.ClusterStateAbnormal) {
			return diag.FromErr(errors.New(stateResp.AbnormalReason))
		}
	}

	if d.HasChange("coordinator_node_count") && !d.IsNewResource() {
		o, n := d.GetChange("coordinator_node_count")

		var actionID string
		if n.(int) > o.(int) {
			resp, err := clusterAPI.ScaleOut(ctx, &cluster.ScaleOutReq{
				RequestId:  uuid.NewString(),
				ClusterId:  clusterId,
				ModuleType: cluster.ClusterModuleTypeFE,
				ExpectNum:  int32(n.(int)),
			})
			if err != nil {
				return diag.FromErr(fmt.Errorf("cluster (%s) failed to scale out fe nodes: %s", d.Id(), err))
			}

			actionID = resp.ActionId
		} else if n.(int) < o.(int) {
			resp, err := clusterAPI.ScaleIn(ctx, &cluster.ScaleInReq{
				RequestId:  uuid.NewString(),
				ClusterId:  clusterId,
				ModuleType: cluster.ClusterModuleTypeFE,
				ExpectNum:  int32(n.(int)),
			})
			if err != nil {
				return diag.FromErr(fmt.Errorf("cluster (%s) failed to scale in fe nodes: %s", d.Id(), err))
			}

			actionID = resp.ActionId
		}

		stateResp, err := WaitClusterStateChangeComplete(ctx, &waitStateReq{
			clusterAPI:    clusterAPI,
			actionID:      actionID,
			clusterID:     clusterId,
			timeout:       common.DeployOrScaleClusterTimeout,
			pendingStates: []string{string(cluster.ClusterStateScaling)},
			targetStates:  []string{string(cluster.ClusterStateRunning), string(cluster.ClusterStateAbnormal)},
		})
		if err != nil {
			return diag.FromErr(fmt.Errorf("waiting for cluster (%s) running: %s", d.Id(), err))
		}

		if stateResp.ClusterState == string(cluster.ClusterStateAbnormal) {
			return diag.FromErr(errors.New(stateResp.AbnormalReason))
		}
	}

	if d.HasChange("coordinator_node_volume_config") {
		o, n := d.GetChange("coordinator_node_volume_config")

		oldVolumeConfig, newVolumeConfig := cluster.DefaultFeVolumeMap(), cluster.DefaultFeVolumeMap()

		if len(o.([]interface{})) > 0 {
			oldVolumeConfig = o.([]interface{})[0].(map[string]interface{})
		}
		if len(n.([]interface{})) > 0 {
			newVolumeConfig = n.([]interface{})[0].(map[string]interface{})
		}

		nodeType := cluster.ClusterModuleTypeFE
		req := &cluster.ModifyClusterVolumeReq{
			ClusterId: clusterId,
			Type:      nodeType,
		}

		if v, ok := newVolumeConfig["vol_size"]; ok && v != oldVolumeConfig["vol_size"] {
			req.VmVolSize = int64(v.(int))
		}
		if v, ok := newVolumeConfig["iops"]; ok && v != oldVolumeConfig["iops"] {
			req.Iops = int64(v.(int))
		}
		if v, ok := newVolumeConfig["throughput"]; ok && v != oldVolumeConfig["throughput"] {
			req.Throughput = int64(v.(int))
		}

		log.Printf("[DEBUG] modify cluster volume detail, req:%+v", req)
		resp, err := clusterAPI.ModifyClusterVolume(ctx, req)
		if err != nil {
			log.Printf("[ERROR] modify cluster volume detail failed, err:%+v", err)
			return diag.FromErr(err)
		}

		infraActionId := resp.ActionID
		if len(infraActionId) > 0 {
			infraActionResp, err := WaitClusterInfraActionStateChangeComplete(ctx, &waitStateReq{
				clusterAPI: clusterAPI,
				clusterID:  clusterId,
				actionID:   infraActionId,
				timeout:    30 * time.Minute,
				pendingStates: []string{
					string(cluster.ClusterInfraActionStatePending),
					string(cluster.ClusterInfraActionStateOngoing),
				},
				targetStates: []string{
					string(cluster.ClusterInfraActionStateSucceeded),
					string(cluster.ClusterInfraActionStateCompleted),
					string(cluster.ClusterInfraActionStateFailed),
				},
			})

			summary := fmt.Sprintf("Modify %s node volume detail of the cluster[%s] failed", nodeType, clusterId)
			if err != nil {
				return diag.Diagnostics{
					diag.Diagnostic{
						Severity: diag.Error,
						Summary:  summary,
						Detail:   err.Error(),
					},
				}
			}

			if infraActionResp.InfraActionState == string(cluster.ClusterInfraActionStateFailed) {
				return diag.Diagnostics{
					diag.Diagnostic{
						Severity: diag.Error,
						Summary:  summary,
						Detail:   infraActionResp.ErrMsg,
					},
				}
			}
		}
	}

	if d.HasChange("coordinator_node_configs") {
		configMap := d.Get("coordinator_node_configs").(map[string]interface{})
		configs := make(map[string]string, 0)
		for k, v := range configMap {
			configs[k] = v.(string)
		}
		warnDiag := UpsertClusterConfig(ctx, clusterAPI, &cluster.UpsertClusterConfigReq{
			ClusterID:  clusterId,
			ConfigType: cluster.CustomConfigTypeFE,
			Configs:    configs,
		})
		if warnDiag != nil {
			return warnDiag
		}
	}

	if d.HasChange("default_warehouse") {
		o, n := d.GetChange("default_warehouse")
		oldWh := o.([]interface{})[0].(map[string]interface{})
		newWh := n.([]interface{})[0].(map[string]interface{})
		whExternalInfoMap := d.Get("warehouse_external_info").(map[string]interface{})

		// modified
		whExternalInfoStr := whExternalInfoMap[DEFAULT_WAREHOUSE_NAME].(string)
		whExternalInfo := &cluster.WarehouseExternalInfo{}
		json.Unmarshal([]byte(whExternalInfoStr), whExternalInfo)
		diags := updateWarehouse(ctx, &UpdateWarehouseReq{
			d:              d,
			clusterAPI:     clusterAPI,
			clusterId:      clusterId,
			oldParamMap:    oldWh,
			newParamMap:    newWh,
			whExternalInfo: whExternalInfo,
		})
		if diags != nil {
			return diags
		}
	}

	if d.HasChange("warehouse") {
		o, n := d.GetChange("warehouse")
		old := o.([]interface{})
		new := n.([]interface{})
		whExternalInfoMap := d.Get("warehouse_external_info").(map[string]interface{})

		oldWhMap := make(map[string]map[string]interface{})
		for _, v := range old {
			whMap := v.(map[string]interface{})
			oldWhMap[whMap["name"].(string)] = whMap
		}
		newWhMap := make(map[string]map[string]interface{})
		for _, v := range new {
			whMap := v.(map[string]interface{})
			newWhMap[whMap["name"].(string)] = whMap
		}

		for _, v := range new {
			newWh := v.(map[string]interface{})
			whName := newWh["name"].(string)
			if oldWh, ok := oldWhMap[whName]; ok {
				// modified
				whExternalInfoStr := whExternalInfoMap[whName].(string)
				whExternalInfo := &cluster.WarehouseExternalInfo{}
				json.Unmarshal([]byte(whExternalInfoStr), whExternalInfo)
				diags := updateWarehouse(ctx, &UpdateWarehouseReq{
					d:              d,
					clusterAPI:     clusterAPI,
					clusterId:      clusterId,
					oldParamMap:    oldWh,
					newParamMap:    newWh,
					whExternalInfo: whExternalInfo,
				})
				if diags != nil {
					return diags
				}
			} else {
				// added
				diags := createWarehouse(ctx, clusterAPI, clusterId, newWh)
				if diags != nil {
					return diags
				}
			}
		}

		for _, v := range old {
			oldWh := v.(map[string]interface{})
			whName := oldWh["name"].(string)
			if _, ok := newWhMap[whName]; !ok {
				// removed
				whExternalInfoStr := whExternalInfoMap[whName].(string)
				whExternalInfo := &cluster.WarehouseExternalInfo{}
				json.Unmarshal([]byte(whExternalInfoStr), whExternalInfo)
				whId := whExternalInfo.Id
				diags := DeleteWarehouse(ctx, clusterAPI, clusterId, whId)
				if diags != nil {
					return diags
				}
			}
		}
	}

	if needSuspend(d) {
		o, n := d.GetChange("expected_cluster_state")
		errDiag := UpdateClusterState(ctx, clusterAPI, d.Get("id").(string), o.(string), n.(string))
		if errDiag != nil {
			return errDiag
		}
	}

	if d.HasChange("custom_ami") && !d.IsNewResource() {
		o, _ := d.GetChange("custom_ami")
		if len(o.([]interface{})) == 0 {
			return diag.FromErr(errors.New("custom ami can only be specified when creating cluster"))
		}

		if d.HasChange("custom_ami.0.os") && !d.IsNewResource() {
			oOs, nOs := d.GetChange("custom_ami.0.os")
			if len(oOs.(string)) > 0 && oOs.(string) != nOs.(string) {
				return diag.FromErr(errors.New("custom ami os can not be changed"))
			}
		}

		if d.HasChange("custom_ami.0.ami") && !d.IsNewResource() {
			_, nAmi := d.GetChange("custom_ami.0.ami")
			_, nOs := d.GetChange("custom_ami.0.os")

			clusterResp, err := clusterAPI.Get(ctx, &cluster.GetReq{ClusterID: clusterId})
			if err != nil {
				return diag.FromErr(err)
			}

			if !IsAllRunning(clusterResp.Cluster) {
				return diag.FromErr(errors.New("custom ami can only be upgraded when the cluster and all warehouse states are running"))
			}

			for _, wh := range clusterResp.Cluster.Warehouses {
				err := upgradeAMI(ctx, clusterAPI, &cluster.UpgradeAMIReq{
					ClusterId:   clusterId,
					Os:          nOs.(string),
					Ami:         nAmi.(string),
					WarehouseId: wh.Id,
					ModuleType:  cluster.ClusterModuleTypeWarehouse,
				})
				if err != nil {
					return diag.FromErr(err)
				}
			}

			err = upgradeAMI(ctx, clusterAPI, &cluster.UpgradeAMIReq{
				ClusterId:  clusterId,
				Os:         nOs.(string),
				Ami:        nAmi.(string),
				ModuleType: cluster.ClusterModuleTypeFE,
			})
			if err != nil {
				return diag.FromErr(err)
			}
		}
	}

	return diags
}

func upgradeAMI(ctx context.Context, clusterAPI cluster.IClusterAPI, req *cluster.UpgradeAMIReq) error {
	resp, err := clusterAPI.UpgradeAMI(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to update custom ami, %s. %s", err.Error(), req)
	}

	infraActionResp, err := WaitClusterInfraActionStateChangeComplete(ctx, &waitStateReq{
		clusterAPI: clusterAPI,
		clusterID:  req.ClusterId,
		actionID:   resp.InfraActionId,
		timeout:    common.DeployOrScaleClusterTimeout,
		pendingStates: []string{
			string(cluster.ClusterInfraActionStatePending),
			string(cluster.ClusterInfraActionStateOngoing),
		},
		targetStates: []string{
			string(cluster.ClusterInfraActionStateSucceeded),
			string(cluster.ClusterInfraActionStateCompleted),
			string(cluster.ClusterInfraActionStateFailed),
		},
	})
	if err != nil {
		return fmt.Errorf("failed to wait upgrade ami, %s. action:%s,%s", err.Error(), resp.InfraActionId, req)
	}

	if infraActionResp.InfraActionState == string(cluster.ClusterInfraActionStateFailed) {
		return fmt.Errorf("failed to wait upgrade ami, %s. action:%s,%s", infraActionResp.ErrMsg, resp.InfraActionId, req)
	}

	return nil
}

func setWarehouseAutoScalingPolicy(ctx context.Context, clusterAPI cluster.IClusterAPI, clusterId, warehouseId, policyJson string) error {

	if len(policyJson) > 0 {
		autoScalingConfig := &cluster.WarehouseAutoScalingConfig{}
		json.Unmarshal([]byte(policyJson), autoScalingConfig)
		req := &cluster.SaveWarehouseAutoScalingConfigReq{
			ClusterId:                  clusterId,
			WarehouseId:                warehouseId,
			WarehouseAutoScalingConfig: *autoScalingConfig,
			State:                      true,
		}
		_, err := clusterAPI.SaveWarehouseAutoScalingConfig(ctx, req)
		return err
	}
	return nil
}

func createWarehouse(ctx context.Context, clusterAPI cluster.IClusterAPI, clusterId string, whParamMap map[string]interface{}) diag.Diagnostics {

	warehouseName := whParamMap["name"].(string)

	diskNumber := 2
	perDiskSize := 100
	iops := 0
	throughput := 0
	if len(whParamMap["compute_node_volume_config"].([]interface{})) > 0 {
		volumeConfig := whParamMap["compute_node_volume_config"].([]interface{})[0].(map[string]interface{})
		if v, ok := volumeConfig["vol_number"]; ok {
			diskNumber = v.(int)
		}
		if v, ok := volumeConfig["vol_size"]; ok {
			perDiskSize = v.(int)
		}
		if v, ok := volumeConfig["iops"]; ok {
			iops = v.(int)
		}
		if v, ok := volumeConfig["throughput"]; ok {
			throughput = v.(int)
		}
	}

	req := &cluster.CreateWarehouseReq{
		ClusterId:          clusterId,
		Name:               warehouseName,
		VmCate:             whParamMap["compute_node_size"].(string),
		VmNum:              int32(whParamMap["compute_node_count"].(int)),
		VolumeSizeGB:       int64(perDiskSize),
		VolumeNum:          int32(diskNumber),
		Iops:               int64(iops),
		Throughput:         int64(throughput),
		DistributionPolicy: whParamMap["distribution_policy"].(string),
		SpecifyAZ:          whParamMap["specify_az"].(string),
	}

	log.Printf("[DEBUG] Create warehouse, req:%+v", req)
	resp, err := clusterAPI.CreateWarehouse(ctx, req)
	if err != nil {
		log.Printf("[ERROR] Create warehouse failed, err:%+v", err)
		return diag.FromErr(err)
	}
	log.Printf("[DEBUG] Create warehouse, resp:%+v", resp)

	warehouseId := resp.WarehouseId
	infraActionId := resp.ActionID
	if len(infraActionId) > 0 {
		stateResp, err := WaitClusterStateChangeComplete(ctx, &waitStateReq{
			clusterAPI: clusterAPI,
			clusterID:  clusterId,
			actionID:   resp.ActionID,
			timeout:    common.DeployOrScaleClusterTimeout,
			pendingStates: []string{
				string(cluster.ClusterStateDeploying),
				string(cluster.ClusterStateScaling),
				string(cluster.ClusterStateResuming),
				string(cluster.ClusterStateSuspending),
				string(cluster.ClusterStateReleasing),
				string(cluster.ClusterStateUpdating),
			},
			targetStates: []string{
				string(cluster.ClusterStateRunning),
				string(cluster.ClusterStateAbnormal),
			},
		})

		if err != nil {
			summary := fmt.Sprintf("create warehouse[%s] of the cluster[%s] failed, errMsg:%s", warehouseName, clusterId, err.Error())
			return diag.FromErr(fmt.Errorf("%s", summary))
		}

		if stateResp.ClusterState == string(cluster.ClusterStateAbnormal) {
			return diag.FromErr(errors.New(stateResp.AbnormalReason))
		}
	}

	if v, ok := whParamMap["auto_scaling_policy"]; ok {
		policyJson := v.(string)
		err := setWarehouseAutoScalingPolicy(ctx, clusterAPI, clusterId, warehouseId, policyJson)
		if err != nil {
			msg := fmt.Sprintf("Add warehouse auto-scaling configuration failed, errMsg:%s", err.Error())
			log.Printf("[ERROR] %s", msg)
			return diag.Diagnostics{
				diag.Diagnostic{
					Severity: diag.Warning,
					Summary:  fmt.Sprintf("Config warehouse[%s] auto-scaling configuration failed", warehouseName),
					Detail:   msg,
				},
			}
		}
	}

	if v, ok := whParamMap["compute_node_configs"]; ok && len(whParamMap["compute_node_configs"].(map[string]interface{})) > 0 {
		configMap := v.(map[string]interface{})
		configs := make(map[string]string, len(configMap))
		for k, v := range configMap {
			configs[k] = v.(string)
		}
		warnDiag := UpsertClusterConfig(ctx, clusterAPI, &cluster.UpsertClusterConfigReq{
			ClusterID:   clusterId,
			ConfigType:  cluster.CustomConfigTypeBE,
			WarehouseID: warehouseId,
			Configs:     configs,
		})
		if warnDiag != nil {
			return warnDiag
		}
	}

	expectedState := whParamMap["expected_state"].(string)
	if expectedState == string(cluster.ClusterStateSuspended) {
		summary := fmt.Sprintf("Suspend warehouse[%s] failed", warehouseName)
		suspendWhResp, err := clusterAPI.SuspendWarehouse(ctx, &cluster.SuspendWarehouseReq{
			WarehouseId: warehouseId,
		})
		if err != nil {
			return diag.Diagnostics{
				diag.Diagnostic{
					Severity: diag.Warning,
					Summary:  summary,
					Detail:   err.Error(),
				},
			}
		}
		infraActionId := suspendWhResp.ActionID
		if len(infraActionId) > 0 {
			stateResp, err := WaitClusterStateChangeComplete(ctx, &waitStateReq{
				clusterAPI: clusterAPI,
				clusterID:  clusterId,
				actionID:   infraActionId,
				timeout:    common.DeployOrScaleClusterTimeout,
				pendingStates: []string{
					string(cluster.ClusterStateDeploying),
					string(cluster.ClusterStateScaling),
					string(cluster.ClusterStateResuming),
					string(cluster.ClusterStateSuspending),
					string(cluster.ClusterStateReleasing),
					string(cluster.ClusterStateUpdating),
				},
				targetStates: []string{
					string(cluster.ClusterStateSuspended),
					string(cluster.ClusterStateAbnormal),
				},
			})

			summary := fmt.Sprintf("suspend warehouse[%s] failed", warehouseName)
			if err != nil {
				return diag.Diagnostics{
					diag.Diagnostic{
						Severity: diag.Warning,
						Summary:  summary,
						Detail:   fmt.Sprintf("%s. errMsg:%s", summary, err.Error()),
					},
				}
			}

			if stateResp.ClusterState == string(cluster.ClusterStateAbnormal) {
				return diag.Diagnostics{
					diag.Diagnostic{
						Severity: diag.Warning,
						Summary:  summary,
						Detail:   fmt.Sprintf("%s. errMsg:%s", summary, stateResp.AbnormalReason),
					},
				}
			}
		}
	}

	idleSuspendInterval := whParamMap["idle_suspend_interval"].(int)
	if idleSuspendInterval > 0 {
		err = clusterAPI.UpdateWarehouseIdleConfig(ctx, &cluster.UpdateWarehouseIdleConfigReq{
			WarehouseId: warehouseId,
			IntervalMs:  int64(idleSuspendInterval * 60 * 1000),
			State:       true,
		})
		if err != nil {
			return diag.Diagnostics{
				diag.Diagnostic{
					Severity: diag.Warning,
					Summary:  fmt.Sprintf("Config warehouse[%s] idle config failed", warehouseName),
					Detail:   err.Error(),
				},
			}
		}
	}
	return nil
}

func updateWarehouse(ctx context.Context, req *UpdateWarehouseReq) diag.Diagnostics {
	clusterAPI := req.clusterAPI
	clusterId := req.clusterId
	oldParamMap, newParamMap := req.oldParamMap, req.newParamMap
	whExternalInfo := req.whExternalInfo

	warehouseId := whExternalInfo.Id
	isDefaultWarehouse := whExternalInfo.IsDefaultWarehouse
	computeNodeIsInstanceStore := whExternalInfo.IsInstanceStore

	warehouseName := newParamMap["name"].(string)

	computeNodeDistributionChanged := oldParamMap["distribution_policy"].(string) != newParamMap["distribution_policy"].(string) ||
		(newParamMap["distribution_policy"].(string) == string(cluster.DistributionPolicySpecifyAZ) && oldParamMap["specify_az"].(string) != newParamMap["specify_az"].(string))
	if computeNodeDistributionChanged {
		distributionPolicy := newParamMap["distribution_policy"].(string)
		specifyAz := newParamMap["specify_az"].(string)
		resp, err := clusterAPI.ChangeWarehouseDistribution(ctx, &cluster.ChangeWarehouseDistributionReq{
			WarehouseID:        warehouseId,
			DistributionPolicy: distributionPolicy,
			SpecifyAz:          specifyAz,
		})
		if err != nil {
			return diag.FromErr(fmt.Errorf("failed to change warehouse distribution, clusterId:%s warehouseId:%s, errMsg:%s", clusterId, warehouseId, err.Error()))
		}

		infraActionResp, err := WaitClusterInfraActionStateChangeComplete(ctx, &waitStateReq{
			clusterAPI: clusterAPI,
			clusterID:  clusterId,
			actionID:   resp.InfraActionId,
			timeout:    common.DeployOrScaleClusterTimeout,
			pendingStates: []string{
				string(cluster.ClusterInfraActionStatePending),
				string(cluster.ClusterInfraActionStateOngoing),
			},
			targetStates: []string{
				string(cluster.ClusterInfraActionStateSucceeded),
				string(cluster.ClusterInfraActionStateCompleted),
				string(cluster.ClusterInfraActionStateFailed),
			},
		})

		if err != nil {
			return diag.FromErr(fmt.Errorf("failed to wait change warehouse distribution[%s], clusterId:%s warehouseId:%s, errMsg:%s", resp.InfraActionId, clusterId, warehouseId, err.Error()))
		}

		if infraActionResp.InfraActionState == string(cluster.ClusterInfraActionStateFailed) {
			return diag.FromErr(fmt.Errorf("failed to wait change warehouse distribution[%s], clusterId:%s warehouseId:%s, errMsg:%s", resp.InfraActionId, clusterId, warehouseId, infraActionResp.ErrMsg))
		}
	}

	// Modify warehouse node size
	computeNodeSizeChanged := oldParamMap["compute_node_size"].(string) != newParamMap["compute_node_size"].(string)
	if computeNodeSizeChanged {
		vmCate := newParamMap["compute_node_size"].(string)
		resp, err := clusterAPI.ScaleWarehouseSize(ctx, &cluster.ScaleWarehouseSizeReq{
			WarehouseId: warehouseId,
			VmCate:      vmCate,
		})

		if err != nil {
			return diag.FromErr(fmt.Errorf("failed to scale warehouse size, clusterId:%s warehouseId:%s, errMsg:%s", clusterId, warehouseId, err))
		}

		stateResp, err := WaitClusterStateChangeComplete(ctx, &waitStateReq{
			clusterAPI: clusterAPI,
			actionID:   resp.ActionID,
			clusterID:  clusterId,
			timeout:    common.DeployOrScaleClusterTimeout,
			pendingStates: []string{
				string(cluster.ClusterStateRunning),
				string(cluster.ClusterStateScaling)},
			targetStates: []string{string(cluster.ClusterStateRunning), string(cluster.ClusterStateAbnormal)},
		})
		if err != nil {
			return diag.FromErr(fmt.Errorf("waiting for cluster (%s) running: %s", clusterId, err))
		}

		if stateResp.ClusterState == string(cluster.ClusterStateAbnormal) {
			return diag.FromErr(errors.New(stateResp.AbnormalReason))
		}
	}

	// Modify warehouse node count
	computeNodeCountChanged := oldParamMap["compute_node_count"].(int) != newParamMap["compute_node_count"].(int)
	if computeNodeCountChanged {
		vmNum := int32(newParamMap["compute_node_count"].(int))
		resp, err := clusterAPI.ScaleWarehouseNum(ctx, &cluster.ScaleWarehouseNumReq{
			WarehouseId: warehouseId,
			VmNum:       vmNum,
		})

		if err != nil {
			return diag.FromErr(fmt.Errorf("failed to scale warehouse number, clusterId:%s warehouseId:%s, errMsg:%s", clusterId, warehouseId, err))
		}

		stateResp, err := WaitClusterStateChangeComplete(ctx, &waitStateReq{
			clusterAPI: clusterAPI,
			actionID:   resp.ActionID,
			clusterID:  clusterId,
			timeout:    common.DeployOrScaleClusterTimeout,
			pendingStates: []string{
				string(cluster.ClusterStateRunning),
				string(cluster.ClusterStateScaling)},
			targetStates: []string{string(cluster.ClusterStateRunning), string(cluster.ClusterStateAbnormal)},
		})
		if err != nil {
			return diag.FromErr(fmt.Errorf("waiting for cluster (%s) running: %s", clusterId, err))
		}

		if stateResp.ClusterState == string(cluster.ClusterStateAbnormal) {
			return diag.FromErr(errors.New(stateResp.AbnormalReason))
		}
	}

	// Moidify warehouse volume config
	oldVolumeConfig, newVolumeConfig := cluster.DefaultBeVolumeMap(), cluster.DefaultBeVolumeMap()
	if len(oldParamMap["compute_node_volume_config"].([]interface{})) > 0 {
		oldVolumeConfig = oldParamMap["compute_node_volume_config"].([]interface{})[0].(map[string]interface{})
	}
	if len(newParamMap["compute_node_volume_config"].([]interface{})) > 0 {
		newVolumeConfig = newParamMap["compute_node_volume_config"].([]interface{})[0].(map[string]interface{})
	}
	VolumeConfigChanged := !cluster.Equal(oldVolumeConfig, newVolumeConfig)

	if VolumeConfigChanged {
		log.Printf("[DEBUG] warehouse[%s] volume config changed, old:%+v, new:%+v", warehouseName, oldVolumeConfig, newVolumeConfig)
	}

	if !computeNodeIsInstanceStore && VolumeConfigChanged {
		if oldVolumeConfig["vol_number"].(int) != newVolumeConfig["vol_number"].(int) {
			return diag.FromErr(fmt.Errorf("the compute node `vol_number` is not allowed to be modified"))
		}

		if oldVolumeConfig["vol_size"].(int) > newVolumeConfig["vol_size"].(int) {
			return diag.FromErr(fmt.Errorf("the compute node `vol_size` does not support decrease"))
		}

		req := &cluster.ModifyClusterVolumeReq{
			ClusterId:   clusterId,
			WarehouseID: warehouseId,
			Type:        cluster.ClusterModuleTypeWarehouse,
		}

		if v, ok := newVolumeConfig["vol_size"]; ok && v != oldVolumeConfig["vol_size"] {
			req.VmVolSize = int64(v.(int))
		}
		if v, ok := newVolumeConfig["iops"]; ok && v != oldVolumeConfig["iops"] {
			req.Iops = int64(v.(int))
		}
		if v, ok := newVolumeConfig["throughput"]; ok && v != oldVolumeConfig["throughput"] {
			req.Throughput = int64(v.(int))
		}

		log.Printf("[DEBUG] modify warehouse[%s] volume config, req:%+v", warehouseName, req)
		modifyVolumeResp, err := clusterAPI.ModifyClusterVolume(ctx, req)
		if err != nil {
			log.Printf("[ERROR] modify warehouse[%s] volume config failed, err:%+v", warehouseName, err)
			return diag.FromErr(err)
		}
		infraActionId := modifyVolumeResp.ActionID
		if len(infraActionId) > 0 {
			infraActionResp, err := WaitClusterInfraActionStateChangeComplete(ctx, &waitStateReq{
				clusterAPI: clusterAPI,
				clusterID:  clusterId,
				actionID:   infraActionId,
				timeout:    30 * time.Minute,
				pendingStates: []string{
					string(cluster.ClusterInfraActionStatePending),
					string(cluster.ClusterInfraActionStateOngoing),
				},
				targetStates: []string{
					string(cluster.ClusterInfraActionStateSucceeded),
					string(cluster.ClusterInfraActionStateCompleted),
					string(cluster.ClusterInfraActionStateFailed),
				},
			})

			summary := fmt.Sprintf("Modify warehouse[%s] volume config failed", warehouseName)

			if err != nil {
				return diag.Diagnostics{
					diag.Diagnostic{
						Severity: diag.Error,
						Summary:  summary,
						Detail:   err.Error(),
					},
				}
			}

			if infraActionResp.InfraActionState == string(cluster.ClusterInfraActionStateFailed) {
				return diag.Diagnostics{
					diag.Diagnostic{
						Severity: diag.Error,
						Summary:  summary,
						Detail:   infraActionResp.ErrMsg,
					},
				}
			}
		}
	}

	// Modify idle suspend interval
	if !isDefaultWarehouse {
		idleSuspendIntervalChanged := oldParamMap["idle_suspend_interval"].(int) != newParamMap["idle_suspend_interval"].(int)
		if idleSuspendIntervalChanged {
			idleSuspendInterval := newParamMap["idle_suspend_interval"].(int)
			err := clusterAPI.UpdateWarehouseIdleConfig(ctx, &cluster.UpdateWarehouseIdleConfigReq{
				WarehouseId: warehouseId,
				IntervalMs:  int64(idleSuspendInterval * 60 * 1000),
				State:       idleSuspendInterval > 0,
			})
			if err != nil {
				return diag.Diagnostics{
					diag.Diagnostic{
						Severity: diag.Warning,
						Summary:  "Config warehouse idle config failed",
						Detail:   err.Error(),
					},
				}
			}
		}
	}

	// Modify sr config
	oldSrConfigMap := oldParamMap["compute_node_configs"].(map[string]interface{})
	oldConfigs := make(map[string]string, 0)
	for k, v := range oldSrConfigMap {
		oldConfigs[k] = v.(string)
	}

	newSrConfigMap := newParamMap["compute_node_configs"].(map[string]interface{})
	newConfigs := make(map[string]string, 0)
	for k, v := range newSrConfigMap {
		newConfigs[k] = v.(string)
	}
	srConfigChanged := !cluster.Equal(oldConfigs, newConfigs)

	if !isDefaultWarehouse {
		expectedState := newParamMap["expected_state"].(string)
		expectedStateChanged := oldParamMap["expected_state"].(string) != newParamMap["expected_state"].(string)
		if expectedStateChanged {
			if expectedState == string(cluster.ClusterStateRunning) {
				resp := ResumeWarehouse(ctx, clusterAPI, clusterId, warehouseId, warehouseName)
				if resp != nil {
					return resp
				}
			}
		}
	}

	if srConfigChanged {
		warnDiag := UpsertClusterConfig(ctx, clusterAPI, &cluster.UpsertClusterConfigReq{
			ClusterID:   clusterId,
			ConfigType:  cluster.CustomConfigTypeBE,
			WarehouseID: warehouseId,
			Configs:     newConfigs,
		})
		if warnDiag != nil {
			return warnDiag
		}
	}

	if !isDefaultWarehouse {
		expectedState := newParamMap["expected_state"].(string)
		expectedStateChanged := oldParamMap["expected_state"].(string) != newParamMap["expected_state"].(string)
		// Modidy warehouse state
		if expectedStateChanged {
			if expectedState == string(cluster.ClusterStateSuspended) {
				resp := SuspendWarehouse(ctx, clusterAPI, clusterId, warehouseId, warehouseName)
				if resp != nil {
					return resp
				}
			}
		}
	}

	// Modify auto scaling policy
	autoScalingPolicyChanged := oldParamMap["auto_scaling_policy"].(string) != newParamMap["auto_scaling_policy"].(string)
	if autoScalingPolicyChanged {
		policyJson := ""
		if v, ok := newParamMap["auto_scaling_policy"]; ok {
			policyJson = v.(string)
		}

		if len(policyJson) > 0 {
			autoScalingConfig := &cluster.WarehouseAutoScalingConfig{}
			json.Unmarshal([]byte(policyJson), autoScalingConfig)
			req := &cluster.SaveWarehouseAutoScalingConfigReq{
				ClusterId:                  clusterId,
				WarehouseId:                warehouseId,
				WarehouseAutoScalingConfig: *autoScalingConfig,
				State:                      true,
			}
			_, err := clusterAPI.SaveWarehouseAutoScalingConfig(ctx, req)
			if err != nil {
				msg := fmt.Sprintf("Update warehouse auto-scaling configuration failed, errMsg:%s", err.Error())
				log.Printf("[ERROR] %s", msg)
				return diag.FromErr(fmt.Errorf("%s", msg))
			}
		} else {
			err := clusterAPI.DeleteWarehouseAutoScalingConfig(ctx, &cluster.DeleteWarehouseAutoScalingConfigReq{
				WarehouseId: warehouseId,
			})
			if err != nil {
				return diag.Diagnostics{
					diag.Diagnostic{
						Severity: diag.Warning,
						Summary:  "Delete warehouse auto scaling config failed",
						Detail:   err.Error(),
					},
				}
			}
		}
	}

	return nil
}

func DeleteWarehouse(ctx context.Context, clusterAPI cluster.IClusterAPI, clusterId, warehouseId string) (diags diag.Diagnostics) {

	resp, err := clusterAPI.ReleaseWarehouse(ctx, &cluster.ReleaseWarehouseReq{
		WarehouseId: warehouseId,
	})

	if err != nil {
		log.Printf("[ERROR] release warehouse failed, err:%+v", err)
		return diag.FromErr(err)
	}

	infraActionId := resp.ActionID
	if len(infraActionId) > 0 {
		stateResp, err := WaitClusterStateChangeComplete(ctx, &waitStateReq{
			clusterAPI: clusterAPI,
			clusterID:  clusterId,
			actionID:   resp.ActionID,
			timeout:    common.DeployOrScaleClusterTimeout,
			pendingStates: []string{
				string(cluster.ClusterStateDeploying),
				string(cluster.ClusterStateRunning),
				string(cluster.ClusterStateScaling),
				string(cluster.ClusterStateResuming),
				string(cluster.ClusterStateSuspending),
				string(cluster.ClusterStateReleasing),
				string(cluster.ClusterStateUpdating),
			},
			targetStates: []string{
				string(cluster.ClusterStateReleased),
				string(cluster.ClusterStateAbnormal),
			},
		})

		if err != nil {
			summary := fmt.Sprintf("release warehouse[%s] of the cluster[%s] failed, errMsg:%s", warehouseId, clusterId, err.Error())
			return diag.FromErr(fmt.Errorf(summary))
		}

		if stateResp.ClusterState == string(cluster.ClusterStateAbnormal) {
			return diag.FromErr(errors.New(stateResp.AbnormalReason))
		}
	}
	return diags
}

func SuspendWarehouse(ctx context.Context, clusterAPI cluster.IClusterAPI, clusterId, warehouseId, warehouseName string) (diags diag.Diagnostics) {
	suspendWhResp, err := clusterAPI.SuspendWarehouse(ctx, &cluster.SuspendWarehouseReq{
		WarehouseId: warehouseId,
	})
	if err != nil {
		return diag.Diagnostics{
			diag.Diagnostic{
				Severity: diag.Warning,
				Summary:  "Suspend warehouse failed",
				Detail:   err.Error(),
			},
		}
	}
	infraActionId := suspendWhResp.ActionID
	if len(infraActionId) > 0 {
		stateResp, err := WaitClusterStateChangeComplete(ctx, &waitStateReq{
			clusterAPI: clusterAPI,
			clusterID:  clusterId,
			actionID:   infraActionId,
			timeout:    common.DeployOrScaleClusterTimeout,
			pendingStates: []string{
				string(cluster.ClusterStateDeploying),
				string(cluster.ClusterStateRunning),
				string(cluster.ClusterStateScaling),
				string(cluster.ClusterStateResuming),
				string(cluster.ClusterStateSuspending),
				string(cluster.ClusterStateReleasing),
				string(cluster.ClusterStateUpdating),
			},
			targetStates: []string{
				string(cluster.ClusterStateSuspended),
				string(cluster.ClusterStateAbnormal),
			},
		})

		if err != nil {
			msg := fmt.Sprintf("suspend warehouse[%s] of the cluster[%s] failed, errMsg:%s", warehouseName, clusterId, err.Error())
			return diag.Diagnostics{
				diag.Diagnostic{
					Severity: diag.Warning,
					Summary:  "Suspend warehouse",
					Detail:   msg,
				},
			}
		}

		if stateResp.ClusterState == string(cluster.ClusterStateAbnormal) {
			return diag.Diagnostics{
				diag.Diagnostic{
					Severity: diag.Warning,
					Summary:  "Suspend warehouse",
					Detail:   stateResp.AbnormalReason,
				},
			}
		}
	}
	return diags
}

func ResumeWarehouse(ctx context.Context, clusterAPI cluster.IClusterAPI, clusterId, warehouseId, warehouseName string) (diags diag.Diagnostics) {
	resumeWhResp, err := clusterAPI.ResumeWarehouse(ctx, &cluster.ResumeWarehouseReq{
		WarehouseId: warehouseId,
	})
	if err != nil {
		return diag.Diagnostics{
			diag.Diagnostic{
				Severity: diag.Error,
				Summary:  "Resume warehouse failed",
				Detail:   err.Error(),
			},
		}
	}
	infraActionId := resumeWhResp.ActionID
	if len(infraActionId) > 0 {
		stateResp, err := WaitClusterStateChangeComplete(ctx, &waitStateReq{
			clusterAPI: clusterAPI,
			clusterID:  clusterId,
			actionID:   infraActionId,
			timeout:    common.DeployOrScaleClusterTimeout,
			pendingStates: []string{
				string(cluster.ClusterStateDeploying),
				string(cluster.ClusterStateScaling),
				string(cluster.ClusterStateResuming),
				string(cluster.ClusterStateSuspending),
				string(cluster.ClusterStateSuspended),
				string(cluster.ClusterStateReleasing),
				string(cluster.ClusterStateUpdating),
			},
			targetStates: []string{
				string(cluster.ClusterStateRunning),
				string(cluster.ClusterStateAbnormal),
			},
		})

		if err != nil {
			summary := fmt.Sprintf("resume warehouse[%s] of the cluster[%s] failed, errMsg:%s", warehouseName, clusterId, err.Error())
			return diag.FromErr(fmt.Errorf(summary))
		}

		if stateResp.ClusterState == string(cluster.ClusterStateAbnormal) {
			return diag.FromErr(errors.New(stateResp.AbnormalReason))
		}
	}
	return diags
}

func IsAllRunning(c *cluster.Cluster) bool {
	if c.ClusterState != cluster.ClusterStateRunning {
		return false
	}

	for _, wh := range c.Warehouses {
		if wh.State != cluster.ClusterStateRunning {
			return false
		}
	}

	return true
}

type UpdateWarehouseReq struct {
	d              *schema.ResourceData
	clusterAPI     cluster.IClusterAPI
	clusterId      string
	oldParamMap    map[string]interface{}
	newParamMap    map[string]interface{}
	whExternalInfo *cluster.WarehouseExternalInfo
}
