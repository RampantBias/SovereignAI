const { test } = require("node:test");
const assert = require("node:assert/strict");
const { format, tiedEventIDs } = require("./timestamps.js");

test("shows milliseconds, including whole-second records, and handles absent times", () => {
  assert.match(format("2026-09-11T12:00:00.987654321Z"), /[.,]987/);
  assert.match(format("2026-09-11T12:00:00Z"), /[.,]000/);
  for (const value of [undefined, "", "0001-01-01T00:00:00Z", "invalid"]) {
    assert.equal(format(value), "Not recorded");
  }
});

test("marks exact ties without conflating submillisecond events or repeated IDs", () => {
  const events = [
    { eventId: "a", occurredAt: "2026-09-11T12:00:00.987654321Z" },
    { eventId: "b", occurredAt: "2026-09-11T12:00:00.987654322Z" },
    { eventId: "c", occurredAt: "2026-09-11T12:00:01Z" },
    { eventId: "d", occurredAt: "2026-09-11T12:00:01Z" },
  ];
  assert.deepEqual([...tiedEventIDs([...events, events[0]])].sort(), ["c", "d"]);
  assert.deepEqual([...tiedEventIDs(events.toReversed())].sort(), ["c", "d"]);
});
