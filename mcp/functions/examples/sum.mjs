// sum — adds the numbers in the event. Shows reading a shaped event
// payload and returning a JSON object.
//
//   POST /fn/sum  { "numbers": [1, 2, 3] }
//   → { "sum": 6, "count": 3 }

export default async function handler(event, context) {
  const numbers = Array.isArray(event?.numbers) ? event.numbers : [];
  return {
    sum: numbers.reduce((a, b) => a + Number(b || 0), 0),
    count: numbers.length,
  };
}
