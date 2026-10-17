# Offline plan checks. The provider is mocked, so no AWS credentials or calls
# are needed: run `tofu -chdir=infra test` after `init -backend=false`.

mock_provider "aws" {
  mock_resource "aws_s3_bucket" {
    defaults = {
      arn = "arn:aws:s3:::foundry-tofu-state-793680745829"
    }
  }

  mock_resource "aws_iam_role" {
    defaults = {
      arn = "arn:aws:iam::793680745829:role/mock"
    }
  }

  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}"
    }
  }

  mock_data "aws_vpc" {
    defaults = {
      id = "vpc-033b6d36d513f2b49"
    }
  }

  mock_data "aws_subnet" {
    defaults = {
      id                = "subnet-09419fb2922a65f2a"
      availability_zone = "us-west-2a"
    }
  }

  mock_resource "aws_ebs_volume" {
    defaults = {
      id = "vol-0123456789abcdef0"
    }
  }

  mock_resource "aws_ssm_document" {
    defaults = {
      arn = "arn:aws:ssm:us-west-2:793680745829:document/foundry-shutdown"
    }
  }

  mock_resource "aws_instance" {
    defaults = {
      id  = "i-0123456789abcdef0"
      arn = "arn:aws:ec2:us-west-2:793680745829:instance/i-0123456789abcdef0"
    }
  }

  mock_resource "aws_cloudwatch_log_group" {
    defaults = {
      arn = "arn:aws:logs:us-west-2:793680745829:log-group:/aws/lambda/foundry-mock-controller"
    }
  }

  mock_resource "aws_lambda_function" {
    defaults = {
      arn = "arn:aws:lambda:us-west-2:793680745829:function:foundry-mock-controller"
    }
  }

  mock_resource "aws_lambda_function_url" {
    defaults = {
      function_url = "https://mock.lambda-url.us-west-2.on.aws/"
    }
  }
}

# The controller's zip comes from `task build`, which the offline checks don't
# run. The fixture stands in for it.
variables {
  controller_package_path = "tests/fixtures/bootstrap.zip"
}

run "state_bucket_is_tls_only" {
  command = plan

  assert {
    condition = anytrue([
      for c in data.aws_iam_policy_document.state_tls_only.statement[0].condition :
      c.test == "Bool" && c.variable == "aws:SecureTransport" && contains(tolist(c.values), "false")
    ]) && data.aws_iam_policy_document.state_tls_only.statement[0].effect == "Deny"
    error_message = "The state bucket policy must deny requests without TLS."
  }
}

# --- Workloads host ---

run "security_group_opens_only_player_ports" {
  command = plan

  assert {
    condition = toset([
      for r in aws_vpc_security_group_ingress_rule.public :
      "${r.ip_protocol}/${r.from_port}-${r.to_port}/${r.cidr_ipv4}"
      ]) == toset([
      "tcp/80-80/0.0.0.0/0",
      "tcp/30000-30000/0.0.0.0/0",
    ]) && length(aws_vpc_security_group_ingress_rule.public) == 2
    error_message = "Ingress must be exactly 80/tcp and 30000/tcp from 0.0.0.0/0: no SSH, no /32 rule."
  }

  assert {
    condition = alltrue([
      for r in aws_vpc_security_group_ingress_rule.public :
      r.cidr_ipv6 == null && r.prefix_list_id == null && r.referenced_security_group_id == null
    ])
    error_message = "Ingress rules must have no other sources."
  }

  assert {
    condition = (
      aws_vpc_security_group_egress_rule.all_ipv4.ip_protocol == "-1" &&
      aws_vpc_security_group_egress_rule.all_ipv4.cidr_ipv4 == "0.0.0.0/0"
    )
    error_message = "The explicit allow-all IPv4 egress rule must exist, or first-boot user data has no network."
  }

  assert {
    condition     = data.aws_subnet.host.availability_zone_id == "usw2-az2" && data.aws_subnet.host.default_for_az
    error_message = "The host subnet must be the default subnet in usw2-az2."
  }
}

