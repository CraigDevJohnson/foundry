# Offline plan checks of the deploy role and the boundary. The provider is
# mocked, so no AWS credentials or calls are needed: run
# `tofu -chdir=infra/bootstrap test` after `init -backend=false`.

mock_provider "aws" {
  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}"
    }
  }

  mock_data "aws_iam_openid_connect_provider" {
    defaults = {
      arn = "arn:aws:iam::793680745829:oidc-provider/token.actions.githubusercontent.com"
    }
  }

  mock_resource "aws_iam_policy" {
    defaults = {
      arn = "arn:aws:iam::793680745829:policy/foundry-workload-boundary"
    }
  }
}

run "deployer_trusts_only_the_deploy_environment" {
  command = plan

  assert {
    condition = jsonencode({
      for c in data.aws_iam_policy_document.deployer_trust.statement[0].condition : c.variable => [c.test, c.values]
      }) == jsonencode({
      "token.actions.githubusercontent.com:aud" = ["StringEquals", ["sts.amazonaws.com"]]
      "token.actions.githubusercontent.com:sub" = ["StringEquals", ["repo:CraigDevJohnson@42454849/foundry@1361164956:environment:deploy"]]
    })
    error_message = "The deploy role must require the STS audience and pin the immutable sub to the deploy environment."
  }

  assert {
    condition     = aws_iam_role.deployer.name == "foundry-deployer" && aws_iam_policy.boundary.name == "foundry-workload-boundary"
    error_message = "The infra root names the boundary and the README names the role: keep both names."
  }
}

# A role is made or given a policy only inside the boundary, the boundary
# stays on, and the deploy role can't reach itself.
run "roles_change_only_inside_the_boundary" {
  command = plan

  assert {
    condition = alltrue([
      for s in data.aws_iam_policy_document.deployer.statement :
      length(setintersection(s.actions, toset([
        "iam:AttachRolePolicy", "iam:CreateRole", "iam:DeleteRolePolicy", "iam:DetachRolePolicy",
        "iam:PutRolePermissionsBoundary", "iam:PutRolePolicy",
        ]))) == 0 || (
        s.sid == "RolesInsideBoundary" && anytrue([
          for c in s.condition : c.test == "StringEquals" && c.variable == "iam:PermissionsBoundary" &&
          toset(c.values) == toset(["arn:aws:iam::793680745829:policy/foundry-workload-boundary"])
        ])
      )
    ])
    error_message = "Only RolesInsideBoundary may grant a role's policies, and only with the boundary on."
  }

  assert {
    condition = anytrue([
      for s in data.aws_iam_policy_document.deployer.statement :
      s.effect == "Deny" && contains(tolist(s.actions), "iam:DeleteRolePermissionsBoundary") && s.resources == toset(["*"])
    ])
    error_message = "The deploy role must never remove a role's boundary."
  }

  # Every IAM change is on the bounded roles, whose patterns don't match
  # foundry-deployer, foundry-host or foundry-dlm. Reads and PassRole aren't
  # changes.
  assert {
    condition = alltrue(flatten([
      for s in data.aws_iam_policy_document.deployer.statement : [
        for r in s.resources : alltrue([
          for name in ["foundry-deployer", "foundry-host", "foundry-dlm"] :
          !can(regex("^${replace(r, "*", ".*")}$", "arn:aws:iam::793680745829:role/${name}"))
        ])
        ] if s.effect != "Deny" && anytrue([
          for a in s.actions : startswith(a, "iam:") && !startswith(a, "iam:Get") && !startswith(a, "iam:List") && a != "iam:PassRole"
      ])
    ]))
    error_message = "No IAM change may reach the deploy role itself, the host's role or the snapshot role."
  }

  assert {
    condition = alltrue(flatten([
      for s in data.aws_iam_policy_document.deployer.statement : [
        for a in s.actions : !startswith(a, "iam:") || startswith(a, "iam:Get") || startswith(a, "iam:List") || a == "iam:PassRole" || contains(["RolesInsideBoundary", "BoundedRoles"], s.sid)
      ] if s.effect != "Deny"
    ]))
    error_message = "IAM changes belong in RolesInsideBoundary or BoundedRoles, nowhere else."
  }
}

