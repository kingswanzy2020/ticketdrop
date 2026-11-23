#!/usr/bin/env bash
# Lists everything in the AWS account that would still bill after a teardown,
# by asking AWS, never from Terraform state: a controller's load balancer, a
# volume or a log group that EKS recreated are never in state.
#
#   scripts/orphan-check.sh                  # AWS_REGION, default us-east-1
#   ALL_REGIONS=1 scripts/orphan-check.sh    # every enabled region (slow)
#
# Exit 0: nothing found. 1: something bills. 2: it could not check (an expired
# SSO session, a denied call); a failed call never passes for "nothing found".
#
# Kept on purpose and so not checked: the Route 53 hosted zone, the ECR
# repositories and the Terraform state bucket (live/foundation and bootstrap).
set -euo pipefail

export AWS_PAGER=""
found=0

mask() { sed -E 's/\b[0-9]{12}\b/<account-id>/g'; }

# aws_json <region> <args...>: the call's JSON, or exit 2 if it fails.
aws_json() {
  local region=$1 out
  shift
  if ! out=$(aws --region "$region" --output json "$@" 2>&1); then
    echo "  ERROR  aws $*: $out" | mask >&2
    exit 2
  fi
  printf '%s' "$out"
}

# report <FOUND|INFO> <label> <lines>: one line per resource, nothing if empty.
report() {
  local kind=$1 label=$2 lines=$3
  if [ -z "$lines" ]; then
    [ "$kind" = FOUND ] && printf '  ok     %s\n' "$label"
    return 0
  fi
  [ "$kind" = FOUND ] && found=$((found + 1))
  printf '  %-6s %s: %s\n' "$kind" "$label" "$(wc -l <<<"$lines")"
  while IFS= read -r l; do printf '           %s\n' "$l"; done <<<"$lines" | mask
}