run "host_role_is_least_privilege" {
  command = plan

  assert {
    condition     = aws_iam_role_policy_attachments_exclusive.host.policy_arns == toset(["arn:aws:iam::aws:policy/AmazonSSMManagedEC2InstanceDefaultPolicy"])
    error_message = "The host role keeps only the SSM default instance policy, never AmazonSSMManagedInstanceCore."
  }

  assert {
    condition = alltrue(flatten([
      for s in data.aws_iam_policy_document.host.statement : [
        for a in s.actions : !startswith(lower(a), "kms:") && !strcontains(a, "*")
      ]
    ]))
    error_message = "The host role must have no kms actions and no action wildcards."
  }

  assert {
    condition = alltrue(flatten([
      for s in data.aws_iam_policy_document.host.statement : [
        for r in s.resources : r != "*"
      ]
    ]))
    error_message = "The host role must grant nothing on *."
  }

  assert {
    condition = alltrue(flatten([
      for s in data.aws_iam_policy_document.host.statement : [
        for r in s.resources : can(regex("^arn:aws:ssm:us-west-2:793680745829:parameter/foundry(/\\*)?$", r))
      ] if anytrue([for a in s.actions : startswith(a, "ssm:")])
    ]))
    error_message = "ssm:GetParameter* must be scoped to parameter/foundry and parameter/foundry/*."
  }

  assert {
    condition     = one(data.aws_iam_policy_document.host_trust.statement[0].principals).identifiers == toset(["ec2.amazonaws.com"])
    error_message = "The host role must trust only EC2."
  }

  assert {
    condition     = toset(aws_iam_role_policies_exclusive.host.policy_names) == toset(["foundry-host"])
    error_message = "The host role's exclusive inline list must hold only foundry-host."
  }
}

run "host_instance_settings" {
  command = plan

  assert {
    condition     = aws_instance.host.instance_type == "t4g.small" && aws_instance.host.ami == "ami-0baa3cf0d687ee495"
    error_message = "The host is a t4g.small on the committed AL2023 arm64 AMI."
  }

  assert {
    condition     = aws_instance.host.instance_initiated_shutdown_behavior == "stop"
    error_message = "A power-off from inside the host must stop it, never terminate it: the foundry-stop document powers it off."
  }

  assert {
    condition = (
      aws_instance.host.metadata_options[0].http_tokens == "required" &&
      aws_instance.host.metadata_options[0].http_put_response_hop_limit == 1
    )
    error_message = "IMDSv2 must be required, with a hop limit of 1."
  }

  assert {
    condition = (
      aws_instance.host.root_block_device[0].encrypted &&
      aws_instance.host.root_block_device[0].volume_type == "gp3" &&
      aws_instance.host.root_block_device[0].volume_size == 10
    )
    error_message = "The root volume must be an encrypted 10 GiB gp3 volume."
  }

  assert {
    condition = alltrue([
      strcontains(aws_instance.host.user_data, "dnf install -y nodejs24 certbot"),
      strcontains(aws_instance.host.user_data, "DATA_DEVICE='/dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_vol0123456789abcdef0'"),
      strcontains(aws_instance.host.user_data, "echo \"$DATA_DEVICE /foundry ext4 defaults,nofail 0 2\" >>/etc/fstab"),
      strcontains(aws_instance.host.user_data, "ExecStart=/usr/bin/node-24 /foundry/foundryvtt/main.js"),
      strcontains(aws_instance.host.user_data, "OnCalendar=*-*-* 10:00:00 UTC\nPersistent=true\n"),
      strcontains(aws_instance.host.user_data, "systemctl enable --now certbot-renew.timer"),
      strcontains(aws_instance.host.user_data, "/etc/letsencrypt/renewal-hooks/deploy/foundry.sh"),
      strcontains(aws_instance.host.user_data, "systemctl try-restart foundry.service"),
      strcontains(aws_instance.host.user_data, "\nRestart=on-failure\nRestartSec=5\n"),
    ])
    error_message = "User data must install Node.js 24 and certbot, the fstab entry for the data volume ID, the unit with Restart=on-failure, the timer override that catches up a missed run at boot, and the deploy hook."
  }

  assert {
    condition = !anytrue([
      strcontains(aws_instance.host.user_data, "ExecStartPre"),
      strcontains(aws_instance.host.user_data, "mkfs"),
      strcontains(aws_instance.host.user_data, "enable foundry"),
      strcontains(aws_instance.host.user_data, "enable --now foundry"),
      strcontains(aws_instance.host.user_data, "Restart=no"),
      strcontains(aws_instance.host.user_data, "LABEL=foundry-data /foundry"),
    ])
    error_message = "User data must not keep the preflight line or Restart=no, mount /foundry by label, format a disk, or enable Foundry before the restore."
  }
}

