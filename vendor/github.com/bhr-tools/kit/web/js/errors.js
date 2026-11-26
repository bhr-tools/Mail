// Error of an API call (or of a local change): its message can be shown to
// the user, status is the HTTP status (0 when the server cannot be reached).

export class ApiError extends Error {
  constructor(message, status) {
    super(message);
    this.status = status;
  }
}
