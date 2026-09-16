// A worked example of the openlog provider: a Slack channel, a rule that notifies it, an availability SLO
// and the burn-rate rule that watches that SLO's error budget. Walk-through and prerequisites (above all an
// API key that may write): docs/operations/terraform.md.
//
//   export OPENLOG_ENDPOINT=https://openlog.example.com
//   export OPENLOG_API_KEY=ola_…
//   terraform plan

terraform {
  required_providers {
    openlog = {
      source = "onuragtas/openlog"
    }
  }
}

provider "openlog" {
  # endpoint and api_key come from OPENLOG_ENDPOINT and OPENLOG_API_KEY.
}

variable "slack_webhook_url" {
  description = "Slack incoming webhook the on-call channel posts to."
  type        = string
  sensitive   = true
}

resource "openlog_notification_channel" "oncall_slack" {
  name = "on-call Slack"
  type = "slack"

  secrets = {
    url = var.slack_webhook_url
  }
}

resource "openlog_alert_rule" "host_cpu_high" {
  name        = "Host CPU high"
  description = "CPU above 90 % for five minutes, one incident per host."
  type        = "metric_threshold"
  severity    = "critical"

  interval_seconds = 60
  for_seconds      = 300

  condition = jsonencode({
    metric             = "system.cpu.utilization"
    aggregation        = "avg"
    window_seconds     = 300
    group_by           = ["host"]
    operator           = "gt"
    threshold          = 0.9
    recovery_threshold = 0.8
    filters = [
      { field = "attr.cpu.mode", op = "not_in", values = ["idle"] },
    ]
  })

  channel_ids = [openlog_notification_channel.oncall_slack.id]
  labels      = { team = "infra" }
}

resource "openlog_slo" "checkout_availability" {
  name         = "Checkout availability"
  description  = "99.9 % of checkout requests succeed over 28 days."
  service_name = "checkout"
  environment  = "prod"
  sli_type     = "availability"
  objective    = 99.9
  window_days  = 28
}

# Alerting on an SLO is an ordinary rule of type slo_burn that names the SLO.
resource "openlog_alert_rule" "checkout_budget_burn" {
  name     = "Checkout error budget burning"
  type     = "slo_burn"
  severity = "critical"

  condition = jsonencode({
    slo_id = openlog_slo.checkout_availability.id
  })

  channel_ids = [openlog_notification_channel.oncall_slack.id]
}

# Critical incidents of the checkout service go to the on-call channel whatever the rule's own channels are.
resource "openlog_alert_routing_rule" "critical_checkout" {
  name     = "critical checkout to on-call"
  position = 0

  channel_ids = [openlog_notification_channel.oncall_slack.id]

  match = {
    severities = ["critical"]
    services   = ["checkout"]
  }
}

resource "openlog_dashboard" "checkout" {
  name        = "Checkout"
  description = "Managed by Terraform."
  visibility  = "org"

  pages = [
    {
      name = "Overview"
      widgets = [
        {
          title         = "Errors per minute"
          visualization = "line"
          layout        = { x = 0, y = 0, w = 6, h = 3 }
          query         = "SELECT count(*) FROM Log WHERE severity_text = 'ERROR' TIMESERIES"
        },
        {
          title         = "Checkout p95 latency"
          visualization = "billboard"
          layout        = { x = 6, y = 0, w = 6, h = 3 }
          unit          = "ms"
          query         = "SELECT percentile(duration.ms, 95) FROM Transaction WHERE service.name = 'checkout'"
          thresholds = [
            { value = 800, severity = "warning" },
            { value = 1500, severity = "critical" },
          ]
        },
      ]
    },
  ]
}