run "data_volume_is_kept_with_the_host" {
  command = plan

  assert {
    condition = (
      aws_ebs_volume.data.size == 20 &&
      aws_ebs_volume.data.type == "gp3" &&
      aws_ebs_volume.data.encrypted &&
      aws_ebs_volume.data.tags["Name"] == "foundry-data"
    )
    error_message = "The data volume must be an encrypted 20 GiB gp3 volume named foundry-data."
  }

  assert {
    condition     = aws_ebs_volume.data.availability_zone == data.aws_subnet.host.availability_zone
    error_message = "The data volume must be in the host's Availability Zone."
  }

  assert {
    condition = (
      aws_volume_attachment.data.device_name == "/dev/sdf" &&
      aws_volume_attachment.data.stop_instance_before_detaching
    )
    error_message = "The data volume attaches on /dev/sdf and detaches only from a stopped instance."
  }
}

run "workloads_snapshot_policy" {
  command = plan

  assert {
    condition = (
      aws_dlm_lifecycle_policy.host.state == "ENABLED" &&
      aws_dlm_lifecycle_policy.host.policy_details[0].resource_types == tolist(["VOLUME"]) &&
      aws_dlm_lifecycle_policy.host.policy_details[0].target_tags == tomap({ project = "foundry" })
    )
    error_message = "The workloads snapshot policy must be enabled and target volumes tagged project=foundry."
  }

  assert {
    condition = jsonencode([
      for s in aws_dlm_lifecycle_policy.host.policy_details[0].schedule : {
        name     = s.name
        copy     = s.copy_tags
        time     = try(s.create_rule[0].times[0], null)
        interval = s.create_rule[0].interval
        cron     = s.create_rule[0].cron_expression
        keep     = s.retain_rule[0].count
      }
      ]) == jsonencode([
      { name = "daily", copy = true, time = "10:00", interval = 24, cron = null, keep = 7 },
      { name = "weekly", copy = true, time = null, interval = null, cron = "cron(0 11 ? * SUN *)", keep = 4 },
    ])
    error_message = "Expected daily 10:00 UTC keeping 7 and weekly Sunday 11:00 UTC keeping 4."
  }

  assert {
    condition = jsonencode({
      for c in data.aws_iam_policy_document.host_dlm_trust.statement[0].condition : c.variable => [c.test, c.values]
      }) == jsonencode({
      "aws:SourceAccount" = ["StringEquals", ["793680745829"]]
      "aws:SourceArn"     = ["ArnLike", ["arn:aws:dlm:us-west-2:793680745829:policy/*"]]
    })
    error_message = "The workloads DLM role must trust only DLM policies in the workloads account."
  }
}

run "status_alarm_targets_workloads_alerts" {
  command = plan

  assert {
    condition = (
      aws_cloudwatch_metric_alarm.host_status_check.metric_name == "StatusCheckFailed" &&
      aws_cloudwatch_metric_alarm.host_status_check.alarm_actions == toset(["arn:aws:sns:us-west-2:793680745829:alerts"])
    )
    error_message = "D18: the StatusCheckFailed alarm notifies the workloads alerts topic."
  }
}

# --- GitHub Actions host control ---

