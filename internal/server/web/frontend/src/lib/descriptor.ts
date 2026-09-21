// Wire types for the payload pkg/sep2admin/descriptor.go's Descriptor
// marshals to. Kind is typed as a plain string rather than a closed
// union: a server ahead of this frontend's build can send a kind this
// frontend does not know, and DescriptorPanel is what has to handle
// that case honestly rather than the type system hiding it. Value
// carries no type signal on the wire either: Go's Value is a named
// string type, but JSON erases the distinction, so every value here is
// a plain string and a renderer supplies its own escaping discipline
// rather than trusting the type.
export type DescriptorValue = string

export type DescriptorRow = DescriptorValue[]

// columns and rows are nullable, not just empty-array-capable: Go's
// TableBody declares them as plain slices with no `omitempty`, so an
// empty collection marshals as JSON null rather than []. A consumer
// reads both as empty.
export interface DescriptorTableBody {
  columns: string[] | null
  rows: DescriptorRow[] | null
}

export interface DescriptorDefinitionEntry {
  key: string
  value: DescriptorValue
}

export interface DescriptorDefinitionGroup {
  heading?: string
  entries: DescriptorDefinitionEntry[]
}

// groups is nullable for the same reason columns and rows are: Groups
// has no `omitempty` on the Go side, so an empty list marshals as null.
export interface DescriptorDefinitionListBody {
  groups: DescriptorDefinitionGroup[] | null
}

export interface Descriptor {
  version: number
  kind?: string
  body?: unknown
}
