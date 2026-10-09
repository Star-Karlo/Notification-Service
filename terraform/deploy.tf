# Deployment: CodeBuild, triggered by this repository's own GitHub webhook.
#
# WHY NOT CODEPIPELINE
#
# CodePipeline itself runs in ap-southeast-3, but its GitHub source action does
# not. `CodeStarSourceConnection` is not offered in Jakarta — AWS names the
# region explicitly in the action reference — and CodeConnections has no
# endpoint here either, so a connection created in Singapore still cannot be
# read by a pipeline created here. A pipeline in this region has no way to see
# a commit.
#
# Building in Singapore and deploying across the region boundary was the
# alternative, and was rejected: Jakarta was chosen so the data stays
# in-country, and a source checkout on a Singapore build host moves it out
# again on every push.
#
# CodeBuild's own GitHub integration is a different mechanism — a repository
# webhook authorised by a personal access token, not CodeConnections — and it
# is available here. So the deploy is one CodeBuild project, in region, running
# the steps that used to live in .github/workflows/deploy.yml. The steps
# themselves are in buildspec.yml at the repository root.
#
# ONE MANUAL PREREQUISITE
#
# CodeBuild's GitHub credential is per account and region, not per project, so
# no single service's state can own it — and a token in Terraform is a token in
# the state file. It is imported once, by hand, and that one import covers
# every repository:
#
#   aws codebuild import-source-credentials --server-type GITHUB \
#     --auth-type PERSONAL_ACCESS_TOKEN --region ap-southeast-3 \
#     --token <PAT with scopes: repo, admin:repo_hook>
#
# Until it exists, `terraform apply` fails creating the webhook below —
# CodeBuild registers the webhook in GitHub using that token.
#
# WHAT REPLACED WHAT
#
#   workflow_run: [CI] gate  ->  the unit gate in buildspec.yml's pre_build,
#                                plus branch protection on main
#   workflow_dispatch        ->  aws codebuild start-build --project-name <out>
#   OIDC role assumption     ->  aws_iam_role.deploy below, assumed by CodeBuild
#   concurrency group        ->  concurrent_build_limit = 1

# var.github_repository is already declared in variables.tf — it was added for
# the CodePipeline attempt this replaces, and names the same repository.

variable "run_migrations" {
  description = "Run `migrate` as a one-off ECS task between registering the task definition and rolling the service."
  type        = bool
  default     = false
}

variable "deploy_test_image" {
  description = "Toolchain image the pre-deploy unit gate runs in. Pinned to the version the Dockerfile builds with."
  type        = string
  default     = "golang:1.25-alpine"
}

variable "deploy_test_command" {
  description = "The unit gate. Fast checks only; the full suite stays on GitHub Actions, which can also report on pull requests."
  type        = string
  default     = "go build ./... && go vet ./... && go test ./..."
}

variable "deploy_build_timeout" {
  description = "Minutes. Must exceed the unit gate, the image build, the migration and `wait services-stable` combined."
  type        = number
  default     = 30
}

# The repository the platform created. Looked up rather than constructed, so a
# changed naming convention fails at plan time instead of at the push.
data "aws_ecr_repository" "service" {
  name = "karlo/${var.service_name}"
}

resource "aws_cloudwatch_log_group" "deploy" {
  name              = "/aws/codebuild/${local.name}-deploy"
  retention_in_days = 30

  tags = { Name = "${local.name}-deploy" }
}

# --- The role the build runs as ---------------------------------------------
#
# Scoped tighter than the OIDC deploy role it replaces, which held ecs:* on
# "*" because a GitHub workflow has no Terraform state to name resources from.
# This one does: it can roll THIS service and pass THIS service's two task
# roles, and nothing else.