# infra/bootstrap makes the role and owns its trust policy, which its tests
# check (ADR 0007).
run "host_control_role_is_least_privilege" {
  command = plan

  assert {
    condition = (
      aws_iam_role_policy.host_control.role == "foundry-host-control-ci" &&
      toset(aws_iam_role_policies_exclusive.host_control.policy_names) == toset(["foundry-host-control"]) &&
      aws_iam_role_policies_exclusive.host_control.role_name == "foundry-host-control-ci" &&
      length(aws_iam_role_policy_attachments_exclusive.host_control.policy_arns) == 0
    )
    error_message = "foundry-host-control-ci keeps only its own inline policy and no managed policies."
  }

  assert {
    condition = alltrue([
      for s in data.aws_iam_policy_document.host_control.statement :
      s.resources == toset(["*"]) ? s.actions == toset(["ec2:DescribeInstances"]) || s.actions == toset(["ssm:GetCommandInvocation"]) : true
    ])
    error_message = "Only the read actions without resource-level permissions may use a * resource."
  }

  assert {
    condition = alltrue([
      for r in flatten([for s in data.aws_iam_policy_document.host_control.statement : tolist(s.resources) if contains(tolist(s.actions), "ssm:SendCommand")]) :
      !strcontains(r, ":document/") || r == "arn:aws:ssm:us-west-2:793680745829:document/foundry-shutdown"
    ])
    error_message = "SendCommand may use only the foundry-shutdown document, never AWS-RunShellScript."
  }

  assert {
    condition = (
      aws_ssm_document.shutdown.document_type == "Command" &&
      !strcontains(aws_ssm_document.shutdown.content, "\"parameters\"")
    )
    error_message = "foundry-shutdown must be a Command document with no parameters."
  }
}

# --- The Deploy workflow (ADR 0007) ---

# The deploy role never makes a role or changes who may assume one: the
# controllers and the Host control role come from infra/bootstrap, inside the
# boundary, and this root sets only their inline policies. The host's role and
# the snapshot role are left to Craig's admin profile.
run "the_root_makes_only_the_admin_roles" {
  command = plan

  assert {
    condition     = toset([for r in [aws_iam_role.host, aws_iam_role.host_dlm] : r.name]) == toset(["foundry-host", "foundry-dlm"])
    error_message = "The only roles this root makes are foundry-host and foundry-dlm, which only Craig's admin profile changes."
  }

  assert {
    condition = alltrue([
      for environment in ["dev", "prod"] :
      aws_lambda_function.controller[environment].role == "arn:aws:iam::793680745829:role/foundry-${environment}-controller"
    ])
    error_message = "Each controller runs as the role of its own name, which infra/bootstrap makes."
  }

  assert {
    condition     = aws_iam_role.host.permissions_boundary == null && aws_iam_role.host_dlm.permissions_boundary == null
    error_message = "The host and snapshot roles carry no boundary: the deploy role never changes them."
  }
}

# On a computer the plan runs as Craig's admin profile; the Deploy workflow's
# deploy.tfvars clears it, so the deploy role's credentials are used.
run "the_workflow_uses_the_environment_credentials" {
  command = plan

  variables {
    aws_profile = null
  }

  assert {
    condition     = var.aws_profile == null && can(regex("(?m)^aws_profile = null$", file("deploy.tfvars")))
    error_message = "deploy.tfvars must clear the profile, and the root must accept that."
  }
}

# --- The Lobby's controller ---

run "controller_names_carry_the_site_environment" {
  command = plan

  assert {
    condition = alltrue([
      for environment in ["dev", "prod"] : (
        aws_lambda_function.controller[environment].function_name == "foundry-${environment}-controller" &&
        aws_cloudwatch_log_group.controller[environment].name == "/aws/lambda/foundry-${environment}-controller" &&
        aws_cloudwatch_metric_alarm.controller_failing[environment].alarm_name == "foundry-${environment}-controller-failing"
      )
    ]) && length(aws_lambda_function.controller) == 2
    error_message = "There is one controller per Lobby site environment, dev and prod, named foundry-<env>-controller, because the site's boundary lets site-<env>-server call only *-<env>-*."
  }

  assert {
    condition = alltrue([
      for f in aws_lambda_function.controller : (
        f.runtime == "provided.al2023" &&
        f.architectures == tolist(["arm64"]) &&
        f.handler == "bootstrap" &&
        f.memory_size == 256 &&
        f.timeout == 10
      )
    ])
    error_message = "The zip from `task build` is one static arm64 binary named bootstrap for provided.al2023, with 256 MB and a 10 second timeout."
  }

  assert {
    condition = alltrue([
      for environment in ["dev", "prod"] : (
        aws_cloudwatch_log_group.controller[environment].retention_in_days == 30 &&
        aws_lambda_function.controller[environment].logging_config[0].log_group == "/aws/lambda/foundry-${environment}-controller"
      )
    ])
    error_message = "Each controller logs to its own log group, kept 30 days (G8)."
  }
}

