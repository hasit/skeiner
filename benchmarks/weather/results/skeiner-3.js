// Fetch the daily max temperature data for the given input
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

const response = await api.call("getDailyWeather", {query});

const temps = response.daily.temperature_2m_max;
const dates = response.daily.time;
const threshold = api.input.threshold;

let longestStart = null;
let longestEnd = null;
let longestDays = 0;
let longestPeak = null;

let currentStart = null;
let currentPeak = null;
let currentDays = 0;

for (let i = 0; i < temps.length; i++) {
  const temp = temps[i];
  if (temp !== null && temp >= threshold) {
    if (currentStart === null) {
      currentStart = dates[i];
      currentPeak = temp;
      currentDays = 1;
    } else {
      currentDays++;
      if (temp > currentPeak) {
        currentPeak = temp;
      }
    }
  } else {
    if (currentDays > longestDays) {
      longestStart = currentStart;
      longestEnd = dates[i-1];
      longestDays = currentDays;
      longestPeak = currentPeak;
    }
    currentStart = null;
    currentPeak = null;
    currentDays = 0;
  }
}

if (currentDays > longestDays) {
  longestStart = currentStart;
  longestEnd = dates[temps.length-1];
  longestDays = currentDays;
  longestPeak = currentPeak;
}

if (longestDays === 0) {
  return {start: null, end: null, days: 0, peak_c: null};
} else {
  return {start: longestStart, end: longestEnd, days: longestDays, peak_c: longestPeak};
}