# Each role is handed only to the service that uses it.
run "roles_are_passed_only_to_their_service" {
  command = plan

  assert {
    condition = jsonencode({
      for s in data.aws_iam_policy_document.deployer.statement :
      one(s.resources) => [for c in s.condition : c.values if c.variable == "iam:PassedToService"][0]
      if contains(tolist(s.actions), "iam:PassRole")
      }) == jsonencode({
      "arn:aws:iam::793680745829:role/foundry-*-controller" = ["lambda.amazonaws.com"]
      "arn:aws:iam::793680745829:role/foundry-dlm"          = ["dlm.amazonaws.com"]
      "arn:aws:iam::793680745829:role/foundry-host"         = ["ec2.amazonaws.com"]
    })
    error_message = "PassRole must name one role per statement and the one service that uses it."
  }
}

run "controllers_stay_private" {
  command = plan

  assert {
    condition = anytrue([
      for s in data.aws_iam_policy_document.deployer.statement :
      s.actions == toset(["lambda:CreateFunctionUrlConfig", "lambda:UpdateFunctionUrlConfig"]) &&
      anytrue([for c in s.condition : c.variable == "lambda:FunctionUrlAuthType" && toset(c.values) == toset(["AWS_IAM"])])
    ])
    error_message = "A Function URL may only be AWS_IAM."
  }

  assert {
    condition = alltrue([
      for s in data.aws_iam_policy_document.deployer.statement :
      !contains(tolist(s.actions), "lambda:AddPermission") || anytrue([
        for c in s.condition : c.test == "StringEquals" && c.variable == "lambda:Principal" &&
        toset(c.values) == toset(["arn:aws:iam::793680745829:role/site-dev-server", "arn:aws:iam::793680745829:role/site-prod-server"])
      ])
    ])
    error_message = "A controller may only be opened to The Lobby's site roles."
  }

  assert {
    condition = alltrue([
      for s in data.aws_iam_policy_document.deployer.statement :
      length(setintersection(s.actions, toset(["lambda:CreateFunctionUrlConfig", "lambda:UpdateFunctionUrlConfig", "lambda:AddPermission", "lambda:*"]))) == 0 ||
      s.sid == "FunctionUrlIamOnly" || s.sid == "InvokeGrantsToTheSiteOnly"
    ])
    error_message = "No other statement may grant Function URL or invoke permission changes."
  }
}

run "alerts_go_only_to_the_alerts_topic" {
  command = plan

  assert {
    condition = alltrue([
      for s in data.aws_iam_policy_document.deployer.statement :
      !contains(tolist(s.actions), "cloudwatch:PutMetricAlarm") || anytrue([
        for c in s.condition : c.test == "ForAllValues:StringEquals" && c.variable == "cloudwatch:AlarmActions" &&
        toset(c.values) == toset(["arn:aws:sns:us-west-2:793680745829:alerts"])
      ])
    ])
    error_message = "An alarm may only notify the alerts topic."
  }

  assert {
    condition = alltrue([
      for s in data.aws_iam_policy_document.deployer.statement :
      !contains(tolist(s.actions), "events:PutTargets") || anytrue([
        for c in s.condition : c.test == "StringEquals" && c.variable == "events:TargetArn" &&
        toset(c.values) == toset(["arn:aws:sns:us-west-2:793680745829:alerts"])
      ])
    ])
    error_message = "The stop rule may only target the alerts topic."
  }
}

# The deploy role reads and writes only the infra root's state, and never a
# parameter, secret or key.
run "no_state_or_secret_beyond_its_own" {
  command = plan

  assert {
    condition = one([
      for s in data.aws_iam_policy_document.deployer.statement : s.resources if contains(tolist(s.actions), "s3:PutObject")
      ]) == toset([
      "arn:aws:s3:::foundry-tofu-state-793680745829/foundry/terraform.tfstate",
      "arn:aws:s3:::foundry-tofu-state-793680745829/foundry/terraform.tfstate.tflock",
    ])
    error_message = "State writes must be the infra root's key and lock file only, never the bootstrap's."
  }

  assert {
    condition = alltrue(flatten([
      for s in data.aws_iam_policy_document.deployer.statement : [
        for a in s.actions : !can(regex("^(ssm:GetParameter|ssm:SendCommand|ssm:StartSession|ssm:ModifyDocumentPermission|secretsmanager:|kms:|sts:)", a))
      ] if s.effect != "Deny"
    ]))
    error_message = "The deploy role must never read a parameter, secret or key, run a command, open a session, share a document or assume another role."
  }

  assert {
    condition = alltrue([
      for s in data.aws_iam_policy_document.deployer.statement :
      !contains(tolist(s.actions), "s3:Get*") || s.resources == toset(["arn:aws:s3:::foundry-tofu-state-793680745829"])
    ])
    error_message = "s3:Get* must stay on the bucket's own ARN, where it reaches no object."
  }

  assert {
    condition = alltrue(flatten([
      for s in data.aws_iam_policy_document.deployer.statement : [
        for a in s.actions : !startswith(a, "s3:") || contains(["s3:ListBucket", "s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:Get*"], a)
      ] if s.effect != "Deny"
    ]))
    error_message = "The deploy role must never change the state bucket's settings: that stays with Craig's admin sign-in."
  }
}