run "only_each_environments_site_role_may_call_its_controller" {
  command = plan

  assert {
    condition     = alltrue([for u in aws_lambda_function_url.controller : u.authorization_type == "AWS_IAM"])
    error_message = "Each Function URL must require a signed request."
  }

  assert {
    condition = alltrue([
      for environment in ["dev", "prod"] : (
        aws_lambda_permission.controller_site_url[environment].function_name == "foundry-${environment}-controller" &&
        aws_lambda_permission.controller_site_url[environment].principal == "arn:aws:iam::793680745829:role/site-${environment}-server" &&
        aws_lambda_permission.controller_site_url[environment].action == "lambda:InvokeFunctionUrl" &&
        aws_lambda_permission.controller_site_url[environment].function_url_auth_type == "AWS_IAM"
      )
    ]) && length(aws_lambda_permission.controller_site_url) == 2
    error_message = "lambda:InvokeFunctionUrl on each controller goes to that environment's site role alone."
  }

  assert {
    condition = alltrue([
      for environment in ["dev", "prod"] : (
        aws_lambda_permission.controller_site_invoke[environment].function_name == "foundry-${environment}-controller" &&
        aws_lambda_permission.controller_site_invoke[environment].principal == "arn:aws:iam::793680745829:role/site-${environment}-server" &&
        aws_lambda_permission.controller_site_invoke[environment].action == "lambda:InvokeFunction" &&
        aws_lambda_permission.controller_site_invoke[environment].invoked_via_function_url == true
      )
    ]) && length(aws_lambda_permission.controller_site_invoke) == 2
    error_message = "lambda:InvokeFunction on each controller goes to that environment's site role alone, and only through the URL."
  }
}

