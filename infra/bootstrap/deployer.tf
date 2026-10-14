# The role the Deploy workflow (.github/workflows/deploy.yml) applies the
# infra root as, and the permissions boundary every role that root may create
# or change must carry (ADR 0007).
#
# GitHub signs a short-lived OpenID Connect token for each job. AWS trusts that
# token through the account's GitHub provider, which aws-setup owns, and hands
# the deploy job temporary credentials, so no AWS key is stored in GitHub
# (aws-design §5.6, G2).

locals {
  # GitHub's immutable OIDC subject for this repository (infra/ci.tf explains
  # the owner@id/repo@id form), pinned to the Deploy workflow's environment.
  # Only main may deploy to it, and Craig approves every run in GitHub.
  github_repository  = "CraigDevJohnson@42454849/foundry@1361164956"
  deploy_environment = "deploy"

  account = local.workloads_account_id

  state_bucket_arn = "arn:aws:s3:::foundry-tofu-state-${local.account}"
  # The infra root's state and lock file only, not this root's.
  state_object_arns = [
    "${local.state_bucket_arn}/foundry/terraform.tfstate",
    "${local.state_bucket_arn}/foundry/terraform.tfstate.tflock",
  ]

  boundary_name = "foundry-workload-boundary"

  # The roles the deploy role may create or change, always inside the
  # boundary: the controllers (controller.tf) and the Host control role
  # (ci.tf). The host's role and the snapshot role carry policies the
  # boundary doesn't cover, so the deploy role may hand them to EC2 and Data
  # Lifecycle Manager but never change them. Its own name, foundry-deployer,
  # matches none of these.
  bounded_roles = [
    "arn:aws:iam::${local.account}:role/foundry-*-controller",
    "arn:aws:iam::${local.account}:role/foundry-host-control-ci",
  ]
  foundry_roles = "arn:aws:iam::${local.account}:role/foundry-*"

  functions = [
    "arn:aws:lambda:${local.region}:${local.account}:function:foundry-*-controller",
    "arn:aws:lambda:${local.region}:${local.account}:function:foundry-*-controller:*",
  ]
  # The Lobby's site roles, the only principals a controller may be opened to.
  site_role_arns = [
    "arn:aws:iam::${local.account}:role/site-dev-server",
    "arn:aws:iam::${local.account}:role/site-prod-server",
  ]
  log_groups = [
    "arn:aws:logs:${local.region}:${local.account}:log-group:/aws/lambda/foundry-*",
    "arn:aws:logs:${local.region}:${local.account}:log-group:/aws/lambda/foundry-*:*",
  ]
  alarms    = "arn:aws:cloudwatch:${local.region}:${local.account}:alarm:foundry-*"
  rules     = "arn:aws:events:${local.region}:${local.account}:rule/foundry-*"
  documents = "arn:aws:ssm:${local.region}:${local.account}:document/foundry-*"

  # The workloads alerts topic (aws-setup), the one place a Foundry alarm or
  # rule may notify.
  alerts_topic_arn = "arn:aws:sns:${local.region}:${local.account}:alerts"

  # The EC2 calls the infra root's resources make in a create, an update, a
  # host rebuild or a delete, besides reading and tagging.
  ec2_create_actions = [
    "ec2:AllocateAddress",
    "ec2:AuthorizeSecurityGroupEgress",
    "ec2:AuthorizeSecurityGroupIngress",
    "ec2:CreateSecurityGroup",
    "ec2:CreateVolume",
    "ec2:RunInstances",
  ]
  ec2_created_resources = [
    "arn:aws:ec2:${local.region}:${local.account}:elastic-ip/*",
    "arn:aws:ec2:${local.region}:${local.account}:instance/*",
    "arn:aws:ec2:${local.region}:${local.account}:security-group/*",
    "arn:aws:ec2:${local.region}:${local.account}:security-group-rule/*",
    "arn:aws:ec2:${local.region}:${local.account}:volume/*",
  ]
  ec2_change_actions = [
    "ec2:AssociateAddress",
    "ec2:AttachVolume",
    "ec2:DeleteSecurityGroup",
    "ec2:DeleteVolume",
    "ec2:DetachVolume",
    "ec2:DisassociateAddress",
    "ec2:ModifyInstanceAttribute",
    "ec2:ModifyInstanceMetadataOptions",
    "ec2:ModifySecurityGroupRules",
    "ec2:ModifyVolume",
    "ec2:ReleaseAddress",
    "ec2:RevokeSecurityGroupEgress",
    "ec2:RevokeSecurityGroupIngress",
    "ec2:StartInstances",
    "ec2:StopInstances",
    "ec2:TerminateInstances",
  ]

  # The EC2 resources the infra root manages carry project=foundry
  # (providers.tf's default tags). Changing or deleting one without it is
  # refused, so the deploy role can't touch another project's host.
  ec2_resources = [
    "arn:aws:ec2:${local.region}:${local.account}:instance/*",
    "arn:aws:ec2:${local.region}:${local.account}:volume/*",
    "arn:aws:ec2:${local.region}:${local.account}:security-group/*",
    "arn:aws:ec2:${local.region}:${local.account}:elastic-ip/*",
  ]

  # The controller records a stop on the host under these tags (controller.tf).
  stop_tag_keys = ["foundry:stop-command", "foundry:stop-requested"]
}

