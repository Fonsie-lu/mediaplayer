package tui

import "charm.land/lipgloss/v2"

// Tokyo Night palette, matching the web UI's theme.
var (
	colFg     = lipgloss.Color("#c0caf5")
	colDim    = lipgloss.Color("#565f89")
	colAccent = lipgloss.Color("#7aa2f7")
	colGreen  = lipgloss.Color("#9ece6a")
	colRed    = lipgloss.Color("#f7768e")
	colYellow = lipgloss.Color("#e0af68")
	colMag    = lipgloss.Color("#bb9af7")
	colSelBg  = lipgloss.Color("#283457")
	colBar    = lipgloss.Color("#1a1b26")
)

var (
	tabActive = lipgloss.NewStyle().
			Foreground(colBar).Background(colAccent).Bold(true).Padding(0, 2)
	tabInactive = lipgloss.NewStyle().
			Foreground(colDim).Padding(0, 2)
	tabBar = lipgloss.NewStyle().Padding(0, 0, 1, 0)

	titleStyle = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	dimStyle   = lipgloss.NewStyle().Foreground(colDim)
	selStyle   = lipgloss.NewStyle().Foreground(colFg).Background(colSelBg).Bold(true)
	countStyle = lipgloss.NewStyle().Foreground(colYellow)
	greenStyle = lipgloss.NewStyle().Foreground(colGreen)
	magStyle   = lipgloss.NewStyle().Foreground(colMag)
	redStyle   = lipgloss.NewStyle().Foreground(colRed)

	helpStyle = lipgloss.NewStyle().Foreground(colDim).Padding(1, 0, 0, 0)

	modalStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colAccent).
			Padding(1, 2)

	inputLabel = lipgloss.NewStyle().Foreground(colMag).Bold(true)
)