run "controller_role_is_least_privilege" {
  command = plan

  assert {
    condition = alltrue([
      for d in data.aws_iam_policy_document.controller :
      toset(flatten([for s in d.statement : tolist(s.actions)])) == toset([
        "logs:CreateLogStream",
        "logs:PutLogEvents",
        "ec2:DescribeInstances",
        "ssm:DescribeInstanceInformation",
        "ssm:GetCommandInvocation",
        "ec2:StartInstances",
        "ssm:SendCommand",
        "ec2:CreateTags",
      ]) && length(d.statement) == 5
    ])
    error_message = "The controller role grants exactly its log writes, the three reads, ec2:StartInstances, ssm:SendCommand and ec2:CreateTags: no EC2 stop, no parameter reads and no command listing."
  }

  assert {
    condition = alltrue([
      for d in data.aws_iam_policy_document.controller : (
        one([for s in d.statement : s if s.sid == "RecordStopOnHost"]).actions == toset(["ec2:CreateTags"]) &&
        one([for s in d.statement : s if s.sid == "RecordStopOnHost"]).resources == toset(["arn:aws:ec2:us-west-2:793680745829:instance/i-0123456789abcdef0"]) &&
        length(one([for s in d.statement : s if s.sid == "RecordStopOnHost"]).condition) == 2 &&
        anytrue([
          for c in one([for s in d.statement : s if s.sid == "RecordStopOnHost"]).condition :
          c.test == "ForAllValues:StringEquals" && c.variable == "aws:TagKeys" && toset(c.values) == toset(["foundry:stop-command", "foundry:stop-requested"])
        ]) &&
        anytrue([
          for c in one([for s in d.statement : s if s.sid == "RecordStopOnHost"]).condition :
          c.test == "Null" && c.variable == "aws:TagKeys" && toset(c.values) == toset(["false"])
        ])
      )
    ])
    error_message = "The controller may tag only the Foundry host, and only with the two stop tags."
  }

  assert {
    condition = alltrue([
      for d in data.aws_iam_policy_document.controller : (
        one([for s in d.statement : s if s.sid == "StartHost"]).actions == toset(["ec2:StartInstances"]) &&
        one([for s in d.statement : s if s.sid == "StartHost"]).resources == toset(["arn:aws:ec2:us-west-2:793680745829:instance/i-0123456789abcdef0"])
      )
    ])
    error_message = "The controller may start only the Foundry host."
  }

  assert {
    condition = alltrue([
      for d in data.aws_iam_policy_document.controller : (
        one([for s in d.statement : s if s.sid == "StopHost"]).actions == toset(["ssm:SendCommand"]) &&
        one([for s in d.statement : s if s.sid == "StopHost"]).resources == toset([
          "arn:aws:ec2:us-west-2:793680745829:instance/i-0123456789abcdef0",
          "arn:aws:ssm:us-west-2:793680745829:document/foundry-stop",
        ])
      )
    ])
    error_message = "The controller may send only the parameterless foundry-stop document, and only to the Foundry host: never foundry-shutdown alone or AWS-RunShellScript."
  }

  assert {
    condition = alltrue([
      for environment in ["dev", "prod"] : alltrue([
        for s in data.aws_iam_policy_document.controller[environment].statement :
        s.resources == toset(["arn:aws:logs:us-west-2:793680745829:log-group:/aws/lambda/foundry-${environment}-controller:*"])
        if anytrue([for a in s.actions : startswith(a, "logs:")])
      ])
    ])
    error_message = "Each controller may write only its own log group's streams."
  }

  assert {
    condition = alltrue(flatten([
      for d in data.aws_iam_policy_document.controller : [
        for s in d.statement :
        s.actions == toset(["ec2:DescribeInstances", "ssm:DescribeInstanceInformation", "ssm:GetCommandInvocation"]) if contains(tolist(s.resources), "*")
      ]
    ]))
    error_message = "Only the read actions, which have no resource-level permissions, may use a * resource."
  }

  assert {
    condition = alltrue([
      for environment in ["dev", "prod"] : (
        toset(aws_iam_role_policies_exclusive.controller[environment].policy_names) == toset(["foundry-${environment}-controller"]) &&
        length(aws_iam_role_policy_attachments_exclusive.controller[environment].policy_arns) == 0 &&
        aws_iam_role_policy.controller[environment].role == "foundry-${environment}-controller" &&
        aws_iam_role_policies_exclusive.controller[environment].role_name == "foundry-${environment}-controller"
      )
    ])
    error_message = "Each controller role keeps only its own inline policy and no managed policies."
  }
}

run "controller_watches_the_host" {
  command = plan

  assert {
    condition = alltrue([
      for environment in ["dev", "prod"] :
      aws_lambda_function.controller[environment].environment[0].variables == tomap({
        FOUNDRY_AWS_REGION    = "us-west-2"
        FOUNDRY_INSTANCE_ID   = "i-0123456789abcdef0"
        FOUNDRY_HTTPS_URL     = "https://foundry.craigdevjohnson.com:30000/join"
        FOUNDRY_SITE_ROLE_ARN = "arn:aws:iam::793680745829:role/site-${environment}-server"
      })
    ])
    error_message = "The controller gets the Region, the host's instance ID, Foundry's /join page and its own environment's site role, and nothing else."
  }

  assert {
    condition     = alltrue([for f in aws_lambda_function.controller : f.environment[0].variables["FOUNDRY_INSTANCE_ID"] == aws_instance.host.id])
    error_message = "FOUNDRY_INSTANCE_ID must follow aws_instance.host, so a rebuild updates it."
  }

  assert {
    condition     = alltrue([for f in aws_lambda_function.controller : f.source_code_hash == filebase64sha256("tests/fixtures/bootstrap.zip")])
    error_message = "The function's hash comes from controller_package_path, so a new build redeploys it."
  }
}

