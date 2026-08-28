export const toolColors: Record<string, string> = {
    claude: '#c4a0ff',
    codex: '#66e088',
    gemini: '#7fd4ff',
    copilot: '#66b3ff',
    opencode: '#bc8cff',
    pi: '#ff7b29',
}

export const statusConfig: Record<string, { color: string; label: string; icon?: string; bg?: string }> = {
    active: { color: 'var(--success)', label: 'Running', bg: 'color-mix(in oklch, var(--success) 8%, transparent)' },
    waiting: { color: 'var(--warning)', label: 'Waiting', icon: '●', bg: 'color-mix(in oklch, var(--warning) 8%, transparent)' },
    error: { color: 'var(--destructive)', label: 'Error', icon: '!', bg: 'color-mix(in oklch, var(--destructive) 8%, transparent)' },
    stuck: { color: 'oklch(0.70 0.18 50)', label: 'Stuck', icon: '◴', bg: 'color-mix(in oklch, oklch(0.70 0.18 50) 10%, transparent)' },
    completed: { color: 'var(--success)', label: 'Completed', icon: '✓', bg: 'color-mix(in oklch, var(--success) 8%, transparent)' },
}


import type { SessionState } from './lib/sessionState'

export interface SignalTreatment {
    dot: string
    text: string
    pulse: boolean
    hollow: boolean
}

export const signalTreatment: Record<SessionState, SignalTreatment> = {
    needs_you: { dot: 'var(--warning)', text: 'var(--warning)', pulse: true, hollow: false },
    working: { dot: 'var(--success)', text: 'var(--body-text)', pulse: false, hollow: false },
    idle: { dot: 'var(--mute)', text: 'var(--mute)', pulse: false, hollow: true },
    offline: { dot: 'var(--stone)', text: 'var(--mute)', pulse: false, hollow: true },
}

export interface ThemePreset {
    name: string
    label: string
    cssVars: Record<string, string>
    xterm: {
        background: string
        foreground: string
        cursor: string
        cursorAccent: string
        selectionBackground: string
        black: string
        red: string
        green: string
        yellow: string
        blue: string
        magenta: string
        cyan: string
        white: string
        brightBlack: string
        brightRed: string
        brightGreen: string
        brightYellow: string
        brightBlue: string
        brightMagenta: string
        brightCyan: string
        brightWhite: string
    }
}