check_region() {
  local r=$1 j keys k meta fail_keys="" info_keys=""
  echo "== $r"

  j=$(aws_json "$r" eks list-clusters)
  report FOUND "EKS clusters" "$(jq -r '.clusters[]' <<<"$j")"

  # A stopped instance's disk still bills.
  j=$(aws_json "$r" ec2 describe-instances \
    --filters Name=instance-state-name,Values=pending,running,stopping,stopped)
  report FOUND "EC2 instances" "$(jq -r '.Reservations[].Instances[] | "\(.InstanceId) \(.InstanceType) \(.State.Name)"' <<<"$j")"

  j=$(aws_json "$r" ec2 describe-volumes)
  report FOUND "EBS volumes" "$(jq -r '.Volumes[] | "\(.VolumeId) \(.Size)GiB \(.State)"' <<<"$j")"

  # Without --owner-ids self this returns every public snapshot in the region.
  j=$(aws_json "$r" ec2 describe-snapshots --owner-ids self)
  report FOUND "EBS snapshots" "$(jq -r '.Snapshots[] | "\(.SnapshotId) \(.VolumeSize)GiB"' <<<"$j")"

  j=$(aws_json "$r" ec2 describe-images --owners self)
  report FOUND "AMIs" "$(jq -r '.Images[] | "\(.ImageId) \(.Name)"' <<<"$j")"

  # Deleted ones stay listed for about an hour.
  j=$(aws_json "$r" ec2 describe-nat-gateways --filter Name=state,Values=pending,available)
  report FOUND "NAT gateways" "$(jq -r '.NatGateways[] | "\(.NatGatewayId) \(.State)"' <<<"$j")"

  j=$(aws_json "$r" ec2 describe-addresses)
  report FOUND "Elastic IPs" "$(jq -r '.Addresses[] | "\(.PublicIp) \(.AllocationId)"' <<<"$j")"

  # Every public IPv4 address bills, not only Elastic IPs (those are above).
  j=$(aws_json "$r" ec2 describe-network-interfaces)
  report FOUND "other public IPv4 addresses" "$(jq -r '.NetworkInterfaces[] | select(.Association.PublicIp and (.Association.AllocationId | not)) | "\(.Association.PublicIp) on \(.NetworkInterfaceId) (\(.InterfaceType))"' <<<"$j")"
  # Free, but they stop a VPC from being deleted.
  report INFO "unattached network interfaces" "$(jq -r '.NetworkInterfaces[] | select(.Status == "available") | "\(.NetworkInterfaceId) \(.Description)"' <<<"$j")"

  j=$(aws_json "$r" elbv2 describe-load-balancers)
  report FOUND "load balancers" "$(jq -r '.LoadBalancers[] | "\(.LoadBalancerName) \(.Type)"' <<<"$j")"

  j=$(aws_json "$r" elb describe-load-balancers)
  report FOUND "classic load balancers" "$(jq -r '.LoadBalancerDescriptions[].LoadBalancerName' <<<"$j")"

  j=$(aws_json "$r" ec2 describe-vpc-endpoints \
    --filters Name=vpc-endpoint-state,Values=pendingAcceptance,pending,available,rejected,failed)
  report FOUND "VPC endpoints" "$(jq -r '.VpcEndpoints[] | "\(.VpcEndpointId) \(.ServiceName)"' <<<"$j")"

  j=$(aws_json "$r" ec2 describe-vpcs --filters Name=is-default,Values=false)
  report FOUND "non-default VPCs" "$(jq -r '.Vpcs[] | "\(.VpcId) \(.CidrBlock)"' <<<"$j")"

  # Customer-managed keys bill whether enabled or disabled; AWS-managed
  # (alias/aws/*) keys are free. A key pending deletion no longer bills.
  j=$(aws_json "$r" kms list-keys)
  keys=$(jq -r '.Keys[].KeyId' <<<"$j")
  for k in $keys; do
    meta=$(aws_json "$r" kms describe-key --key-id "$k")
    case $(jq -r '.KeyMetadata | "\(.KeyManager) \(.KeyState)"' <<<"$meta") in
      "CUSTOMER PendingDeletion" | "CUSTOMER PendingReplicaDeletion")
        info_keys+=$(jq -r '.KeyMetadata | "\(.KeyId) \(.KeyState), deleted on \(.DeletionDate // "a date set once its replicas are gone")"' <<<"$meta")$'\n' ;;
      CUSTOMER\ *)
        fail_keys+=$(jq -r '.KeyMetadata | "\(.KeyId) \(.KeyState), created \(.CreationDate): \(.Description)"' <<<"$meta")$'\n' ;;
    esac
  done
  report FOUND "customer-managed KMS keys" "${fail_keys%$'\n'}"
  report INFO "KMS keys pending deletion" "${info_keys%$'\n'}"

  j=$(aws_json "$r" secretsmanager list-secrets --include-planned-deletion)
  report FOUND "secrets" "$(jq -r '.SecretList[] | select(.DeletedDate | not) | .Name' <<<"$j")"
  report INFO "secrets pending deletion" "$(jq -r '.SecretList[] | select(.DeletedDate) | "\(.Name) deleted \(.DeletedDate)"' <<<"$j")"

  # EKS can recreate its log group after the cluster is destroyed.
  j=$(aws_json "$r" logs describe-log-groups)
  report FOUND "EKS log groups" "$(jq -r '.logGroups[] | select(.logGroupName | startswith("/aws/eks/")) | "\(.logGroupName) \(.storedBytes) bytes"' <<<"$j")"
  report INFO "other log groups" "$(jq -r '.logGroups[] | select(.logGroupName | startswith("/aws/eks/") | not) | "\(.logGroupName) \(.storedBytes) bytes"' <<<"$j")"
}

region=${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}
who=$(aws_json "$region" sts get-caller-identity)
echo "orphan-check as $(jq -r .Arn <<<"$who" | mask | sed -E 's/(AWSReservedSSO_[A-Za-z]+)_[0-9a-f]{16}/\1_<id>/')"

if [ "${ALL_REGIONS:-0}" = 1 ]; then
  j=$(aws_json "$region" ec2 describe-regions)
  regions=$(jq -r '.Regions[].RegionName' <<<"$j" | sort)
else
  regions=$region
fi
for r in $regions; do check_region "$r"; done

echo
if [ "$found" -gt 0 ]; then
  echo "orphan-check: $found kind(s) of billable resource found"
  exit 1
fi
echo "orphan-check: nothing billable found"
