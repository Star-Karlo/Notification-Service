locals {

  # Secrets, resolved from the platform's remote state; see the same block in
  # the other services for why nothing is pasted into prod.tfvars.
  #
  # A ":key::" suffix selects one field of a JSON secret; a bare ARN is the
  # whole value, for the secrets stored as plain strings.
  secrets = [
    # The Atlas URI is stored as a plain string, so no ":key::" selector.
    { name = "MONGO_URI", valueFrom = local.platform.secret_arns.mongodb },
    { name = "JWT_PUBLIC_KEY", valueFrom = local.platform.secret_arns.jwt_public },
    { name = "SERVICE_TOKEN", valueFrom = "${local.platform.secret_arns.service_tokens}:notification-service::" },
    { name = "ACCEPTED_SERVICE_TOKENS", valueFrom = "${local.platform.secret_arns.service_tokens}:all::" },
    # Delivery channels. Each is off until its key is filled in, and /health
    # reports which are live. Push (FCM) and email are not wired here yet:
    # nothing needs them until a driver app exists.
    { name = "WHATSAPP_TOKEN", valueFrom = "${local.platform.secret_arns.integrations}:whatsapp_token::" },
    { name = "WHATSAPP_PHONE_NUMBER_ID", valueFrom = "${local.platform.secret_arns.integrations}:whatsapp_phone_number_id::" },
    { name = "WHATSAPP_VERIFY_TOKEN", valueFrom = "${local.platform.secret_arns.integrations}:whatsapp_verify_token::" },
  ]

  secret_arns = [
    local.platform.secret_arns.mongodb,
    local.platform.secret_arns.jwt_public,
    local.platform.secret_arns.service_tokens,
    local.platform.secret_arns.integrations,
  ]
}
