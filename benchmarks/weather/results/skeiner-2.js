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

const daily = await api.call("getDailyWeather", {query});

const temps = daily.daily.temperature_2m_max;
const dates = daily.daily.time;

let bestStart = null;
let bestEnd = null;
let bestDays = 0;
let bestPeak = null;

let currentStart = null;
let currentPeak = null;

for (let i = 0; i < temps.length; i++) {
  const temp = temps[i];
  if (temp === null || temp < threshold) {
    // End current spell if any
    if (currentStart !== null) {
      const currentDays = i - currentStart;
      if (currentDays > bestDays) {
        bestDays = currentDays;
        bestStart = currentStart;
        bestEnd = i - 1;
        bestPeak = currentPeak;
      }
      currentStart = null;
      currentPeak = null;
    }
  } else {
    // Continue or start a spell
    if (currentStart === null) {
      currentStart = i;
      currentPeak = temp;
    } else {
      if (temp > currentPeak) {
        currentPeak = temp;
      }
    }
  }
}

// Check last spell if ended at the end
if (currentStart !== null) {
  const currentDays = temps.length - currentStart;
  if (currentDays > bestDays) {
    bestDays = currentDays;
    bestStart = currentStart;
    bestEnd = temps.length - 1;
    bestPeak = currentPeak;
  }
}

if (bestDays === 0) {
  return {start: null, end: null, days: 0, peak_c: null};
}

return {
  start: dates[bestStart],
  end: dates[bestEnd],
  days: bestDays,
  peak_c: bestPeak
};