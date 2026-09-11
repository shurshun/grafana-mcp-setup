package server

import "html/template"

// Plain marks rather than anyone's logo: enough to tell the tabs apart at a
// glance, and nothing to get wrong about a trademark.
const (
	iconSpark = template.HTML(`<svg viewBox="0 0 24 24" width="15" height="15" fill="none" aria-hidden="true">
		<path d="M12 3v18M3 12h18M5.6 5.6l12.8 12.8M18.4 5.6L5.6 18.4"
		      stroke="currentColor" stroke-width="1.9" stroke-linecap="round"/></svg>`)

	iconWindow = template.HTML(`<svg viewBox="0 0 24 24" width="15" height="15" fill="none" aria-hidden="true">
		<rect x="3" y="4.5" width="18" height="15" rx="2.5" stroke="currentColor" stroke-width="1.8"/>
		<path d="M3 9h18" stroke="currentColor" stroke-width="1.8"/></svg>`)

	iconTerminal = template.HTML(`<svg viewBox="0 0 24 24" width="15" height="15" fill="none" aria-hidden="true">
		<path d="M5 6.5l5 5.5-5 5.5M12.5 17.5H19" stroke="currentColor" stroke-width="1.9"
		      stroke-linecap="round" stroke-linejoin="round"/></svg>`)

	iconCursor = template.HTML(`<svg viewBox="0 0 24 24" width="15" height="15" fill="none" aria-hidden="true">
		<path d="M5 3.5l14 7.5-6.2 1.9L10 19.5 5 3.5z" stroke="currentColor" stroke-width="1.7"
		      stroke-linejoin="round"/></svg>`)

	iconBrackets = template.HTML(`<svg viewBox="0 0 24 24" width="15" height="15" fill="none" aria-hidden="true">
		<path d="M9 6.5L3.5 12 9 17.5M15 6.5L20.5 12 15 17.5" stroke="currentColor" stroke-width="1.9"
		      stroke-linecap="round" stroke-linejoin="round"/></svg>`)

	iconZ = template.HTML(`<svg viewBox="0 0 24 24" width="15" height="15" fill="none" aria-hidden="true">
		<path d="M6 6.5h12L6 17.5h12" stroke="currentColor" stroke-width="1.9"
		      stroke-linecap="round" stroke-linejoin="round"/></svg>`)
)
