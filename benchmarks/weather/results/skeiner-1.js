const threshold = api.input.threshold;
const query = {
  latitude: api.input.latitude,
  longitude: api.input.longitude,
  timezone: api.input.timezone,
  start_date: api.input.start_date,
  end_date: api.input.end_date,
  daily: api.input.daily,
  models: api.input.models,
  temperature_unit: api.input.temperature_unit
};

const dailyTemps = await api.call("getDailyWeather", { query });

const times = dailyTemps.daily.time;
const temps = dailyTemps.daily.temperature_2m_max;

let longestStart = null;
let longestEnd = null;
let longestDays = 0;
let longestPeak = null;

let currentStart = null;
let currentPeak = null;

for (let i = 0; i < times.length; i++) {
  const temp = temps[i];
  if (temp !== null && temp >= threshold) {
    if (currentStart === null) {
      currentStart = times[i];
      currentPeak = temp;
    } else {
      if (temp > currentPeak) currentPeak = temp;
    }
  } else {
    if (currentStart !== null) {
      const currentEnd = times[i-1];
      const currentDays = i - times.indexOf(currentStart);
      if (currentDays > longestDays) {
        longestStart = currentStart;
        longestEnd = currentEnd;
        longestDays = currentDays;
        longestPeak = currentPeak;
      }
      currentStart = null;
      currentPeak = null;
    }
  }
}

// Check last spell if ended at the last day
if (currentStart !== null) {
  const currentEnd = times[times.length - 1];
  const currentDays = times.length - times.indexOf(currentStart);
  if (currentDays > longestDays) {
    longestStart = currentStart;
    longestEnd = currentEnd;
    longestDays = currentDays;
    longestPeak = currentPeak;
  }
}

return {
  start: longestStart,
  end: longestEnd,
  days: longestDays,
  peak_c: longestPeak
};