export const themePresets: Record<string, ThemePreset> = {
    'raycast': {
        name: 'raycast',
        label: 'Default',
        cssVars: {
            // Design tokens (Default palette — warm terracotta)
            '--canvas': '#0a0a09',
            '--surface': '#100f0e',
            '--surface-elevated': '#131211',
            '--surface-card': '#151413',
            '--hairline': '#302d2b',
            '--hairline-soft': 'rgba(255,255,255,0.10)',
            '--hairline-strong': 'rgba(255,255,255,0.20)',
            '--ink': '#f6f5f4',
            '--body-text': '#e7e5e2',
            '--mute': '#b8b3ad',
            '--ash': '#938e88',
            '--stone': '#6b6763',
            '--on-dark': '#ffffff',
            '--on-dark-mute': 'rgba(255,255,255,0.72)',
            '--accent-blue': '#f5813f',
            '--accent-blue-soft': 'rgba(245,129,63,0.16)',
            '--accent-red': '#ff5a68',
            '--accent-red-soft': 'rgba(255,90,104,0.15)',
            '--accent-green': '#93cf88',
            '--accent-green-soft': 'rgba(147,207,136,0.15)',
            '--accent-yellow': '#ffb454',
            '--accent-yellow-soft': 'rgba(255,180,84,0.15)',
            '--hero-stripe-start': '#f5813f',
            '--hero-stripe-end': '#8a2f14',
            // Shadcn CSS variable mappings
            '--background': 'var(--canvas)',
            '--foreground': 'var(--ink)',
            '--card': 'var(--surface)',
            '--card-foreground': 'var(--ink)',
            '--popover': 'var(--surface-elevated)',
            '--popover-foreground': 'var(--ink)',
            '--primary': 'var(--accent-blue)',
            '--primary-foreground': '#1a0e08',
            '--secondary': 'rgba(255,255,255,0.06)',
            '--secondary-foreground': 'var(--on-dark)',
            '--muted': 'var(--surface-elevated)',
            '--muted-foreground': 'var(--mute)',
            '--accent': 'var(--on-dark)',
            '--accent-foreground': '#000000',
            '--destructive': 'var(--accent-red)',
            '--destructive-foreground': 'var(--on-dark)',
            '--border': 'var(--hairline)',
            '--input': 'var(--surface-elevated)',
            '--ring': 'var(--accent-blue)',
            '--success': 'var(--accent-green)',
            '--warning': 'var(--accent-yellow)',
            '--sidebar': 'var(--canvas)',
            '--sidebar-foreground': 'var(--ink)',
            '--sidebar-primary': 'var(--accent-blue)',
            '--sidebar-primary-foreground': '#1a0e08',
            '--sidebar-accent': 'var(--surface-elevated)',
            '--sidebar-accent-foreground': 'var(--on-dark)',
            '--sidebar-border': 'var(--hairline)',
            '--sidebar-ring': 'var(--accent-blue)',
            '--chart-primary': 'var(--accent-blue)',
            '--chart-secondary': 'var(--accent-yellow)',
        },
        xterm: {
            background: '#0a0a09',
            foreground: '#efedeb',
            cursor: '#f5813f',
            cursorAccent: '#0a0a09',
            selectionBackground: 'rgba(245, 129, 63, 0.28)',
            black: '#100f0e',
            red: '#ff5a68',
            green: '#93cf88',
            yellow: '#ffb454',
            blue: '#82aaff',
            magenta: '#c792ea',
            cyan: '#63ccc3',
            white: '#e2e0dd',
            brightBlack: '#8b8680',
            brightRed: '#ff8593',
            brightGreen: '#bce3a8',
            brightYellow: '#ffd08a',
            brightBlue: '#a3c2ff',
            brightMagenta: '#ddb3f5',
            brightCyan: '#9ceae3',
            brightWhite: '#ffffff',
        },
    },
    'dark': {
        name: 'dark',
        label: 'Dark',
        cssVars: {
            // Design tokens (Dark palette)
            '--canvas': 'oklch(0.1 0 0)',
            '--surface': 'oklch(0.13 0 0)',
            '--surface-elevated': 'oklch(0.15 0 0)',
            '--surface-card': 'oklch(0.17 0 0)',
            '--hairline': 'oklch(0.25 0 0)',
            '--hairline-soft': 'oklch(0.25 0 0 / 0.5)',
            '--hairline-strong': 'oklch(0.25 0 0)',
            '--ink': 'oklch(0.85 0 0)',
            '--body-text': 'oklch(0.7 0 0)',
            '--mute': 'oklch(0.55 0 0)',
            '--ash': 'oklch(0.45 0 0)',
            '--stone': 'oklch(0.35 0 0)',
            '--on-dark': 'oklch(0.95 0 0)',
            '--on-dark-mute': 'oklch(0.95 0 0 / 0.72)',
            '--accent-blue': 'oklch(0.72 0.16 210)',
            '--accent-blue-soft': 'oklch(0.72 0.16 210 / 0.15)',
            '--accent-red': 'oklch(0.55 0.2 25)',
            '--accent-red-soft': 'oklch(0.55 0.2 25 / 0.15)',
            '--accent-green': 'oklch(0.65 0.15 145)',
            '--accent-green-soft': 'oklch(0.65 0.15 145 / 0.15)',
            '--accent-yellow': 'oklch(0.65 0.15 80)',
            '--accent-yellow-soft': 'oklch(0.65 0.15 80 / 0.15)',
            '--hero-stripe-start': 'oklch(0.55 0.2 25)',
            '--hero-stripe-end': 'oklch(0.4 0.15 25)',
            // Shadcn CSS variable mappings
            '--background': 'var(--canvas)',
            '--foreground': 'var(--ink)',
            '--card': 'var(--surface)',
            '--card-foreground': 'var(--ink)',
            '--popover': 'var(--surface-elevated)',
            '--popover-foreground': 'var(--ink)',
            '--primary': 'oklch(0.72 0.16 210)',
            '--primary-foreground': 'oklch(0.1 0 0)',
            '--secondary': 'oklch(0.18 0 0)',
            '--secondary-foreground': 'oklch(0.7 0 0)',
            '--muted': 'var(--surface-elevated)',
            '--muted-foreground': 'var(--mute)',
            '--accent': 'oklch(0.65 0.1 210)',
            '--accent-foreground': 'oklch(0.1 0 0)',
            '--destructive': 'var(--accent-red)',
            '--destructive-foreground': 'var(--on-dark)',
            '--border': 'var(--hairline)',
            '--input': 'var(--surface-elevated)',
            '--ring': 'oklch(0.72 0.16 210)',
            '--success': 'var(--accent-green)',
            '--warning': 'var(--accent-yellow)',
            '--sidebar': 'var(--canvas)',
            '--sidebar-foreground': 'var(--ink)',
            '--sidebar-primary': 'oklch(0.72 0.16 210)',
            '--sidebar-primary-foreground': 'oklch(0.1 0 0)',
            '--sidebar-accent': 'oklch(0.18 0 0)',
            '--sidebar-accent-foreground': 'var(--ink)',
            '--sidebar-border': 'var(--hairline)',
            '--sidebar-ring': 'oklch(0.72 0.16 210)',
            '--chart-primary': 'oklch(0.68 0.14 210)',
            '--chart-secondary': 'oklch(0.65 0.1 310)',
        },
        xterm: {
            // WebGL resolves transparent terminal colors against this value.
            // Keep it black so its opaque fallback matches the terminal surface.
            background: '#000000',
            foreground: '#d4d4d4',
            cursor: '#d4d4d4',
            cursorAccent: '#1a1a1a',
            selectionBackground: 'rgba(212, 212, 212, 0.2)',
            black: '#1a1a1a',
            red: '#f44747',
            green: '#6a9955',
            yellow: '#d7ba7d',
            blue: '#569cd6',
            magenta: '#c586c0',
            cyan: '#4ec9b0',
            white: '#d4d4d4',
            brightBlack: '#808080',
            brightRed: '#f44747',
            brightGreen: '#6a9955',
            brightYellow: '#d7ba7d',
            brightBlue: '#569cd6',
            brightMagenta: '#c586c0',
            brightCyan: '#4ec9b0',
            brightWhite: '#e5e5e5',
        },
    },
    'light': {
        name: 'light',
        label: 'Light',
        cssVars: {
            // Design tokens (Light palette)
            '--canvas': 'oklch(0.97 0 0)',
            '--surface': 'oklch(1 0 0)',
            '--surface-elevated': 'oklch(0.94 0 0)',
            '--surface-card': 'oklch(0.92 0 0)',
            '--hairline': 'oklch(0.85 0 0)',
            '--hairline-soft': 'oklch(0.85 0 0 / 0.5)',
            '--hairline-strong': 'oklch(0.85 0 0)',
            '--ink': 'oklch(0.2 0 0)',
            '--body-text': 'oklch(0.35 0 0)',
            '--mute': 'oklch(0.5 0 0)',
            '--ash': 'oklch(0.6 0 0)',
            '--stone': 'oklch(0.7 0 0)',
            '--on-dark': 'oklch(0.2 0 0)',
            '--on-dark-mute': 'oklch(0.2 0 0 / 0.72)',
            '--accent-blue': 'oklch(0.45 0.12 250)',
            '--accent-blue-soft': 'oklch(0.45 0.12 250 / 0.15)',
            '--accent-red': 'oklch(0.5 0.2 25)',
            '--accent-red-soft': 'oklch(0.5 0.2 25 / 0.15)',
            '--accent-green': 'oklch(0.5 0.15 145)',
            '--accent-green-soft': 'oklch(0.5 0.15 145 / 0.15)',
            '--accent-yellow': 'oklch(0.55 0.15 80)',
            '--accent-yellow-soft': 'oklch(0.55 0.15 80 / 0.15)',
            '--hero-stripe-start': 'oklch(0.5 0.2 25)',
            '--hero-stripe-end': 'oklch(0.35 0.15 25)',
            // Shadcn CSS variable mappings
            '--background': 'var(--canvas)',
            '--foreground': 'var(--ink)',
            '--card': 'var(--surface)',
            '--card-foreground': 'var(--ink)',
            '--popover': 'var(--surface-elevated)',
            '--popover-foreground': 'var(--ink)',
            '--primary': 'oklch(0.45 0.12 250)',
            '--primary-foreground': 'oklch(0.98 0 0)',
            '--secondary': 'oklch(0.92 0 0)',
            '--secondary-foreground': 'oklch(0.35 0 0)',
            '--muted': 'var(--surface-elevated)',
            '--muted-foreground': 'var(--mute)',
            '--accent': 'oklch(0.5 0.12 250)',
            '--accent-foreground': 'oklch(0.98 0 0)',
            '--destructive': 'var(--accent-red)',
            '--destructive-foreground': 'var(--on-dark)',
            '--border': 'var(--hairline)',
            '--input': 'var(--surface-elevated)',
            '--ring': 'oklch(0.45 0.12 250)',
            '--success': 'var(--accent-green)',
            '--warning': 'var(--accent-yellow)',
            '--sidebar': 'var(--canvas)',
            '--sidebar-foreground': 'var(--ink)',
            '--sidebar-primary': 'oklch(0.45 0.12 250)',
            '--sidebar-primary-foreground': 'oklch(0.98 0 0)',
            '--sidebar-accent': 'oklch(0.9 0 0)',
            '--sidebar-accent-foreground': 'var(--ink)',
            '--sidebar-border': 'var(--hairline)',
            '--sidebar-ring': 'oklch(0.45 0.12 250)',
            '--chart-primary': 'oklch(0.5 0.12 250)',
            '--chart-secondary': 'oklch(0.5 0.12 310)',
        },
        xterm: {
            background: '#ffffff',
            foreground: '#383a42',
            cursor: '#383a42',
            cursorAccent: '#ffffff',
            selectionBackground: 'rgba(56, 58, 66, 0.15)',
            black: '#383a42',
            red: '#e45649',
            green: '#50a14f',
            yellow: '#c18401',
            blue: '#4078f2',
            magenta: '#a626a4',
            cyan: '#0184bc',
            white: '#fafafa',
            brightBlack: '#a0a1a7',
            brightRed: '#e45649',
            brightGreen: '#50a14f',
            brightYellow: '#c18401',
            brightBlue: '#4078f2',
            brightMagenta: '#a626a4',
            brightCyan: '#0184bc',
            brightWhite: '#ffffff',
        },
    },
}

