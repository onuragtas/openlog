-- openlog:phase expand
-- 0081_metrics_summary_quantiles: quantile values of OTLP summary data points (docs/contracts/semantic-conventions.md
-- "Metric data points", D-119). Before this migration the processor stored only count, sum and mean of summaries and
-- dropped their quantiles; POST /api/v1/metrics/query reads p50…p99 of summaries from these columns.
-- Exponential histograms need no schema change: the processor now converts their buckets to explicit_bounds and
-- bucket_counts (previously only the positive bucket counts were stored, without scale or offset).
--
-- Mixed versions: older processors do not write the columns (empty arrays); older API servers do not read them.

ALTER TABLE openlog.metrics_local ON CLUSTER '{cluster}'
    ADD COLUMN IF NOT EXISTS quantiles       Array(Float64) CODEC(ZSTD(1)) AFTER flags,
    ADD COLUMN IF NOT EXISTS quantile_values Array(Float64) CODEC(ZSTD(1)) AFTER quantiles;

ALTER TABLE openlog.metrics ON CLUSTER '{cluster}'
    ADD COLUMN IF NOT EXISTS quantiles       Array(Float64) AFTER flags,
    ADD COLUMN IF NOT EXISTS quantile_values Array(Float64) AFTER quantiles;