run "ec2_changes_stay_on_foundry_resources" {
  command = plan

  # Exactly the calls the provider makes for the host, its data volume,
  # Elastic IP and security group, besides reading.
  assert {
    condition = toset(flatten([
      for s in data.aws_iam_policy_document.deployer.statement :
      [for a in s.actions : a if startswith(a, "ec2:")] if s.effect != "Deny"
      ])) == toset([
      "ec2:Describe*", "ec2:GetInstanceUefiData",
      "ec2:AllocateAddress", "ec2:AuthorizeSecurityGroupEgress", "ec2:AuthorizeSecurityGroupIngress",
      "ec2:CreateSecurityGroup", "ec2:CreateVolume", "ec2:RunInstances",
      "ec2:AssociateAddress", "ec2:AttachVolume", "ec2:DeleteSecurityGroup", "ec2:DeleteVolume",
      "ec2:DetachVolume", "ec2:DisassociateAddress", "ec2:ModifyInstanceAttribute",
      "ec2:ModifyInstanceMetadataOptions", "ec2:ModifyNetworkInterfaceAttribute",
      "ec2:ModifySecurityGroupRules", "ec2:ModifyVolume", "ec2:ReleaseAddress",
      "ec2:RevokeSecurityGroupEgress", "ec2:RevokeSecurityGroupIngress", "ec2:StartInstances",
      "ec2:StopInstances", "ec2:TerminateInstances", "ec2:CreateTags", "ec2:DeleteTags",
    ])
    error_message = "The deploy role may make only the EC2 calls the infra root needs."
  }

  # A new instance, volume, Elastic IP or rule is allowed only with
  # project=foundry in the request, and a new host or rule may use only a
  # security group that carries it.
  assert {
    condition = alltrue([
      for s in data.aws_iam_policy_document.deployer.statement :
      s.effect == "Deny" ||
      length(setintersection(s.actions, toset(["ec2:AllocateAddress", "ec2:AuthorizeSecurityGroupEgress", "ec2:AuthorizeSecurityGroupIngress", "ec2:CreateSecurityGroup", "ec2:CreateVolume", "ec2:RunInstances"]))) == 0 ||
      anytrue([for c in s.condition : c.test == "StringEquals" && c.variable == "aws:RequestTag/project" && toset(c.values) == toset(["foundry"])]) ||
      (
        length(setintersection(s.resources, toset([
          "*",
          "arn:aws:ec2:us-west-2:793680745829:elastic-ip/*",
          "arn:aws:ec2:us-west-2:793680745829:instance/*",
          "arn:aws:ec2:us-west-2:793680745829:security-group-rule/*",
          "arn:aws:ec2:us-west-2:793680745829:volume/*",
        ]))) == 0 &&
        (
          !contains(tolist(s.resources), "arn:aws:ec2:us-west-2:793680745829:security-group/*") ||
          (
            !contains(tolist(s.actions), "ec2:CreateSecurityGroup") &&
            anytrue([for c in s.condition : c.test == "StringEquals" && c.variable == "aws:ResourceTag/project" && toset(c.values) == toset(["foundry"])])
          )
        )
      )
    ])
    error_message = "Every EC2 resource the deploy role creates must carry project=foundry in the request, and use only Foundry's security group."
  }

  assert {
    condition = anytrue([
      for s in data.aws_iam_policy_document.deployer.statement :
      s.sid == "OnlyFoundryEc2Resources" && s.effect == "Deny" &&
      length(setintersection(s.actions, toset(["ec2:TerminateInstances", "ec2:DeleteVolume", "ec2:DetachVolume", "ec2:ReleaseAddress", "ec2:DeleteSecurityGroup", "ec2:ModifyNetworkInterfaceAttribute", "ec2:AuthorizeSecurityGroupIngress"]))) == 7 &&
      anytrue([for c in s.condition : c.test == "StringNotEquals" && c.variable == "aws:ResourceTag/project" && toset(c.values) == toset(["foundry"])])
    ])
    error_message = "Destroying or changing an EC2 resource must need project=foundry."
  }

  assert {
    condition = anytrue([
      for s in data.aws_iam_policy_document.deployer.statement :
      s.sid == "TagOnlyFoundryEc2Resources" && s.effect == "Deny" && s.actions == toset(["ec2:CreateTags", "ec2:DeleteTags"]) && s.resources == toset(["*"]) &&
      anytrue([for c in s.condition : c.test == "StringNotEquals" && c.variable == "aws:ResourceTag/project" && toset(c.values) == toset(["foundry"])]) &&
      anytrue([for c in s.condition : c.test == "Null" && c.variable == "ec2:CreateAction" && toset(c.values) == toset(["true"])])
    ])
    error_message = "Outside a create, only a resource carrying project=foundry may be tagged."
  }

  assert {
    condition = anytrue([
      for s in data.aws_iam_policy_document.deployer.statement :
      s.sid == "ProjectTagOnlyAtCreation" && s.effect == "Deny" && s.actions == toset(["ec2:CreateTags", "ec2:DeleteTags"])
    ])
    error_message = "The project tag must only be set when a resource is created."
  }

  # A grant on every resource holds only in us-west-2; the others name
  # us-west-2 in their ARNs.
  assert {
    condition = alltrue([
      for s in data.aws_iam_policy_document.deployer.statement :
      s.effect == "Deny" || length([for a in s.actions : a if startswith(a, "ec2:")]) == 0 || (
        contains(tolist(s.resources), "*") ?
        anytrue([for c in s.condition : c.variable == "aws:RequestedRegion" && toset(c.values) == toset(["us-west-2"])]) :
        alltrue([for r in s.resources : startswith(r, "arn:aws:ec2:us-west-2:")])
      )
    ])
    error_message = "EC2 is granted in us-west-2 only."
  }
}