data "aws_iam_openid_connect_provider" "github" {
  url = "https://token.actions.githubusercontent.com"
}

# ---------------------------------------------------------------------------
# The most a role the deploy role makes may ever do, whatever its own policy
# says: what the controllers and the Host control role need, on hosts tagged
# project=foundry and Foundry's own documents and log groups. Nothing here
# reads a parameter, decrypts, or changes IAM.

data "aws_iam_policy_document" "boundary" {
  statement {
    sid       = "OwnLogs"
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["arn:aws:logs:${local.region}:${local.account}:log-group:/aws/lambda/foundry-*:*"]
  }

  # None of these has resource-level permissions.
  statement {
    sid = "ObserveHosts"
    actions = [
      "ec2:DescribeInstances",
      "ssm:DescribeInstanceInformation",
      "ssm:GetCommandInvocation",
    ]
    resources = ["*"]
  }

  statement {
    sid       = "StartAndStopFoundryHosts"
    actions   = ["ec2:StartInstances", "ec2:StopInstances"]
    resources = ["arn:aws:ec2:${local.region}:${local.account}:instance/*"]

    condition {
      test     = "StringEquals"
      variable = "aws:ResourceTag/project"
      values   = ["foundry"]
    }
  }

  # SendCommand needs both the instance and the document.
  statement {
    sid       = "CommandsToFoundryHosts"
    actions   = ["ssm:SendCommand"]
    resources = ["arn:aws:ec2:${local.region}:${local.account}:instance/*"]

    condition {
      test     = "StringEquals"
      variable = "ssm:resourceTag/project"
      values   = ["foundry"]
    }
  }

  statement {
    sid       = "FoundryDocumentsOnly"
    actions   = ["ssm:SendCommand"]
    resources = [local.documents]
  }

  statement {
    sid       = "RecordStopsOnFoundryHosts"
    actions   = ["ec2:CreateTags"]
    resources = ["arn:aws:ec2:${local.region}:${local.account}:instance/*"]

    condition {
      test     = "StringEquals"
      variable = "aws:ResourceTag/project"
      values   = ["foundry"]
    }

    condition {
      test     = "ForAllValues:StringEquals"
      variable = "aws:TagKeys"
      values   = local.stop_tag_keys
    }

    condition {
      test     = "Null"
      variable = "aws:TagKeys"
      values   = ["false"]
    }
  }
}

