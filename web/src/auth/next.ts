const signInPath = '/sign-in'

function isSameSitePath(value: string): boolean {
  // Control characters (tab, newline) are stripped by browsers inside URLs, which could turn "/\t/x" into "//x".
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(value)) {
    return false
  }
  return value.startsWith('/') && !value.startsWith('//') && !value.startsWith('/\\')
}

/**
 * Returns `value` when it is a path on this site, otherwise "/". Only a single leading "/" is accepted:
 * "//host", "/\host", schemes and relative paths are not, even when percent-encoded.
 * The sign-in page itself is not a destination.
 */
export function safeNext(value: string | null | undefined): string {
  if (!value || !isSameSitePath(value)) {
    return '/'
  }
  let decoded = value
  try {
    decoded = decodeURIComponent(value)
  } catch {
    return '/'
  }
  if (!isSameSitePath(decoded)) {
    return '/'
  }
  if (value === signInPath || value.startsWith(`${signInPath}?`) || value.startsWith(`${signInPath}/`)) {
    return '/'
  }
  return value
}
