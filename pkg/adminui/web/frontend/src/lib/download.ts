// downloadText saves contents to the operator's machine as filename.
//
// The object URL is revoked as soon as the click is dispatched, so nothing
// downloaded here outlives the download. That matters for private key
// material: a blob URL left alive is readable for the lifetime of the tab
// by anything running in it.

export function downloadText(filename: string, contents: string, mime: string): void {
  const url = URL.createObjectURL(new Blob([contents], { type: mime }))
  try {
    const a = document.createElement('a')
    a.href = url
    a.download = filename
    a.style.display = 'none'
    document.body.appendChild(a)
    a.click()
    a.remove()
  } finally {
    URL.revokeObjectURL(url)
  }
}

// PEM_MIME is the media type for the certificate and key files this admin
// UI hands to an operator.
export const PEM_MIME = 'application/x-pem-file'
