package celerdatabyoc

import (
	"context"
	"errors"
	"log"
	"regexp"
	"terraform-provider-celerdatabyoc/celerdata-sdk/client"
	"terraform-provider-celerdatabyoc/celerdata-sdk/service/network"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

func resourceNetwork() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceNetworkCreate,
		ReadContext:   resourceNetworkRead,
		DeleteContext: resourceNetworkDelete,
		Schema: map[string]*schema.Schema{
			"id": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
			},
			"name": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.StringMatch(regexp.MustCompile(`^[0-9a-zA-Z_-]{1,128}$`), "The name is restricted to a maximum length of 128 characters and can only consist of alphanumeric characters (a-z, A-Z, 0-9), hyphens (-), and underscores (_)."),
			},
			"subnet_id": {
				Type:     schema.TypeString,
				Optional: true,
				// Computed so that Read can unconditionally backfill this from the
				// network's actual (primary) subnet_id — including for a multi-AZ
				// network created via subnet_ids alone, whose config may never set this
				// field. Without Computed, Terraform's diff would see the config's
				// implicit null and plan to reset the field, forcing replacement of
				// every existing multi-AZ network on the next apply.
				Computed: true,
				ForceNew: true,
				// AtLeastOneOf (not ExactlyOneOf): a multi-AZ network can set subnet_ids
				// alone (backend leaves the network's primary subnet_id empty, as before),
				// or subnet_ids *and* subnet_id together to pin a specific one of the 3
				// subnets as the primary — required so a multi-AZ network can be an
				// eligible single-AZ -> multi-AZ conversion target (the backend requires
				// the target's primary subnet_id to exactly equal the original cluster
				// network's subnet_id; subnet_ids is a Set, so there is no other way for
				// the caller to pin which of the 3 subnets that must be). See
				// customizeAwsNetworkDiff for the subnet_id-must-be-a-member-of-subnet_ids
				// check.
				AtLeastOneOf: []string{"subnet_id", "subnet_ids"},
			},
			"security_group_id": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},
			"deployment_credential_id": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},
			"vpc_endpoint_id": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
			},
			"region": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},
			"subnet_ids": {
				Type:         schema.TypeSet,
				Optional:     true,
				ForceNew:     true,
				MaxItems:     3,
				MinItems:     3,
				Elem:         &schema.Schema{Type: schema.TypeString},
				Set:          schema.HashString,
				AtLeastOneOf: []string{"subnet_id", "subnet_ids"},
			},
			"multi_az": {
				Type:     schema.TypeBool,
				Optional: true,
				Computed: true,
			},
		},
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},
		CustomizeDiff: customizeAwsNetworkDiff,
	}
}

// customizeAwsNetworkDiff enforces that, when both subnet_id and subnet_ids are set,
// subnet_id names one of the 3 subnets in subnet_ids. subnet_ids is a Set (unordered),
// so this is the only way a caller can pin which of its 3 subnets becomes the network's
// top-level primary subnet_id — needed for the network to be a valid single-AZ ->
// multi-AZ conversion target (see celerdatabyoc_elastic_cluster_v2's network_id docs).
func customizeAwsNetworkDiff(ctx context.Context, d *schema.ResourceDiff, m interface{}) error {
	subnetID := d.Get("subnet_id").(string)
	subnetIDsSet := d.Get("subnet_ids").(*schema.Set)
	if len(subnetID) == 0 || subnetIDsSet.Len() == 0 {
		return nil
	}
	if !subnetIDsSet.Contains(subnetID) {
		return errors.New("subnet_id must be one of the 3 subnets listed in subnet_ids when both are set")
	}
	return nil
}

func resourceNetworkCreate(ctx context.Context, d *schema.ResourceData, m interface{}) (diags diag.Diagnostics) {
	c := m.(*client.CelerdataClient)

	subnetIDsSet := d.Get("subnet_ids").(*schema.Set)
	subnetIDs := make([]string, subnetIDsSet.Len())

	for i, v := range subnetIDsSet.List() {
		subnetIDs[i] = v.(string)
	}
	networkCli := network.NewNetworkAPI(c)
	req := &network.CreateNetworkReq{
		Name:            d.Get("name").(string),
		SubnetId:        d.Get("subnet_id").(string),
		SecurityGroupId: d.Get("security_group_id").(string),
		DeployCredID:    d.Get("deployment_credential_id").(string),
		SubnetIds:       subnetIDs,
		Csp:             "aws",
		Region:          d.Get("region").(string),
	}
	if v, ok := d.GetOk("vpc_endpoint_id"); ok {
		req.VpcEndpointId = v.(string)
	}

	resp, err := networkCli.CreateNetwork(ctx, req)
	if err != nil {
		return diag.FromErr(err)
	}

	log.Printf("[DEBUG] create network succeeded, id:%s]", resp.NetworkID)
	d.SetId(resp.NetworkID)
	return diags
}

func resourceNetworkRead(ctx context.Context, d *schema.ResourceData, m interface{}) (diags diag.Diagnostics) {
	c := m.(*client.CelerdataClient)

	netID := d.Id()
	networkCli := network.NewNetworkAPI(c)
	// // Warning or errors can be collected in a slice type
	log.Printf("[DEBUG] get network, id[%s]", netID)
	resp, err := networkCli.GetNetwork(ctx, netID)
	if err != nil {
		return diag.FromErr(err)
	}

	d.Set("multi_az", resp.Network.MultiAz)
	// Backfill unconditionally so an imported (or derived, e.g. the multi-AZ target of
	// a single-AZ -> multi-AZ conversion) network round-trips correctly: subnet_id
	// always reflects the network's actual primary subnet, and subnet_ids is set
	// whenever the network actually has AZ rows (gated on the data itself rather than
	// the multi_az flag, so it stays correct even if that flag and the AZ rows ever
	// disagree).
	d.Set("subnet_id", resp.Network.SubnetId)
	if len(resp.Network.AZNetWorkInterfaces) > 0 {
		subnetIds := make([]string, 0, len(resp.Network.AZNetWorkInterfaces))
		for _, net := range resp.Network.AZNetWorkInterfaces {
			subnetIds = append(subnetIds, net.SubnetId)
		}

		d.Set("subnet_ids", subnetIds)
	}

	log.Printf("[DEBUG] get Network, resp:%+v", resp)
	return diags
}

func resourceNetworkDelete(ctx context.Context, d *schema.ResourceData, m interface{}) (diags diag.Diagnostics) {
	c := m.(*client.CelerdataClient)

	netID := d.Id()
	networkCli := network.NewNetworkAPI(c)
	// // Warning or errors can be collected in a slice type
	log.Printf("[DEBUG] delete network, id[%s]", netID)
	err := networkCli.DeleteNetwork(ctx, netID)
	if err != nil {
		return diag.FromErr(err)
	}

	// d.SetId("") is automatically called assuming delete returns no errors, but
	// it is added here for explicitness.
	d.SetId("")
	return diags
}