run "controller_alarm_targets_workloads_alerts" {
  command = plan

  assert {
    condition = alltrue([
      for environment in ["dev", "prod"] : (
        aws_cloudwatch_metric_alarm.controller_failing[environment].namespace == "AWS/Lambda" &&
        aws_cloudwatch_metric_alarm.controller_failing[environment].metric_name == "Errors" &&
        aws_cloudwatch_metric_alarm.controller_failing[environment].dimensions["FunctionName"] == "foundry-${environment}-controller" &&
        aws_cloudwatch_metric_alarm.controller_failing[environment].statistic == "Sum" &&
        aws_cloudwatch_metric_alarm.controller_failing[environment].period == 300 &&
        aws_cloudwatch_metric_alarm.controller_failing[environment].evaluation_periods == 1 &&
        aws_cloudwatch_metric_alarm.controller_failing[environment].threshold == 1 &&
        aws_cloudwatch_metric_alarm.controller_failing[environment].comparison_operator == "GreaterThanOrEqualToThreshold" &&
        aws_cloudwatch_metric_alarm.controller_failing[environment].treat_missing_data == "notBreaching"
      )
    ])
    error_message = "Each controller's alarm fires on its first Lambda error in five minutes."
  }

  assert {
    condition = alltrue([
      for a in aws_cloudwatch_metric_alarm.controller_failing : (
        a.alarm_actions == toset(["arn:aws:sns:us-west-2:793680745829:alerts"]) &&
        a.ok_actions == toset(["arn:aws:sns:us-west-2:793680745829:alerts"])
      )
    ])
    error_message = "The controller alarms notify the workloads alerts topic, and clear there too."
  }
}

run "controller_outputs_are_keyed_by_site_environment" {
  command = plan

  assert {
    condition = (
      keys(output.controller_urls) == ["dev", "prod"] &&
      keys(output.controller_function_arns) == ["dev", "prod"]
    )
    error_message = "controller_urls and controller_function_arns are keyed by Lobby site environment."
  }

  # The mock gives invoke_arn and qualified_arn other values than arn, so an
  # output that takes the wrong attribute fails here.
  assert {
    condition = alltrue([
      for environment in ["dev", "prod"] : (
        output.controller_function_arns[environment] == aws_lambda_function.controller[environment].arn &&
        output.controller_urls[environment] == aws_lambda_function_url.controller[environment].function_url
      )
    ])
    error_message = "Each environment's outputs are that environment's function ARN (not invoke_arn or qualified_arn) and Function URL."
  }
}

run "controller_package_must_be_a_zip" {
  command = plan

  variables {
    # A file that exists, so only the validation fails.
    controller_package_path = "tests/foundry.tftest.hcl"
  }

  expect_failures = [var.controller_package_path]
}

