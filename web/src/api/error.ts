// The error every failed API call raises. `code` is the server's machine code,
// or "network" when no response arrived at all.
export class ApiError extends Error {
  status: number
  code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}
