# Three zones, a public and a private subnet in each, one NAT gateway
# (DECISIONS 71) and a free S3 gateway endpoint, so image layers pulled from
# ECR, which are served from S3, do not pass through the NAT and its per-GB
# charge.
#
#   private  10.40.0.0/19, 10.40.32.0/19, 10.40.64.0/19   nodes and pods
#   public   10.40.96.0/24, 10.40.97.0/24, 10.40.98.0/24  load balancers, NAT
#
# The private subnets are large because every pod takes a VPC address.

module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "6.8.0"

  name = var.cluster_name
  cidr = var.vpc_cidr
  azs  = local.azs

  private_subnets = [for i in range(3) : cidrsubnet(var.vpc_cidr, 3, i)]
  public_subnets  = [for i in range(3) : cidrsubnet(var.vpc_cidr, 8, 96 + i)]

  enable_nat_gateway = true
  single_nat_gateway = true

  # EKS needs both.
  enable_dns_hostnames = true
  enable_dns_support   = true

  # Nothing in a public subnet gets an address unless it asks for one.
  map_public_ip_on_launch = false

  # The VPC's default security group is adopted with no rules, so anything
  # that falls back to it can neither send nor receive. Said here rather than
  # left to the module's default.
  manage_default_security_group  = true
  default_security_group_ingress = []
  default_security_group_egress  = []

  # Flow logs stay off: they bill per GB, and Trivy rates the gap MEDIUM
  # (AWS-0178), below CI's gate (DECISIONS 71).
  enable_flow_log = false

  # Load balancers: internet-facing ones in the public subnets, internal ones
  # in the private subnets. Karpenter launches nodes where its tag is.
  public_subnet_tags = {
    "kubernetes.io/role/elb" = "1"
  }
  private_subnet_tags = {
    "kubernetes.io/role/internal-elb" = "1"
    "karpenter.sh/discovery"          = var.cluster_name
  }
}

# Gateway type: free. The Interface type bills by the hour in each zone.
resource "aws_vpc_endpoint" "s3" {
  vpc_id            = module.vpc.vpc_id
  service_name      = "com.amazonaws.${var.aws_region}.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = module.vpc.private_route_table_ids

  tags = {
    Name = "${var.cluster_name}-s3"
  }
}
