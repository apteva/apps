package main

// Computer enforces the same five-million-character/serialized-response
// ceiling. Start small and grow only when the browser reports truncation.
const maxActorExtractChars = 5_000_000

var actorHTMLReadBudgets = [...]int{200_000, 400_000, 800_000, 1_000_000, 2_000_000, maxActorExtractChars}
