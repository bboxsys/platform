// Status presentation, in one place. Views tend to reach for raw Tailwind
// palette classes (bg-emerald-500, text-amber-700 dark:text-amber-300), one
// view at a time, which drift from the status tokens and cannot follow a
// theme change. These maps resolve to the tokens in theme.css instead, so a
// dot, a banner and a chart series all read the same green.
//
// Fills (DOT) are fixed in both themes. Text (TEXT) is themed, because the
// fills are too light to read on a light surface.

export type Tone = 'default' | 'muted' | 'good' | 'warn' | 'bad'

export const TONE_DOT: Record<Tone, string> = {
  default: 'bg-muted-foreground/40',
  muted:   'bg-muted-foreground/40',
  good:    'bg-status-good',
  warn:    'bg-status-warn',
  bad:     'bg-status-bad',
}

export const TONE_TEXT: Record<Tone, string> = {
  default: '',
  muted:   'text-muted-foreground',
  good:    'text-status-good-text',
  warn:    'text-status-warn-text',
  bad:     'text-status-bad-text',
}

// A tinted notice: border, wash and text at one tone.
export const TONE_NOTICE: Record<Tone, string> = {
  default: 'border-border bg-muted/40 text-foreground',
  muted:   'border-border bg-muted/40 text-muted-foreground',
  good:    'border-status-good/40 bg-status-good/10 text-status-good-text',
  warn:    'border-status-warn/40 bg-status-warn/10 text-status-warn-text',
  bad:     'border-status-bad/40 bg-status-bad/10 text-status-bad-text',
}

// The left rail and wash for a metric tile. A brand rail marks what a tile is
// about -- `brand` and `highlight` are the two identity accents, for an app
// to assign to its own domains -- and a status tone takes over whenever the
// tile carries a real reading.
export type Rail = 'brand' | 'highlight' | Tone

export const RAIL: Record<Rail, string> = {
  brand:     'border-l-brand bg-brand/[0.04]',
  highlight: 'border-l-brand-highlight bg-brand-highlight/[0.05]',
  default:   'border-l-border',
  muted:     'border-l-border',
  good:      'border-l-status-good bg-status-good/[0.05]',
  warn:      'border-l-status-warn bg-status-warn/[0.06]',
  bad:       'border-l-status-bad bg-status-bad/[0.06]',
}

export const RAIL_ICON: Record<Rail, string> = {
  brand:     'text-brand',
  highlight: 'text-brand-highlight-ink',
  default:   'text-muted-foreground',
  muted:     'text-muted-foreground',
  good:      'text-status-good-text',
  warn:      'text-status-warn-text',
  bad:       'text-status-bad-text',
}

// An accent surface: a card or table frame with a top rail and a wash at one
// accent. The four accents are named by the token they read, not by a color,
// because the same token is blue under one brand and orange under another:
// `brand` and `highlight` are the two identity accents, `chart-3` and
// `chart-5` the two series accents a brand may restate. Which of an app's
// domains wears which accent is the app's decision.
export type AccentTone = 'brand' | 'highlight' | 'chart-3' | 'chart-5'

export const ACCENT_SURFACE: Record<AccentTone, { frame: string; rail: string }> = {
  'brand': {
    frame: 'border-brand/30 from-brand/[0.12] hover:border-brand/50',
    rail:  'bg-brand',
  },
  'highlight': {
    frame: 'border-brand-highlight/30 from-brand-highlight/[0.12] hover:border-brand-highlight/50',
    rail:  'bg-brand-highlight',
  },
  'chart-3': {
    frame: 'border-chart-3/30 from-chart-3/[0.12] hover:border-chart-3/50',
    rail:  'bg-chart-3',
  },
  'chart-5': {
    frame: 'border-chart-5/30 from-chart-5/[0.12] hover:border-chart-5/50',
    rail:  'bg-chart-5',
  },
}

// The fill of a meter at one rail: the accent for a tile that is about a
// domain, the status fill for a tile that carries a reading.
export const RAIL_METER: Record<Rail, string> = {
  brand:     'bg-brand',
  highlight: 'bg-brand-highlight',
  default:   'bg-primary',
  muted:     'bg-muted-foreground/40',
  good:      'bg-status-good',
  warn:      'bg-status-warn',
  bad:       'bg-status-bad',
}