// ── Custom theme (user-defined palette) ──────────────────────────────────
//
// A small, curated set of primitives the user can control directly, rather
// than exposing all ~50 cssVar keys. Anything in the Shadcn CSS variable
// mapping section that doesn't have a direct primitive equivalent here just
// reuses the closest matching primitive (e.g. all surface tiers collapse to
// `background`, all secondary-text tiers collapse to `muted`).
export interface CustomThemePalette {
    background: string
    foreground: string
    muted: string
    accent: string
    success: string
    warning: string
    destructive: string
    ansiBlack: string
    ansiRed: string
    ansiGreen: string
    ansiYellow: string
    ansiBlue: string
    ansiMagenta: string
    ansiCyan: string
    ansiWhite: string
    ansiBrightBlack: string
    ansiBrightRed: string
    ansiBrightGreen: string
    ansiBrightYellow: string
    ansiBrightBlue: string
    ansiBrightMagenta: string
    ansiBrightCyan: string
    ansiBrightWhite: string
    cursor: string
    selectionBackground: string
}

// Fallback values used for any field missing from the persisted custom
// palette (e.g. a user who has only touched a couple of fields so far).
// Sourced from the Default preset so an unconfigured custom theme still
// looks coherent.
export const defaultCustomThemePalette: CustomThemePalette = {
    background: '#0a0a09',
    foreground: '#f6f5f4',
    muted: '#b2ada7',
    accent: '#f5813f',
    success: '#93cf88',
    warning: '#ffb454',
    destructive: '#ff5a68',
    ansiBlack: '#100f0e',
    ansiRed: '#ff5a68',
    ansiGreen: '#93cf88',
    ansiYellow: '#ffb454',
    ansiBlue: '#82aaff',
    ansiMagenta: '#c792ea',
    ansiCyan: '#63ccc3',
    ansiWhite: '#e2e0dd',
    ansiBrightBlack: '#8b8680',
    ansiBrightRed: '#ff8593',
    ansiBrightGreen: '#bce3a8',
    ansiBrightYellow: '#ffd08a',
    ansiBrightBlue: '#a3c2ff',
    ansiBrightMagenta: '#ddb3f5',
    ansiBrightCyan: '#9ceae3',
    ansiBrightWhite: '#ffffff',
    cursor: '#f5813f',
    selectionBackground: 'rgba(245, 129, 63, 0.28)',
}

