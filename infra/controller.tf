# The controller (ADR 0003, ADR 0005): the Go Lambda behind The Lobby's
# /foundry page. It answers the site's signed requests through a Function URL
# and only reads the host's state, so it changes nothing in AWS. One function
# per Lobby site environment, and only that environment's site role may call
# it (the-lobby ADR 0006).

locals {
  # The Lobby's site environments. The site's permissions boundary lets
  # site-<env>-server invoke only Lambdas named *-<env>-*, so each environment
  # gets its own function with the environment in its name.
  site_environments = toset(["dev", "prod"])

  controller_names = {
    for environment in local.site_environments : environment => "foundry-${environment}-controller"
  }

  # Built from the name rather than read from the log group, so the plan shows
  # the role's whole policy before anything is created.
  controller_log_group_names = {
    for environment, name in local.controller_names : environment => "/aws/lambda/${name}"
  }

  controller_site_role_arns = {
    for environment in local.site_environments :
    environment => "arn:aws:iam::${local.workloads_account_id}:role/site-${environment}-server"
  }

  # The page players open. The controller's HTTPS probe requires exactly
  # https://host[:port]/join and reports Foundry as responding only on a 200.
  foundry_join_url = "https://foundry.craigdevjohnson.com:30000/join"
}

resource "aws_cloudwatch_log_group" "controller" {
  for_each = local.site_environments

  name              = local.controller_log_group_names[each.key]
  retention_in_days = 30
}

# Only Lambda may assume the role: each role trusts only its service principal
# (aws-design §5.3). Like soccer's and The Lobby's function roles, and unlike
# the DLM role, it has no aws:SourceAccount condition.
data "aws_iam_policy_document" "controller_trust" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "controller" {
  for_each = local.site_environments

  name                  = local.controller_names[each.key]
  description           = "Foundry controller for The Lobby ${each.key} site - reads host status only"
  assume_role_policy    = data.aws_iam_policy_document.controller_trust.json
  force_detach_policies = true
}

# Least privilege (G7): the function writes its own log group and reads the
# host's state. It can't start, stop or run anything on the host; those stay
# with the Host control role (ci.tf).
data "aws_iam_policy_document" "controller" {
  for_each = local.site_environments

  statement {
    sid = "OwnLogsOnly"
    actions = [
      "logs:CreateLogStream",
      "logs:PutLogEvents",
    ]
    resources = ["arn:aws:logs:${local.region}:${local.workloads_account_id}:log-group:${local.controller_log_group_names[each.key]}:*"]
  }

  # Neither action has resource-level permissions, so both need *. The
  # function asks only about the host in FOUNDRY_INSTANCE_ID.
  statement {
    sid = "ObserveHost"
    actions = [
      "ec2:DescribeInstances",
      "ssm:DescribeInstanceInformation",
    ]
    resources = ["*"]
  }
}

resource "aws_iam_role_policy" "controller" {
  for_each = local.site_environments

  name   = local.controller_names[each.key]
  role   = aws_iam_role.controller[each.key].id
  policy = data.aws_iam_policy_document.controller[each.key].json
}

# Deletes any inline policy or managed attachment not listed here, so one added
# by hand shows up as drift (G7).
resource "aws_iam_role_policies_exclusive" "controller" {
  for_each = local.site_environments

  role_name    = aws_iam_role.controller[each.key].name
  policy_names = [aws_iam_role_policy.controller[each.key].name]
}

resource "aws_iam_role_policy_attachments_exclusive" "controller" {
  for_each = local.site_environments

  role_name   = aws_iam_role.controller[each.key].name
  policy_arns = []
}

resource "aws_lambda_function" "controller" {
  for_each = local.site_environments

  function_name = local.controller_names[each.key]
  description   = "Foundry host status for The Lobby ${each.key} site - read only"
  role          = aws_iam_role.controller[each.key].arn

  # `task build` writes the same zip for the same source, so an unchanged
  # build plans no change.
  filename         = var.controller_package_path
  source_code_hash = filebase64sha256(var.controller_package_path)
  handler          = "bootstrap"
  runtime          = "provided.al2023"
  architectures    = ["arm64"]

  memory_size = 256
  # GET /server answers within 6 seconds and the site gives up at 7, so a
  # longer run would only cost money.
  timeout = 10

  environment {
    variables = {
      FOUNDRY_AWS_REGION = local.region
      # A rebuild changes the instance ID; the next apply updates it here.
      FOUNDRY_INSTANCE_ID = aws_instance.host.id
      FOUNDRY_HTTPS_URL   = local.foundry_join_url
    }
  }

  logging_config {
    log_format = "Text"
    log_group  = aws_cloudwatch_log_group.controller[each.key].name
  }

  depends_on = [aws_iam_role_policy.controller]
}

resource "aws_lambda_function_url" "controller" {
  for_each = local.site_environments

  function_name      = aws_lambda_function.controller[each.key].function_name
  authorization_type = "AWS_IAM"
}

# The environment's site role is the only principal this function grants a
# call to. The second statement is the lambda:InvokeFunction grant AWS also
# requires for a Function URL, limited to calls that come through the URL. AWS
# lets a principal in the same account call the URL on its own identity policy
# too, so the account's workload roles carrying no lambda:InvokeFunctionUrl
# (G7) is what keeps the site the only caller; the admin profile can call it,
# which the deploy check in README.md uses.
resource "aws_lambda_permission" "controller_site_url" {
  for_each = local.site_environments

  statement_id           = "AllowSiteThroughFunctionUrl"
  action                 = "lambda:InvokeFunctionUrl"
  function_name          = aws_lambda_function.controller[each.key].function_name
  principal              = local.controller_site_role_arns[each.key]
  function_url_auth_type = "AWS_IAM"
}

resource "aws_lambda_permission" "controller_site_invoke" {
  for_each = local.site_environments

  statement_id             = "AllowSiteInvokeThroughFunctionUrl"
  action                   = "lambda:InvokeFunction"
  function_name            = aws_lambda_function.controller[each.key].function_name
  principal                = local.controller_site_role_arns[each.key]
  invoked_via_function_url = true
}

# A crash, a timeout, or a start with invalid configuration (an init error).
# The function answers every other failure itself: a host it can't observe is
# reported as unknown, not as an error.
resource "aws_cloudwatch_metric_alarm" "controller_failing" {
  for_each = local.site_environments

  alarm_name          = "${local.controller_names[each.key]}-failing"
  alarm_description   = "The Foundry controller for The Lobby ${each.key} site crashed, timed out or could not start. See docs/operations.md."
  namespace           = "AWS/Lambda"
  metric_name         = "Errors"
  dimensions          = { FunctionName = aws_lambda_function.controller[each.key].function_name }
  statistic           = "Sum"
  period              = 300
  evaluation_periods  = 1
  threshold           = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"

  alarm_actions = [local.alerts_topic_arn]
  ok_actions    = [local.alerts_topic_arn]
}
