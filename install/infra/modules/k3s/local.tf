locals {
    dns_sa = "dns-sa-${var.cluster_name}"
    gitpod_dns_names = var.domain_name == null ? toset([
        var.domain_name,
        "*.${var.domaon_name}",
        "ws.${var.domaon_name}",
        "*.ws.${var.domaon_name}",
    ]): []
}