export function resolveCustomThemePalette(stored?: Partial<CustomThemePalette> | null): CustomThemePalette {
    return { ...defaultCustomThemePalette, ...(stored || {}) }
}

export function buildCustomThemePreset(stored?: Partial<CustomThemePalette> | null): ThemePreset {
    const p = resolveCustomThemePalette(stored)
    return {
        name: 'custom',
        label: 'Custom',
        cssVars: {
            // Design tokens
            '--canvas': p.background,
            '--surface': p.background,
            '--surface-elevated': p.background,
            '--surface-card': p.background,
            '--hairline': `color-mix(in srgb, ${p.foreground} 20%, transparent)`,
            '--hairline-soft': `color-mix(in srgb, ${p.foreground} 10%, transparent)`,
            '--hairline-strong': `color-mix(in srgb, ${p.foreground} 30%, transparent)`,
            '--ink': p.foreground,
            '--body-text': p.foreground,
            '--mute': p.muted,
            '--ash': p.muted,
            '--stone': p.muted,
            '--on-dark': p.foreground,
            '--on-dark-mute': `color-mix(in srgb, ${p.foreground} 72%, transparent)`,
            '--accent-blue': p.accent,
            '--accent-blue-soft': `color-mix(in srgb, ${p.accent} 15%, transparent)`,
            '--accent-red': p.destructive,
            '--accent-red-soft': `color-mix(in srgb, ${p.destructive} 15%, transparent)`,
            '--accent-green': p.success,
            '--accent-green-soft': `color-mix(in srgb, ${p.success} 15%, transparent)`,
            '--accent-yellow': p.warning,
            '--accent-yellow-soft': `color-mix(in srgb, ${p.warning} 15%, transparent)`,
            '--hero-stripe-start': p.accent,
            '--hero-stripe-end': p.destructive,
            // Shadcn CSS variable mappings
            '--background': 'var(--canvas)',
            '--foreground': 'var(--ink)',
            '--card': 'var(--surface)',
            '--card-foreground': 'var(--ink)',
            '--popover': 'var(--surface-elevated)',
            '--popover-foreground': 'var(--ink)',
            '--primary': 'var(--accent-blue)',
            '--primary-foreground': p.background,
            '--secondary': 'var(--surface-elevated)',
            '--secondary-foreground': 'var(--ink)',
            '--muted': 'var(--surface-elevated)',
            '--muted-foreground': 'var(--mute)',
            '--accent': 'var(--accent-blue)',
            '--accent-foreground': p.background,
            '--destructive': 'var(--accent-red)',
            '--destructive-foreground': p.background,
            '--border': 'var(--hairline)',
            '--input': 'var(--surface-elevated)',
            '--ring': 'var(--accent-blue)',
            '--success': 'var(--accent-green)',
            '--warning': 'var(--accent-yellow)',
            '--sidebar': 'var(--canvas)',
            '--sidebar-foreground': 'var(--ink)',
            '--sidebar-primary': 'var(--accent-blue)',
            '--sidebar-primary-foreground': p.background,
            '--sidebar-accent': 'var(--surface-elevated)',
            '--sidebar-accent-foreground': 'var(--ink)',
            '--sidebar-border': 'var(--hairline)',
            '--sidebar-ring': 'var(--accent-blue)',
            '--chart-primary': 'var(--accent-blue)',
            '--chart-secondary': 'var(--accent-green)',
        },
        xterm: {
            background: p.background,
            foreground: p.foreground,
            cursor: p.cursor,
            cursorAccent: p.background,
            selectionBackground: p.selectionBackground,
            black: p.ansiBlack,
            red: p.ansiRed,
            green: p.ansiGreen,
            yellow: p.ansiYellow,
            blue: p.ansiBlue,
            magenta: p.ansiMagenta,
            cyan: p.ansiCyan,
            white: p.ansiWhite,
            brightBlack: p.ansiBrightBlack,
            brightRed: p.ansiBrightRed,
            brightGreen: p.ansiBrightGreen,
            brightYellow: p.ansiBrightYellow,
            brightBlue: p.ansiBrightBlue,
            brightMagenta: p.ansiBrightMagenta,
            brightCyan: p.ansiBrightCyan,
            brightWhite: p.ansiBrightWhite,
        },
    }
}

export function applyTheme(themeName: string, customPalette?: Partial<CustomThemePalette> | null) {
    const theme = themeName === 'custom'
        ? buildCustomThemePreset(customPalette)
        : (themePresets[themeName] || themePresets['dark'])
    const root = document.documentElement
    for (const [key, value] of Object.entries(theme.cssVars)) {
        root.style.setProperty(key, value)
    }
}

export function getXtermTheme(themeName: string, customPalette?: Partial<CustomThemePalette> | null) {
    const theme = themeName === 'custom'
        ? buildCustomThemePreset(customPalette)
        : (themePresets[themeName] || themePresets['dark'])
    return theme.xterm
}
