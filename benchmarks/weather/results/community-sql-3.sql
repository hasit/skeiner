WITH filtered AS (
  SELECT time, temperature_2m_max
  FROM {{table}}
  WHERE time >= '2020-06-01' AND time <= '2020-08-31'
),
marked AS (
  SELECT time, temperature_2m_max,
    CASE WHEN temperature_2m_max >= :threshold THEN 1 ELSE 0 END AS is_hot
  FROM filtered
),
spells AS (
  SELECT time, temperature_2m_max, is_hot,
    DATE(time) AS day,
    ROW_NUMBER() OVER (ORDER BY time) AS rn,
    ROW_NUMBER() OVER (PARTITION BY is_hot ORDER BY time) AS rn_is_hot
  FROM marked
  WHERE temperature_2m_max IS NOT NULL
),
groups AS (
  SELECT day, temperature_2m_max, is_hot,
    rn - rn_is_hot AS grp
  FROM spells
  WHERE is_hot = 1
),
agg_spells AS (
  SELECT MIN(day) AS start_date, MAX(day) AS end_date, COUNT(*) AS days, MAX(temperature_2m_max) AS peak_c
  FROM groups
  GROUP BY grp
)
SELECT
  COALESCE((SELECT start_date FROM agg_spells ORDER BY days DESC, start_date LIMIT 1), NULL) AS start,
  COALESCE((SELECT end_date FROM agg_spells ORDER BY days DESC, start_date LIMIT 1), NULL) AS end,
  COALESCE((SELECT days FROM agg_spells ORDER BY days DESC, start_date LIMIT 1), 0) AS days,
  COALESCE((SELECT peak_c FROM agg_spells ORDER BY days DESC, start_date LIMIT 1), NULL) AS peak_c
;