data "aws_iam_policy_document" "deploy_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["codebuild.amazonaws.com"]
    }
    # Only builds in this account may assume it, and only this project.
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.deploy.account_id]
    }
    # The project name as a literal, not a reference to the project resource:
    # the project names this role, so referring back to it would be a cycle.
    condition {
      test     = "ArnLike"
      variable = "aws:SourceArn"
      values   = ["arn:aws:codebuild:${var.region}:${data.aws_caller_identity.deploy.account_id}:project/${local.name}-deploy"]
    }
  }
}

# Named for this file rather than reused: three of the five repositories
# already declare a "current" caller identity for their archive schedule, and
# two do not.
data "aws_caller_identity" "deploy" {}

resource "aws_iam_role" "deploy" {
  name               = "${local.name}-deploy"
  assume_role_policy = data.aws_iam_policy_document.deploy_assume.json

  tags = { Name = "${local.name}-deploy" }
}

data "aws_iam_policy_document" "deploy" {
  statement {
    sid    = "Logs"
    effect = "Allow"
    actions = [
      "logs:CreateLogStream",
      "logs:PutLogEvents",
    ]
    resources = ["${aws_cloudwatch_log_group.deploy.arn}:*"]
  }

  statement {
    sid       = "ECRLogin"
    effect    = "Allow"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"] # Not a resource-scoped action.
  }

  statement {
    sid    = "ECRPush"
    effect = "Allow"
    actions = [
      "ecr:BatchCheckLayerAvailability",
      "ecr:CompleteLayerUpload",
      "ecr:InitiateLayerUpload",
      "ecr:PutImage",
      "ecr:UploadLayerPart",
      # The reuse check, and the pull the build base layers come from.
      "ecr:BatchGetImage",
      "ecr:GetDownloadUrlForLayer",
    ]
    resources = [data.aws_ecr_repository.service.arn]
  }

  # Neither action supports resource-level permissions. Describe is read-only;
  # Register creates a revision that only UpdateService below can roll out.
  statement {
    sid    = "TaskDefinitions"
    effect = "Allow"
    actions = [
      "ecs:DescribeTaskDefinition",
      "ecs:RegisterTaskDefinition",
    ]
    resources = ["*"]
  }

  statement {
    sid    = "RollThisService"
    effect = "Allow"
    actions = [
      "ecs:DescribeServices",
      "ecs:UpdateService",
    ]
    resources = [aws_ecs_service.main.id]
  }

  # RegisterTaskDefinition names the execution and task roles, and IAM refuses
  # the registration unless the caller may PASS them.
  statement {
    sid       = "PassTaskRoles"
    effect    = "Allow"
    actions   = ["iam:PassRole"]
    resources = [aws_iam_role.task_execution.arn, aws_iam_role.task.arn]
    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["ecs-tasks.amazonaws.com"]
    }
  }

  # The migration step. Only granted where there is one to run.
  dynamic "statement" {
    for_each = var.run_migrations ? [1] : []
    content {
      sid    = "RunMigrationTask"
      effect = "Allow"
      # Any revision of this family, so the build runs the image it just
      # registered rather than whatever was current at plan time.
      actions   = ["ecs:RunTask"]
      resources = ["${replace(aws_ecs_task_definition.main.arn, "/:\\d+$/", "")}:*"]
      condition {
        test     = "ArnEquals"
        variable = "ecs:cluster"
        values   = [local.platform.ecs_cluster_arn]
      }
    }
  }

  dynamic "statement" {
    for_each = var.run_migrations ? [1] : []
    content {
      sid    = "ReadMigrationTask"
      effect = "Allow"
      # Task ARNs are not knowable in advance, so this is scoped by cluster.
      actions   = ["ecs:DescribeTasks"]
      resources = ["*"]
      condition {
        test     = "ArnEquals"
        variable = "ecs:cluster"
        values   = [local.platform.ecs_cluster_arn]
      }
    }
  }
}

resource "aws_iam_role_policy" "deploy" {
  name   = "${local.name}-deploy"
  role   = aws_iam_role.deploy.id
  policy = data.aws_iam_policy_document.deploy.json
}

# --- The project ------------------------------------------------------------

