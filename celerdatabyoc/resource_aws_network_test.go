package celerdatabyoc

import (
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

func TestAccCelerdataAwsNetworkBasic(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:     func() { testPreCheck(t) },
		Providers:    testAccProviders,
		CheckDestroy: testCheckAwsNetworkDestroy,
		Steps: []resource.TestStep{
			{
				Config: testCheckAwsNetworkConfigBasic(),
				Check: resource.ComposeTestCheckFunc(
					testCheckCelerdataAwsNetworkExists("celerdatabyoc_aws_network.new"),
				),
			},
		},
	})
}

func testCheckAwsNetworkDestroy(s *terraform.State) error {
	for _, rs := range s.RootModule().Resources {
		if rs.Type != "celerdatabyoc_aws_network" {
			continue
		}

		fmt.Println(rs.Primary.ID)
	}

	return nil
}

func testCheckAwsNetworkConfigBasic() string {
	return fmt.Sprintf(`
	resource "celerdatabyoc_aws_network" "new" {
		name = "test-network"
		subnet_id = "%s"
		security_group_id = "%s"
		deployment_credential_id = "%s"
	}
	`, os.Getenv("CELERDATA_SUBNET_ID"),
		os.Getenv("CELERDATA_SECURITY_GROUP_ID"),
		os.Getenv("CELERDATA_DEPLOYMENT_CREDENTIAL_ID"))
}

// TestAwsNetworkSubnetIDBackfillIsDiffStable guards the schema change that makes the
// Read backfill of subnet_id safe (Computed: true). Without Computed, unconditionally
// setting subnet_id on Read for a multi-AZ network (whose config only ever sets
// subnet_ids) would show up on every subsequent plan as a subnet_id change and, since
// subnet_id is ForceNew, plan to destroy and recreate the network. This test reproduces
// that exact scenario against the resource's real schema.
func TestAwsNetworkSubnetIDBackfillIsDiffStable(t *testing.T) {
	res := resourceNetwork()

	t.Run("existing multi-AZ network: backfilled subnet_id causes no diff", func(t *testing.T) {
		priorState := &terraform.InstanceState{
			ID: "net-1",
			Attributes: map[string]string{
				// Every other attribute matches config exactly (steady state); only
				// subnet_id is the interesting one: as Read would leave it after the
				// backfill, set to the network's real (pinned) primary subnet — one of
				// the 3 subnet_ids entries, per customizeAwsNetworkDiff — even though
				// config never declares subnet_id explicitly (config only sets
				// subnet_ids; this is the transient state before an import is
				// reconciled with matching config, or a caller who is fine leaving the
				// primary to whatever Computed carries forward).
				"subnet_id":                "subnet-a",
				"name":                     "net",
				"security_group_id":        "sg-1",
				"deployment_credential_id": "cred-1",
				"region":                   "us-west-2",
				"multi_az":                 "true",
			},
		}
		set := schema.NewSet(schema.HashString, []interface{}{"subnet-a", "subnet-b", "subnet-c"})
		priorState.Attributes["subnet_ids.#"] = fmt.Sprintf("%d", set.Len())
		for _, v := range set.List() {
			priorState.Attributes[fmt.Sprintf("subnet_ids.%d", schema.HashString(v))] = v.(string)
		}

		cfg := terraform.NewResourceConfigRaw(map[string]interface{}{
			"name":                     "net",
			"security_group_id":        "sg-1",
			"deployment_credential_id": "cred-1",
			"region":                   "us-west-2",
			"subnet_ids":               []interface{}{"subnet-a", "subnet-b", "subnet-c"},
		})

		diff, err := res.Diff(nil, priorState, cfg, nil)
		if err != nil {
			t.Fatalf("diff error: %v", err)
		}
		if diff != nil && !diff.Empty() {
			t.Logf("diff attrs: %+v", diff.Attributes)
			if _, ok := diff.Attributes["subnet_id"]; ok {
				t.Fatalf("UNSAFE: backfilled subnet_id shows up as a diff for an existing multi-AZ network: %+v", diff.Attributes["subnet_id"])
			}
			if diff.RequiresNew() {
				t.Fatalf("UNSAFE: diff requires replacement in steady state: %+v", diff.Attributes)
			}
		}
	})

	t.Run("single-AZ network: a real subnet_id change still forces replacement", func(t *testing.T) {
		priorState := &terraform.InstanceState{
			ID:         "net-1",
			Attributes: map[string]string{"subnet_id": "subnet-old"},
		}
		cfg := terraform.NewResourceConfigRaw(map[string]interface{}{
			"name":                     "net",
			"security_group_id":        "sg-1",
			"deployment_credential_id": "cred-1",
			"region":                   "us-west-2",
			"subnet_id":                "subnet-new",
		})

		diff, err := res.Diff(nil, priorState, cfg, nil)
		if err != nil {
			t.Fatalf("diff error: %v", err)
		}
		if diff == nil || diff.Empty() || !diff.RequiresNew() {
			t.Fatalf("expected a real subnet_id change to force replacement, got diff=%+v", diff)
		}
	})
}

