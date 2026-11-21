WITH filtered AS (
  SELECT time, temperature_2m_max
  FROM {{table}}
  WHERE time BETWEEN '2020-06-01' AND '2020-08-31'
    AND temperature_2m_max IS NOT NULL
), flagged AS (
  SELECT time, temperature_2m_max,
    CASE WHEN temperature_2m_max >= :threshold THEN 1 ELSE 0 END AS meets_threshold
  FROM filtered
), grouped AS (
  SELECT time, temperature_2m_max, meets_threshold,
    DATE(time) AS day,
    ROW_NUMBER() OVER (ORDER BY time) -
    ROW_NUMBER() OVER (PARTITION BY meets_threshold ORDER BY time) AS grp
  FROM flagged
  WHERE meets_threshold = 1
), spans AS (
  SELECT MIN(day) AS "start", MAX(day) AS "end", COUNT(*) AS days, MAX(temperature_2m_max) AS peak_c
  FROM grouped
  GROUP BY grp
), best_span AS (
  SELECT "start", "end", days, peak_c
  FROM spans
  ORDER BY days DESC, "start" ASC
  LIMIT 1
)
SELECT "start", "end", days, peak_c
FROM best_span
UNION ALL
SELECT NULL AS "start", NULL AS "end", 0 AS days, NULL AS peak_c
WHERE NOT EXISTS (SELECT 1 FROM best_span)