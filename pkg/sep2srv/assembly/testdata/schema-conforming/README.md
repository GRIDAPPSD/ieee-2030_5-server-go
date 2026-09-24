# Schema-conforming request bodies

Request bodies for tests that must post what the IEEE 2030.5 schema requires,
rather than what our own Go structs happen to marshal.

Every other POST body in this suite comes from `xml.Marshal` on a `sep2`
struct. A body built that way can only show that the server agrees with
itself: when a struct field has the wrong shape, the encoder and the decoder
are wrong in the same direction and the round trip still passes. A conforming
`FlowReservationRequest` was answered with 400 behind a green suite for
exactly that reason (#689).

Each file here is a document we wrote to satisfy `sep.xsd`. None of it is
copied from the schema. `sep.xsd` is copyrighted by IEEE, is not distributed with
this project, and must never be committed (see NOTICE); a fixture is a
document that satisfies the schema, never an extract of it.

Conformance is therefore established out of band, by validating the file
against a licensed copy of `sep.xsd`. Each file's header comment records the
element set it covers and the rule that fixed it, so a reader can re-derive
the document from a licensed schema without having one to hand.

Naming: `<resource>.xml` carries exactly the mandatory element set for that
resource, which is the floor a conforming client may send. A variant that adds
optional elements is `<resource>_<what-it-adds>.xml`.
