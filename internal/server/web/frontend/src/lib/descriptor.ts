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

export interface DescriptorTableBody {
  columns: string[]
  rows: DescriptorRow[]
}

export interface DescriptorDefinitionEntry {
  key: string
  value: DescriptorValue
}

export interface DescriptorDefinitionGroup {
  heading?: string
  entries: DescriptorDefinitionEntry[]
}

export interface DescriptorDefinitionListBody {
  groups: DescriptorDefinitionGroup[]
}

export interface Descriptor {
  version: number
  kind?: string
  body?: unknown
}