// TestAwsNetworkSubnetIDPinning covers the AtLeastOneOf relaxation (subnet_id and
// subnet_ids may now both be set) and the accompanying customizeAwsNetworkDiff
// cross-check that subnet_id must be a member of subnet_ids when both are present. This
// is what lets a caller create a multi-AZ celerdatabyoc_aws_network whose primary
// subnet_id is pinned to a specific one of its 3 subnets — required for it to be a valid
// single-AZ -> multi-AZ conversion target.
func TestAwsNetworkSubnetIDPinning(t *testing.T) {
	res := resourceNetwork()
	emptyState := &terraform.InstanceState{}

	baseAttrs := map[string]interface{}{
		"name":                     "net",
		"security_group_id":        "sg-1",
		"deployment_credential_id": "cred-1",
		"region":                   "us-west-2",
	}

	t.Run("subnet_id pinned to a member of subnet_ids is accepted", func(t *testing.T) {
		attrs := map[string]interface{}{
			"subnet_id":  "subnet-a",
			"subnet_ids": []interface{}{"subnet-a", "subnet-b", "subnet-c"},
		}
		for k, v := range baseAttrs {
			attrs[k] = v
		}
		cfg := terraform.NewResourceConfigRaw(attrs)

		if _, err := res.Diff(nil, emptyState, cfg, nil); err != nil {
			t.Fatalf("expected subnet_id pinned to a subnet_ids member to be accepted, got: %v", err)
		}
	})

	t.Run("subnet_id not a member of subnet_ids is rejected", func(t *testing.T) {
		attrs := map[string]interface{}{
			"subnet_id":  "subnet-x",
			"subnet_ids": []interface{}{"subnet-a", "subnet-b", "subnet-c"},
		}
		for k, v := range baseAttrs {
			attrs[k] = v
		}
		cfg := terraform.NewResourceConfigRaw(attrs)

		if _, err := res.Diff(nil, emptyState, cfg, nil); err == nil {
			t.Fatalf("expected subnet_id not in subnet_ids to be rejected")
		}
	})

	t.Run("subnet_ids alone (no subnet_id) is still accepted, unchanged from before", func(t *testing.T) {
		attrs := map[string]interface{}{
			"subnet_ids": []interface{}{"subnet-a", "subnet-b", "subnet-c"},
		}
		for k, v := range baseAttrs {
			attrs[k] = v
		}
		cfg := terraform.NewResourceConfigRaw(attrs)

		if _, err := res.Diff(nil, emptyState, cfg, nil); err != nil {
			t.Fatalf("expected subnet_ids alone to be accepted, got: %v", err)
		}
	})

	t.Run("subnet_id alone (no subnet_ids) is still accepted, unchanged from before", func(t *testing.T) {
		attrs := map[string]interface{}{
			"subnet_id": "subnet-only",
		}
		for k, v := range baseAttrs {
			attrs[k] = v
		}
		cfg := terraform.NewResourceConfigRaw(attrs)

		if _, err := res.Diff(nil, emptyState, cfg, nil); err != nil {
			t.Fatalf("expected subnet_id alone to be accepted, got: %v", err)
		}
	})

	t.Run("neither subnet_id nor subnet_ids set is still rejected", func(t *testing.T) {
		// AtLeastOneOf/ExactlyOneOf are enforced by Resource.Validate (the
		// terraform validate / plan-time config validation step), not by Diff.
		cfg := terraform.NewResourceConfigRaw(baseAttrs)

		if diags := res.Validate(cfg); !diags.HasError() {
			t.Fatalf("expected a validation error when neither subnet_id nor subnet_ids is set")
		}
	})
}

func testCheckCelerdataAwsNetworkExists(n string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[n]

		if !ok {
			return fmt.Errorf("Not found: %s", n)
		}

		if rs.Primary.ID == "" {
			return fmt.Errorf("No credential set")
		}

		return nil
	}
}