run "stop_document_shuts_foundry_down_then_powers_off" {
  command = plan

  assert {
    condition = (
      aws_ssm_document.stop.name == "foundry-stop" &&
      aws_ssm_document.stop.document_type == "Command" &&
      !contains(keys(jsondecode(aws_ssm_document.stop.content)), "parameters")
    )
    error_message = "foundry-stop is a Command document with no parameters, so a caller can't change what runs."
  }

  assert {
    condition = (
      length(jsondecode(aws_ssm_document.stop.content).mainSteps) == 1 &&
      jsondecode(aws_ssm_document.stop.content).mainSteps[0].action == "aws:runShellScript" &&
      jsondecode(aws_ssm_document.stop.content).mainSteps[0].inputs.timeoutSeconds == "300" &&
      jsondecode(aws_ssm_document.stop.content).mainSteps[0].inputs.runCommand == [
        templatefile("templates/foundry-stop.sh.tftpl", {
          shutdown_script = file("templates/foundry-shutdown.sh")
          power_off       = file("templates/foundry-power-off.sh")
        })
      ]
    )
    error_message = "foundry-stop is one script built from the stop template, the shutdown script and the power-off."
  }

  assert {
    condition = (
      strcontains(
        jsondecode(aws_ssm_document.stop.content).mainSteps[0].inputs.runCommand[0],
        "if ! cat >\"$shutdown\" <<'FOUNDRY_SHUTDOWN_SCRIPT'\n${file("templates/foundry-shutdown.sh")}FOUNDRY_SHUTDOWN_SCRIPT\nthen\n  echo \"Could not write the shutdown script; leaving the host running\" >&2\n  exit 1\nfi\n\nif ! bash \"$shutdown\"; then\n  echo \"The shutdown failed; leaving the host running\" >&2\n  exit 1\nfi\n"
      ) &&
      endswith(
        jsondecode(aws_ssm_document.stop.content).mainSteps[0].inputs.runCommand[0],
        "\nif ! (\n${file("templates/foundry-power-off.sh")}); then\n  rm -f \"$succeeded\"\n  exit 1\nfi\n"
      )
    )
    error_message = "foundry-stop must write the shutdown script whole to its own file, stop if that write fails, run it, and power off only if it exits 0."
  }

  # The dev and prod controllers can both send a stop at once.
  assert {
    condition = (
      strcontains(
        jsondecode(aws_ssm_document.stop.content).mainSteps[0].inputs.runCommand[0],
        "if ! flock -n 9; then\n  echo \"Another stop is shutting Foundry down; waiting for it\"\n"
      ) &&
      strcontains(
        jsondecode(aws_ssm_document.stop.content).mainSteps[0].inputs.runCommand[0],
        "if powering_off; then\n  echo \"Another stop shut Foundry down; the host powers off in a minute\"\n  exit 0\nfi\nif [[ \"$waited\" == true ]]; then\n  echo \"The other stop failed; leaving the host running\" >&2\n  exit 1\nfi\nif ! rm -f \"$succeeded\"; then\n"
      ) &&
      strcontains(
        jsondecode(aws_ssm_document.stop.content).mainSteps[0].inputs.runCommand[0],
        "  written=$(stat -c %Y \"$succeeded\" 2>/dev/null) || return 1\n  (($(date +%s) - written < 120))\n"
      ) &&
      endswith(
        jsondecode(aws_ssm_document.stop.content).mainSteps[0].inputs.runCommand[0],
        "\nif ! : >\"$succeeded\"; then\n  echo \"Could not record that Foundry shut down; leaving the host running\" >&2\n  exit 1\nfi\nif ! (\n${file("templates/foundry-power-off.sh")}); then\n  rm -f \"$succeeded\"\n  exit 1\nfi\n"
      )
    )
    error_message = "Only one foundry-stop runs the shutdown; one that waited for it, or arrives within two minutes of its success, ends as it did. The stop that ran it records its success before scheduling the power-off, and clears that record if the power-off can't be scheduled."
  }

  assert {
    condition = (
      strcontains(file("templates/foundry-power-off.sh"), "shutdown --poweroff +1") &&
      jsondecode(aws_ssm_document.shutdown.content).mainSteps[0].inputs.runCommand == [file("templates/foundry-shutdown.sh")]
    )
    error_message = "The power-off waits a minute, and foundry-shutdown, which task host:stop runs, never powers the host off."
  }
}

run "a_failed_stop_alerts" {
  command = plan

  assert {
    condition = jsondecode(aws_cloudwatch_event_rule.stop_failed.event_pattern) == {
      source        = ["aws.ssm"]
      "detail-type" = ["EC2 Command Status-change Notification"]
      detail = {
        "document-name" = ["foundry-stop"]
        status          = ["Failed", "TimedOut", "Cancelled", "Canceled"]
      }
    }
    error_message = "A foundry-stop command that fails, times out or is cancelled must match the alert rule."
  }

  assert {
    condition     = aws_cloudwatch_event_target.stop_failed.arn == "arn:aws:sns:us-west-2:793680745829:alerts" && aws_cloudwatch_event_target.stop_failed.rule == "foundry-stop-failed"
    error_message = "The failed-stop rule notifies the workloads alerts topic."
  }
}
