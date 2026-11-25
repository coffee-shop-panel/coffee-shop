// Passkeys in the browser: the WebAuthn ceremonies the sign-in page and
// the security settings run, over the options the panel issues.
//
// The panel speaks the library's JSON, in which every binary value -- the
// challenge, the credential ids, the answer's parts -- travels as a
// base64url string. The browser's API wants ArrayBuffers going in and
// hands ArrayBuffers back, so the whole of this file is the translation
// between the two, plus the one question both pages ask: can this
// browser, on this origin, do this at all.

// passkeySupported is whether a passkey ceremony can even be attempted:
// the API exists, and the page is in a secure context -- which is the
// browser's own way of saying the panel was opened over HTTPS (or on
// this machine). Where it is false the panel's endpoints refuse too, so
// the pages hide the feature rather than offering a button that cannot
// work.
export function passkeySupported() {
  return (
    typeof window !== 'undefined' &&
    window.isSecureContext &&
    typeof window.PublicKeyCredential !== 'undefined'
  )
}

function b64urlToBuffer(value) {
  const b64 = String(value).replace(/-/g, '+').replace(/_/g, '/')
  const padded = b64.padEnd(b64.length + ((4 - (b64.length % 4)) % 4), '=')
  const raw = atob(padded)
  const buf = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) buf[i] = raw.charCodeAt(i)
  return buf.buffer
}

function bufferToB64url(buf) {
  const bytes = new Uint8Array(buf)
  let raw = ''
  for (let i = 0; i < bytes.length; i++) raw += String.fromCharCode(bytes[i])
  return btoa(raw).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

// createOptions turns the panel's creation JSON into what
// navigator.credentials.create is given.
function createOptions(options) {
  const pk = { ...options.publicKey }
  pk.challenge = b64urlToBuffer(pk.challenge)
  pk.user = { ...pk.user, id: b64urlToBuffer(pk.user.id) }
  if (Array.isArray(pk.excludeCredentials)) {
    pk.excludeCredentials = pk.excludeCredentials.map((c) => ({
      ...c,
      id: b64urlToBuffer(c.id),
    }))
  }
  return pk
}

// requestOptions turns the panel's request JSON into what
// navigator.credentials.get is given.
function requestOptions(options) {
  const pk = { ...options.publicKey }
  pk.challenge = b64urlToBuffer(pk.challenge)
  if (Array.isArray(pk.allowCredentials)) {
    pk.allowCredentials = pk.allowCredentials.map((c) => ({
      ...c,
      id: b64urlToBuffer(c.id),
    }))
  }
  return pk
}

// serialise is a finished ceremony as the panel reads it: every buffer
// back to a base64url string, in the shape the library parses.
function serialise(cred, isCreate) {
  const out = {
    id: cred.id,
    rawId: bufferToB64url(cred.rawId),
    type: cred.type,
    clientExtensionResults: cred.getClientExtensionResults?.() ?? {},
    response: { clientDataJSON: bufferToB64url(cred.response.clientDataJSON) },
  }
  if (typeof cred.authenticatorAttachment === 'string') {
    out.authenticatorAttachment = cred.authenticatorAttachment
  }
  if (isCreate) {
    out.response.attestationObject = bufferToB64url(cred.response.attestationObject)
    if (typeof cred.response.getTransports === 'function') {
      out.response.transports = cred.response.getTransports()
    }
  } else {
    out.response.authenticatorData = bufferToB64url(cred.response.authenticatorData)
    out.response.signature = bufferToB64url(cred.response.signature)
    out.response.userHandle = cred.response.userHandle
      ? bufferToB64url(cred.response.userHandle)
      : null
  }
  return out
}

// createPasskey runs a registration ceremony over the panel's options
// and returns the answer to send back. A dismissed prompt rejects with
// the browser's NotAllowedError, which the caller turns into a quiet
// word rather than an alarm.
export async function createPasskey(options) {
  const cred = await navigator.credentials.create({
    publicKey: createOptions(options),
  })
  if (!cred) throw new Error('the browser returned no passkey')
  return serialise(cred, true)
}

// getPasskey runs a sign-in ceremony over the panel's options and
// returns the assertion to send back.
export async function getPasskey(options) {
  const cred = await navigator.credentials.get({
    publicKey: requestOptions(options),
  })
  if (!cred) throw new Error('the browser returned no passkey')
  return serialise(cred, false)
}
