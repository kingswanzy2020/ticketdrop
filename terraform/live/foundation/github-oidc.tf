# GitHub Actions gets short-lived AWS credentials by presenting an OIDC token,
# so no access key is ever stored in GitHub. Each role trusts one exact `sub`:
#
#   plan   any pull_request run in this repository (fork PRs get no token)
#   apply  only a job that names the foundation-apply environment, which is
#          limited to main in GitHub; a plain main `sub` would also let the
#          scheduled Monday run and every other main job take it
#   push   any run on main, for the image push in Phase 6
#
# Limit of the apply role: it may rewrite the ticketdrop-ci-* roles' inline
# policies, so in principle it could widen itself. What stands in the way is the
# environment, main, and the ruleset that requires a reviewed PR to reach main.

resource "aws_iam_openid_connect_provider" "github" {
  url            = "https://token.actions.githubusercontent.com"
  client_id_list = ["sts.amazonaws.com"]
}

locals {
  oidc_host = "token.actions.githubusercontent.com"
  state_arn = "arn:aws:s3:::ticketdrop-tfstate-kahmedt"

  ci_subjects = {
    plan  = "${var.github_sub_prefix}:pull_request"
    apply = "${var.github_sub_prefix}:environment:foundation-apply"
    push  = "${var.github_sub_prefix}:ref:refs/heads/main"
  }

  read_only = "arn:aws:iam::aws:policy/ReadOnlyAccess"
}

data "aws_iam_policy_document" "ci_trust" {
  for_each = local.ci_subjects

  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.github.arn]
    }

    condition {
      test     = "StringEquals"
      variable = "${local.oidc_host}:aud"
      values   = ["sts.amazonaws.com"]
    }

    # StringEquals, never StringLike: a wildcard here lets any branch in.
    condition {
      test     = "StringEquals"
      variable = "${local.oidc_host}:sub"
      values   = [each.value]
    }
  }
}

resource "aws_iam_role" "ci" {
  for_each = local.ci_subjects

  name                 = "ticketdrop-ci-${each.key}"
  description          = "GitHub Actions, ${each.value}"
  assume_role_policy   = data.aws_iam_policy_document.ci_trust[each.key].json
  max_session_duration = 3600
}

# plan and apply both read everything, for the refresh.
resource "aws_iam_role_policy_attachment" "ci_read_only" {
  for_each = toset(["plan", "apply"])

  role       = aws_iam_role.ci[each.key].name
  policy_arn = local.read_only
}

# --- plan ------------------------------------------------------------------

data "aws_iam_policy_document" "plan" {
  # use_lockfile takes the lock during plan too.
  statement {
    sid       = "StateLock"
    actions   = ["s3:PutObject", "s3:DeleteObject"]
    resources = ["${local.state_arn}/*.tflock"]
  }

  # ReadOnlyAccess can read these; a pull request must not.
  statement {
    sid    = "NoSecretValues"
    effect = "Deny"
    actions = [
      "secretsmanager:GetSecretValue",
      "ssm:GetParameter",
      "ssm:GetParameters",
      "ssm:GetParametersByPath",
      "kms:Decrypt",
    ]
    resources = ["*"]
  }
}

resource "aws_iam_role_policy" "plan" {
  name   = "plan"
  role   = aws_iam_role.ci["plan"].id
  policy = data.aws_iam_policy_document.plan.json
}

# --- apply -----------------------------------------------------------------
# Writes only what this layer manages; reads come from ReadOnlyAccess.

data "aws_iam_policy_document" "apply" {
  statement {
    sid       = "FoundationState"
    actions   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"]
    resources = ["${local.state_arn}/foundation/*"]
  }

  statement {
    sid = "GitHubOIDCProvider"
    actions = [
      "iam:CreateOpenIDConnectProvider",
      "iam:DeleteOpenIDConnectProvider",
      "iam:UpdateOpenIDConnectProviderThumbprint",
      "iam:AddClientIDToOpenIDConnectProvider",
      "iam:RemoveClientIDFromOpenIDConnectProvider",
      "iam:TagOpenIDConnectProvider",
      "iam:UntagOpenIDConnectProvider",
    ]
    resources = [aws_iam_openid_connect_provider.github.arn]
  }

  statement {
    sid = "CIRoles"
    actions = [
      "iam:CreateRole",
      "iam:DeleteRole",
      "iam:UpdateRole",
      "iam:UpdateRoleDescription",
      "iam:UpdateAssumeRolePolicy",
      "iam:TagRole",
      "iam:UntagRole",
      "iam:PutRolePolicy",
      "iam:DeleteRolePolicy",
    ]
    resources = ["arn:aws:iam::${local.account_id}:role/ticketdrop-ci-*"]
  }

  # ReadOnlyAccess and nothing else, so never AdministratorAccess.
  statement {
    sid       = "AttachOnlyReadOnly"
    actions   = ["iam:AttachRolePolicy", "iam:DetachRolePolicy"]
    resources = ["arn:aws:iam::${local.account_id}:role/ticketdrop-ci-*"]

    condition {
      test     = "ArnEquals"
      variable = "iam:PolicyARN"
      values   = [local.read_only]
    }
  }

  statement {
    sid = "Registry"
    actions = [
      "ecr:CreateRepository",
      "ecr:DeleteRepository",
      "ecr:PutImageTagMutability",
      "ecr:PutImageScanningConfiguration",
      "ecr:PutLifecyclePolicy",
      "ecr:DeleteLifecyclePolicy",
      "ecr:TagResource",
      "ecr:UntagResource",
    ]
    resources = ["arn:aws:ecr:${var.aws_region}:${local.account_id}:repository/ticketdrop/*"]
  }

  # No DeleteHostedZone: the zone is not this role's to remove.
  statement {
    sid       = "Zone"
    actions   = ["route53:UpdateHostedZoneComment", "route53:ChangeTagsForResource"]
    resources = [aws_route53_zone.main.arn]
  }

  statement {
    sid       = "Budget"
    actions   = ["budgets:ModifyBudget", "budgets:TagResource", "budgets:UntagResource"]
    resources = ["arn:aws:budgets::${local.account_id}:budget/*"]
  }
}

resource "aws_iam_role_policy" "apply" {
  name   = "apply"
  role   = aws_iam_role.ci["apply"].id
  policy = data.aws_iam_policy_document.apply.json
}

# --- push ------------------------------------------------------------------
# Push and read back, nothing that deletes.

data "aws_iam_policy_document" "push" {
  statement {
    sid       = "Login"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }

  statement {
    sid = "PushImages"
    actions = [
      "ecr:BatchCheckLayerAvailability",
      "ecr:InitiateLayerUpload",
      "ecr:UploadLayerPart",
      "ecr:CompleteLayerUpload",
      "ecr:PutImage",
      "ecr:BatchGetImage",
      "ecr:GetDownloadUrlForLayer",
    ]
    resources = [for r in aws_ecr_repository.service : r.arn]
  }
}

resource "aws_iam_role_policy" "push" {
  name   = "push"
  role   = aws_iam_role.ci["push"].id
  policy = data.aws_iam_policy_document.push.json
}
