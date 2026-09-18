package sep2admin

// Theme is the seam SetTheme accepts. A later issue defines its parsed
// fields (colours, typography, spacing, logo) as a closed, server-declared
// token set: a graft never supplies CSS text, a selector, a URL, or a
// template. This package carries only the seam and the Freeze/frozen
// refusal semantics until then.
type Theme struct{}
