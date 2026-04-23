// Module-level record of the most recent server request_id observed by
// apiRequest. The "Report an issue" menu item reads this so the mailto
// body carries a trace handle operators can grep in logs without asking
// the user to reproduce the problem. Never persisted — resets on reload.
let lastRequestId = ''

export function setLastRequestId(id: string): void {
  if (id) lastRequestId = id
}

export function getLastRequestId(): string {
  return lastRequestId
}