resource "aws_codebuild_project" "deploy" {
  name          = "${local.name}-deploy"
  description   = "Build, migrate and roll ${var.service_name} on every push to main."
  service_role  = aws_iam_role.deploy.arn
  build_timeout = var.deploy_build_timeout

  # A second push arriving mid-deploy is refused rather than queued, which is
  # the safer of the two failures: two builds racing would let the slower one
  # finish last and leave the service on the OLDER image. A refused build is
  # visible on the commit, and the next push — or a manual start-build —
  # deploys it. This is what the workflow's `concurrency` group did.
  concurrent_build_limit = 1

  artifacts {
    type = "NO_ARTIFACTS" # The artifact is the image in ECR.
  }

  # The GitHub Actions build had `cache-from: type=gha`; without an equivalent
  # every deploy would re-pull the base images and re-download every Go module
  # or npm package, and the migration would land as a slower deploy. Docker
  # layer caching is Linux-only and requires privileged mode, which this
  # project has. LOCAL caching is best-effort — a cold build host simply starts
  # from scratch — so nothing here depends on the cache being warm.
  cache {
    type  = "LOCAL"
    modes = ["LOCAL_DOCKER_LAYER_CACHE", "LOCAL_SOURCE_CACHE"]
  }

  source {
    type            = "GITHUB"
    location        = "https://github.com/${var.github_repository}.git"
    git_clone_depth = 1
    buildspec       = "buildspec.yml"

    # Report the result back to the commit, so a failed deploy is visible where
    # the change was made and not only in the CodeBuild console.
    report_build_status = true
    build_status_config {
      context = "deploy"
    }
  }

  # What a manual `start-build` with no --source-version builds.
  source_version = "refs/heads/main"

  environment {
    compute_type = "BUILD_GENERAL1_SMALL"
    image        = "aws/codebuild/amazonlinux2-x86_64-standard:5.0"
    type         = "LINUX_CONTAINER"
    # `docker build`, and the unit gate, both need a daemon.
    privileged_mode = true

    environment_variable {
      name  = "ECS_CLUSTER"
      value = local.platform.ecs_cluster_name
    }
    environment_variable {
      name  = "SERVICE_NAME"
      value = aws_ecs_service.main.name
    }
    environment_variable {
      name  = "CONTAINER_NAME"
      value = var.service_name
    }
    # The family Terraform registered. Read from the resource rather than
    # retyped: the workflow this replaces hardcoded it.
    environment_variable {
      name  = "TASK_FAMILY"
      value = aws_ecs_task_definition.main.family
    }
    environment_variable {
      name  = "ECR_REPOSITORY_URL"
      value = data.aws_ecr_repository.service.repository_url
    }
    environment_variable {
      name  = "ECR_REPOSITORY_NAME"
      value = data.aws_ecr_repository.service.name
    }
    environment_variable {
      name  = "RUN_MIGRATIONS"
      value = tostring(var.run_migrations)
    }
    environment_variable {
      name  = "TEST_IMAGE"
      value = var.deploy_test_image
    }
    environment_variable {
      name  = "TEST_COMMAND"
      value = var.deploy_test_command
    }
  }

  logs_config {
    cloudwatch_logs {
      group_name = aws_cloudwatch_log_group.deploy.name
    }
  }

  tags = { Name = "${local.name}-deploy" }
}

# Pushes to main, and nothing else. A pull request from a fork cannot reach
# this: it is not a push to this repository's main.
resource "aws_codebuild_webhook" "deploy" {
  project_name = aws_codebuild_project.deploy.name
  build_type   = "BUILD"

  filter_group {
    filter {
      type    = "EVENT"
      pattern = "PUSH"
    }
    filter {
      type    = "HEAD_REF"
      pattern = "^refs/heads/main$"
    }
  }
}

output "deploy_project_name" {
  description = "Redeploy or roll back: aws codebuild start-build --project-name <this> [--source-version <sha>]"
  value       = aws_codebuild_project.deploy.name
}
