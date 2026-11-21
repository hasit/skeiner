WITH filtered AS (
  SELECT time, temperature_2m_max
  FROM {{table}}
  WHERE time BETWEEN '2020-06-01' AND '2020-08-31'
), marked AS (
  SELECT time, temperature_2m_max,
         CASE WHEN temperature_2m_max >= :threshold THEN 1 ELSE 0 END AS hot
  FROM filtered
), groups AS (
  SELECT time, temperature_2m_max, hot,
         DATE(time) AS date,
         ROW_NUMBER() OVER (ORDER BY time) - 
         ROW_NUMBER() OVER (PARTITION BY hot ORDER BY time) AS grp
  FROM marked
  WHERE hot = 1
), spells AS (
  SELECT MIN(date) AS start_date, MAX(date) AS end_date, COUNT(*) AS days, MAX(temperature_2m_max) AS peak_c, grp
  FROM groups
  GROUP BY grp
)
SELECT 
  COALESCE(MIN(start_date), NULL) AS start,
  COALESCE(MIN(end_date), NULL) AS end,
  COALESCE(MAX(days), 0) AS days,
  COALESCE(MAX(peak_c), NULL) AS peak_c
FROM spells
WHERE days = (SELECT MAX(days) FROM spells)
LIMIT 1;