# The boundary is what a role the deploy role makes can ever do: Foundry's
# hosts, documents and logs, and never IAM, parameters, keys or other
# documents.
run "boundary_keeps_roles_to_foundry" {
  command = plan

  assert {
    condition = alltrue(flatten([
      for s in data.aws_iam_policy_document.boundary.statement : [
        for a in s.actions : contains([
          "logs:CreateLogStream", "logs:PutLogEvents",
          "ec2:DescribeInstances", "ssm:DescribeInstanceInformation", "ssm:GetCommandInvocation",
          "ec2:StartInstances", "ec2:StopInstances", "ssm:SendCommand", "ec2:CreateTags",
        ], a)
      ]
    ]))
    error_message = "The boundary grants only the controllers' and the Host control role's actions."
  }

  assert {
    condition = alltrue([
      for s in data.aws_iam_policy_document.boundary.statement :
      s.resources != toset(["*"]) || s.actions == toset(["ec2:DescribeInstances", "ssm:DescribeInstanceInformation", "ssm:GetCommandInvocation"])
    ])
    error_message = "Only the reads without resource-level permissions may use a * resource."
  }

  assert {
    condition = alltrue([
      for s in data.aws_iam_policy_document.boundary.statement :
      length(setintersection(s.actions, toset(["ec2:StartInstances", "ec2:StopInstances", "ec2:CreateTags"]))) == 0 ||
      anytrue([for c in s.condition : c.variable == "aws:ResourceTag/project" && toset(c.values) == toset(["foundry"])])
    ])
    error_message = "Starting, stopping and tagging need a host tagged project=foundry."
  }

  assert {
    condition = alltrue([
      for s in data.aws_iam_policy_document.boundary.statement :
      !contains(tolist(s.actions), "ssm:SendCommand") ||
      s.resources == toset(["arn:aws:ssm:us-west-2:793680745829:document/foundry-*"]) ||
      anytrue([for c in s.condition : c.variable == "ssm:resourceTag/project" && toset(c.values) == toset(["foundry"])])
    ])
    error_message = "SendCommand only of Foundry's documents to hosts tagged project=foundry, never AWS-RunShellScript."
  }

  assert {
    condition = alltrue([
      for s in data.aws_iam_policy_document.boundary.statement :
      !contains(tolist(s.actions), "ec2:CreateTags") || anytrue([
        for c in s.condition : c.test == "ForAllValues:StringEquals" && c.variable == "aws:TagKeys" &&
        toset(c.values) == toset(["foundry:stop-command", "foundry:stop-requested"])
      ])
    ])
    error_message = "Tagging is only the controller's two stop tags."
  }
}
