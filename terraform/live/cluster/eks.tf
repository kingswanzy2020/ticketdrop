# EKS 1.36 with two arm64 on-demand nodes, one in each of two zones. Ported
# from the sample-fastapi-react wrapper (module v20), not copied: v21 drops the
# cluster_ prefix from its inputs and needs AWS provider 6.
#
#   who gets in   access entries only, no aws-auth ConfigMap; one explicit
#                 entry for the SSO admin role, looked up by name
#   the API       public, but answering admin_cidr only; private too, for
#                 the nodes
#   Secrets       encrypted with this cluster's own KMS key (7-day window)
#   pods' AWS     Pod Identity, so no per-cluster OIDC provider for IRSA
#   nodes         IMDSv2 only, hop limit 1, so a pod cannot borrow the
#                 node's role
#   NetworkPolicy enforced by the VPC CNI, in var.network_policy_mode

# The SSO permission set's role. Its name ends in a random suffix that changes
# if the permission set is ever reassigned, so it is looked up, not written in.
data "aws_iam_roles" "sso_admin" {
  name_regex  = "^AWSReservedSSO_AdministratorAccess_[0-9a-f]+$"
  path_prefix = "/aws-reserved/sso.amazonaws.com/"
}

locals {
  # The full IAM role ARN, path included, which access entries accept. Not
  # the assumed-role ARN that `aws sts get-caller-identity` prints.
  admin_role_arn = one(data.aws_iam_roles.sso_admin.arns)
}

# Two findings Trivy raises inside the module, accepted:
#   AWS-0040  the public endpoint: kubectl runs from a laptop, so the API is
#             public, but it answers admin_cidr only and every call is IAM-
#             authenticated.
#   AWS-0104  the node security group's egress to 0.0.0.0/0: nodes must reach
#             ECR, STS and the EKS API through the NAT. What pods may reach is
#             narrowed by NetworkPolicy, not by this group.
#trivy:ignore:AWS-0040
#trivy:ignore:AWS-0104
module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "21.30.2"

  name               = var.cluster_name
  kubernetes_version = var.kubernetes_version

  vpc_id     = module.vpc.vpc_id
  subnet_ids = module.vpc.private_subnets

  endpoint_public_access       = true
  endpoint_public_access_cidrs = [var.admin_cidr]
  endpoint_private_access      = true

  # Not the creator entry: it would follow whoever runs the plan, so CI's
  # plan role (step 9) would see it change every time.
  authentication_mode                      = "API"
  enable_cluster_creator_admin_permissions = false
  access_entries = {
    sso_admin = {
      principal_arn = local.admin_role_arn
      policy_associations = {
        admin = {
          policy_arn   = "arn:aws:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy"
          access_scope = { type = "cluster" }
        }
      }
    }
  }

  encryption_config               = { resources = ["secrets"] }
  kms_key_deletion_window_in_days = 7
  # Fixed for the same reason as the access entry: the default is the caller.
  kms_key_administrators = [local.admin_role_arn]

  # Who called the API, and who signed in. EKS can recreate this log group
  # after a destroy; the orphan check looks for it.
  enabled_log_types                      = ["audit", "authenticator"]
  cloudwatch_log_group_retention_in_days = 7

  # Pod Identity instead.
  enable_irsa = false

  # The module's own node rules let nodes reach each other only on 53 and
  # 1025-65535, so a pod could not reach a pod on another node on port 80
  # (TROUBLESHOOTING 23). Between nodes everything is open; what a pod may
  # reach is NetworkPolicy's to decide, not this group's.
  node_security_group_additional_rules = {
    ingress_self_all = {
      description = "Node to node, all ports and protocols"
      protocol    = "-1"
      from_port   = 0
      to_port     = 0
      type        = "ingress"
      self        = true
    }
  }

  # Karpenter (Phase 5) gives the nodes it launches this group by the tag.
  node_security_group_tags = {
    "karpenter.sh/discovery" = var.cluster_name
  }

  # Pinned, so a plan never changes an add-on by itself. vpc-cni and the Pod
  # Identity agent go on before the nodes, or the nodes come up NotReady.
  addons = {
    vpc-cni = {
      addon_version  = "v1.23.3-eksbuild.2"
      before_compute = true
      configuration_values = jsonencode({
        enableNetworkPolicy = "true"
        env = {
          NETWORK_POLICY_ENFORCING_MODE = var.network_policy_mode
        }
      })
    }
    eks-pod-identity-agent = {
      addon_version  = "v1.4.1-eksbuild.3"
      before_compute = true
    }
    kube-proxy = {
      addon_version = "v1.36.0-eksbuild.65"
    }
    coredns = {
      addon_version = "v1.14.7-eksbuild.35"
    }
  }

  eks_managed_node_groups = {
    system = {
      ami_type       = "AL2023_ARM_64_STANDARD"
      instance_types = [var.node_instance_type]
      capacity_type  = "ON_DEMAND"

      min_size     = 2
      desired_size = 2
      max_size     = 3

      block_device_mappings = {
        root = {
          device_name = "/dev/xvda"
          ebs = {
            volume_size           = 20
            volume_type           = "gp3"
            encrypted             = true
            delete_on_termination = true
          }
        }
      }

      metadata_options = {
        http_endpoint               = "enabled"
        http_tokens                 = "required"
        http_put_response_hop_limit = 1
      }
    }
  }
}
