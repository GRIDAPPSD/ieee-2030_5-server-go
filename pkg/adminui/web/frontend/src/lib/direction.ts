// The one definition of "is this discharge or charge" and of the word the
// server's direction field is shown as. Every view that reads a direction
// goes through these, so a third value or a changed word lands in one place.

export type Direction = 'discharge' | 'charge'

export function isDirection(value: unknown): value is Direction {
  return value === 'discharge' || value === 'charge'
}

// directionWord is the server's own word for a direction, lower case, and
// `otherwise` for anything else, null included: a missing direction is stated
// as missing rather than guessed from the sign of a power.
export function directionWord(direction: unknown, otherwise: string): string {
  return isDirection(direction) ? direction : otherwise
}
