---
# generated by https://github.com/hashicorp/terraform-plugin-docs
page_title: "celerdatabyoc_aws_deployment_role_credential Resource - terraform-provider-celerdatabyoc"
subcategory: ""
description: |-
  
---

~> The resource's API may change in subsequent versions to simplify the user experience.

To ensure a successful deployment in your VPC, you must create an AWS deployment credential. For more information, see [Create an AWS deployment credential](https://docs.celerdata.com/en-us/main/cloud_settings/aws_cloud_settings/manage_aws_data_credentials.).

This resource depends on the following resources and the [celerdatabyoc_aws_data_credential_assume_policy](../data-sources/aws_data_credential_assume_policy.md) data source:

- [celerdatabyoc_aws_data_credential_policy](../resources/aws_data_credential_policy.md)
- [aws_iam_role](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role) (data credential role)
- [celerdatabyoc_aws_deployment_credential_policy](../resources/aws_deployment_credential_policy.md)
- [celerdatabyoc_aws_deployment_credential_assume_policy](../resources/aws_deployment_credential_assume_policy.md)
- [aws_iam_role](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role) (deployment_credential_role)

You must have configured these resources before you can implement this resource.

## Example Usage

```terraform
resource "celerdatabyoc_aws_data_credential_policy" "new" {
   bucket = local.s3_bucket
}

data "celerdatabyoc_aws_data_credential_assume_policy" "assume_role" {}

resource "aws_iam_role" "celerdata_data_cred_role" {
  name               = "<celerdata_data_credential_role_name>"
  assume_role_policy = data.celerdatabyoc_aws_data_credential_assume_policy.assume_role.json
  description        = "<celerdata_data_credential_role_description>"
  inline_policy {
    name   = "<celerdata_data_credential_role_policy_name>"
    policy = celerdatabyoc_aws_data_credential_policy.role_policy.json
  }
}

resource "celerdatabyoc_aws_deployment_credential_policy" "role_policy" {
  bucket = local.s3_bucket
  data_role_arn = aws_iam_role.celerdata_data_cred_role.arn 
}

resource "celerdatabyoc_aws_deployment_credential_assume_policy" "role_policy" {}

resource "aws_iam_role" "deploy_cred_role" {
  name               = "<celerdata_deployment_credential_role_name>"
  assume_role_policy = celerdatabyoc_aws_deployment_credential_assume_policy.role_policy.json
  description        = "<celerdata_deployment_credential_role_description>"
  inline_policy {
    name   = "<celerdata_deployment_credential_role_policy_name>"
    policy = celerdatabyoc_aws_deployment_credential_policy.role_policy.json
  }
}

resource "celerdatabyoc_aws_deployment_role_credential" "deployment_role_credential" {
  name = "<celerdata_deployment_credential_name>"
  role_arn = aws_iam_role.deploy_cred_role.arn
  external_id = celerdatabyoc_aws_deployment_credential_assume_policy.role_policy.external_id
  policy_version = celerdatabyoc_aws_deployment_credential_policy.role_policy.version
}
```

## Argument Reference

~> This section explains only the arguments of the `celerdatabyoc_aws_deployment_role_credential` resource. For the explanation of arguments of other resources, see the corresponding resource topics.

This resource contains the following required arguments and optional arguments:

**Required:**

- `role_arn`: (Forces new resource) The ARN of the cross-account IAM role referenced in the deployment credential. Set the value to `aws_iam_role.deploy_cred_role.arn`.

- `external_id`: (Forces new resource) The external ID that is used to create the cross-account IAM role referenced in the deployment credential. Set the value to `celerdatabyoc_aws_deployment_credential_assume_policy.role_policy.external_id`.

- `policy_version`: The version of the policy. Set the value to `celerdatabyoc_aws_deployment_credential_policy.role_policy.version`.

**Optional:**

- `name`: (Forces new resource) The name of the deployment credential. Enter a unique name. If omitted, Terraform will assign a random, unique name.

## Attribute Reference

This resource exports the following attribute:

- `id`: The ID of the deployment credential.

## See Also

- [celerdatabyoc_aws_deployment_credential_policy](../resources/aws_deployment_credential_policy.md)
- [celerdatabyoc_aws_deployment_credential_assume_policy](../resources/aws_deployment_credential_assume_policy.md)
- [aws_iam_role](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/iam_role)
