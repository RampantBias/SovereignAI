// Presentation precision never changes the backend's event ordering.
const lineageTimestamps = (() => {
  function format(value) {
    if (!value || value.startsWith("0001-")) return "Not recorded";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return "Not recorded";
    return date.toLocaleString(undefined, {
      year: "numeric", month: "numeric", day: "numeric",
      hour: "numeric", minute: "2-digit", second: "2-digit", fractionalSecondDigits: 3,
    });
  }

  function tiedEventIDs(events) {
    const times = new Map();
    for (const event of events) {
      const value = event.occurredAt;
      if (!value || value.startsWith("0001-")) continue;
      // Backend timestamps are canonical RFC3339Nano. Comparing the original
      // strings avoids collapsing distinct submillisecond values through Date.
      if (!times.has(value)) times.set(value, new Set());
      times.get(value).add(event.eventId);
    }
    return new Set([...times.values()].filter(ids => ids.size > 1).flatMap(ids => [...ids]));
  }

  return { format, tiedEventIDs };
})();

if (typeof module !== "undefined") module.exports = lineageTimestamps;
