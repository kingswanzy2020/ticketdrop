# The public zone for kahmedt.com. Porkbun delegates to its four name servers,
# so replacing it (a new name, or a destroy) would mean new name servers and
# redoing the delegation. Its NS and SOA records are left to Route 53.
resource "aws_route53_zone" "main" {
  name    = "kahmedt.com"
  comment = "This hosted is for a Ticketdrop microservice web app"

  lifecycle {
    prevent_destroy = true
  }
}
