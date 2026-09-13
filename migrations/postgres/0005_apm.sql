-- openlog:phase expand
-- 0005_apm: per-service APM settings (docs/contracts/apm.md §4, postgres.md "apm_service_settings").
-- A row with service_namespace = '' and deployment_environment = '' applies to every namespace and
-- environment of the service unless a more specific row exists. Services without a row use
-- OPENLOG_APM_DEFAULT_APDEX_T (500 ms).

CREATE TABLE apm_service_settings (
    org_id                  uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    service_name            text NOT NULL CHECK (length(service_name) BETWEEN 1 AND 512),
    service_namespace       text NOT NULL DEFAULT '' CHECK (length(service_namespace) <= 512),
    deployment_environment  text NOT NULL DEFAULT '' CHECK (length(deployment_environment) <= 512),
    apdex_t_ms              integer NOT NULL CHECK (apdex_t_ms BETWEEN 1 AND 600000),
    updated_at              timestamptz NOT NULL DEFAULT now(),
    updated_by              uuid REFERENCES users (id) ON DELETE SET NULL,
    PRIMARY KEY (org_id, service_name, service_namespace, deployment_environment)
);
