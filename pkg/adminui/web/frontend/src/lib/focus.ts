// focusOnMount is a Svelte action that moves keyboard focus to its element
// when the element is created, for a message or control that appears in
// response to the operator's action.
export function focusOnMount(node: HTMLElement): void {
  node.focus()
}
