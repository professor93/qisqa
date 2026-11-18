// The author's signature on what the service sends: a header on every
// response and a comment at the end of every HTML page.
package app

// signature is the text of both.
const signature = "Inoyatulloh (professor93)"

// signatureHeader names the response header that carries the signature, and
// signatureValue is its value: one slice for all responses, which nobody
// changes.
const signatureHeader = "X-Powered-By"

var signatureValue = []string{signature}

// signatureComment ends every HTML page.
const signatureComment = "<!-- " + signature + " -->\n"