resource "aws_iam_policy" "boundary" {
  name        = local.boundary_name
  description = "Permissions boundary for the Foundry controllers and Host control role - Foundry hosts, documents and logs only"
  policy      = data.aws_iam_policy_document.boundary.json
}

# ---------------------------------------------------------------------------
# The deploy role. It trusts only the token of a job in this repository's
# deploy environment, with the exact aud and sub.

data "aws_iam_policy_document" "deployer_trust" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [data.aws_iam_openid_connect_provider.github.arn]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values   = ["repo:${local.github_repository}:environment:${local.deploy_environment}"]
    }
  }
}

resource "aws_iam_role" "deployer" {
  name                  = "foundry-deployer"
  description           = "GitHub Actions: plan and apply the foundry infra root from the deploy environment"
  assume_role_policy    = data.aws_iam_policy_document.deployer_trust.json
  max_session_duration  = 3600
  force_detach_policies = true
}

# Everything a plan and apply of the infra root needs. Each service is pinned
# to Foundry's names or tags wherever the action allows it. Fixed rules hold
# whatever a commit says: a role is made or given a policy only inside the
# boundary, which stays on; a Function URL is AWS_IAM only and a call is
# granted only to The Lobby's site roles; alarms and the stop rule notify
# only the alerts topic; and nothing reads a parameter or secret.
data "aws_iam_policy_document" "deployer" {
  # OpenTofu state: the infra root's key and lock file.
  statement {
    sid       = "StateList"
    actions   = ["s3:ListBucket"]
    resources = [local.state_bucket_arn]
  }

  statement {
    sid       = "StateReadWrite"
    actions   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"]
    resources = local.state_object_arns
  }

  # Reading the bucket's own settings, which state.tf manages. Changing them
  # stays with Craig's admin sign-in, so a deploy can't loosen who reaches
  # the state. On the bucket's ARN, s3:Get* matches only bucket-level reads:
  # an object read needs an object's ARN.
  statement {
    sid       = "StateBucketSettingsRead"
    actions   = ["s3:Get*"]
    resources = [local.state_bucket_arn]
  }

  # EC2: only the calls OpenTofu makes for the host, its data volume, Elastic
  # IP and security group (recorded from the provider's own requests), all in
  # us-west-2. Nothing else: no other kind of EC2 resource, no snapshot, image,
  # network or account setting.
  statement {
    sid       = "Ec2Read"
    actions   = ["ec2:Describe*", "ec2:GetInstanceUefiData"]
    resources = ["*"]

    condition {
      test     = "StringEquals"
      variable = "aws:RequestedRegion"
      values   = [local.region]
    }
  }

  # Each new instance, volume, security group, rule or Elastic IP carries
  # project=foundry from the start (providers.tf's default tags), or isn't
  # made.
  statement {
    sid       = "CreateOnlyTaggedFoundry"
    actions   = local.ec2_create_actions
    resources = local.ec2_created_resources

    condition {
      test     = "StringEquals"
      variable = "aws:RequestTag/project"
      values   = ["foundry"]
    }
  }

  # The rest of what a create names: the host's subnet, AMI and primary
  # network interface, and the VPC its security group goes in. EC2 checks no
  # tag on an interface here, so a launch could also name another workload's
  # unattached interface (ADR 0007).
  statement {
    sid     = "CreateInTheSharedNetwork"
    actions = ["ec2:CreateSecurityGroup", "ec2:RunInstances"]
    resources = [
      "arn:aws:ec2:${local.region}::image/*",
      "arn:aws:ec2:${local.region}:${local.account}:network-interface/*",
      "arn:aws:ec2:${local.region}:${local.account}:subnet/*",
      "arn:aws:ec2:${local.region}:${local.account}:vpc/*",
    ]
  }

  # A new host or rule may use only Foundry's security group.
  statement {
    sid       = "UseOnlyFoundrySecurityGroups"
    actions   = ["ec2:AuthorizeSecurityGroupEgress", "ec2:AuthorizeSecurityGroupIngress", "ec2:RunInstances"]
    resources = ["arn:aws:ec2:${local.region}:${local.account}:security-group/*"]

    condition {
      test     = "StringEquals"
      variable = "aws:ResourceTag/project"
      values   = ["foundry"]
    }
  }

  # Tagging a resource as it is made. ec2:CreateAction holds the API's bare
  # name, such as RunInstances, not the IAM action.
  statement {
    sid       = "TagOnCreate"
    actions   = ["ec2:CreateTags"]
    resources = ["arn:aws:ec2:${local.region}:${local.account}:*/*"]

    condition {
      test     = "StringEquals"
      variable = "ec2:CreateAction"
      values   = [for action in local.ec2_create_actions : trimprefix(action, "ec2:")]
    }
  }

  # Changing, retagging or deleting a resource, which the denies below allow
  # only when it carries project=foundry.
  statement {
    sid       = "ChangeFoundryEc2Resources"
    actions   = concat(local.ec2_change_actions, ["ec2:CreateTags", "ec2:DeleteTags"])
    resources = ["*"]

    condition {
      test     = "StringEquals"
      variable = "aws:RequestedRegion"
      values   = [local.region]
    }
  }

  statement {
    sid       = "OnlyFoundryEc2Resources"
    effect    = "Deny"
    actions   = concat(local.ec2_change_actions, ["ec2:AuthorizeSecurityGroupEgress", "ec2:AuthorizeSecurityGroupIngress"])
    resources = local.ec2_resources

    condition {
      test     = "StringNotEquals"
      variable = "aws:ResourceTag/project"
      values   = ["foundry"]
    }
  }

  # Outside a create, only a resource that already carries project=foundry
  # may be tagged, whatever kind it is.
  statement {
    sid       = "TagOnlyFoundryEc2Resources"
    effect    = "Deny"
    actions   = ["ec2:CreateTags", "ec2:DeleteTags"]
    resources = ["*"]

    condition {
      test     = "StringNotEquals"
      variable = "aws:ResourceTag/project"
      values   = ["foundry"]
    }

    condition {
      test     = "Null"
      variable = "ec2:CreateAction"
      values   = ["true"]
    }
  }

  # The project tag is set only when a resource is created, so another
  # project's resource can't be tagged into the rules above.
  statement {
    sid       = "ProjectTagOnlyAtCreation"
    effect    = "Deny"
    actions   = ["ec2:CreateTags", "ec2:DeleteTags"]
    resources = ["*"]

    condition {
      test     = "ForAnyValue:StringEquals"
      variable = "aws:TagKeys"
      values   = ["project"]
    }

    condition {
      test     = "Null"
      variable = "ec2:CreateAction"
      values   = ["true"]
    }
  }

  # Lambda: the controllers, their configuration and tags.
  statement {
    sid = "Controllers"
    actions = [
      "lambda:CreateFunction",
      "lambda:DeleteFunction",
      "lambda:DeleteFunctionUrlConfig",
      "lambda:Get*",
      "lambda:List*",
      "lambda:RemovePermission",
      "lambda:TagResource",
      "lambda:UntagResource",
      "lambda:UpdateFunctionCode",
      "lambda:UpdateFunctionConfiguration",
    ]
    resources = local.functions
  }

  # A create always names the auth type; an update names it only when it
  # changes it, and then it must still be AWS_IAM.
  statement {
    sid       = "FunctionUrlIamOnly"
    actions   = ["lambda:CreateFunctionUrlConfig", "lambda:UpdateFunctionUrlConfig"]
    resources = local.functions

    condition {
      test     = "StringEqualsIfExists"
      variable = "lambda:FunctionUrlAuthType"
      values   = ["AWS_IAM"]
    }
  }

  statement {
    sid       = "InvokeGrantsToTheSiteOnly"
    actions   = ["lambda:AddPermission"]
    resources = local.functions

    condition {
      test     = "StringEquals"
      variable = "lambda:Principal"
      values   = local.site_role_arns
    }
  }

  # CloudWatch Logs: the controllers' log groups.
  statement {
    sid = "LogGroups"
    actions = [
      "logs:CreateLogGroup",
      "logs:DeleteLogGroup",
      "logs:DeleteRetentionPolicy",
      "logs:ListTagsForResource",
      "logs:PutRetentionPolicy",
      "logs:TagResource",
      "logs:UntagResource",
    ]
    resources = local.log_groups
  }

  # DescribeLogGroups takes no resource. It returns names and settings, never
  # log lines.
  statement {
    sid       = "LogGroupList"
    actions   = ["logs:DescribeLogGroups"]
    resources = ["*"]
  }

  # Alarms named foundry-*, which may notify only the alerts topic.
  statement {
    sid       = "AlarmsNotifyOnlyAlerts"
    actions   = ["cloudwatch:PutMetricAlarm"]
    resources = [local.alarms]

    condition {
      test     = "ForAllValues:StringEquals"
      variable = "cloudwatch:AlarmActions"
      values   = [local.alerts_topic_arn]
    }
  }

  statement {
    sid = "Alarms"
    actions = [
      "cloudwatch:DeleteAlarms",
      "cloudwatch:ListTagsForResource",
      "cloudwatch:TagResource",
      "cloudwatch:UntagResource",
    ]
    resources = [local.alarms]
  }

  # Alarm settings hold no secrets. On every alarm, because a refused read
  # would fail every plan.
  statement {
    sid       = "AlarmRead"
    actions   = ["cloudwatch:DescribeAlarms"]
    resources = ["*"]
  }

  # The foundry-stop-failed rule, whose only target is the alerts topic.
  statement {
    sid = "StopRule"
    actions = [
      "events:DeleteRule",
      "events:DescribeRule",
      "events:DisableRule",
      "events:EnableRule",
      "events:ListTagsForResource",
      "events:ListTargetsByRule",
      "events:PutRule",
      "events:RemoveTargets",
      "events:TagResource",
      "events:UntagResource",
    ]
    resources = [local.rules]
  }

  # events:TargetArn holds every target in the request, so each must be the
  # alerts topic (ForAllValues), and a request must name one (Null).
  statement {
    sid       = "StopRuleTargetsOnlyAlerts"
    actions   = ["events:PutTargets"]
    resources = [local.rules]

    condition {
      test     = "ForAllValues:ArnEquals"
      variable = "events:TargetArn"
      values   = [local.alerts_topic_arn]
    }

    condition {
      test     = "Null"
      variable = "events:TargetArn"
      values   = ["false"]
    }
  }

  # The foundry-shutdown and foundry-stop documents. Never their sharing, and
  # never sending one: only the Host control role and the controllers send.
  statement {
    sid = "Documents"
    actions = [
      "ssm:AddTagsToResource",
      "ssm:CreateDocument",
      "ssm:DeleteDocument",
      "ssm:DescribeDocument",
      "ssm:DescribeDocumentPermission",
      "ssm:GetDocument",
      "ssm:ListDocumentVersions",
      "ssm:ListTagsForResource",
      "ssm:RemoveTagsFromResource",
      "ssm:UpdateDocument",
      "ssm:UpdateDocumentDefaultVersion",
    ]
    resources = [local.documents]
  }

  # The snapshot policy is only read, so a plan can refresh it. A policy
  # snapshots every volume its target tags match and can share the snapshots
  # with other accounts, through foundry-dlm's service role, and DLM has no
  # condition key that limits either. So any change to it stays with Craig's
  # admin sign-in, like its role.
  statement {
    sid       = "SnapshotPolicyList"
    actions   = ["dlm:GetLifecyclePolicies"]
    resources = ["*"]
  }

  statement {
    sid = "SnapshotPolicyRead"
    actions = [
      "dlm:GetLifecyclePolicy",
      "dlm:ListTagsForResource",
    ]
    resources = ["arn:aws:dlm:${local.region}:${local.account}:policy/*"]

    condition {
      test     = "StringEquals"
      variable = "aws:ResourceTag/project"
      values   = ["foundry"]
    }
  }

  # IAM. A controller or the Host control role is made, or given a policy or
  # a boundary, only while it carries the boundary.
  statement {
    sid = "RolesInsideBoundary"
    actions = [
      "iam:AttachRolePolicy",
      "iam:CreateRole",
      "iam:DeleteRolePolicy",
      "iam:DetachRolePolicy",
      "iam:PutRolePermissionsBoundary",
      "iam:PutRolePolicy",
    ]
    resources = local.bounded_roles

    condition {
      test     = "StringEquals"
      variable = "iam:PermissionsBoundary"
      values   = [aws_iam_policy.boundary.arn]
    }
  }

  statement {
    sid = "BoundedRoles"
    actions = [
      "iam:DeleteRole",
      "iam:TagRole",
      "iam:UntagRole",
      "iam:UpdateAssumeRolePolicy",
      "iam:UpdateRole",
      "iam:UpdateRoleDescription",
    ]
    resources = local.bounded_roles
  }

  statement {
    sid    = "BoundaryStays"
    effect = "Deny"
    actions = [
      "iam:DeleteRolePermissionsBoundary",
    ]
    resources = ["*"]
  }

  # It makes and changes roles but never becomes one, even a role whose trust
  # policy names it: a trust policy that names a role in the same account
  # needs no allow from that role's own policy.
  statement {
    sid       = "NeverAssumeRoles"
    effect    = "Deny"
    actions   = ["sts:AssumeRole*"]
    resources = ["*"]
  }

  # Reading Foundry's roles and the host's instance profile back.
  statement {
    sid = "ReadFoundryRoles"
    actions = [
      "iam:GetInstanceProfile",
      "iam:GetRole",
      "iam:GetRolePolicy",
      "iam:ListAttachedRolePolicies",
      "iam:ListInstanceProfileTags",
      "iam:ListInstanceProfilesForRole",
      "iam:ListRolePolicies",
      "iam:ListRoleTags",
    ]
    resources = [
      local.foundry_roles,
      "arn:aws:iam::${local.account}:instance-profile/foundry-*",
    ]
  }

  # The GitHub provider ci.tf looks up. Finding it by URL lists the account's
  # providers.
  statement {
    sid       = "GitHubProviderList"
    actions   = ["iam:ListOpenIDConnectProviders"]
    resources = ["*"]
  }

  statement {
    sid       = "GitHubProviderRead"
    actions   = ["iam:GetOpenIDConnectProvider"]
    resources = ["arn:aws:iam::${local.account}:oidc-provider/*"]
  }

  # Each role goes only to the service that uses it.
  statement {
    sid       = "PassControllerRolesToLambda"
    actions   = ["iam:PassRole"]
    resources = ["arn:aws:iam::${local.account}:role/foundry-*-controller"]

    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["lambda.amazonaws.com"]
    }
  }

  statement {
    sid       = "PassHostRoleToEc2"
    actions   = ["iam:PassRole"]
    resources = ["arn:aws:iam::${local.account}:role/foundry-host"]

    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role_policy" "deployer" {
  name   = "foundry-deployer"
  role   = aws_iam_role.deployer.id
  policy = data.aws_iam_policy_document.deployer.json
}

# Deletes any inline policy or managed attachment not listed here, so one added
# by hand shows up as drift (G7).
resource "aws_iam_role_policies_exclusive" "deployer" {
  role_name    = aws_iam_role.deployer.name
  policy_names = [aws_iam_role_policy.deployer.name]
}

resource "aws_iam_role_policy_attachments_exclusive" "deployer" {
  role_name   = aws_iam_role.deployer.name
  policy_arns = []
}
