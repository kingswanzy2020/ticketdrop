# The account's monthly budget. The forecast alert is the one that catches a
# forgotten cluster: it fires on the projected month, not the spend so far.
resource "aws_budgets_budget" "monthly" {
  name              = "Budget for 20 dollars"
  budget_type       = "COST"
  limit_amount      = "20.0"
  limit_unit        = "USD"
  time_unit         = "MONTHLY"
  time_period_start = "2025-11-01_00:00"
  time_period_end   = "2087-06-15_00:00"
  billing_view_arn  = "arn:aws:billing::${local.account_id}:billingview/primary"

  notification {
    comparison_operator        = "GREATER_THAN"
    notification_type          = "ACTUAL"
    threshold                  = 90
    threshold_type             = "PERCENTAGE"
    subscriber_email_addresses = [var.budget_email]
  }

  notification {
    comparison_operator        = "GREATER_THAN"
    notification_type          = "FORECASTED"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    subscriber_email_addresses = [var.budget_email]
  }

  tags = {
    Cost = "Budget for 20 dollars"
  }

  # The console set metrics = ["UnblendedCost"]. The provider accepts metrics
  # only together with a filter_expression, and dropping it from a working
  # budget gains nothing, so it is left as AWS has it.
  lifecycle {
    ignore_changes = [metrics]
  }
}
