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
      const currentDays = (currentEnd - currentStart) / (1000 * 60 * 60 * 24) + 1;
      if (currentDays > longestDays) {
        longestDays = currentDays;
        longestStart = currentStart.toISOString().slice(0, 10);
        longestEnd = currentEnd.toISOString().slice(0, 10);
        longestPeak = currentPeak;
      }
      currentStart = null;
      currentPeak = null;
    }
  }
}

// Check at the end if a spell was ongoing
if (currentStart !== null) {
  const currentEnd = new Date(start_date);
  currentEnd.setUTCDate(currentEnd.getUTCDate() + values.length - 1);
  const currentDays = (currentEnd - currentStart) / (1000 * 60 * 60 * 24) + 1;
  if (currentDays > longestDays) {
    longestDays = currentDays;
    longestStart = currentStart.toISOString().slice(0, 10);
    longestEnd = currentEnd.toISOString().slice(0, 10);
    longestPeak = currentPeak;
  }
}

if (longestDays === 0) {
  return { start: null, end: null, days: 0, peak_c: null };
}

return { start: longestStart, end: longestEnd, days: longestDays, peak_c: longestPeak };