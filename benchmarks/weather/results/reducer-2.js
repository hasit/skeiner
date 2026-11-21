let longestStart = null;
let longestEnd = null;
let longestDays = 0;
let longestPeak = null;

let currentStart = null;
let currentPeak = null;
let currentDays = 0;

for (let i = 0; i < values.length; i++) {
  const temp = values[i];
  if (temp !== null && temp >= threshold) {
    if (currentStart === null) {
      currentStart = new Date(start_date);
      currentStart.setUTCDate(currentStart.getUTCDate() + i);
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
      longestEnd = new Date(currentStart);
      longestEnd.setUTCDate(longestEnd.getUTCDate() + currentDays - 1);
      longestDays = currentDays;
      longestPeak = currentPeak;
    }
    currentStart = null;
    currentDays = 0;
    currentPeak = null;
  }
}

// Check at the end of the loop
if (currentDays > longestDays) {
  longestStart = currentStart;
  longestEnd = new Date(currentStart);
  longestEnd.setUTCDate(longestEnd.getUTCDate() + currentDays - 1);
  longestDays = currentDays;
  longestPeak = currentPeak;
}

if (longestDays === 0) {
  return {start: null, end: null, days: 0, peak_c: null};
}

return {
  start: longestStart.toISOString().slice(0, 10),
  end: longestEnd.toISOString().slice(0, 10),
  days: longestDays,
  peak_c: longestPeak
};