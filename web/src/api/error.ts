// The error every failed API call raises. `code` is the server's machine code,
// or "network" when no response arrived at all.
export class ApiError extends Error {
  status: number
  code: string
  /** What the server found wrong with a request body (a 422 answer); [] for every other error. */
  problems: { file: string; line: number; message: string }[]

  constructor(status: number, code: string, message: string, problems: { file: string; line: number; message: string }[] = []) {
    super(message)
    this.problems = problems
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}
