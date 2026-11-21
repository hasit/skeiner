# Weather data attribution

The ten city files contain the original JSON responses from Open-Meteo's historical archive. Each query requests ERA5 daily maximum air temperature at 2 m in °C in the city's local time zone. These are gridded reanalysis estimates. The SHA-256 checksums cover the original response bytes, including each request's `generationtime_ms` field. [Queries](../dataset.py) · [Checksums](sha256.json)

Phoenix, Paris and London cover June 1 through August 31, 2020. New York, Chicago, Madrid, Berlin, Tokyo and Seoul cover June 1 through August 31, 2019. Sydney covers December 1, 2019 through February 29, 2020. All ten files were retrieved on November 20, 2026 (Pacific time). [Extension enrollment](../extension.md)

Weather data by Open-Meteo, distributed under CC BY 4.0. Contains modified Copernicus Climate Change Service information (ERA5, 2019–2020). The API derives the daily series from the original reanalysis. [Open-Meteo Historical Weather API](https://open-meteo.com/en/docs/historical-weather-api) · [Open-Meteo terms](https://open-meteo.com/en/terms) · [ERA5 source dataset](https://doi.org/10.24381/cds.adbb2d47) · [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/)
