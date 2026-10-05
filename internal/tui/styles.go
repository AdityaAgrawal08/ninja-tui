package tui

import (
	"math"

	"charm.land/lipgloss/v2"
	"ninja-tui/internal/webhook"
)

// Palette: score colors run 0 green through 4 red; the rest is deliberately
// muted so a red badge is the loudest thing on screen.
var (
	colGreen  = lipgloss.Color("#3fb950")
	colTeal   = lipgloss.Color("#39c5cf")
	colYellow = lipgloss.Color("#d29922")
	colOrange = lipgloss.Color("#f0883e")
	colRed    = lipgloss.Color("#f85149")
	colDim    = lipgloss.Color("#8b949e")
	colText   = lipgloss.Color("#c9d1d9")
	colAccent = lipgloss.Color("#58a6ff")
	colWhite  = lipgloss.Color("#ffffff")
)

var (
	styleTitle   = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	styleText    = lipgloss.NewStyle().Foreground(colText)
	styleDim     = lipgloss.NewStyle().Foreground(colDim)
	styleBold    = lipgloss.NewStyle().Bold(true).Foreground(colText)
	styleSection = lipgloss.NewStyle().Bold(true).Foreground(colDim)
	styleGood    = lipgloss.NewStyle().Foreground(colGreen)
	styleWarn    = lipgloss.NewStyle().Foreground(colYellow)
	styleBad     = lipgloss.NewStyle().Foreground(colRed)
	styleTeal    = lipgloss.NewStyle().Foreground(colTeal)
	styleNotice  = lipgloss.NewStyle().Foreground(colYellow)
	styleBullet  = lipgloss.NewStyle().Foreground(colRed).Bold(true)

	// styleAccent marks the Import button; styleSelected marks the focused
	// list row.
	styleAccent   = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	styleSelected = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
)

// scoreBadge renders the 0-4 verdict badge with the mandated color ramp.
func scoreBadge(score float64, scoreMax int) string {
	n := scoreBucket(score, scoreMax)
	switch n {
	case 0:
		return lipgloss.NewStyle().Bold(true).Foreground(colGreen).Render(" 0 ")
	case 1:
		return lipgloss.NewStyle().Bold(true).Foreground(colTeal).Render(" 1 ")
	case 2:
		return lipgloss.NewStyle().Bold(true).Foreground(colYellow).Render(" 2 ")
	case 3:
		return lipgloss.NewStyle().Bold(true).Foreground(colOrange).Render(" 3 ")
	default:
		return lipgloss.NewStyle().Bold(true).Foreground(colWhite).Background(colRed).Render(" 4 ")
	}
}

// scoreBucket maps a verdict score onto the 0-4 badge scale. ninja's score is
// 0..score_max (4 in practice); anything outside the scale is clamped.
func scoreBucket(score float64, scoreMax int) int {
	if scoreMax <= 0 || scoreMax == 4 {
		return clampInt(int(math.Round(score)), 0, 4)
	}
	return clampInt(int(math.Round(score/float64(scoreMax)*4)), 0, 4)
}

// verdictLine renders "category · confidence N%" next to a badge.
func verdictLine(v *webhook.Verdict) string {
	if v == nil {
		return styleDim.Render("unscored")
	}
	if v.Category == nil {
		return styleDim.Render("no category")
	}
	cat := v.Category.Choice
	conf := int(math.Round(v.Category.Confidence * 100))
	if v.Category.Confidence <= 1 {
		return cat + styleDim.Render(" · confidence ") + itoa(conf) + "%"
	}
	return cat
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
