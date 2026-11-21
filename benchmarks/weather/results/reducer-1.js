let longestStart = null;
let longestEnd = null;
let longestDays = 0;
let longestPeak = null;

let currentStart = null;
let currentPeak = null;

for (let i = 0; i < values.length; i++) {
  const temp = values[i];
  if (temp !== null && temp >= threshold) {
    if (currentStart === null) {
      currentStart = new Date(start_date);
      currentStart.setUTCDate(currentStart.getUTCDate() + i);
      currentPeak = temp;
    } else {
      if (temp > currentPeak) {
        currentPeak = temp;
      }
    }
  } else {
    if (currentStart !== null) {
      const currentEnd = new Date(start_date);
      currentEnd.setUTCDate(currentEnd.getUTCDate() + i - 1);
      const currentDays = Math.round((currentEnd - currentStart) / (1000 * 60 * 60 * 24)) + 1;
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

// Check last spell if it ended at the last day
if (currentStart !== null) {
  const currentEnd = new Date(start_date);
  currentEnd.setUTCDate(currentEnd.getUTCDate() + values.length - 1);
  const currentDays = Math.round((currentEnd - currentStart) / (1000 * 60 * 60 * 24)) + 1;
  if (currentDays > longestDays) {
    longestStart = currentStart;
    longestEnd = currentEnd;
    longestDays = currentDays;
    longestPeak = currentPeak;
  }
}

if (longestDays === 0) {
  return { start: null, end: null, days: 0, peak_c: null };
} else {
  return {
    start: longestStart.toISOString().slice(0, 10),
    end: longestEnd.toISOString().slice(0, 10),
    days: longestDays,
    peak_c: longestPeak
  };
}