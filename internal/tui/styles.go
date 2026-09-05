package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// styles is the palette the watch screens paint with. It is built once the
// terminal has said whether its background is dark, because the same accent
// that reads well on a dark ground disappears on a light one; until then the
// dark set is used, which is the common case for a terminal.
//
// The accent is a warm early-sunrise set (amber, coral, gold) rather than
// the purple every terminal dashboard reaches for; green, rose and red keep
// their conventional meanings (free, exclusive, trouble).
type styles struct {
	dark bool

	ink, muted, amber, coral, gold, green, rose, red color.Color

	brand    lipgloss.Style // the "incoda" tag in the header
	title    lipgloss.Style // screen title text
	dim      lipgloss.Style // metadata, secondary lines
	bold     lipgloss.Style
	accent   lipgloss.Style
	colHead  lipgloss.Style // table column headings
	selected lipgloss.Style // the highlighted row
	box      lipgloss.Style // bordered panel for prompts
	help     lipgloss.Style
	toastOK  lipgloss.Style
	toastErr lipgloss.Style

	badgeFree, badgeHeld, badgeExcl, badgeClosed, badgeWait lipgloss.Style
	gaugeFill, gaugeHot, gaugeEmpty                         lipgloss.Style
	evAcquire, evEnqueue, evRelease, evBad, evConfig        lipgloss.Style
}

func newStyles(dark bool) styles {
	ld := lipgloss.LightDark(dark)
	s := styles{
		dark:  dark,
		ink:   ld(lipgloss.Color("#1F2328"), lipgloss.Color("#E6EDF3")),
		muted: ld(lipgloss.Color("#6E7781"), lipgloss.Color("#8B949E")),
		amber: lipgloss.Color("#F5A524"),
		coral: lipgloss.Color("#FF7A59"),
		gold:  lipgloss.Color("#FFD166"),
		green: ld(lipgloss.Color("#1A7F37"), lipgloss.Color("#3FB950")),
		rose:  ld(lipgloss.Color("#BF3989"), lipgloss.Color("#F778BA")),
		red:   ld(lipgloss.Color("#CF222E"), lipgloss.Color("#F85149")),
	}
	dark0 := lipgloss.Color("#1F2328")
	s.brand = lipgloss.NewStyle().Bold(true).Foreground(dark0).Background(s.amber).Padding(0, 1)
	s.title = lipgloss.NewStyle().Bold(true).Foreground(s.ink)
	s.dim = lipgloss.NewStyle().Foreground(s.muted)
	s.bold = lipgloss.NewStyle().Bold(true).Foreground(s.ink)
	s.accent = lipgloss.NewStyle().Foreground(s.amber)
	s.colHead = lipgloss.NewStyle().Foreground(s.muted).Bold(true)
	s.selected = lipgloss.NewStyle().Foreground(dark0).Background(s.gold).Bold(true)
	s.box = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(s.amber).Padding(0, 1)
	s.help = lipgloss.NewStyle().Foreground(s.muted)
	s.toastOK = lipgloss.NewStyle().Foreground(s.green).Bold(true)
	s.toastErr = lipgloss.NewStyle().Foreground(s.red).Bold(true)

	badge := lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(dark0)
	s.badgeFree = badge.Background(s.green)
	s.badgeHeld = badge.Background(s.amber)
	s.badgeExcl = badge.Background(s.rose)
	s.badgeClosed = badge.Background(s.red).Foreground(lipgloss.Color("#FFFFFF"))
	s.badgeWait = lipgloss.NewStyle().Foreground(s.gold).Bold(true)

	s.gaugeFill = lipgloss.NewStyle().Foreground(s.amber)
	s.gaugeHot = lipgloss.NewStyle().Foreground(s.red)
	s.gaugeEmpty = lipgloss.NewStyle().Foreground(s.muted)

	s.evAcquire = lipgloss.NewStyle().Foreground(s.green)
	s.evEnqueue = lipgloss.NewStyle().Foreground(s.amber)
	s.evRelease = s.dim
	s.evBad = lipgloss.NewStyle().Foreground(s.red)
	s.evConfig = lipgloss.NewStyle().Foreground(s.gold)
	return s
}
